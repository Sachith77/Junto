package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/junto/junto/internal/middleware"
	"github.com/junto/junto/internal/service"
	"github.com/junto/junto/internal/transport/ws"
)

// RouterConfig configures route assembly.
type RouterConfig struct {
	AllowedOrigins []string
	SecureCookies  bool
	// TrustProxyHeaders enables chi's RealIP, which rewrites RemoteAddr from X-Forwarded-For.
	//
	// Off by default and deliberately explicit. X-Forwarded-For is client-controlled, so
	// trusting it when NOT behind a proxy that overwrites it lets any caller spoof their
	// address — evading the per-IP rate limiter and poisoning session audit records. Enable
	// it only when a trusted reverse proxy is actually in front.
	TrustProxyHeaders bool

	// AuthRateLimit and GeneralRateLimit override the default throttles. Zero values fall
	// back to the presets in internal/middleware.
	//
	// Configurable rather than hardcoded for two reasons. Operationally, the right numbers
	// depend on the deployment and nobody should have to recompile to change them. And for
	// tests: every request from a test suite shares one source address, so a production-strict
	// limiter throttles the suite itself — which is exactly what happened the first time
	// these were constants.
	AuthRateLimit    *middleware.RateLimitConfig
	GeneralRateLimit *middleware.RateLimitConfig
}

// Deps are the handlers and services a router needs.
type Deps struct {
	Auth     *service.AuthService
	Trips    *service.TripService
	Members  *service.MembershipService
	Days     *service.DayService
	Slots    *service.SlotService
	Options  *service.SlotOptionService
	Votes    *service.VoteService
	Comments *service.CommentService
	Budget   *service.BudgetService
	Files    *service.AttachmentService
	Logger   *slog.Logger
	Config   RouterConfig

	// WS mounts the sync transport. Nil leaves the API REST-only, which is what the auth and
	// planning API tests want — and is a useful property in itself: every write still reaches
	// the operation log, because the log is written by the service layer rather than by the
	// socket (Rule 3).
	WS *ws.Handlers
}

