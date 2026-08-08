// Package testsupport provides shared test infrastructure.
//
// It is a normal (non-test) package so that test files in several packages can use it —
// Go does not let one package import another's _test.go files. Nothing under cmd/ imports
// it, so it never reaches a production binary.
package testsupport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/junto/junto/migrations"
)

// Postgres is a running test database.
type Postgres struct {
	DSN       string
	Pool      *pgxpool.Pool
	terminate func() error
}

// StartPostgres launches Postgres 16, applies the embedded migrations, and opens a pool.
//
// The image is pinned to match production exactly. Testing against a different major version
// would defeat the purpose: ON DELETE SET NULL (day_id) needs PG15+, and a version skew would
// either fail confusingly or hide a real incompatibility.
func StartPostgres(ctx context.Context) (*Postgres, error) {
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("junto_test"),
		tcpostgres.WithUsername("junto"),
		tcpostgres.WithPassword("junto"),
		testcontainers.WithWaitStrategy(
			// Occurrence 2 because Postgres logs "ready to accept connections" once for the
			// bootstrap instance during initdb and again for the real one. Waiting for the
			// first would connect to a server that is about to be shut down.
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("starting postgres container: %w", err)
	}

	terminate := func() error { return testcontainers.TerminateContainer(container) }

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = terminate()
		return nil, fmt.Errorf("building connection string: %w", err)
	}

	if err := ApplyMigrations(dsn); err != nil {
		_ = terminate()
		return nil, fmt.Errorf("applying migrations: %w", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = terminate()
		return nil, fmt.Errorf("opening pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = terminate()
		return nil, fmt.Errorf("pinging: %w", err)
	}

	return &Postgres{DSN: dsn, Pool: pool, terminate: terminate}, nil
}

// Close shuts down the pool and the container.
func (p *Postgres) Close() error {
	if p == nil {
		return nil
	}
	if p.Pool != nil {
		p.Pool.Close()
	}
	return p.terminate()
}

// ApplyMigrations runs the same embedded migrations the application ships with.
//
// Not a hand-maintained test schema: a separate DDL file for tests drifts from production and
// turns every test into a check that the test schema matches itself.
func ApplyMigrations(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, NormalizeDSN(dsn))
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// NormalizeDSN rewrites a postgres:// URL to the scheme golang-migrate registers its pgx/v5
// driver under, so DATABASE_URL can stay in the standard form everything else uses.
func NormalizeDSN(dsn string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(dsn, prefix) {
			return "pgx5://" + strings.TrimPrefix(dsn, prefix)
		}
	}
	return dsn
}
