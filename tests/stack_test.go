package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/middleware"
	"github.com/junto/junto/internal/repository"
	"github.com/junto/junto/internal/security"
	"github.com/junto/junto/internal/service"
	"github.com/junto/junto/internal/storage"
	"github.com/junto/junto/internal/syncengine"
	juntohttp "github.com/junto/junto/internal/transport/http"
	"github.com/junto/junto/internal/transport/ws"
)

// One wiring, used by every full-stack test.
//
// This exists because Slice 2 needs to stand TWO complete instances up side by side, and a
// second hand-written copy of the construction order would be the easiest possible place for
// the two-instance test to differ from the thing it claims to be testing. The order here is
// the order in cmd/api/main.go: broker, then services (which take it as a domain.OpPublisher),
// then the engine that dispatches to them.

// stackConfig is what distinguishes one instance from another.
type stackConfig struct {
	Pool *pgxpool.Pool

	// Tickets and Transport are the two pieces that decide whether instances are independent.
	// nil Tickets gives the in-memory store; nil Transport gives domain.NoopTransport — i.e.
	// exactly the Slice 1 single-instance behaviour.
	Tickets   ws.TicketStore
	Transport domain.OpTransport

	// RevocationTransport carries session revocations between instances (D91). nil gives
	// domain.NoopRevocationTransport — local closure only, which is complete for a
	// single-instance test and is exactly what the two-instance revocation test replaces to
	// prove the peer path is doing the work.
	RevocationTransport domain.RevocationTransport

	// ReconcileInterval is exposed so a test can force the broker's "did I miss the last
	// operation" check to run on a human timescale instead of the production default.
	ReconcileInterval time.Duration

	// MaxResyncOps lets a test assert the replay ceiling against a tiny bound instead of
	// writing ten thousand operations to prove a constant.
	MaxResyncOps int
}

// stack is one fully wired API instance.
type stack struct {
	Server *httptest.Server
	Broker *syncengine.Broker
	Auth   *service.AuthService

	// Connections is this instance's socket registry, exposed so a test can assert how many
	// sockets it is holding — the direct observation of "the revoked connection is gone",
	// rather than inferring it from a client that stopped receiving.
	Connections *ws.Registry

	OpLog    domain.OpLogRepository
	Trips    domain.TripRepository
	Slots    domain.SlotRepository
	Options  domain.SlotOptionRepository
	Votes    domain.VoteRepository
	Comments domain.CommentRepository
	Budget   domain.BudgetRepository

	// Attachments is the concrete type rather than the port, because these tests need
	// ListForTrip — the read that lets an assertion compare the whole trip's attachments
	// against a fold of the log. It is deliberately not on the domain port, so that no
	// service can reach for it as a shortcut around the owner-scoped reads.
	Attachments *repository.AttachmentRepository

	// Storage is the in-process object store the attachment service was wired with. Tests
	// simulate the browser's direct PUT by writing to it, which is the only way to reach the
	// confirm path — the API never sees the bytes, by design.
	Storage *storage.MemoryStorage

	stopBroker context.CancelFunc
	cleanup    []func()
}

