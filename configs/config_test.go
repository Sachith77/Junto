package configs

import (
	"strings"
	"testing"
	"time"
)

// The configuration layer is where a deployment mistake becomes a security incident, so
// these tests are about the rules that only bite in production — the ones that are
// convenient in development and dangerous outside it.

// devEnv is a minimal valid environment. Tests copy it and break one thing at a time, so a
// failure names exactly one cause.
func devEnv() map[string]string {
	return map[string]string{
		"JUNTO_ENV":    "development",
		"DATABASE_URL": "postgres://junto:pw@localhost:5433/junto?sslmode=disable",
		"JWT_SECRET":   strings.Repeat("k", 48),
	}
}

func loadWith(t *testing.T, envs map[string]string) (*Config, error) {
	t.Helper()
	// t.Setenv restores the previous value automatically and fails if the test is parallel,
	// which is what we want: these tests mutate global process state.
	for k, v := range envs {
		t.Setenv(k, v)
	}
	return Load()
}

func TestLoadAcceptsMinimalDevelopmentConfig(t *testing.T) {
	cfg, err := loadWith(t, devEnv())
	if err != nil {
		t.Fatalf("a minimal development config should load: %v", err)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("expected the default listen address, got %q", cfg.HTTP.Addr)
	}
	if cfg.Auth.AccessTokenTTL != 15*time.Minute {
		t.Errorf("expected the default access TTL, got %v", cfg.Auth.AccessTokenTTL)
	}
	if cfg.Auth.Argon2.MemoryKiB != 64*1024 {
		t.Errorf("expected the default Argon2 memory, got %d", cfg.Auth.Argon2.MemoryKiB)
	}
}

func TestSecretsHaveNoDefaults(t *testing.T) {
	// The classic production incident is a service that booted happily with a development
	// signing key. Absent secrets must abort startup, never fall back.
	for _, missing := range []string{"JWT_SECRET", "DATABASE_URL"} {
		t.Run("missing "+missing, func(t *testing.T) {
			env := devEnv()
			env[missing] = ""
			_, err := loadWith(t, env)
			if err == nil {
				t.Fatalf("startup must fail when %s is absent", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("the error should name %s, got: %v", missing, err)
			}
		})
	}
}

func TestShortJWTSecretRejected(t *testing.T) {
	// HS256 uses a 256-bit HMAC key. A shorter secret makes the signature the weak part of
	// the system rather than the strong part.
	env := devEnv()
	env["JWT_SECRET"] = strings.Repeat("k", MinJWTSecretLength-1)
	if _, err := loadWith(t, env); err == nil {
		t.Fatal("a JWT secret under 32 bytes must be rejected")
	}
}

func TestMalformedValuesAreErrorsNotSilentFallbacks(t *testing.T) {
	// This is the subtle one. `ACCESS_TOKEN_TTL=15` (no unit) must not quietly run with the
	// 15-minute default: the operator who set it would have no way to tell their change had
	// no effect, and Validate() cannot catch it because the default is a legal value.
	env := devEnv()
	env["ACCESS_TOKEN_TTL"] = "15"
	_, err := loadWith(t, env)
	if err == nil {
		t.Fatal("a duration without a unit must be reported, not silently defaulted")
	}
	if !strings.Contains(err.Error(), "ACCESS_TOKEN_TTL") {
		t.Errorf("the error should name the offending variable, got: %v", err)
	}

	env = devEnv()
	env["DB_MAX_CONNS"] = "twenty"
	if _, err := loadWith(t, env); err == nil {
		t.Fatal("a non-numeric integer must be reported")
	}
}

func TestAllProblemsReportedTogether(t *testing.T) {
	// A misconfigured deploy should require one fix cycle, not five.
	env := devEnv()
	env["JWT_SECRET"] = "short"
	env["LOG_LEVEL"] = "verbose"
	env["ACCESS_TOKEN_TTL"] = "nonsense"

	_, err := loadWith(t, env)
	if err == nil {
		t.Fatal("expected failure")
	}
	msg := err.Error()
	for _, want := range []string{"JWT_SECRET", "LOG_LEVEL", "ACCESS_TOKEN_TTL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected every problem reported at once; %s missing from: %v", want, msg)
		}
	}
}

