// Command api is the Junto API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"github.com/junto/junto/configs"
	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/email"
	"github.com/junto/junto/internal/pubsub"
	"github.com/junto/junto/internal/repository"
	"github.com/junto/junto/internal/security"
	"github.com/junto/junto/internal/service"
	"github.com/junto/junto/internal/storage"
	"github.com/junto/junto/internal/syncengine"
	junto "github.com/junto/junto/internal/transport/http"
	"github.com/junto/junto/internal/transport/ws"
)

func main() {
	if err := run(); err != nil {
		// slog rather than log.Fatal: a structured error line is greppable in aggregation,
		// and log.Fatal's os.Exit(1) would skip every deferred cleanup below.
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load() // absent in production; configuration comes from the environment

	cfg, err := configs.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)

	// The signal context is established before any resource is acquired, so a Ctrl-C during
	// slow startup still unwinds cleanly instead of leaving a half-open pool.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting junto", "env", cfg.Env, "go_version", runtime.Version())

	pool, err := newPool(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	if err := verifySchema(ctx, pool); err != nil {
		return err
	}
	logger.Info("database connected", "max_conns", cfg.DB.MaxConns)

	// Dependency injection by constructor, wired here and nowhere else. This function is the
	// only place that knows which concrete implementation satisfies which port, which is what
	// lets every layer below be tested with substitutes.
	var (
		users       = repository.NewUserRepository(pool)
		sessions    = repository.NewSessionRepository(pool)
		tokens      = repository.NewUserTokenRepository(pool)
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
		hasher      = security.NewArgon2Hasher(cfg.Auth.Argon2)
		issuer      = security.NewJWTIssuer(cfg.Auth.JWTSecret, cfg.Auth.JWTIssuer, cfg.Auth.AccessTokenTTL)
		mailer      = newMailer(cfg, logger)
	)

	tripService := service.NewTripService(service.TripDeps{
		Trips: trips, Members: members, Tx: txm, Clock: domain.SystemClock{},
	})
	membershipService := service.NewMembershipService(service.MembershipDeps{
		Members: members, Trips: trips, Users: users, Invitations: invitations,
		Mailer: mailer, Tx: txm, Clock: domain.SystemClock{}, Logger: logger,
		Config: service.MembershipConfig{WebBaseURL: cfg.App.WebBaseURL},
	})
	// Redis is optional and its absence is a topology, not an error: no Redis means one
	// instance, in-memory fan-out, in-memory tickets and purely local revocation. See
	// configs.RedisConfig.
	var (
		redisClient  *redis.Client
		opTransport  domain.OpTransport         = domain.NoopTransport{}
		revTransport domain.RevocationTransport = domain.NoopRevocationTransport{}
		tickets      ws.TicketStore
	)
	if cfg.Redis.URL != "" {
		redisClient, err = pubsub.NewClient(cfg.Redis.URL)
		if err != nil {
			return err
		}
		defer func() { _ = redisClient.Close() }()

		// Fail fast for the same reason the database is pinged: a lazily-connecting client
		// would turn an unreachable Redis into a successful startup followed by silently
		// isolated instances.
		pingCtx, cancelPing := context.WithTimeout(ctx, 10*time.Second)
		err = redisClient.Ping(pingCtx).Err()
		cancelPing()
		if err != nil {
			return fmt.Errorf("connecting to redis: %w", err)
		}

		transport := pubsub.NewOpTransport(redisClient, logger)
		defer func() { _ = transport.Close() }()
		opTransport = transport

		// Its own subscriber connection, deliberately not shared with the operation transport:
		// a connection churning through per-trip subscribe/unsubscribe calls as rooms open and
		// close is not the one that should carry the message closing a compromised session.
		revocations := pubsub.NewRevocationTransport(redisClient, logger)
		defer func() { _ = revocations.Close() }()
		revTransport = revocations

		tickets = ws.NewRedisTicketStore(redisClient, domain.SystemClock{})
		logger.Info("redis connected: multi-instance fan-out, shared handshake tickets and "+
			"cross-instance session revocation",
			"instance_id", transport.InstanceID())
	} else {
		memTickets := ws.NewMemoryTicketStore(domain.SystemClock{})
		defer memTickets.Close()
		tickets = memTickets
		logger.Warn("no REDIS_URL configured: running single-instance. " +
			"Handshake tickets and operation fan-out are in-process, so a second instance " +
			"would fail handshakes at random and its subscribers would not see this one's writes")
	}

	// The registry holds every live socket on this instance and closes the ones whose session
	// has been revoked (D91). It is the auth service's RevocationPublisher, which is why both
	// it and the Redis block sit ABOVE that service rather than with the rest of the WebSocket
	// plumbing further down — the same construction-order-not-cycle shape as the broker being
	// built before the planning services.
	connections := ws.NewRegistry(revTransport, logger)

	authService, err := service.NewAuthService(service.AuthDeps{
		Users:    users,
		Sessions: sessions,
		Tokens:   tokens,
		Hasher:   hasher,
		Issuer:   issuer,
		Mailer:   mailer,
		Tx:       txm,
		Clock:    domain.SystemClock{},
		Logger:   logger,
		// Closes a revoked session's live sockets. Without it the session is revoked in the
		// database while an already-open WebSocket keeps reading and writing until its lifetime
		// cap fires — the D73 gap, closed here.
		Revocations: connections,
		Config: service.AuthConfig{
			AccessTokenTTL:   cfg.Auth.AccessTokenTTL,
			RefreshTokenTTL:  cfg.Auth.RefreshTokenTTL,
			SessionTTL:       cfg.Auth.SessionTTL,
			EmailVerifyTTL:   cfg.Auth.EmailVerifyTTL,
			PasswordResetTTL: cfg.Auth.PasswordResetTTL,
			WebBaseURL:       cfg.App.WebBaseURL,
			AutoVerifyEmail:  cfg.Auth.AutoVerifyEmail,
		},
	})
	if err != nil {
		return fmt.Errorf("building auth service: %w", err)
	}
	if cfg.Auth.AutoVerifyEmail {
		logger.Warn("AUTH_AUTO_VERIFY_EMAIL is on: new accounts are verified at signup and no " +
			"verification email is sent. Development convenience only — config validation " +
			"refuses this in production")
	}

	// The broker is built BEFORE the planning services, because they take it as a
	// domain.OpPublisher, and the engine is built after them because it dispatches to them.
	// Splitting the sync engine into two objects is what turns that mutual need into a plain
	// construction order rather than a cycle — and it is why no service imports syncengine.
	broker := syncengine.NewBroker(syncengine.BrokerConfig{
		Logger: logger,
		// Read-only, and only on the repair path: this is what lets the broker refill a hole
		// in the broadcast from the log instead of leaving a client silently stale.
		Ops:       ops,
		Trips:     trips,
		Transport: opTransport,
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

	// Attachments are OPTIONAL, in the same way Redis is: with no storage endpoint the API
	// simply has no attachment surface, which is a legitimate way to run this and is what a
	// deployment without a provisioned bucket gets. A nil service leaves the routes unmounted
	// rather than mounting handlers that panic on first use.
	var attachmentService *service.AttachmentService
	if cfg.Storage.Enabled() {
		files, err := storage.NewS3Storage(ctx, storage.S3Config{
			Endpoint:  cfg.Storage.Endpoint,
			AccessKey: cfg.Storage.AccessKey,
			SecretKey: cfg.Storage.SecretKey,
			Bucket:    cfg.Storage.Bucket,
			Region:    cfg.Storage.Region,
			UseSSL:    cfg.Storage.UseSSL,
		})
		if err != nil {
			return fmt.Errorf("connecting to object storage: %w", err)
		}
		attachmentService = service.NewAttachmentService(service.AttachmentDeps{
			Attachments: attachments, Storage: files, Slots: slots, Options: options,
			Budget: budget, Members: members, Trips: trips, Ops: ops, Tx: txm, Pub: broker,
			Clock: domain.SystemClock{}, Logger: logger,
		})
		logger.Info("object storage connected: attachments enabled",
			"endpoint", cfg.Storage.Endpoint, "bucket", cfg.Storage.Bucket)
	} else {
		logger.Warn("no STORAGE_ENDPOINT configured: attachment endpoints are not mounted")
	}

	engine := syncengine.NewEngine(syncengine.EngineConfig{
		Broker:   broker,
		Services: newSyncEngineServices(tripService, dayService, slotService, optionService, voteService, commentService, budgetService),
		Ops:      ops, Trips: trips, Logger: logger,
	})

	// The broker's peer pump runs for the life of the process. With NoopTransport it simply
	// blocks, so there is no branch here on whether Redis is configured.
	brokerCtx, stopBroker := context.WithCancel(context.Background())
	defer stopBroker()
	go func() {
		if err := broker.Run(brokerCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("peer fan-out stopped", "error", err)
		}
	}()

	// The registry's peer pump, alongside the broker's. With NoopRevocationTransport it simply
	// blocks, so there is no branch here on whether Redis is configured.
	go func() {
		if err := connections.Run(brokerCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("session revocation fan-out stopped", "error", err)
		}
	}()

	wsHandlers := ws.NewHandlers(engine, tickets, connections, logger, ws.Config{
		AllowedOrigins: cfg.HTTP.CORSAllowedOrigins,
	})

	router, cleanupRouter := junto.NewRouter(junto.Deps{
		WS:       wsHandlers,
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
		Logger:   logger,
		Config: junto.RouterConfig{
			AllowedOrigins: cfg.HTTP.CORSAllowedOrigins,
			SecureCookies:  cfg.Env.IsProduction(),
			// Enable only when a trusted reverse proxy actually sits in front; see
			// RouterConfig.TrustProxyHeaders.
			TrustProxyHeaders: cfg.Env.IsProduction(),
		},
	})
	defer cleanupRouter()

	server := junto.NewServer(junto.ServerConfig{
		Addr:              cfg.HTTP.Addr,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}, router)

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTP.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	// Shutdown gets a FRESH context. The parent is already cancelled by the signal, so
	// reusing it would give in-flight requests zero time to finish — which is the difference
	// between a graceful shutdown and an abrupt one that happens to log the word "graceful".
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

// newSyncEngineServices assembles syncengine.Services in exactly one place.
//
// It exists because this literal used to be built inline inside run(), and that is precisely
// how Budget went missing (D104): syncengine.Services declared a Budget field, dispatch.go's
// budgetSet/OpBudgetDelete cases called e.services.Budget unconditionally, and nothing caught
// the omission because tests/stack_test.go builds its OWN copy of this literal rather than
// calling run()'s. Pulling it out into a named function does not by itself stop a second
// hand-written copy from drifting — cmd/api_test.go's TestSyncEngineServicesHasNoNilFields
// (below) is what actually closes that gap, by calling this exact function with distinct
// sentinel values and reflecting over the result. What the extraction buys is a function small
// and pure enough for that test to call without standing up a database.
func newSyncEngineServices(
	trips *service.TripService,
	days *service.DayService,
	slots *service.SlotService,
	options *service.SlotOptionService,
	votes *service.VoteService,
	comments *service.CommentService,
	budget *service.BudgetService,
) syncengine.Services {
	return syncengine.Services{
		Trips: trips, Days: days, Slots: slots,
		Options: options, Votes: votes, Comments: comments, Budget: budget,
	}
}

// newMailer picks an email implementation.
//
// Development defaults to logging rather than SMTP so a missing mail container cannot break
// signup. Production always uses SMTP, and config validation already requires TLS there.
func newMailer(cfg *configs.Config, logger *slog.Logger) domain.EmailSender {
	if cfg.SMTP.Host == "" {
		logger.Warn("no SMTP host configured; emails will be logged, not sent")
		return email.NewLogSender(logger)
	}
	return email.NewSMTPSender(email.SMTPConfig{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,
		UseTLS:   cfg.SMTP.UseTLS,
	})
}

func newLogger(cfg configs.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func newPool(ctx context.Context, cfg configs.DBConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}

	// pgxpool connects lazily, so without an explicit ping a completely unreachable database
	// would look like a successful startup until the first request arrived.
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// verifySchema refuses to start against an un-migrated database.
//
// Starting anyway and failing on the first query trades a clear startup error for a confusing
// runtime one, usually in front of a user.
func verifySchema(ctx context.Context, pool *pgxpool.Pool) error {
	// Probes the LAST table the migrations create, not the first. Checking an early table
	// would pass against a database that is half-migrated.
	const q = `SELECT to_regclass('public.attachments') IS NOT NULL`

	var ready bool
	if err := pool.QueryRow(ctx, q).Scan(&ready); err != nil {
		return fmt.Errorf("checking schema: %w", err)
	}
	if !ready {
		return errors.New("database schema is not initialised: run `go run ./cmd/migrate up`")
	}
	return nil
}
