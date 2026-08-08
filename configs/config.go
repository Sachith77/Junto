// Package configs loads and validates configuration from the environment.
//
// Two rules shape everything here:
//
//  1. Fail fast and loudly. A misconfigured process must refuse to start rather than run
//     with a silently insecure default. The classic production incident is a service that
//     booted happily with a development signing key.
//  2. No secret has a default. Non-secret values get sensible defaults so local development
//     needs almost no setup; secrets must be supplied or startup aborts.
package configs

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment identifies the deployment environment.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// IsProduction reports whether stricter rules apply.
func (e Environment) IsProduction() bool { return e == EnvProduction }

// Config is the fully validated application configuration.
type Config struct {
	Env     Environment
	Log     LogConfig
	HTTP    HTTPConfig
	DB      DBConfig
	Redis   RedisConfig
	Storage StorageConfig
	Auth    AuthConfig
	SMTP    SMTPConfig
	App     AppConfig
}

// StorageConfig points the attachment adapter at an S3-compatible endpoint.
//
// Endpoint is OPTIONAL, and like Redis its absence is a topology rather than a misconfiguration:
// with no storage configured the API runs without the attachment surface, which is exactly what
// the auth and planning API tests want and what a deployment that has not provisioned a bucket
// yet gets. Startup says which mode it chose at WARN level, because "why is uploading returning
// 404" is a question best answered before it is asked.
//
// Everything else is required ONLY when Endpoint is set — validated together, so a half-configured
// bucket fails at startup rather than at the first upload.
type StorageConfig struct {
	Endpoint  string // host:port, no scheme
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// Enabled reports whether attachments are configured.
func (c StorageConfig) Enabled() bool { return c.Endpoint != "" }

// RedisConfig controls cross-instance fan-out and handshake ticket storage.
//
// URL is OPTIONAL, and its absence is a deployment topology rather than a misconfiguration:
// with no Redis the process runs single-instance, operations are fanned out in memory, and
// tickets live in memory. That is a legitimate way to run this application and it is what
// `docker compose up` gives by default.
//
// Running MORE THAN ONE instance without it is not legitimate, and cannot be detected from
// inside a single process: a ticket minted on one instance would be unredeemable on another,
// and neither instance's subscribers would see the other's writes. Startup logs the mode it
// chose at WARN level when Redis is absent, because "why did half the handshakes fail" is a
// question best answered before it is asked.
type RedisConfig struct {
	URL string
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level  string // debug | info | warn | error
	Format string // json | text
}

// HTTPConfig controls the API server.
type HTTPConfig struct {
	Addr string

	// ReadHeaderTimeout is the defence against Slowloris: a client that opens a connection
	// and dribbles headers forever holds a goroutine hostage without it.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// ShutdownTimeout bounds graceful shutdown: stop accepting new connections, let
	// in-flight requests finish, then force the issue.
	ShutdownTimeout time.Duration

	CORSAllowedOrigins []string
}

// DBConfig controls the Postgres connection pool.
type DBConfig struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// AuthConfig controls tokens and password hashing.
type AuthConfig struct {
	// JWTSecret signs access tokens. HS256 is used rather than RS256 because there is
	// exactly one verifier; asymmetric signing pays off when independent services verify
	// without holding the signing key, which is not this topology.
	JWTSecret string
	JWTIssuer string

	// AccessTokenTTL is short because access tokens are not revocable — the only bound on
	// a stolen one is its expiry.
	AccessTokenTTL time.Duration

	// RefreshTokenTTL is the lifetime of a single link in the rotation chain.
	RefreshTokenTTL time.Duration

	// SessionTTL is the absolute lifetime of a login regardless of rotation, so a
	// continuously refreshed session cannot live forever.
	SessionTTL time.Duration

	EmailVerifyTTL   time.Duration
	PasswordResetTTL time.Duration

	// WSTicketTTL is deliberately tiny: the ticket is redeemed within one round trip of
	// being issued, and a short window makes leakage into logs inert. Stage 2.
	WSTicketTTL time.Duration

	Argon2 Argon2Config
}

// Argon2Config holds Argon2id parameters. They are configurable so cost can be raised as
// hardware improves; hashes encode the parameters they were made with, so existing
// passwords keep verifying and get transparently upgraded on next login.
type Argon2Config struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// SMTPConfig controls outbound email.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// UseTLS is off locally (Mailpit) and must be on everywhere else.
	UseTLS bool
}

