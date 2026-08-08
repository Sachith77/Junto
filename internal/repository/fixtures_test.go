package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/fracdex"
)

// Fixture helpers.
//
// Every fixture generates its own UUIDs and a unique email, so nothing depends on suite
// ordering and the concurrency tests (which commit) cannot collide with each other.

type repos struct {
	users       *UserRepository
	sessions    *SessionRepository
	userTokens  *UserTokenRepository
	trips       *TripRepository
	members     *MembershipRepository
	invitations *InvitationRepository
	days        *DayRepository
	slots       *SlotRepository
	options     *SlotOptionRepository
	votes       *VoteRepository
	budget      *BudgetRepository
	attachments *AttachmentRepository
	comments    *CommentRepository
	tx          *TxManager
}

func newRepos() repos {
	return repos{
		users:       NewUserRepository(testPool),
		sessions:    NewSessionRepository(testPool),
		userTokens:  NewUserTokenRepository(testPool),
		trips:       NewTripRepository(testPool),
		members:     NewMembershipRepository(testPool),
		invitations: NewInvitationRepository(testPool),
		days:        NewDayRepository(testPool),
		slots:       NewSlotRepository(testPool),
		options:     NewSlotOptionRepository(testPool),
		votes:       NewVoteRepository(testPool),
		budget:      NewBudgetRepository(testPool),
		attachments: NewAttachmentRepository(testPool),
		comments:    NewCommentRepository(testPool),
		tx:          NewTxManager(testPool),
	}
}

var uniqueCounter = make(chan int, 1)

func init() { uniqueCounter <- 0 }

func uniqueEmail(prefix string) string {
	n := <-uniqueCounter
	n++
	uniqueCounter <- n
	return fmt.Sprintf("%s-%d-%s@example.test", prefix, n, domain.NewID().String()[:8])
}

func makeUser(t *testing.T, ctx context.Context, r repos) *domain.User {
	t.Helper()
	u := &domain.User{
		ID:           domain.NewID(),
		Email:        uniqueEmail("user"),
		PasswordHash: "$argon2id$fake",
		DisplayName:  "Test User",
	}
	if err := r.users.Create(ctx, u); err != nil {
		t.Fatalf("creating user: %v", err)
	}
	return u
}

func makeTrip(t *testing.T, ctx context.Context, r repos, owner *domain.User) *domain.Trip {
	t.Helper()
	trip := &domain.Trip{
		ID:       domain.NewID(),
		Name:     "Test Trip",
		TimeZone: "Europe/Lisbon",
	}
	if err := r.trips.Create(ctx, trip); err != nil {
		t.Fatalf("creating trip: %v", err)
	}
	m := &domain.Member{
		ID:     domain.NewID(),
		TripID: trip.ID,
		UserID: owner.ID,
		Role:   domain.RoleOwner,
	}
	if err := r.members.Add(ctx, m); err != nil {
		t.Fatalf("adding owner: %v", err)
	}
	return trip
}

func makeDay(t *testing.T, ctx context.Context, r repos, trip *domain.Trip, date *time.Time) *domain.Day {
	t.Helper()

	// Append: bracket by (last existing, unbounded).
	prev, next := "", ""
	days, err := r.days.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing days: %v", err)
	}
	if len(days) > 0 {
		prev = days[len(days)-1].Position
	}
	pos, err := fracdex.KeyBetween(prev, next)
	if err != nil {
		t.Fatalf("generating position: %v", err)
	}

	d := &domain.Day{
		ID:       domain.NewID(),
		TripID:   trip.ID,
		Date:     date,
		Position: pos,
	}
	if err := r.days.Create(ctx, d); err != nil {
		t.Fatalf("creating day: %v", err)
	}
	return d
}