func TestTokenTTLRelationships(t *testing.T) {
	t.Run("access token TTL is bounded", func(t *testing.T) {
		// An access token cannot be revoked, so its TTL is the entire blast radius of a
		// stolen one. Anything beyond an hour makes refresh rotation pointless.
		env := devEnv()
		env["ACCESS_TOKEN_TTL"] = "24h"
		if _, err := loadWith(t, env); err == nil {
			t.Error("an access token TTL over 1h must be rejected")
		}
	})

	t.Run("refresh must outlive access", func(t *testing.T) {
		env := devEnv()
		env["ACCESS_TOKEN_TTL"] = "15m"
		env["REFRESH_TOKEN_TTL"] = "5m"
		if _, err := loadWith(t, env); err == nil {
			t.Error("a refresh TTL shorter than the access TTL is incoherent and must be rejected")
		}
	})

	t.Run("session must outlive refresh", func(t *testing.T) {
		env := devEnv()
		env["REFRESH_TOKEN_TTL"] = "720h"
		env["SESSION_TTL"] = "1h"
		if _, err := loadWith(t, env); err == nil {
			t.Error("a session shorter than its refresh tokens is incoherent and must be rejected")
		}
	})
}

func TestArgon2FloorEnforced(t *testing.T) {
	// Below OWASP's 19 MiB floor, the memory-hardness that justifies choosing Argon2id
	// over bcrypt stops applying.
	env := devEnv()
	env["ARGON2_MEMORY_KIB"] = "1024"
	if _, err := loadWith(t, env); err == nil {
		t.Fatal("an Argon2 memory cost under the OWASP floor must be rejected")
	}
}

func TestProductionOnlyRules(t *testing.T) {
	prodEnv := func() map[string]string {
		return map[string]string{
			"JUNTO_ENV":            "production",
			"DATABASE_URL":         "postgres://junto:pw@db.internal:5432/junto?sslmode=require",
			"JWT_SECRET":           strings.Repeat("k", 48),
			"SMTP_USE_TLS":         "true",
			"PUBLIC_BASE_URL":      "https://api.junto.app",
			"WEB_BASE_URL":         "https://junto.app",
			"CORS_ALLOWED_ORIGINS": "https://junto.app",
		}
	}

	if _, err := loadWith(t, prodEnv()); err != nil {
		t.Fatalf("a correct production config should load: %v", err)
	}

	cases := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"plaintext SMTP", "SMTP_USE_TLS", "false", "SMTP_USE_TLS"},
		{"http api url", "PUBLIC_BASE_URL", "http://api.junto.app", "PUBLIC_BASE_URL"},
		{"http web url", "WEB_BASE_URL", "http://junto.app", "WEB_BASE_URL"},
		{"wildcard CORS", "CORS_ALLOWED_ORIGINS", "*", "CORS"},
		{"http CORS origin", "CORS_ALLOWED_ORIGINS", "http://junto.app", "https"},
		{"unencrypted database", "DATABASE_URL", "postgres://j:p@db/j?sslmode=disable", "TLS"},
		// D105: the development convenience that bypasses email verification must not be
		// merely ignored in production — an operator who set it believes signups are
		// auto-verified, and booting anyway with it silently off is the worse failure (D19).
		{"auto-verified email", "AUTH_AUTO_VERIFY_EMAIL", "true", "AUTH_AUTO_VERIFY_EMAIL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := prodEnv()
			env[tc.key] = tc.value
			_, err := loadWith(t, env)
			if err == nil {
				t.Fatalf("%s must be rejected in production", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected the error to mention %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestDevelopmentIsNotHeldToProductionRules(t *testing.T) {
	// The same settings that abort a production boot must stay convenient locally,
	// otherwise the environment gate has no point.
	env := devEnv()
	env["SMTP_USE_TLS"] = "false"
	env["PUBLIC_BASE_URL"] = "http://localhost:8080"
	env["CORS_ALLOWED_ORIGINS"] = "http://localhost:3000"
	if _, err := loadWith(t, env); err != nil {
		t.Fatalf("plaintext local development must remain valid: %v", err)
	}
}

func TestUnknownEnvironmentRejected(t *testing.T) {
	env := devEnv()
	env["JUNTO_ENV"] = "prod" // a typo that would otherwise silently skip every production rule
	if _, err := loadWith(t, env); err == nil {
		t.Fatal("an unrecognised JUNTO_ENV must be rejected rather than treated as non-production")
	}
}

func TestCORSOriginListParsing(t *testing.T) {
	env := devEnv()
	env["CORS_ALLOWED_ORIGINS"] = " http://a.test , http://b.test ,, "
	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	want := []string{"http://a.test", "http://b.test"}
	if len(cfg.HTTP.CORSAllowedOrigins) != len(want) {
		t.Fatalf("got %v, want %v", cfg.HTTP.CORSAllowedOrigins, want)
	}
	for i, w := range want {
		if cfg.HTTP.CORSAllowedOrigins[i] != w {
			t.Errorf("origin %d = %q, want %q", i, cfg.HTTP.CORSAllowedOrigins[i], w)
		}
	}
}
