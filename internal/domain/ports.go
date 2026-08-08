package domain

import (
	"context"
	"time"
)

// Ports: the interfaces the domain requires of the outside world.
//
// They are declared here, in the innermost layer, and implemented further out. That
// inversion is what "business logic independent of HTTP, Postgres and WebSockets" actually
// means in practice — internal/service depends on these types and never on pgx, and
// internal/repository depends on the domain rather than the other way round.
//
// Two rules keep this honest:
//   - No method here mentions a driver type, a SQL construct, or an HTTP concept.
//   - internal/service must never import a sqlc-generated struct. The repository layer maps
//     sqlc rows to these domain types. That mapping is real, boring boilerplate; it is the
//     price of the architectural claim and it is paid deliberately.

// TxManager runs work inside a database transaction.
//
// The transaction travels on the context, so repository methods keep an unchanged
// signature whether or not they are inside one, and no service method has to accept a
// *pgx.Tx — which would drag the driver straight into the layer that must not know it exists.
//
// Accepted trade-off: the transaction is an implicit context value rather than an explicit
// parameter, which is less obvious to a reader. The alternatives are worse — either the
// driver type leaks into service signatures, or every repository grows a duplicate
// WithTx variant.
//
// Nested calls join the existing transaction rather than opening a second one.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// PasswordHasher hashes and verifies passwords.
//
// A port rather than a direct call because Argon2id is intentionally slow: a test suite
// that hashes for real spends most of its time burning 64MB of memory per call. Tests
// substitute a fast fake; production wires Argon2id.
type PasswordHasher interface {
	// Hash returns an encoded hash string that embeds its own parameters, so the cost can
	// be raised later without invalidating existing hashes.
	Hash(ctx context.Context, plaintext string) (string, error)

	// Verify reports whether plaintext matches the encoded hash. needsRehash is true when
	// the hash was produced with parameters weaker than the current configuration, which
	// lets a successful login transparently upgrade the stored hash.
	Verify(ctx context.Context, encodedHash, plaintext string) (ok bool, needsRehash bool, err error)
}

// AccessTokenClaims is the verified content of an access token.
//
// Deliberately minimal. An access token cannot be revoked, so anything embedded in it is
// frozen for its whole lifetime — a role or capability set carried here would keep granting
// access after it was taken away. Authorization is therefore always resolved from the
// database at request time; the token only answers "who is this, and on which session".
type AccessTokenClaims struct {
	UserID    ID
	SessionID ID
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// TokenIssuer mints and verifies access tokens.
//
// A port rather than a direct dependency because JWT is a credential *format* — a transport
// concern. The domain's requirement is "a short-lived, verifiable statement of identity",
// which a different format could satisfy without any change here.
type TokenIssuer interface {
	Issue(userID, sessionID ID, now time.Time) (token string, expiresAt time.Time, err error)
	// Parse verifies the signature and expiry, returning ErrTokenInvalid or ErrTokenExpired.
	Parse(token string) (*AccessTokenClaims, error)
}

// EmailMessage is a message to send. Both bodies are supplied; the sender decides how to
// present them.
type EmailMessage struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

// EmailSender delivers email. The domain knows that verification and reset links must
// reach a user; it does not know that SMTP exists.
type EmailSender interface {
	Send(ctx context.Context, msg EmailMessage) error
}

// FileInfo describes a stored object.
type FileInfo struct {
	Key         string
	SizeBytes   int64
	ContentType string
	ChecksumMD5 string
	ModifiedAt  time.Time
}

// FileStorage stores attachment content.
//
// Deliberately the same shape of port as EmailSender: an interface here, adapters outside,
// and nothing in internal/service that knows the storage is S3-shaped. The domain knows that
// a photo must be storable and retrievable; it does not know about buckets, regions,
// signature versions, or MinIO.
//
// # Why presigned URLs rather than an Upload(io.Reader) method
//
// A method taking a reader would force every byte through the API process — memory, CPU and
// bandwidth spent proxying data that could go straight to storage. Instead the service hands
// the client a time-limited signed URL and the browser uploads directly.
//
// The cost of that choice, stated plainly: the API never sees the content, and a presigned
// PUT cannot enforce a size limit. That is why Stat exists and why attachments have a
// pending -> ready lifecycle — the server verifies size and type AFTER the upload lands and
// deletes anything that fails. Any implementation of this interface must make Stat cheap,
// because it is on the confirm path of every upload.
type FileStorage interface {
	// PresignUpload returns a URL the client may PUT to exactly once, within ttl.
	//
	// contentType is bound into the signature so the client cannot substitute a different
	// one after the fact — otherwise an "image/png" upload could arrive as text/html and be
	// served back to a browser.
	PresignUpload(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)

	// PresignDownload returns a time-limited read URL. Attachments are private; there is no
	// public-bucket path, so every read is signed.
	PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error)

	// Stat returns metadata for a stored object, or ErrNotFound if it was never uploaded.
	Stat(ctx context.Context, key string) (FileInfo, error)

	// Delete removes an object. Idempotent: deleting an absent key is not an error, because
	// the sweeper and the failed-upload path both race with clients that may already be gone.
	Delete(ctx context.Context, key string) error
}