func (s *stack) Close() {
	s.stopBroker()
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newStack builds a complete API instance against an existing database.
func newStack(cfg stackConfig) (*stack, error) {
	logger := discardLogger()
	pool := cfg.Pool

	// The socket registry is the auth service's RevocationPublisher, so it is built first —
	// the same ordering cmd/api uses, and the reason both are above the service wiring there.
	registry := ws.NewRegistry(cfg.RevocationTransport, logger)

	// The one substituted dependency, because the alternative is an SMTP server in the test
	// path. It also lets tests read verification links the way a user reads their inbox.
	//
	// security.TestHasher keeps Argon2id's real code path at parameters cheap enough that a
	// suite creating dozens of accounts is not dominated by the KDF.
	authService, err := service.NewAuthService(service.AuthDeps{
		Users:    repository.NewUserRepository(pool),
		Sessions: repository.NewSessionRepository(pool),
		Tokens:   repository.NewUserTokenRepository(pool),
		Hasher:   security.TestHasher(),
		Issuer:   security.NewJWTIssuer(strings.Repeat("k", 48), "junto-test", 15*time.Minute),
		Mailer:   testMailer,
		Tx:       repository.NewTxManager(pool),
		Clock:    domain.SystemClock{},
		Logger:   logger,
		// Wired exactly as production wires it, so the revocation tests exercise the real path
		// rather than a substitute assembled for them.
		Revocations: registry,
		Config: service.AuthConfig{
			AccessTokenTTL:   15 * time.Minute,
			RefreshTokenTTL:  30 * 24 * time.Hour,
			SessionTTL:       90 * 24 * time.Hour,
			EmailVerifyTTL:   24 * time.Hour,
			PasswordResetTTL: time.Hour,
			WebBaseURL:       "https://junto.test",
		},
	})
	if err != nil {
		return nil, err
	}

	// The planning surface: real repositories against the same Postgres, wired the same way
	// cmd/api/main.go wires them. This is what makes these tests prove the WIRING, not just
	// each service in isolation with fakes (that is what internal/service's own tests do).
	var (
		trips       = repository.NewTripRepository(pool)
		members     = repository.NewMembershipRepository(pool)
		invitations = repository.NewInvitationRepository(pool)
		days        = repository.NewDayRepository(pool)
		slots       = repository.NewSlotRepository(pool)
		options     = repository.NewSlotOptionRepository(pool)
		votes       = repository.NewVoteRepository(pool)
		comments    = repository.NewCommentRepository(pool)
		budget      = repository.NewBudgetRepository(pool)
		attachments = repository.NewAttachmentRepository(pool)
		ops         = repository.NewOpLogRepository(pool)
		txm         = repository.NewTxManager(pool)
		files       = storage.NewMemoryStorage()
	)

	transport := cfg.Transport
	if transport == nil {
		transport = domain.NoopTransport{}
	}
	broker := syncengine.NewBroker(syncengine.BrokerConfig{
		Logger:            logger,
		Ops:               ops,
		Trips:             trips,
		Transport:         transport,
		ReconcileInterval: cfg.ReconcileInterval,
	})

	tripService := service.NewTripService(service.TripDeps{Trips: trips, Members: members, Tx: txm, Clock: domain.SystemClock{}})
	membershipService := service.NewMembershipService(service.MembershipDeps{
		Members: members, Trips: trips, Users: repository.NewUserRepository(pool),
		Invitations: invitations, Mailer: testMailer, Tx: txm, Clock: domain.SystemClock{},
		Logger: logger,
		Config: service.MembershipConfig{WebBaseURL: "https://junto.test"},
	})
	dayService := service.NewDayService(service.DayDeps{
		Days: days, Members: members, Trips: trips, Ops: ops, Tx: txm, Pub: broker,
		Clock: domain.SystemClock{},
	})
	slotService := service.NewSlotService(service.SlotDeps{
		Slots: slots, Members: members, Trips: trips, Ops: ops, Tx: txm, Pub: broker,
		Clock: domain.SystemClock{},
	})
	optionService := service.NewSlotOptionService(service.SlotOptionDeps{
		Options: options, Slots: slots, Members: members, Trips: trips, Ops: ops, Tx: txm,
		Pub: broker, Clock: domain.SystemClock{},
	})
	voteService := service.NewVoteService(service.VoteDeps{
		Votes: votes, Slots: slots, Members: members, Trips: trips, Ops: ops, Tx: txm,
		Pub: broker,
	})
	commentService := service.NewCommentService(service.CommentDeps{
		Comments: comments, Slots: slots, Members: members, Trips: trips, Ops: ops, Tx: txm,
		Pub: broker, Clock: domain.SystemClock{},
	})
	budgetService := service.NewBudgetService(service.BudgetDeps{
		Budget: budget, Members: members, Trips: trips, Ops: ops, Tx: txm, Pub: broker,
		Clock: domain.SystemClock{},
	})
	attachmentService := service.NewAttachmentService(service.AttachmentDeps{
		Attachments: attachments, Storage: files, Slots: slots, Options: options,
		Budget: budget, Members: members, Trips: trips, Ops: ops, Tx: txm, Pub: broker,
		Clock: domain.SystemClock{}, Logger: logger,
	})

	engine := syncengine.NewEngine(syncengine.EngineConfig{
		Broker: broker,
		Services: syncengine.Services{
			Trips: tripService, Days: dayService, Slots: slotService,
			Options: optionService, Votes: voteService, Comments: commentService,
			Budget: budgetService,
		},
		Ops: ops, Trips: trips, Logger: logger,
		MaxResyncOps: cfg.MaxResyncOps,
	})

	s := &stack{
		Broker: broker, Auth: authService, Connections: registry,
		OpLog: ops, Trips: trips, Slots: slots, Options: options, Votes: votes,
		Comments: comments, Budget: budget, Attachments: attachments, Storage: files,
	}

	brokerCtx, stopBroker := context.WithCancel(context.Background())
	s.stopBroker = stopBroker
	go func() { _ = broker.Run(brokerCtx) }()
	go func() { _ = registry.Run(brokerCtx) }()

	tickets := cfg.Tickets
	if tickets == nil {
		memTickets := ws.NewMemoryTicketStore(domain.SystemClock{})
		s.cleanup = append(s.cleanup, memTickets.Close)
		tickets = memTickets
	}

	wsHandlers := ws.NewHandlers(engine, tickets, registry, logger, ws.Config{
		AllowedOrigins: []string{"*"},
		// The convergence tests deliberately fire bursts of concurrent operations; a
		// production-strict per-connection limiter would throttle the test rather than any
		// simulated abuser. Throttling has its own test.
		OpsPerSecond: 10000, OpsBurst: 10000,
	})

	// Permissive throttles for the shared server. Every request in this suite originates from
	// 127.0.0.1, so the production-strict limiter would throttle the SUITE rather than any
	// simulated attacker — which is precisely what happened before the limits were made
	// configurable. Throttling itself is proven separately by newStrictlyLimitedServer.
	permissive := middleware.RateLimitConfig{RequestsPerSecond: 10000, Burst: 10000, TTL: time.Minute}

	router, cleanupRouter := juntohttp.NewRouter(juntohttp.Deps{
		Auth:     authService,
		Trips:    tripService,
		Members:  membershipService,
		Days:     dayService,
		Slots:    slotService,
		Options:  optionService,
		Votes:    voteService,
		Comments: commentService,
		Budget:   budgetService,
		Files:    attachmentService,
		WS:       wsHandlers,
		Logger:   logger,
		Config: juntohttp.RouterConfig{
			AllowedOrigins:   []string{"http://localhost:3000"},
			SecureCookies:    false,
			AuthRateLimit:    &permissive,
			GeneralRateLimit: &permissive,
		},
	})
	s.cleanup = append(s.cleanup, cleanupRouter)

	s.Server = httptest.NewServer(router)
	s.cleanup = append(s.cleanup, s.Server.Close)
	return s, nil
}