// AppConfig holds application-level URLs.
type AppConfig struct {
	// PublicBaseURL is the API's externally reachable base.
	PublicBaseURL string
	// WebBaseURL is the frontend's base, used to build verification and reset links.
	WebBaseURL string
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	parseErrors = nil

	cfg := &Config{
		Env: Environment(env("JUNTO_ENV", string(EnvDevelopment))),
		Log: LogConfig{
			Level:  env("LOG_LEVEL", "info"),
			Format: env("LOG_FORMAT", "json"),
		},
		HTTP: HTTPConfig{
			Addr:               env("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout:  envDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:        envDuration("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:       envDuration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:        envDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout:    envDuration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
			CORSAllowedOrigins: envList("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"}),
		},
		DB: DBConfig{
			URL:             os.Getenv("DATABASE_URL"),
			MaxConns:        int32(envInt("DB_MAX_CONNS", 20)),
			MinConns:        int32(envInt("DB_MIN_CONNS", 2)),
			MaxConnLifetime: envDuration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: envDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
			ConnectTimeout:  envDuration("DB_CONNECT_TIMEOUT", 10*time.Second),
		},
		Redis: RedisConfig{
			URL: os.Getenv("REDIS_URL"),
		},
		Storage: StorageConfig{
			Endpoint:  os.Getenv("STORAGE_ENDPOINT"),
			AccessKey: os.Getenv("STORAGE_ACCESS_KEY"),
			SecretKey: os.Getenv("STORAGE_SECRET_KEY"),
			Bucket:    env("STORAGE_BUCKET", "junto-attachments"),
			Region:    env("STORAGE_REGION", "us-east-1"),
			UseSSL:    envBool("STORAGE_USE_SSL", false),
		},
		Auth: AuthConfig{
			JWTSecret:        os.Getenv("JWT_SECRET"),
			JWTIssuer:        env("JWT_ISSUER", "junto"),
			AccessTokenTTL:   envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
			RefreshTokenTTL:  envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
			SessionTTL:       envDuration("SESSION_TTL", 90*24*time.Hour),
			EmailVerifyTTL:   envDuration("EMAIL_VERIFY_TTL", 24*time.Hour),
			PasswordResetTTL: envDuration("PASSWORD_RESET_TTL", time.Hour),
			WSTicketTTL:      envDuration("WS_TICKET_TTL", 30*time.Second),
			Argon2: Argon2Config{
				// OWASP's second recommended profile: 64 MiB, t=3, p=4.
				MemoryKiB:   uint32(envInt("ARGON2_MEMORY_KIB", 64*1024)),
				Iterations:  uint32(envInt("ARGON2_ITERATIONS", 3)),
				Parallelism: uint8(envInt("ARGON2_PARALLELISM", 4)),
				SaltLength:  uint32(envInt("ARGON2_SALT_LENGTH", 16)),
				KeyLength:   uint32(envInt("ARGON2_KEY_LENGTH", 32)),
			},
		},
		SMTP: SMTPConfig{
			Host:     env("SMTP_HOST", "localhost"),
			Port:     envInt("SMTP_PORT", 1025),
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     env("SMTP_FROM", "Junto <no-reply@junto.local>"),
			UseTLS:   envBool("SMTP_USE_TLS", false),
		},
		App: AppConfig{
			PublicBaseURL: env("PUBLIC_BASE_URL", "http://localhost:8080"),
			WebBaseURL:    env("WEB_BASE_URL", "http://localhost:3000"),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// MinJWTSecretLength is 32 bytes because HS256 uses a 256-bit HMAC key. A shorter secret
// gives less entropy than the algorithm's own output, which makes the signature the weak
// part of the system rather than the strong part.
const MinJWTSecretLength = 32

// Validate checks the configuration, collecting every problem rather than reporting the
// first — a misconfigured deploy should require one fix cycle, not five.
func (c *Config) Validate() error {
	// Malformed environment values are reported alongside semantic problems, so an operator
	// sees both "this value is unparseable" and "this value is out of range" in one pass.
	problems := append([]string(nil), parseErrors...)

	switch c.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		problems = append(problems, fmt.Sprintf("JUNTO_ENV must be development, staging or production (got %q)", c.Env))
	}

	if c.DB.URL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if c.DB.MaxConns < 1 {
		problems = append(problems, "DB_MAX_CONNS must be at least 1")
	}
	if c.DB.MinConns < 0 || c.DB.MinConns > c.DB.MaxConns {
		problems = append(problems, "DB_MIN_CONNS must be between 0 and DB_MAX_CONNS")
	}

	switch {
	case c.Auth.JWTSecret == "":
		problems = append(problems, "JWT_SECRET is required")
	case len(c.Auth.JWTSecret) < MinJWTSecretLength:
		problems = append(problems, fmt.Sprintf("JWT_SECRET must be at least %d bytes", MinJWTSecretLength))
	}

	if c.Auth.AccessTokenTTL <= 0 || c.Auth.AccessTokenTTL > time.Hour {
		// An access token cannot be revoked, so its TTL is the entire blast radius of a
		// stolen one. Anything beyond an hour makes refresh rotation pointless.
		problems = append(problems, "ACCESS_TOKEN_TTL must be > 0 and <= 1h")
	}
	if c.Auth.RefreshTokenTTL <= c.Auth.AccessTokenTTL {
		problems = append(problems, "REFRESH_TOKEN_TTL must exceed ACCESS_TOKEN_TTL")
	}
	if c.Auth.SessionTTL < c.Auth.RefreshTokenTTL {
		problems = append(problems, "SESSION_TTL must be at least REFRESH_TOKEN_TTL")
	}

	if c.Auth.Argon2.MemoryKiB < 19*1024 {
		// OWASP's floor for Argon2id. Below this the memory-hardness that justifies
		// choosing Argon2id over bcrypt stops applying.
		problems = append(problems, "ARGON2_MEMORY_KIB must be at least 19456 (19 MiB)")
	}
	if c.Auth.Argon2.Iterations < 1 {
		problems = append(problems, "ARGON2_ITERATIONS must be at least 1")
	}
	if c.Auth.Argon2.Parallelism < 1 {
		problems = append(problems, "ARGON2_PARALLELISM must be at least 1")
	}
	if c.Auth.Argon2.SaltLength < 16 {
		problems = append(problems, "ARGON2_SALT_LENGTH must be at least 16")
	}
	if c.Auth.Argon2.KeyLength < 32 {
		problems = append(problems, "ARGON2_KEY_LENGTH must be at least 32")
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("LOG_LEVEL must be debug, info, warn or error (got %q)", c.Log.Level))
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		problems = append(problems, fmt.Sprintf("LOG_FORMAT must be json or text (got %q)", c.Log.Format))
	}

	if c.SMTP.From == "" {
		problems = append(problems, "SMTP_FROM is required")
	}

	// A malformed URL is a hard failure while an ABSENT one is not: absent selects
	// single-instance mode deliberately, but a typo'd URL means the operator asked for
	// multi-instance and would otherwise get single-instance silently — the same reasoning
	// that makes a malformed duration abort startup rather than fall back to its default.
	if c.Redis.URL != "" &&
		!strings.HasPrefix(c.Redis.URL, "redis://") && !strings.HasPrefix(c.Redis.URL, "rediss://") {
		problems = append(problems, "REDIS_URL must be a redis:// or rediss:// URL")
	}

	// Storage credentials are validated TOGETHER with the endpoint, for the same reason. An
	// absent endpoint disables attachments deliberately; an endpoint with no credentials is an
	// operator who meant to enable them, and finding that out at the first upload instead of at
	// startup is the failure mode this exists to prevent.
	if c.Storage.Enabled() {
		if c.Storage.AccessKey == "" {
			problems = append(problems, "STORAGE_ACCESS_KEY is required when STORAGE_ENDPOINT is set")
		}
		if c.Storage.SecretKey == "" {
			problems = append(problems, "STORAGE_SECRET_KEY is required when STORAGE_ENDPOINT is set")
		}
		if c.Storage.Bucket == "" {
			problems = append(problems, "STORAGE_BUCKET is required when STORAGE_ENDPOINT is set")
		}
		if strings.Contains(c.Storage.Endpoint, "://") {
			problems = append(problems,
				"STORAGE_ENDPOINT must be host:port without a scheme; use STORAGE_USE_SSL for https")
		}
	}

	// Production-only rules. These are the settings that are convenient in development and
	// dangerous in production, so the environment gate is the whole point.
	if c.Env.IsProduction() {
		if !c.SMTP.UseTLS {
			problems = append(problems, "SMTP_USE_TLS must be true in production")
		}
		if !strings.HasPrefix(c.App.PublicBaseURL, "https://") {
			problems = append(problems, "PUBLIC_BASE_URL must use https in production")
		}
		if !strings.HasPrefix(c.App.WebBaseURL, "https://") {
			problems = append(problems, "WEB_BASE_URL must use https in production")
		}
		for _, o := range c.HTTP.CORSAllowedOrigins {
			if o == "*" {
				problems = append(problems, "CORS_ALLOWED_ORIGINS must not be * in production")
			}
			if strings.HasPrefix(o, "http://") {
				problems = append(problems, fmt.Sprintf("CORS origin %q must use https in production", o))
			}
		}
		if strings.Contains(c.DB.URL, "sslmode=disable") {
			problems = append(problems, "DATABASE_URL must not disable TLS in production")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// Environment parsing.
//
// A malformed value is an error, never a silent fallback to the default. Falling back is
// the tempting shortcut and it is wrong: `ACCESS_TOKEN_TTL=15` (missing the unit) would
// quietly run with the 15-minute default, and the operator who set it would have no way to
// tell their change had no effect. Validate() cannot catch that either, because the
// default is a legal value.
//
// parseErrors is package-level state only for the duration of a Load call; Load is not
// intended to run concurrently with itself, and there is no reason for it to.
var parseErrors []string

func recordParseError(key, value string, err error) {
	parseErrors = append(parseErrors, fmt.Sprintf("%s=%q could not be parsed: %v", key, value, err))
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		recordParseError(key, v, err)
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		recordParseError(key, v, err)
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		recordParseError(key, v, fmt.Errorf("%w (durations need a unit, e.g. 15m or 24h)", err))
		return fallback
	}
	return d
}

func envList(key string, fallback []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
