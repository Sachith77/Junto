package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// OpLogRepository is the Postgres implementation of domain.OpLogRepository.
//
// Note what is absent: there is no Update and no Delete, here or in queries/ops.sql. The log
// is immutable, and that is enforced by the mutating code not existing rather than by a
// comment asking for restraint. tests/schema_verify.sql asserts the same thing from the
// database's side.
type OpLogRepository struct{ base }

// NewOpLogRepository builds an OpLogRepository.
func NewOpLogRepository(pool *pgxpool.Pool) *OpLogRepository {
	return &OpLogRepository{base{pool: pool}}
}

var _ domain.OpLogRepository = (*OpLogRepository)(nil)

// maxOpBatch bounds a single resync page. A client that has been away long enough to need
// more pages a second time; an unbounded query would let one reconnect pull an arbitrarily
// large result set into memory.
const maxOpBatch = 1000

// Append writes one committed operation.
//
// It must be called inside the same transaction as the mutation it records. That co-location
// is the whole reason "everything since seq N" is complete for REST-originated writes as well
// as WebSocket ones (Rule 3, D1) — if the log were written outside the transaction, a
// committed change with a failed log append would be invisible to every reconnecting client.
func (r *OpLogRepository) Append(ctx context.Context, op *domain.Op) error {
	row, err := r.q(ctx).AppendOp(ctx, sqlcgen.AppendOpParams{
		ID:         op.ID,
		TripID:     op.TripID,
		Seq:        op.Seq,
		ActorID:    op.ActorID,
		Kind:       string(op.Kind),
		EntityID:   op.EntityID,
		Fields:     op.Fields,
		Payload:    op.Payload,
		ClientOpID: op.ClientOpID,
		CauseOpID:  op.CauseOpID,
	})
	if err != nil {
		// A duplicate (trip_id, seq) means two writers allocated the same sequence number,
		// which can only happen if someone bypassed LockForWrite. It surfaces as
		// ErrAlreadyExists rather than being retried, because retrying would paper over a
		// broken invariant instead of failing loudly at the place it broke.
		return mapError("op", err)
	}
	*op = *toDomainOp(row)
	return nil
}

// ListSince returns a trip's operations after seq, in order — the resync scan.
func (r *OpLogRepository) ListSince(ctx context.Context, tripID domain.ID, seq int64, limit int) ([]domain.Op, error) {
	if limit <= 0 || limit > maxOpBatch {
		limit = maxOpBatch
	}
	rows, err := r.q(ctx).ListOpsSince(ctx, sqlcgen.ListOpsSinceParams{
		TripID:    tripID,
		AfterSeq:  seq,
		PageLimit: int32(limit),
	})
	if err != nil {
		return nil, mapError("op", err)
	}
	out := make([]domain.Op, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toDomainOp(row))
	}
	return out, nil
}

// FindByClientOpID returns the operation a client already committed under this id, or
// ErrNotFound.
//
// This is what makes replay after a lost acknowledgement safe. Field-mask sets happen to be
// idempotent on their own, but a create is not, and neither is anything that expands into a
// cascade (D63) — so idempotency is established by identity rather than assumed from the
// shape of the operation.
func (r *OpLogRepository) FindByClientOpID(ctx context.Context, tripID, clientOpID domain.ID) (*domain.Op, error) {
	row, err := r.q(ctx).GetOpByClientOpID(ctx, sqlcgen.GetOpByClientOpIDParams{
		TripID:     tripID,
		ClientOpID: &clientOpID,
	})
	if err != nil {
		return nil, mapError("op", err)
	}
	return toDomainOp(row), nil
}