// makeSlot appends a decision to a day, or to the trip backlog when day is nil.
func makeSlot(t *testing.T, ctx context.Context, r repos, trip *domain.Trip, day *domain.Day, title string) *domain.Slot {
	t.Helper()

	var dayID *domain.ID
	if day != nil {
		dayID = &day.ID
	}

	// Anchor on the last existing slot in the bucket, if any.
	var after *domain.ID
	existing, err := listBucket(ctx, r, trip.ID, dayID)
	if err != nil {
		t.Fatalf("listing bucket: %v", err)
	}
	if len(existing) > 0 {
		after = &existing[len(existing)-1].ID
	}

	prev, next, err := r.slots.NeighbourPositions(ctx, trip.ID, dayID, after)
	if err != nil {
		t.Fatalf("neighbour positions: %v", err)
	}
	pos, err := fracdex.KeyBetween(prev, next)
	if err != nil {
		t.Fatalf("generating position: %v", err)
	}

	s := &domain.Slot{
		ID:       domain.NewID(),
		TripID:   trip.ID,
		DayID:    dayID,
		Kind:     domain.SlotKindPlace,
		Title:    title,
		Position: pos,
		Status:   domain.SlotStatusPlanned,
	}
	if err := r.slots.Create(ctx, s); err != nil {
		t.Fatalf("creating slot %q: %v", title, err)
	}
	return s
}

// makeOption adds a candidate under a slot.
func makeOption(t *testing.T, ctx context.Context, r repos, slot *domain.Slot, title string) *domain.SlotOption {
	t.Helper()
	o := &domain.SlotOption{
		ID:     domain.NewID(),
		SlotID: slot.ID,
		TripID: slot.TripID,
		Title:  title,
	}
	if err := r.options.Create(ctx, o); err != nil {
		t.Fatalf("creating option %q: %v", title, err)
	}
	return o
}

// makeComment posts a comment on a slot, authored by the given user.
func makeComment(t *testing.T, ctx context.Context, r repos, slot *domain.Slot, author *domain.User, body string) *domain.Comment {
	t.Helper()
	c := &domain.Comment{
		ID:       domain.NewID(),
		SlotID:   slot.ID,
		TripID:   slot.TripID,
		Body:     body,
		AuthorID: &author.ID,
	}
	if err := r.comments.Create(ctx, c); err != nil {
		t.Fatalf("creating comment %q: %v", body, err)
	}
	return c
}

func listBucket(ctx context.Context, r repos, tripID domain.ID, dayID *domain.ID) ([]*domain.Slot, error) {
	if dayID == nil {
		return r.slots.ListBacklog(ctx, tripID)
	}
	return r.slots.ListForDay(ctx, *dayID)
}

func titles(slots []*domain.Slot) []string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.Title)
	}
	return out
}

func optionTitles(options []*domain.SlotOption) []string {
	out := make([]string, 0, len(options))
	for _, o := range options {
		out = append(out, o.Title)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// mustViolate asserts that a write is rejected by the database, running it inside a SAVEPOINT.
//
// The savepoint is not tidiness — it is D20. Postgres aborts an ENTIRE transaction as soon as
// any statement fails (SQLSTATE 25P02), so a test that provokes a constraint violation and then
// continues in the same transaction gets "current transaction is aborted" for everything
// afterwards, including its own assertions. Nesting through TxManager rolls back exactly the
// failed statement and leaves the enclosing test transaction usable, which is the same
// mechanism a service uses to catch a duplicate-key error and take another path.
func mustViolate(t *testing.T, ctx context.Context, r repos, what string, fn func(ctx context.Context) error) {
	t.Helper()
	if err := r.tx.WithinTx(ctx, fn); err == nil {
		t.Errorf("%s: the database accepted this, but it must be rejected", what)
	}
}

// scanInTx runs a scalar query on the AMBIENT transaction.
//
// Querying testPool directly from a test wrapped in txContext reads from a different
// connection, which cannot see the test's uncommitted writes — it silently returns zero rows
// and the assertion built on it passes or fails for reasons unrelated to the code under test.
func scanInTx(t *testing.T, ctx context.Context, dst any, sql string, args ...any) {
	t.Helper()
	tx, ok := txFromContext(ctx)
	if !ok {
		t.Fatal("scanInTx needs a transaction context; use txContext(t)")
	}
	if err := tx.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
		t.Fatalf("querying %q: %v", sql, err)
	}
}

// randomHash returns a unique 32-byte value, matching the octet_length(...) = 32 CHECK on
// every token_hash column. Derived from a fresh UUID rather than a counter so that
// committing tests running in parallel cannot collide on the unique index.
func randomHash() []byte {
	id := domain.NewID()
	h := make([]byte, 32)
	copy(h, id[:])
	copy(h[16:], domain.NewID().String()[:16])
	return h
}
