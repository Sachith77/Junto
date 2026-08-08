// Command migrate applies or rolls back the embedded database migrations.
//
// Migrations are embedded in the binary rather than read from disk, so a deployed artifact
// can always migrate itself to the schema it was compiled against. The most common way
// migration tooling fails in production is "the .sql files did not get copied"; this makes
// that failure impossible.
//
// Usage:
//
//	migrate up            apply all pending migrations
//	migrate down          roll back one migration
//	migrate version       print current version and dirty state
//	migrate force <n>     clear the dirty flag at version n (recovery only)
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/joho/godotenv"

	"github.com/junto/junto/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// A missing .env is not an error: in production, configuration comes from the
	// environment directly and no file exists.
	_ = godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	if len(os.Args) < 2 {
		return errors.New("usage: migrate <up|down|version|force N>")
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("loading embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, normalizeURL(dbURL))
	if err != nil {
		return fmt.Errorf("opening migrator: %w", err)
	}
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			slog.Warn("closing migrator", "source_error", srcErr, "database_error", dbErr)
		}
	}()

	switch cmd := os.Args[1]; cmd {
	case "up":
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("applying migrations: %w", err)
		}
		return printVersion(m, "migrations applied")

	case "down":
		// One step at a time, never `Down()` to zero. A single mistyped command should not
		// be able to drop the entire schema.
		if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("rolling back: %w", err)
		}
		return printVersion(m, "rolled back one migration")

	case "version":
		return printVersion(m, "current schema")

	case "force":
		if len(os.Args) < 3 {
			return errors.New("usage: migrate force <version>")
		}
		v, err := strconv.Atoi(os.Args[2])
		if err != nil {
			return fmt.Errorf("invalid version %q: %w", os.Args[2], err)
		}
		if err := m.Force(v); err != nil {
			return fmt.Errorf("forcing version: %w", err)
		}
		return printVersion(m, "version forced")

	default:
		return fmt.Errorf("unknown command %q: expected up, down, version or force", cmd)
	}
}

func printVersion(m *migrate.Migrate, msg string) error {
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		slog.Info(msg, "version", "none")
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading version: %w", err)
	}
	if dirty {
		// Dirty means a migration failed part-way. Continuing would apply later migrations
		// on top of an unknown schema state, so this is a hard stop.
		return fmt.Errorf("schema is DIRTY at version %d: a migration failed part-way. "+
			"Inspect the database, fix it by hand, then run `migrate force %d`", v, v)
	}
	slog.Info(msg, "version", v)
	return nil
}

// normalizeURL rewrites a postgres:// URL to the pgx/v5 scheme golang-migrate registers
// its driver under, so DATABASE_URL can stay in the standard form everything else uses.
func normalizeURL(url string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if len(url) > len(prefix) && url[:len(prefix)] == prefix {
			return "pgx5://" + url[len(prefix):]
		}
	}
	return url
}
