package repository

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/testsupport"
)

// Repository tests run against a real Postgres 16, started by testcontainers.
//
// Mocking the database here would test nothing that matters. The interesting behaviour of
// this layer *is* the constraints, the partial indexes, the composite foreign key and the
// row-level concurrency semantics — none of which a mock can reproduce, and all of which are
// exactly what would break silently.
//
// Cost: these tests need Docker and take a few seconds to start. `go test -short` skips them.

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	// testing.Short() reads a flag, and TestMain runs before the testing package parses
	// them. Without this, it panics with "Short called before Parse".
	flag.Parse()

	if testing.Short() {
		log.Println("skipping repository tests in -short mode (they require Docker)")
		return
	}

	code, err := runSuite(m)
	if err != nil {
		log.Printf("repository test setup failed: %v", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	pg, err := testsupport.StartPostgres(context.Background())
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := pg.Close(); err != nil {
			log.Printf("closing test database: %v", err)
		}
	}()

	testPool = pg.Pool
	return m.Run(), nil
}

// txContext returns a context carrying a transaction that is rolled back when the test ends.
//
// This is the isolation mechanism for the whole suite. Because repositories resolve their
// queries from the ambient transaction, every write a test performs is invisible to other
// tests and vanishes at cleanup — no truncation between tests, no ordering dependencies, and
// no schema rebuild per test.
//
// It also means TxManager.WithinTx called inside a test opens a savepoint rather than a new
// transaction, which is precisely the nesting behaviour production code relies on.
//
// Tests that need genuine concurrency cannot use this (two goroutines sharing one transaction
// are serialised by pgx, so the race under test would not occur). Those use concurrentCtx.
func txContext(t *testing.T) context.Context {
	t.Helper()

	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("beginning test transaction: %v", err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rolling back test transaction: %v", err)
		}
	})
	return ContextWithTx(context.Background(), tx)
}

// concurrentCtx returns a plain context for tests that need separate real connections.
//
// Writes made under it are COMMITTED, so such tests must clean up after themselves. They use
// freshly generated UUIDs throughout, so they cannot collide with each other even when run in
// parallel.
func concurrentCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}
