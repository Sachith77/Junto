// Package repository contains the Postgres implementations of the domain's ports.
//
// This is an OUTER layer: it depends on internal/domain, and internal/domain must never
// depend on it. Every type in this package either implements a domain interface or exists
// to translate between Postgres and domain types.
//
// The one rule worth restating: internal/service must never see a sqlcgen type. Everything
// generated is mapped to a domain struct before it leaves this package. That mapping is
// real, boring boilerplate, and it is the price of "business logic independent of the
// database" being a true statement rather than a diagram.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// DBTX is the subset of pgx that generated queries need. Both *pgxpool.Pool and pgx.Tx
// satisfy it, which is what lets the same repository method run inside or outside a
// transaction with no change in signature.
type DBTX = sqlcgen.DBTX

// txKey is the context key carrying the ambient transaction.
//
// An unexported struct type rather than a string: it cannot collide with any other
// package's key, and no code outside this package can extract the transaction and start
// issuing raw SQL against it.
type txKey struct{}

// TxManager implements domain.TxManager on a pgx pool.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager builds a TxManager.
func NewTxManager(pool *pgxpool.Pool) *TxManager { return &TxManager{pool: pool} }

var _ domain.TxManager = (*TxManager)(nil)

// WithinTx runs fn inside a transaction, committing when it returns nil and rolling back
// otherwise.
//
// The transaction travels on the context rather than as a parameter. That is the trade
// discussed in domain.TxManager: services stay free of pgx types, at the cost of the
// transaction being implicit. The alternatives are worse — either *pgx.Tx appears in
// service signatures (dragging the driver into the layer that must not know it exists), or
// every repository grows a duplicate WithTx variant.
//
// A nested call opens a SAVEPOINT on the existing transaction rather than a second physical
// transaction. This matters: without it, a failing inner unit of work would either roll back
// its caller's writes too, or silently commit alongside them. With savepoints, an inner
// failure unwinds exactly the inner work, which is what a caller composing two operations
// would reasonably expect.
//
// The savepoint is also the ONLY way to recover from a constraint violation mid-transaction.
// Postgres aborts an entire transaction as soon as any statement fails — every subsequent
// command returns SQLSTATE 25P02 until rollback. So a service that wants to catch a
// duplicate-key error and take a different path must run the risky statement in a nested
// WithinTx; catching the error without one leaves it holding a dead transaction and a
// well-formatted error it can do nothing with. TestConstraintViolationInNestedTxIsRecoverable
// pins this behaviour.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	var tx pgx.Tx

	if outer, ok := txFromContext(ctx); ok {
		tx, err = outer.Begin(ctx) // pgx implements a nested Begin as a savepoint
		if err != nil {
			return fmt.Errorf("opening savepoint: %w", err)
		}
	} else {
		tx, err = m.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning transaction: %w", err)
		}
	}

	// A panic inside fn must not leave a transaction open holding locks. Roll back and
	// re-panic so the original stack still reaches the recovery handler.
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		// context.WithoutCancel: if fn failed *because* the context was cancelled, a
		// rollback on that same context would itself fail and leak the transaction until
		// the connection is reaped.
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rolling back: %w", rbErr))
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

func txFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// ContextWithTx injects a transaction into a context.
//
// Exported solely so repository tests can wrap each test in a transaction that is rolled
// back at the end, giving perfect isolation without recreating the schema per test. It has
// no production use — application code reaches transactions through WithinTx.
func ContextWithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// base carries what every repository needs: the pool, and the rule for choosing between the
// pool and an ambient transaction.
type base struct {
	pool *pgxpool.Pool
}

// q returns generated queries bound to the ambient transaction if there is one, and to the
// pool otherwise. Every repository method starts with this, which is why none of them need
// to know whether they are being called inside a transaction.
func (b base) q(ctx context.Context) *sqlcgen.Queries {
	if tx, ok := txFromContext(ctx); ok {
		return sqlcgen.New(tx)
	}
	return sqlcgen.New(b.pool)
}
