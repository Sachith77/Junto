package service

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
)

// In-memory fakes for the planning-domain ports (trips, membership, invitations, days,
// slots, options, votes). Same philosophy as fakes_test.go: these implement the real
// invariants (optimistic concurrency, the owner-uniqueness rule, the atomic invitation
// guard) rather than just recording calls, so a test that passes against a fake is evidence
// the orchestration logic is right — not just that a method got called.

// --- trips ---

type fakeTrips struct {
	mu      sync.Mutex
	byID    map[domain.ID]*domain.Trip
	opSeq   map[domain.ID]int64
	members *fakeMembers // needed to answer "which trips is this user a member of"
}

func newFakeTrips(members *fakeMembers) *fakeTrips {
	return &fakeTrips{
		byID:    map[domain.ID]*domain.Trip{},
		opSeq:   map[domain.ID]int64{},
		members: members,
	}
}

func (r *fakeTrips) Create(_ context.Context, t *domain.Trip) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Version = 1
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	clone := *t
	r.byID[t.ID] = &clone
	return nil
}

func (r *fakeTrips) GetByID(_ context.Context, id domain.ID) (*domain.Trip, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok || t.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *t
	return &clone, nil
}

func (r *fakeTrips) Update(_ context.Context, t *domain.Trip) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[t.ID]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != t.Version {
		return domain.ErrVersionConflict
	}
	clone := *existing
	clone.Name, clone.Description, clone.TimeZone = t.Name, t.Description, t.TimeZone
	clone.StartDate, clone.EndDate = t.StartDate, t.EndDate
	clone.Version++
	clone.UpdatedAt = time.Now().UTC()
	r.byID[t.ID] = &clone
	*t = clone
	return nil
}

func (r *fakeTrips) SoftDelete(_ context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[id]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	existing.DeletedAt = &at
	existing.Version++
	return nil
}

// LockForWrite stands in for the row lock. There is no real concurrency to serialize here —
// the fake is single-process and the tests around it are sequential — so what it usefully
// models is the OTHER half of the real method's job: rejecting a missing or deleted trip
// before any work is done. The serialization itself is only testable against real Postgres,
// which is where TestConcurrent... in internal/repository exercises it.
func (r *fakeTrips) LockForWrite(_ context.Context, tripID domain.ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[tripID]
	if !ok || t.DeletedAt != nil {
		return domain.ErrNotFound
	}
	return nil
}

func (r *fakeTrips) NextOpSeq(_ context.Context, tripID domain.ID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[tripID]
	if !ok {
		return 0, domain.ErrNotFound
	}
	r.opSeq[tripID]++
	_ = t
	return r.opSeq[tripID], nil
}

func (r *fakeTrips) CurrentOpSeq(_ context.Context, tripID domain.ID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[tripID]; !ok {
		return 0, domain.ErrNotFound
	}
	return r.opSeq[tripID], nil
}

func (r *fakeTrips) ListForUser(ctx context.Context, userID domain.ID, page domain.PageRequest) (domain.Page[*domain.Trip], error) {
	r.mu.Lock()
	var all []*domain.Trip
	for _, t := range r.byID {
		if t.DeletedAt != nil {
			continue
		}
		if _, err := r.members.Get(ctx, t.ID, userID); err != nil {
			continue
		}
		clone := *t
		all = append(all, &clone)
	}
	r.mu.Unlock()

	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID.String() > all[j].ID.String()
	})

	if page.After != "" {
		for i, t := range all {
			if t.ID.String() == string(page.After) {
				all = all[i+1:]
				break
			}
		}
	}
	limit := page.Limit
	if limit <= 0 {
		limit = domain.DefaultPageSize
	}
	if len(all) > limit+1 {
		all = all[:limit+1]
	}
	return domain.NewPage(all, limit, func(t *domain.Trip) domain.Cursor {
		return domain.Cursor(t.ID.String())
	}), nil
}