// UserRepository persists identities.
type UserRepository interface {
	Create(ctx context.Context, u *User) error
	GetByID(ctx context.Context, id ID) (*User, error)
	// GetByEmail looks up by the normalised form. Callers pass raw input; the
	// implementation applies NormalizeEmail so the lookup can never disagree with the
	// lower(email) unique index.
	GetByEmail(ctx context.Context, email string) (*User, error)
	// Update applies an optimistic-concurrency update, returning ErrVersionConflict when
	// the stored version no longer matches u.Version.
	Update(ctx context.Context, u *User) error
	SoftDelete(ctx context.Context, id ID, at time.Time) error
}

// SessionRepository persists sessions and their refresh-token chains.
//
// Sessions and refresh tokens share one interface because they are one aggregate: a token
// is meaningless outside its family, and reuse detection has to revoke both together, in a
// single transaction.
type SessionRepository interface {
	CreateSession(ctx context.Context, s *AuthSession) error
	GetSession(ctx context.Context, id ID) (*AuthSession, error)
	ListActiveSessions(ctx context.Context, userID ID) ([]*AuthSession, error)
	TouchSession(ctx context.Context, id ID, at time.Time) error
	RevokeSession(ctx context.Context, id ID, at time.Time, reason string) error
	// RevokeAllSessions is what a password reset calls. A reset that leaves an attacker's
	// session alive is worse than useless.
	RevokeAllSessions(ctx context.Context, userID ID, at time.Time, reason string) error

	CreateRefreshToken(ctx context.Context, t *RefreshToken) error
	GetRefreshTokenByHash(ctx context.Context, hash []byte) (*RefreshToken, error)
	MarkRefreshTokenUsed(ctx context.Context, id ID, at time.Time, replacedBy ID) error
	DeleteExpiredRefreshTokens(ctx context.Context, before time.Time) (int64, error)
}

