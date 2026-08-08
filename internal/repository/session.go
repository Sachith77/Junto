package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// SessionRepository is the Postgres implementation of domain.SessionRepository.
//
// Sessions and refresh tokens share one repository because they are one aggregate: reuse
// detection has to mark a token used and revoke its family atomically, and splitting them
// across two repositories would invite a caller to do half of that.
type SessionRepository struct{ base }

// NewSessionRepository builds a SessionRepository.
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{base{pool: pool}}
}

var _ domain.SessionRepository = (*SessionRepository)(nil)

func (r *SessionRepository) CreateSession(ctx context.Context, s *domain.AuthSession) error {
	row, err := r.q(ctx).CreateSession(ctx, sqlcgen.CreateSessionParams{
		ID:        s.ID,
		UserID:    s.UserID,
		UserAgent: s.UserAgent,
		Ip:        s.IP,
		ExpiresAt: s.ExpiresAt,
	})
	if err != nil {
		return mapError("session", err)
	}
	*s = *toDomainSession(row)
	return nil
}

func (r *SessionRepository) GetSession(ctx context.Context, id domain.ID) (*domain.AuthSession, error) {
	row, err := r.q(ctx).GetSession(ctx, id)
	if err != nil {
		return nil, mapError("session", err)
	}
	return toDomainSession(row), nil
}

func (r *SessionRepository) ListActiveSessions(ctx context.Context, userID domain.ID) ([]*domain.AuthSession, error) {
	rows, err := r.q(ctx).ListActiveSessions(ctx, sqlcgen.ListActiveSessionsParams{
		UserID: userID,
		Now:    time.Now().UTC(),
	})
	if err != nil {
		return nil, mapError("session", err)
	}
	out := make([]*domain.AuthSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainSession(row))
	}
	return out, nil
}

func (r *SessionRepository) TouchSession(ctx context.Context, id domain.ID, at time.Time) error {
	// A zero row count here means the session was revoked between authenticating and
	// recording the touch. That is a benign race, not an error: the request that triggered
	// it will fail its own authorization check anyway.
	_, err := r.q(ctx).TouchSession(ctx, sqlcgen.TouchSessionParams{LastUsedAt: at, ID: id})
	return mapError("session", err)
}

func (r *SessionRepository) RevokeSession(ctx context.Context, id domain.ID, at time.Time, reason string) error {
	// Also tolerates zero rows, for the same reason the query is written to be idempotent:
	// revoking an already-revoked session is a no-op that must preserve the ORIGINAL reason.
	// A session killed for token reuse must never be relabelled as a routine logout.
	_, err := r.q(ctx).RevokeSession(ctx, sqlcgen.RevokeSessionParams{
		RevokedAt:     &at,
		RevokedReason: &reason,
		ID:            id,
	})
	return mapError("session", err)
}

func (r *SessionRepository) RevokeAllSessions(ctx context.Context, userID domain.ID, at time.Time, reason string) error {
	_, err := r.q(ctx).RevokeAllSessions(ctx, sqlcgen.RevokeAllSessionsParams{
		RevokedAt:     &at,
		RevokedReason: &reason,
		UserID:        userID,
	})
	return mapError("session", err)
}

func (r *SessionRepository) CreateRefreshToken(ctx context.Context, t *domain.RefreshToken) error {
	row, err := r.q(ctx).CreateRefreshToken(ctx, sqlcgen.CreateRefreshTokenParams{
		ID:        t.ID,
		SessionID: t.SessionID,
		TokenHash: t.TokenHash,
		ExpiresAt: t.ExpiresAt,
	})
	if err != nil {
		return mapError("refresh token", err)
	}
	*t = *toDomainRefreshToken(row)
	return nil
}

func (r *SessionRepository) GetRefreshTokenByHash(ctx context.Context, hash []byte) (*domain.RefreshToken, error) {
	row, err := r.q(ctx).GetRefreshTokenByHash(ctx, hash)
	if err != nil {
		return nil, mapError("refresh token", err)
	}
	return toDomainRefreshToken(row), nil
}

// MarkRefreshTokenUsed consumes a token as part of rotation.
//
// Returns ErrTokenConsumed when the token was already used. That distinction is the entire
// point: it is not a generic failure but the signal that triggers family-wide revocation,
// because the only two explanations are theft or a client bug.
func (r *SessionRepository) MarkRefreshTokenUsed(ctx context.Context, id domain.ID, at time.Time, replacedBy domain.ID) error {
	n, err := r.q(ctx).MarkRefreshTokenUsed(ctx, sqlcgen.MarkRefreshTokenUsedParams{
		UsedAt:     &at,
		ReplacedBy: &replacedBy,
		ID:         id,
	})
	if err != nil {
		return mapError("refresh token", err)
	}
	if n == 0 {
		// The `used_at IS NULL` guard in the query makes this atomic: if two requests race
		// with the same token, exactly one wins and the loser lands here.
		return domain.ErrTokenConsumed
	}
	return nil
}

func (r *SessionRepository) DeleteExpiredRefreshTokens(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.q(ctx).DeleteExpiredRefreshTokens(ctx, before)
	if err != nil {
		return 0, mapError("refresh token", err)
	}
	return n, nil
}

// DeleteExpiredSessions purges dead sessions. Not part of the domain port — it is
// housekeeping invoked by a maintenance task, not by business logic.
func (r *SessionRepository) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.q(ctx).DeleteExpiredSessions(ctx, before)
	if err != nil {
		return 0, mapError("session", err)
	}
	return n, nil
}