// NewRouter assembles the API.
//
// Middleware order is not cosmetic and is commented inline: each layer depends on what the
// previous one established.
func NewRouter(deps Deps) (http.Handler, func()) {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}

	authLimits := middleware.AuthRateLimit()
	if deps.Config.AuthRateLimit != nil {
		authLimits = *deps.Config.AuthRateLimit
	}
	generalLimits := middleware.GeneralRateLimit()
	if deps.Config.GeneralRateLimit != nil {
		generalLimits = *deps.Config.GeneralRateLimit
	}

	authLimiter := middleware.NewRateLimiter(authLimits)
	generalLimiter := middleware.NewRateLimiter(generalLimits)
	cleanup := func() {
		authLimiter.Close()
		generalLimiter.Close()
	}

	r := chi.NewRouter()

	// 1. RequestID first: everything downstream — logs, problem documents, panic reports —
	//    references it, so it must exist before anything can fail.
	r.Use(middleware.RequestID)

	// 2. RealIP before the rate limiter and before any handler reads RemoteAddr. Opt-in,
	//    because trusting a forgeable header without a proxy in front is worse than not
	//    having it.
	if deps.Config.TrustProxyHeaders {
		r.Use(chimw.RealIP)
	}

	// 3. Recoverer before the logger, so a panic still produces a logged, well-formed
	//    response rather than a dropped connection.
	r.Use(middleware.Recoverer(log))

	// 4. Logger next: it wraps the ResponseWriter to capture status and size, so it must sit
	//    outside everything whose status it should report.
	r.Use(middleware.RequestLogger(log))

	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.CORS(deps.Config.AllowedOrigins))
	r.Use(chimw.Timeout(30 * time.Second))

	authHandler := NewAuthHandler(deps.Auth, DefaultCookieConfig(deps.Config.SecureCookies), log)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(generalLimiter.Middleware)

		// Credential endpoints carry the strict limiter. This is the compensating control
		// that makes a length-only password policy defensible — see internal/domain/user.go
		// and internal/middleware/ratelimit.go.
		r.Group(func(r chi.Router) {
			r.Use(authLimiter.Middleware)

			r.Post("/auth/signup", authHandler.Signup)
			r.Post("/auth/login", authHandler.Login)
			r.Post("/auth/refresh", authHandler.Refresh)
			r.Post("/auth/verify-email", authHandler.VerifyEmail)
			r.Post("/auth/request-password-reset", authHandler.RequestPasswordReset)
			r.Post("/auth/reset-password", authHandler.ResetPassword)
		})

		// Logout is not rate limited beyond the general ceiling: it is idempotent, reveals
		// nothing, and throttling it would strand a user who cannot sign out.
		r.Post("/auth/logout", authHandler.Logout)

		// The WebSocket handshake sits OUTSIDE RequireAuth, because a browser cannot set an
		// Authorization header on a WebSocket upgrade (D10). Its credential is the single-use
		// ticket minted below, which does require the header. The ticket endpoint is inside
		// the authenticated group a few lines down.
		if deps.WS != nil {
			r.Get("/ws", deps.WS.Connect)
		}

		// Authenticated routes: session management plus the planning surface. Every
		// authorization decision from here down is made by the SERVICE layer via
		// Actor.Can() — middleware only proves who the caller is, not what they may do to a
		// specific trip, which depends on that trip's membership row.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAuth(deps.Auth))

			r.Get("/me", authHandler.Me)

			if deps.WS != nil {
				r.Post("/ws/ticket", deps.WS.IssueTicket)
			}
			r.Get("/auth/sessions", authHandler.ListSessions)
			r.Delete("/auth/sessions/{sessionID}", func(w http.ResponseWriter, req *http.Request) {
				authHandler.RevokeSession(w, req, chi.URLParam(req, "sessionID"))
			})

			tripHandler := NewTripHandler(deps.Trips, log)
			memberHandler := NewMembershipHandler(deps.Members, log)
			dayHandler := NewDayHandler(deps.Days, log)
			slotHandler := NewSlotHandler(deps.Slots, log)
			optionHandler := NewSlotOptionHandler(deps.Options, log)
			voteHandler := NewVoteHandler(deps.Votes, log)
			commentHandler := NewCommentHandler(deps.Comments, log)

			r.Post("/trips", tripHandler.Create)
			r.Get("/trips", tripHandler.List)
			r.Post("/invitations/accept", memberHandler.AcceptInvitation)

			r.Route("/trips/{tripID}", func(r chi.Router) {
				r.Get("/", tripHandler.Get)
				r.Patch("/", tripHandler.Update)
				r.Delete("/", tripHandler.Delete)

				r.Get("/members", memberHandler.ListMembers)
				r.Patch("/members/{userID}", memberHandler.UpdateMemberRole)
				r.Delete("/members/{userID}", memberHandler.RemoveMember)

				r.Post("/invitations", memberHandler.CreateInvitation)
				r.Get("/invitations", memberHandler.ListInvitations)
				r.Delete("/invitations/{invitationID}", memberHandler.RevokeInvitation)

				r.Get("/days", dayHandler.List)
				r.Post("/days", dayHandler.Create)
				r.Patch("/days/{dayID}", dayHandler.Update)
				r.Post("/days/{dayID}/move", dayHandler.Move)
				r.Delete("/days/{dayID}", dayHandler.Delete)

				r.Get("/slots", slotHandler.List)
				r.Post("/slots", slotHandler.Create)
				r.Get("/slots/{slotID}", slotHandler.Get)
				r.Patch("/slots/{slotID}", slotHandler.Update)
				r.Post("/slots/{slotID}/move", slotHandler.Move)
				r.Post("/slots/{slotID}/select", slotHandler.SetSelectedOption)
				r.Post("/slots/{slotID}/status", slotHandler.SetStatus)
				r.Delete("/slots/{slotID}", slotHandler.Delete)

				r.Get("/slots/{slotID}/options", optionHandler.List)
				r.Post("/slots/{slotID}/options", optionHandler.Create)
				r.Patch("/slots/{slotID}/options/{optionID}", optionHandler.Update)
				r.Delete("/slots/{slotID}/options/{optionID}", optionHandler.Delete)

				r.Get("/slots/{slotID}/votes", voteHandler.List)
				r.Get("/slots/{slotID}/votes/tally", voteHandler.Tally)
				r.Put("/slots/{slotID}/votes/me", voteHandler.Cast)

				// Comments mount unconditionally, unlike the budget/attachment routes below —
				// they have no external dependency (no object storage) to be optional about.
				r.Get("/slots/{slotID}/comments", commentHandler.List)
				r.Post("/slots/{slotID}/comments", commentHandler.Create)
				r.Delete("/slots/{slotID}/comments/{commentID}", commentHandler.Delete)

				// PUT, not PATCH: a budget entry is replaced whole, together with its complete
				// split set (D44). Every other planning resource above is PATCH with a field
				// mask, and the difference in verb IS the difference in conflict grain —
				// worth being visible in the route table rather than only in a doc comment.
				if deps.Budget != nil {
					budgetHandler := NewBudgetHandler(deps.Budget, log)
					r.Get("/budget", budgetHandler.List)
					r.Post("/budget", budgetHandler.Create)
					r.Get("/budget/{entryID}", budgetHandler.Get)
					r.Put("/budget/{entryID}", budgetHandler.Update)
					r.Delete("/budget/{entryID}", budgetHandler.Delete)
				}

				// Attachments are WRITTEN here and only BROADCAST over the socket: an upload is
				// a presign, a direct PUT to object storage and a server-side confirmation,
				// which a WebSocket frame cannot express (D86).
				//
				// Mounted only when object storage is configured. Registering these against a
				// nil service would turn "attachments are not enabled in this deployment" into
				// a panic on the first upload, which is a far worse way to learn it.
				if deps.Files != nil {
					attachmentHandler := NewAttachmentHandler(deps.Files, log)
					r.Get("/attachments", attachmentHandler.List)
					r.Post("/attachments/uploads", attachmentHandler.RequestUpload)
					r.Post("/attachments/links", attachmentHandler.CreateLink)
					r.Post("/attachments/{attachmentID}/confirm", attachmentHandler.ConfirmUpload)
					r.Get("/attachments/{attachmentID}/url", attachmentHandler.DownloadURL)
					r.Delete("/attachments/{attachmentID}", attachmentHandler.Delete)
				}
			})
		})
	})

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, req, Problem{
			Type:   typeNotFound,
			Title:  "Not found",
			Status: http.StatusNotFound,
			Detail: "No route matches this path.",
		})
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, req, Problem{
			Type:   typeBadRequest,
			Title:  "Method not allowed",
			Status: http.StatusMethodNotAllowed,
			Detail: "This method is not supported for this path.",
		})
	})

	return r, cleanup
}

// RequestIDFrom re-exports the middleware accessor so response.go can populate the problem
// document's `instance` member without importing the middleware package from every file.
func RequestIDFrom(ctx context.Context) string {
	return middleware.RequestIDFrom(ctx)
}

// ServerConfig configures the HTTP server.
type ServerConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// NewServer builds an *http.Server with timeouts applied.
//
// Every timeout is set explicitly. Go's zero-value server has NO timeouts at all, which means
// one slow or malicious client can hold a connection — and its goroutine — open indefinitely.
// ReadHeaderTimeout in particular is the Slowloris defence.
func NewServer(cfg ServerConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}