// UserTokenRepository persists single-use email verification and password reset tokens.
type UserTokenRepository interface {
	Create(ctx context.Context, t *UserToken) error
	GetByHash(ctx context.Context, hash []byte) (*UserToken, error)
	Consume(ctx context.Context, id ID, at time.Time) error
	// ConsumeAllForPurpose invalidates outstanding tokens of a purpose. Issuing a new
	// reset link must retire the previous one, or every link ever mailed stays live.
	ConsumeAllForPurpose(ctx context.Context, userID ID, purpose TokenPurpose, at time.Time) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// TripRepository persists trips, and carries the per-trip sequencer the sync engine orders by.
type TripRepository interface {
	Create(ctx context.Context, t *Trip) error
	GetByID(ctx context.Context, id ID) (*Trip, error)
	Update(ctx context.Context, t *Trip) error
	SoftDelete(ctx context.Context, id ID, at time.Time, expectedVersion int) error
	// ListForUser returns trips the user is a member of.
	ListForUser(ctx context.Context, userID ID, page PageRequest) (Page[*Trip], error)

	// LockForWrite takes the trip's row lock and must be the FIRST statement of every write
	// transaction in that trip (D60).
	//
	// It is what serializes writers within a trip BEFORE any of them reads an entity, and
	// that ordering is the whole conflict model: it makes read-modify-write safe (so the
	// Stage 1 repository layer is reused unchanged) and makes field-level merge fall out for
	// free. Taking it AFTER the read would let two transactions read the same state and
	// serialize only at the sequence step — a lost update at field granularity, with a log
	// that still looked perfectly ordered.
	//
	// A separate, explicitly named method rather than a side effect of NextOpSeq, because
	// "this is where writers serialize" is worth being able to read at the call site. It also
	// doubles as the existence-and-not-deleted check on the trip.
	LockForWrite(ctx context.Context, tripID ID) error

	// NextOpSeq allocates the next operation sequence number for a trip.
	//
	// Called once per operation, so a single intent that commits several operations (D63)
	// gets consecutive numbers. Safe to call repeatedly because LockForWrite already holds
	// the row.
	NextOpSeq(ctx context.Context, tripID ID) (int64, error)

	// CurrentOpSeq reads a trip's sequence without allocating, for a subscriber that needs to
	// know where the room currently is.
	CurrentOpSeq(ctx context.Context, tripID ID) (int64, error)
}

// OpLogRepository persists the immutable operation log.
//
// There is deliberately no Update and no Delete. "Immutable" is enforced by this interface
// having no way to express a mutation, by the absence of any such SQL, and by an assertion in
// tests/schema_verify.sql — not by a comment asking people not to.
type OpLogRepository interface {
	// Append writes one committed operation. It must be called inside the same transaction
	// as the mutation it records; that co-location is what makes "everything since seq N"
	// complete for REST-originated and WebSocket-originated writes alike (Rule 3, D1).
	Append(ctx context.Context, op *Op) error

	// ListSince returns a trip's operations after seq, in order — the resync scan. limit
	// bounds the batch; a caller pages by passing the last seq it received.
	ListSince(ctx context.Context, tripID ID, seq int64, limit int) ([]Op, error)

	// FindByClientOpID supports idempotent replay: a client that lost its acknowledgement and
	// retried must get back the operation it already committed, not a second one.
	FindByClientOpID(ctx context.Context, tripID, clientOpID ID) (*Op, error)
}

// OpPublisher hands committed operations to whatever is broadcasting them.
//
// Two things are encoded in this signature deliberately. It returns NO ERROR, and it is
// called only AFTER the transaction commits (D70): the operation log is the delivery
// guarantee and the broadcast is only an accelerator, so a failed publish is recoverable by
// reconnecting and a failed commit must never be published at all. Publishing inside the
// transaction would let a rolled-back write emit a phantom operation that no client could
// ever reconcile — strictly worse than a dropped broadcast.
//
// Implementations must not block: a slow subscriber is the subscriber's problem to solve by
// closing itself, never the writer's to wait on.
type OpPublisher interface {
	Publish(ctx context.Context, ops []Op)
}

// NoopPublisher is the publisher used when nothing is subscribed — the REST-only wiring, and
// most tests. Named rather than nil so that a missing publisher is a deliberate choice at the
// construction site instead of a nil check on every write path.
type NoopPublisher struct{}

// Publish discards the operations. They are already durably in the log.
func (NoopPublisher) Publish(context.Context, []Op) {}

// OpTransport carries committed operations BETWEEN server instances.
//
// # Why this is a port and not a *redis.Client
//
// The horizontal-scaling claim is about topology, not about Redis. One instance's subscribers
// must see operations committed on another instance, and the mechanism for that is an
// implementation detail the sync engine is not allowed to know — the arch test fails on any
// import matching "redis" under internal/syncengine, exactly as it does for "websocket".
//
// # What an implementation must and must not guarantee
//
// It must be lossy-tolerant, and the engine is written on that assumption. Redis pub/sub has
// no persistence and no acknowledgement: a subscriber that is momentarily disconnected misses
// messages permanently, and two instances publishing concurrently give no cross-publisher
// ordering at all. Neither is a defect to be engineered away here, because the operation log
// is the delivery guarantee and the broadcast is only an accelerator (D70). The engine's room
// dispatcher reorders by Seq and refills any hole from the log, so this interface promises
// only best-effort delivery of ops that ORIGINATED ELSEWHERE.
//
// Implementations must not deliver an instance its own published operations back: the local
// fan-out already happened synchronously, and a loopback would only add a duplicate.
type OpTransport interface {
	// Publish hands committed operations to the other instances. An error is a degraded
	// broadcast, never a lost write — the caller logs it and carries on.
	Publish(ctx context.Context, ops []Op) error

	// Join starts receiving operations for a trip, and Leave stops. Membership is per-trip
	// rather than global so an instance only pays for the trips it actually hosts; a room
	// that opens between an op's commit and this call recovers it from the log.
	Join(ctx context.Context, tripID ID) error
	Leave(ctx context.Context, tripID ID) error

	// Run delivers operations from other instances to fn until ctx is done.
	Run(ctx context.Context, fn func(Op)) error

	Close() error
}

// NoopTransport is the single-instance wiring: everything is delivered by the in-process
// broker, and there are no peers to tell. Named rather than nil for the same reason as
// NoopPublisher — a missing transport should be a decision at the construction site.
type NoopTransport struct{}

// Publish discards the operations; every subscriber is in this process.
func (NoopTransport) Publish(context.Context, []Op) error { return nil }

// Join records nothing: there is no peer to hear about it.
func (NoopTransport) Join(context.Context, ID) error { return nil }

// Leave records nothing.
func (NoopTransport) Leave(context.Context, ID) error { return nil }

// Run blocks until the context is done, so a caller can treat it uniformly with a real
// transport rather than branching on whether one is configured.
func (NoopTransport) Run(ctx context.Context, _ func(Op)) error {
	<-ctx.Done()
	return ctx.Err()
}

// Close releases nothing.
func (NoopTransport) Close() error { return nil }

// MembershipRepository is the core/domain seam.
//
// The interface is module-agnostic and would be satisfied unchanged by a second module's
// own membership table. The table behind it is not shared, so referential integrity stays
// enforceable by the database.
type MembershipRepository interface {
	Add(ctx context.Context, m *Member) error
	Get(ctx context.Context, tripID, userID ID) (*Member, error)
	List(ctx context.Context, tripID ID) ([]*Member, error)
	UpdateRole(ctx context.Context, m *Member) error
	Remove(ctx context.Context, tripID, userID ID, at time.Time) error
	CountByRole(ctx context.Context, tripID ID, role Role) (int, error)
}

// InvitationRepository persists trip invitations.
type InvitationRepository interface {
	Create(ctx context.Context, inv *Invitation) error
	GetByHash(ctx context.Context, hash []byte) (*Invitation, error)
	ListForTrip(ctx context.Context, tripID ID) ([]*Invitation, error)
	// IncrementUseCount must be atomic: two people redeeming a single-use link at the same
	// instant must not both succeed. The implementation enforces this in one statement
	// rather than read-modify-write.
	IncrementUseCount(ctx context.Context, id ID) error
	Revoke(ctx context.Context, id ID, at time.Time) error
}

// DayRepository persists days.
type DayRepository interface {
	Create(ctx context.Context, d *Day) error
	GetByID(ctx context.Context, id ID) (*Day, error)
	ListForTrip(ctx context.Context, tripID ID) ([]*Day, error)
	Update(ctx context.Context, d *Day) error
	SoftDelete(ctx context.Context, id ID, at time.Time, expectedVersion int) error
	// NeighbourPositions returns the position keys bracketing the slot immediately after
	// afterDayID, which is exactly what fracdex.KeyBetween needs to place a new or moved
	// row. A nil afterDayID means "at the start". Either return value may be empty, meaning
	// the slot is unbounded on that side.
	//
	// Expressed as "after X" rather than a (before, after) pair because that is what a
	// client actually sends — drag-and-drop reports the item you dropped onto, and a pair
	// would let a caller supply two positions that are not adjacent.
	NeighbourPositions(ctx context.Context, tripID ID, afterDayID *ID) (prev string, next string, err error)
}

// SlotRepository persists slots — the decisions in an itinerary.
type SlotRepository interface {
	Create(ctx context.Context, s *Slot) error
	GetByID(ctx context.Context, id ID) (*Slot, error)
	ListForTrip(ctx context.Context, tripID ID) ([]*Slot, error)
	ListForDay(ctx context.Context, dayID ID) ([]*Slot, error)
	ListBacklog(ctx context.Context, tripID ID) ([]*Slot, error)
	Update(ctx context.Context, s *Slot) error
	// Move changes a slot's day and position together. One statement, one version bump, one
	// operation — a move must never be observable as a delete followed by an insert, because
	// a client that saw only half of that would render a vanished slot.
	Move(ctx context.Context, id ID, dayID *ID, position string, expectedVersion int, at time.Time) error
	// SetSelectedOption records the group's resolution. Separate from Update because it is a
	// single-field decision with its own authorization and its own sync operation.
	SetSelectedOption(ctx context.Context, slotID ID, optionID *ID, expectedVersion int, at time.Time) error
	// SetStatus records Live-mode coverage, with who and when.
	SetStatus(ctx context.Context, slotID ID, status SlotStatus, by ID, expectedVersion int, at time.Time) error
	SoftDelete(ctx context.Context, id ID, at time.Time, expectedVersion int) error
	// NeighbourPositions returns the position keys bracketing the slot immediately after
	// afterSlotID within the given bucket. dayID nil means the trip backlog; afterSlotID nil
	// means "at the start of that bucket". See DayRepository.NeighbourPositions.
	NeighbourPositions(ctx context.Context, tripID ID, dayID *ID, afterSlotID *ID) (prev string, next string, err error)
}

// SlotOptionRepository persists the candidate proposals under a slot.
type SlotOptionRepository interface {
	Create(ctx context.Context, o *SlotOption) error
	GetByID(ctx context.Context, id ID) (*SlotOption, error)
	ListForSlot(ctx context.Context, slotID ID) ([]*SlotOption, error)
	ListForTrip(ctx context.Context, tripID ID) ([]*SlotOption, error)
	Update(ctx context.Context, o *SlotOption) error
	SoftDelete(ctx context.Context, id ID, at time.Time, expectedVersion int) error
}

// VoteRepository persists member choices.
//
// There is no Delete: retraction sets OptionID to nil, keeping exactly one row per
// (slot, user). See the Vote doc comment — that shape is what makes a vote a trivially
// convergent last-writer-wins register.
type VoteRepository interface {
	// Cast records or changes a member's choice. Upsert semantics on (slot_id, user_id),
	// which is what makes "change my vote" a single round trip and a single row.
	Cast(ctx context.Context, v *Vote) error
	Get(ctx context.Context, slotID, userID ID) (*Vote, error)
	ListForSlot(ctx context.Context, slotID ID) ([]*Vote, error)
	ListForTrip(ctx context.Context, tripID ID) ([]*Vote, error)
	// Tally counts votes per option within a slot, excluding retractions.
	Tally(ctx context.Context, slotID ID) ([]VoteTally, error)
}

// BudgetRepository persists ledger entries and their splits.
//
// Save writes an entry AND its complete split set as one atomic unit. There is deliberately
// no method to update a single split: the sum invariant is only meaningful across the whole
// set, so exposing a per-split write would make it possible to break by design. See the
// BudgetEntry doc comment for why the budget has a coarser conflict grain than the itinerary.
type BudgetRepository interface {
	Save(ctx context.Context, e *BudgetEntry) error
	GetByID(ctx context.Context, id ID) (*BudgetEntry, error)
	ListForTrip(ctx context.Context, tripID ID) ([]*BudgetEntry, error)
	SoftDelete(ctx context.Context, id ID, at time.Time, expectedVersion int) error
}

// AttachmentRepository persists attachment records.
//
// No Update: attachments are immutable once ready. Confirm moves a row from pending to ready
// after the object has been verified; anything else is a create or a delete.
type AttachmentRepository interface {
	Create(ctx context.Context, a *Attachment) error
	GetByID(ctx context.Context, id ID) (*Attachment, error)
	ListForOwner(ctx context.Context, owner AttachmentOwner) ([]*Attachment, error)
	Confirm(ctx context.Context, id ID, sizeBytes int64, checksum []byte, at time.Time) error
	MarkFailed(ctx context.Context, id ID, at time.Time) error
	SoftDelete(ctx context.Context, id ID, at time.Time) error
	// ListStalePending drives the sweeper for uploads that were never confirmed.
	ListStalePending(ctx context.Context, before time.Time, limit int) ([]*Attachment, error)
}

// AttachmentOwner names the single entity an attachment hangs off.
//
// A small struct rather than three parameters, so a caller cannot pass two owners and the
// exclusive arc stays expressible in Go as well as in SQL.
type AttachmentOwner struct {
	SlotOptionID  *ID
	BudgetEntryID *ID
	SlotID        *ID
}

// Valid reports whether exactly one owner is set.
func (o AttachmentOwner) Valid() bool {
	n := 0
	for _, id := range []*ID{o.SlotOptionID, o.BudgetEntryID, o.SlotID} {
		if id != nil {
			n++
		}
	}
	return n == 1
}