// --- membership ---

type fakeMembers struct {
	mu   sync.Mutex
	rows map[[2]domain.ID]*domain.Member // {tripID, userID}
}

func newFakeMembers() *fakeMembers { return &fakeMembers{rows: map[[2]domain.ID]*domain.Member{}} }

func (r *fakeMembers) Add(_ context.Context, m *domain.Member) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]domain.ID{m.TripID, m.UserID}
	if existing, ok := r.rows[key]; ok && existing.DeletedAt == nil {
		ve := &domain.ValidationError{}
		ve.Add("user_id", "already_member", "this user is already a member of the trip")
		return ve
	}
	if m.Role == domain.RoleOwner {
		for k, v := range r.rows {
			if k[0] == m.TripID && v.Role == domain.RoleOwner && v.DeletedAt == nil {
				ve := &domain.ValidationError{}
				ve.Add("role", "owner_exists", "this trip already has an owner")
				return ve
			}
		}
	}
	if m.JoinedAt.IsZero() {
		m.JoinedAt = time.Now().UTC()
	}
	m.Version = 1
	clone := *m
	r.rows[key] = &clone
	return nil
}

func (r *fakeMembers) Get(_ context.Context, tripID, userID domain.ID) (*domain.Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.rows[[2]domain.ID{tripID, userID}]
	if !ok || m.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *m
	return &clone, nil
}

func (r *fakeMembers) List(_ context.Context, tripID domain.ID) ([]*domain.Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Member
	for k, v := range r.rows {
		if k[0] == tripID && v.DeletedAt == nil {
			clone := *v
			out = append(out, &clone)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Role == domain.RoleOwner) != (out[j].Role == domain.RoleOwner) {
			return out[i].Role == domain.RoleOwner
		}
		return out[i].JoinedAt.Before(out[j].JoinedAt)
	})
	return out, nil
}

func (r *fakeMembers) UpdateRole(_ context.Context, m *domain.Member) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]domain.ID{m.TripID, m.UserID}
	existing, ok := r.rows[key]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != m.Version {
		return domain.ErrVersionConflict
	}
	clone := *existing
	clone.Role = m.Role
	clone.Version++
	r.rows[key] = &clone
	*m = clone
	return nil
}

func (r *fakeMembers) Remove(_ context.Context, tripID, userID domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]domain.ID{tripID, userID}
	existing, ok := r.rows[key]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	existing.DeletedAt = &at
	return nil
}

func (r *fakeMembers) CountByRole(_ context.Context, tripID domain.ID, role domain.Role) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, v := range r.rows {
		if k[0] == tripID && v.Role == role && v.DeletedAt == nil {
			n++
		}
	}
	return n, nil
}

// --- invitations ---

type fakeInvitations struct {
	mu   sync.Mutex
	byID map[domain.ID]*domain.Invitation
}

func newFakeInvitations() *fakeInvitations {
	return &fakeInvitations{byID: map[domain.ID]*domain.Invitation{}}
}

func (r *fakeInvitations) Create(_ context.Context, inv *domain.Invitation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = time.Now().UTC()
	}
	clone := *inv
	r.byID[inv.ID] = &clone
	return nil
}

func (r *fakeInvitations) GetByHash(_ context.Context, hash []byte) (*domain.Invitation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, inv := range r.byID {
		if bytes.Equal(inv.TokenHash, hash) {
			clone := *inv
			return &clone, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *fakeInvitations) ListForTrip(_ context.Context, tripID domain.ID) ([]*domain.Invitation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Invitation
	for _, inv := range r.byID {
		if inv.TripID == tripID && inv.RevokedAt == nil {
			clone := *inv
			out = append(out, &clone)
		}
	}
	return out, nil
}

// IncrementUseCount is the atomic guard the whole redemption flow depends on: every validity
// condition is checked in the same "statement" that consumes a use, mirroring the real
// repository's single UPDATE.
func (r *fakeInvitations) IncrementUseCount(_ context.Context, id domain.ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.byID[id]
	if !ok {
		return domain.ErrTokenInvalid
	}
	now := time.Now().UTC()
	if inv.RevokedAt != nil || !now.Before(inv.ExpiresAt) {
		return domain.ErrTokenInvalid
	}
	if inv.MaxUses != nil && inv.UseCount >= *inv.MaxUses {
		return domain.ErrTokenInvalid
	}
	inv.UseCount++
	return nil
}

func (r *fakeInvitations) Revoke(_ context.Context, id domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.byID[id]
	if !ok || inv.RevokedAt != nil {
		return domain.ErrNotFound
	}
	inv.RevokedAt = &at
	return nil
}

// --- days ---

type fakeDays struct {
	mu   sync.Mutex
	byID map[domain.ID]*domain.Day
}

func newFakeDays() *fakeDays { return &fakeDays{byID: map[domain.ID]*domain.Day{}} }

func (r *fakeDays) Create(_ context.Context, d *domain.Day) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d.Version = 1
	clone := *d
	r.byID[d.ID] = &clone
	return nil
}

func (r *fakeDays) GetByID(_ context.Context, id domain.ID) (*domain.Day, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.byID[id]
	if !ok || d.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *d
	return &clone, nil
}

func (r *fakeDays) ListForTrip(_ context.Context, tripID domain.ID) ([]*domain.Day, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Day
	for _, d := range r.byID {
		if d.TripID == tripID && d.DeletedAt == nil {
			clone := *d
			out = append(out, &clone)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (r *fakeDays) Update(_ context.Context, d *domain.Day) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[d.ID]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != d.Version {
		return domain.ErrVersionConflict
	}
	// Mirrors days_trip_date_uq: another live day in the same trip on the same date is
	// rejected, so this fake exercises the same conflict a test would see against Postgres.
	if d.Date != nil {
		for id, other := range r.byID {
			if id == d.ID || other.DeletedAt != nil || other.TripID != d.TripID || other.Date == nil {
				continue
			}
			if other.Date.Equal(*d.Date) {
				ve := &domain.ValidationError{}
				ve.Add("date", "date_taken", "this trip already has a day on that date")
				return ve
			}
		}
	}
	clone := *d
	clone.Version++
	r.byID[d.ID] = &clone
	*d = clone
	return nil
}

func (r *fakeDays) SoftDelete(_ context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[id]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	existing.DeletedAt = &at
	existing.Version++
	return nil
}

func (r *fakeDays) NeighbourPositions(ctx context.Context, tripID domain.ID, afterDayID *domain.ID) (string, string, error) {
	days, _ := r.ListForTrip(ctx, tripID)
	if afterDayID == nil {
		if len(days) == 0 {
			return "", "", nil
		}
		return "", days[0].Position, nil
	}
	for i, d := range days {
		if d.ID == *afterDayID {
			next := ""
			if i+1 < len(days) {
				next = days[i+1].Position
			}
			return d.Position, next, nil
		}
	}
	return "", "", domain.ErrNotFound
}

// --- slots ---

type fakeSlots struct {
	mu   sync.Mutex
	byID map[domain.ID]*domain.Slot
}

func newFakeSlots() *fakeSlots { return &fakeSlots{byID: map[domain.ID]*domain.Slot{}} }

func (r *fakeSlots) Create(_ context.Context, s *domain.Slot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.Version = 1
	clone := *s
	r.byID[s.ID] = &clone
	return nil
}

func (r *fakeSlots) GetByID(_ context.Context, id domain.ID) (*domain.Slot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byID[id]
	if !ok || s.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *s
	return &clone, nil
}

func (r *fakeSlots) ListForTrip(_ context.Context, tripID domain.ID) ([]*domain.Slot, error) {
	return r.listWhere(func(s *domain.Slot) bool { return s.TripID == tripID })
}

func (r *fakeSlots) ListForDay(_ context.Context, dayID domain.ID) ([]*domain.Slot, error) {
	return r.listWhere(func(s *domain.Slot) bool { return s.DayID != nil && *s.DayID == dayID })
}

func (r *fakeSlots) ListBacklog(_ context.Context, tripID domain.ID) ([]*domain.Slot, error) {
	return r.listWhere(func(s *domain.Slot) bool { return s.TripID == tripID && s.DayID == nil })
}

func (r *fakeSlots) listWhere(pred func(*domain.Slot) bool) ([]*domain.Slot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Slot
	for _, s := range r.byID {
		if s.DeletedAt == nil && pred(s) {
			clone := *s
			out = append(out, &clone)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (r *fakeSlots) Update(_ context.Context, s *domain.Slot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[s.ID]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != s.Version {
		return domain.ErrVersionConflict
	}
	clone := *existing
	clone.Kind, clone.Title, clone.Notes = s.Kind, s.Title, s.Notes
	clone.StartTime, clone.EndTime = s.StartTime, s.EndTime
	clone.Version++
	r.byID[s.ID] = &clone
	*s = clone
	return nil
}

func (r *fakeSlots) Move(_ context.Context, id domain.ID, dayID *domain.ID, position string, expectedVersion int, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[id]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	clone := *existing
	clone.DayID, clone.Position = dayID, position
	clone.Version++
	clone.UpdatedAt = at
	r.byID[id] = &clone
	return nil
}

// SetSelectedOption mirrors the composite FK the real schema enforces: an option that does
// not belong to this slot is rejected, expressed here as a validation error rather than a
// constraint violation (services see the same shape from the mapped repository error).
func (r *fakeSlots) SetSelectedOption(_ context.Context, slotID domain.ID, optionID *domain.ID, expectedVersion int, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[slotID]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	clone := *existing
	clone.SelectedOptionID = optionID
	clone.Version++
	clone.UpdatedAt = at
	r.byID[slotID] = &clone
	return nil
}

func (r *fakeSlots) SetStatus(_ context.Context, slotID domain.ID, status domain.SlotStatus, by domain.ID, expectedVersion int, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[slotID]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	clone := *existing
	clone.Status = status
	clone.StatusChangedBy = &by
	clone.StatusChangedAt = &at
	clone.Version++
	clone.UpdatedAt = at
	r.byID[slotID] = &clone
	return nil
}

func (r *fakeSlots) SoftDelete(_ context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[id]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	existing.DeletedAt = &at
	existing.Version++
	return nil
}

func (r *fakeSlots) NeighbourPositions(ctx context.Context, tripID domain.ID, dayID *domain.ID, afterSlotID *domain.ID) (string, string, error) {
	var bucket []*domain.Slot
	if dayID == nil {
		bucket, _ = r.ListBacklog(ctx, tripID)
	} else {
		bucket, _ = r.ListForDay(ctx, *dayID)
	}
	if afterSlotID == nil {
		if len(bucket) == 0 {
			return "", "", nil
		}
		return "", bucket[0].Position, nil
	}
	for i, s := range bucket {
		if s.ID == *afterSlotID {
			next := ""
			if i+1 < len(bucket) {
				next = bucket[i+1].Position
			}
			return s.Position, next, nil
		}
	}
	return "", "", domain.ErrNotFound
}

// --- slot options ---

type fakeSlotOptions struct {
	mu   sync.Mutex
	byID map[domain.ID]*domain.SlotOption
}

func newFakeSlotOptions() *fakeSlotOptions {
	return &fakeSlotOptions{byID: map[domain.ID]*domain.SlotOption{}}
}

func (r *fakeSlotOptions) Create(_ context.Context, o *domain.SlotOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o.Version = 1
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	clone := *o
	r.byID[o.ID] = &clone
	return nil
}

func (r *fakeSlotOptions) GetByID(_ context.Context, id domain.ID) (*domain.SlotOption, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.byID[id]
	if !ok || o.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *o
	return &clone, nil
}

func (r *fakeSlotOptions) ListForSlot(_ context.Context, slotID domain.ID) ([]*domain.SlotOption, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.SlotOption
	for _, o := range r.byID {
		if o.SlotID == slotID && o.DeletedAt == nil {
			clone := *o
			out = append(out, &clone)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeSlotOptions) ListForTrip(_ context.Context, tripID domain.ID) ([]*domain.SlotOption, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.SlotOption
	for _, o := range r.byID {
		if o.TripID == tripID && o.DeletedAt == nil {
			clone := *o
			out = append(out, &clone)
		}
	}
	return out, nil
}

func (r *fakeSlotOptions) Update(_ context.Context, o *domain.SlotOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[o.ID]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != o.Version {
		return domain.ErrVersionConflict
	}
	clone := *existing
	clone.Title, clone.Notes, clone.ExternalURL = o.Title, o.Notes, o.ExternalURL
	clone.EstimatedCostMinor, clone.Place = o.EstimatedCostMinor, o.Place
	clone.Version++
	r.byID[o.ID] = &clone
	*o = clone
	return nil
}

func (r *fakeSlotOptions) SoftDelete(_ context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[id]
	if !ok || existing.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	existing.DeletedAt = &at
	existing.Version++
	return nil
}

// --- votes ---

// fakeVotes reproduces the register shape: one row per (slot, user), upserted, never
// deleted. That is the property TestVoteIsAnUpsertRegister-equivalent tests here rely on.
type fakeVotes struct {
	mu   sync.Mutex
	rows map[[2]domain.ID]*domain.Vote // {slotID, userID}
}

func newFakeVotes() *fakeVotes { return &fakeVotes{rows: map[[2]domain.ID]*domain.Vote{}} }

func (r *fakeVotes) Cast(_ context.Context, v *domain.Vote) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]domain.ID{v.SlotID, v.UserID}
	now := time.Now().UTC()
	if existing, ok := r.rows[key]; ok {
		clone := *existing
		clone.OptionID = v.OptionID
		clone.Version++
		clone.UpdatedAt = now
		r.rows[key] = &clone
		*v = clone
		return nil
	}
	if v.ID == domain.NilID {
		v.ID = domain.NewID()
	}
	v.Version = 1
	v.CreatedAt, v.UpdatedAt = now, now
	clone := *v
	r.rows[key] = &clone
	return nil
}

func (r *fakeVotes) Get(_ context.Context, slotID, userID domain.ID) (*domain.Vote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.rows[[2]domain.ID{slotID, userID}]
	if !ok {
		return nil, domain.ErrNotFound
	}
	clone := *v
	return &clone, nil
}

func (r *fakeVotes) ListForSlot(_ context.Context, slotID domain.ID) ([]*domain.Vote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Vote
	for k, v := range r.rows {
		if k[0] == slotID {
			clone := *v
			out = append(out, &clone)
		}
	}
	return out, nil
}

func (r *fakeVotes) ListForTrip(_ context.Context, tripID domain.ID) ([]*domain.Vote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Vote
	for _, v := range r.rows {
		if v.TripID == tripID {
			clone := *v
			out = append(out, &clone)
		}
	}
	return out, nil
}

func (r *fakeVotes) Tally(_ context.Context, slotID domain.ID) ([]domain.VoteTally, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[domain.ID]int{}
	for k, v := range r.rows {
		if k[0] == slotID && v.OptionID != nil {
			counts[*v.OptionID]++
		}
	}
	out := make([]domain.VoteTally, 0, len(counts))
	for opt, n := range counts {
		out = append(out, domain.VoteTally{OptionID: opt, Count: n})
	}
	return out, nil
}

// --- comments ---

// fakeComments reproduces the append-only shape: create and soft-delete only, no version, no
// update — the same treatment as fakeAttachments, decided before CommentService was written.
type fakeComments struct {
	mu   sync.Mutex
	byID map[domain.ID]*domain.Comment
}

func newFakeComments() *fakeComments {
	return &fakeComments{byID: map[domain.ID]*domain.Comment{}}
}

func (r *fakeComments) Create(_ context.Context, c *domain.Comment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[c.ID]; exists {
		return domain.ErrAlreadyExists
	}
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	clone := *c
	r.byID[c.ID] = &clone
	return nil
}

func (r *fakeComments) GetByID(_ context.Context, id domain.ID) (*domain.Comment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byID[id]
	if !ok || c.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *c
	return &clone, nil
}

func (r *fakeComments) ListForSlot(_ context.Context, slotID domain.ID) ([]*domain.Comment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*domain.Comment, 0)
	for _, c := range r.byID {
		if c.SlotID == slotID && c.DeletedAt == nil {
			clone := *c
			out = append(out, &clone)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeComments) SoftDelete(_ context.Context, id domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byID[id]
	if !ok || c.DeletedAt != nil {
		return domain.ErrNotFound
	}
	c.DeletedAt = &at
	c.UpdatedAt = at
	return nil
}

// --- operation log ---

// fakeOpLog records what the services appended, so a test can assert on the SHAPE of the log
// a mutation produces — that a cascade emits two operations (D63), that an edit's mask names
// only the fields that changed (D64), that sequence numbers are consecutive.
//
// Those are exactly the properties a fake can prove and a real database adds nothing to. What
// it deliberately does NOT prove is convergence: that lives in the racing WebSocket test,
// because the concurrency in this system is in the submission race, not in application order.
type fakeOpLog struct {
	mu  sync.Mutex
	ops []domain.Op
}

func newFakeOpLog() *fakeOpLog { return &fakeOpLog{} }

func (r *fakeOpLog) Append(_ context.Context, op *domain.Op) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.ops {
		if existing.TripID == op.TripID && existing.Seq == op.Seq {
			return domain.ErrAlreadyExists
		}
		// Mirrors the partial unique index on (trip_id, client_op_id): idempotent replay is
		// enforced by the constraint, not by the pre-check the engine does for speed.
		if op.ClientOpID != nil && existing.ClientOpID != nil &&
			existing.TripID == op.TripID && *existing.ClientOpID == *op.ClientOpID {
			return domain.ErrAlreadyExists
		}
	}
	r.ops = append(r.ops, *op)
	return nil
}

func (r *fakeOpLog) ListSince(_ context.Context, tripID domain.ID, seq int64, limit int) ([]domain.Op, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Op
	for _, op := range r.ops {
		if op.TripID == tripID && op.Seq > seq {
			out = append(out, op)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeOpLog) FindByClientOpID(_ context.Context, tripID, clientOpID domain.ID) (*domain.Op, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.ops {
		op := r.ops[i]
		if op.TripID == tripID && op.ClientOpID != nil && *op.ClientOpID == clientOpID {
			return &op, nil
		}
	}
	return nil, domain.ErrNotFound
}

// all returns a copy of the recorded log.
func (r *fakeOpLog) all() []domain.Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Op, len(r.ops))
	copy(out, r.ops)
	return out
}

// Compile-time proof the fakes satisfy the ports they stand in for.
var (
	_ domain.TripRepository       = (*fakeTrips)(nil)
	_ domain.OpLogRepository      = (*fakeOpLog)(nil)
	_ domain.MembershipRepository = (*fakeMembers)(nil)
	_ domain.InvitationRepository = (*fakeInvitations)(nil)
	_ domain.DayRepository        = (*fakeDays)(nil)
	_ domain.SlotRepository       = (*fakeSlots)(nil)
	_ domain.SlotOptionRepository = (*fakeSlotOptions)(nil)
	_ domain.VoteRepository       = (*fakeVotes)(nil)
)
