package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
)

// In-memory fakes for the budget, attachment and file-storage ports.
//
// Same philosophy as the other fakes files: these implement the REAL invariants rather than
// recording calls. In particular fakeBudget enforces the sum invariant that the database
// enforces with a deferred trigger, and refuses a stale version — so a service-layer bug that
// would produce an unbalanced ledger fails here rather than only against Postgres.
//
// What it deliberately does NOT model is the deferral itself. The real trigger permits an
// inconsistent split set MID-TRANSACTION and checks at COMMIT, which is what allows the
// delete-and-reinsert rewrite; a fake has no commit, so it checks at the end of Save. That
// difference is why the rewrite behaviour is tested against real Postgres in
// internal/repository/budget_test.go and not here.

// --- budget ---

type fakeBudget struct {
	mu   sync.Mutex
	byID map[domain.ID]*domain.BudgetEntry
}

func newFakeBudget() *fakeBudget {
	return &fakeBudget{byID: map[domain.ID]*domain.BudgetEntry{}}
}

func cloneEntry(e *domain.BudgetEntry) *domain.BudgetEntry {
	clone := *e
	clone.Splits = append([]domain.BudgetSplit(nil), e.Splits...)
	return &clone
}

func (r *fakeBudget) Save(_ context.Context, e *domain.BudgetEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	if e.Version == 0 {
		if _, exists := r.byID[e.ID]; exists {
			return domain.ErrAlreadyExists
		}
		e.Version = 1
		e.CreatedAt, e.UpdatedAt = now, now
	} else {
		existing, ok := r.byID[e.ID]
		if !ok || existing.DeletedAt != nil {
			return domain.ErrNotFound
		}
		if existing.Version != e.Version {
			return domain.ErrVersionConflict
		}
		e.Version = existing.Version + 1
		e.CreatedAt = existing.CreatedAt
		e.UpdatedAt = now
	}

	// The database's backstop, modelled: a non-empty split set must sum to the total. Zero
	// splits is legal — "not split yet" is a different state from "split wrongly".
	if e.IsSplit() && e.SplitTotal() != e.AmountMinor {
		// The real database raises a check_violation which mapError turns into a field-level
		// validation error; the fake produces the same shape so a service test asserting on
		// the error is asserting the same thing production would return.
		ve := &domain.ValidationError{}
		ve.Add("splits", "sum_mismatch", "splits must sum to exactly the entry total")
		return ve
	}

	for i := range e.Splits {
		if e.Splits[i].ID == domain.NilID {
			e.Splits[i].ID = domain.NewID()
		}
		e.Splits[i].CreatedAt = now
	}
	sort.Slice(e.Splits, func(i, j int) bool {
		return e.Splits[i].UserID.String() < e.Splits[j].UserID.String()
	})

	r.byID[e.ID] = cloneEntry(e)
	return nil
}

func (r *fakeBudget) GetByID(_ context.Context, id domain.ID) (*domain.BudgetEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok || e.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	return cloneEntry(e), nil
}

func (r *fakeBudget) ListForTrip(_ context.Context, tripID domain.ID) ([]*domain.BudgetEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*domain.BudgetEntry, 0, len(r.byID))
	for _, e := range r.byID {
		if e.TripID == tripID && e.DeletedAt == nil {
			out = append(out, cloneEntry(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeBudget) SoftDelete(_ context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok || e.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if e.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	e.DeletedAt = &at
	e.UpdatedAt = at
	e.Version++
	return nil
}

// --- attachments ---

type fakeAttachments struct {
	mu   sync.Mutex
	byID map[domain.ID]*domain.Attachment
	keys map[string]domain.ID // mirrors the partial unique index on storage_key
}

func newFakeAttachments() *fakeAttachments {
	return &fakeAttachments{
		byID: map[domain.ID]*domain.Attachment{},
		keys: map[string]domain.ID{},
	}
}

func (r *fakeAttachments) Create(_ context.Context, a *domain.Attachment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[a.ID]; exists {
		return domain.ErrAlreadyExists
	}
	// The exclusive arc, mirrored so a service bug that sets two owners fails here too.
	if a.OwnerCount() != 1 {
		ve := &domain.ValidationError{}
		ve.Add("owner", "exactly_one_required",
			"an attachment must belong to exactly one of: slot option, budget entry, slot")
		return ve
	}
	if a.StorageKey != "" {
		if _, taken := r.keys[a.StorageKey]; taken {
			return domain.ErrAlreadyExists
		}
		r.keys[a.StorageKey] = a.ID
	}
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now
	clone := *a
	r.byID[a.ID] = &clone
	return nil
}

func (r *fakeAttachments) GetByID(_ context.Context, id domain.ID) (*domain.Attachment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok || a.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *a
	return &clone, nil
}

func (r *fakeAttachments) ListForOwner(_ context.Context, owner domain.AttachmentOwner) ([]*domain.Attachment, error) {
	if !owner.Valid() {
		ve := &domain.ValidationError{}
		ve.Add("owner", "exactly_one_required",
			"an attachment owner must be exactly one of: slot option, budget entry, slot")
		return nil, ve
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	matches := func(a *domain.Attachment) bool {
		switch {
		case owner.SlotID != nil:
			return a.SlotID != nil && *a.SlotID == *owner.SlotID
		case owner.SlotOptionID != nil:
			return a.SlotOptionID != nil && *a.SlotOptionID == *owner.SlotOptionID
		default:
			return a.BudgetEntryID != nil && *a.BudgetEntryID == *owner.BudgetEntryID
		}
	}

	out := make([]*domain.Attachment, 0, len(r.byID))
	for _, a := range r.byID {
		if a.DeletedAt == nil && matches(a) {
			clone := *a
			out = append(out, &clone)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeAttachments) Confirm(_ context.Context, id domain.ID, sizeBytes int64, checksum []byte, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	// The status predicate is the real query's, and it is what makes a repeated confirm a
	// no-op rather than an overwrite.
	if !ok || a.DeletedAt != nil || a.Status != domain.AttachmentStatusPending {
		return domain.ErrNotFound
	}
	a.Status = domain.AttachmentStatusReady
	a.SizeBytes = &sizeBytes
	a.ChecksumSHA256 = checksum
	a.UpdatedAt = at
	return nil
}

func (r *fakeAttachments) MarkFailed(_ context.Context, id domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok || a.DeletedAt != nil || a.Status != domain.AttachmentStatusPending {
		return domain.ErrNotFound
	}
	a.Status = domain.AttachmentStatusFailed
	a.UpdatedAt = at
	return nil
}

func (r *fakeAttachments) SoftDelete(_ context.Context, id domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok || a.DeletedAt != nil {
		return domain.ErrNotFound
	}
	a.DeletedAt = &at
	a.UpdatedAt = at
	return nil
}

func (r *fakeAttachments) ListStalePending(_ context.Context, before time.Time, limit int) ([]*domain.Attachment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*domain.Attachment, 0)
	for _, a := range r.byID {
		if a.DeletedAt == nil && a.Status == domain.AttachmentStatusPending && a.CreatedAt.Before(before) {
			clone := *a
			out = append(out, &clone)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// --- file storage ---

// fakeStorage implements domain.FileStorage in memory.
//
// Stat is programmable, because the interesting attachment behaviour is precisely what happens
// when what the storage reports disagrees with what the client implied — an object that is too
// large, or one that never arrived at all. Neither can be provoked by uploading through the
// fake, because the fake has no upload: the whole point of the presigned design is that the
// bytes never pass through this process.
type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]domain.FileInfo
	deleted []string

	// failStat, when set, is returned by Stat instead of consulting objects.
	failStat error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string]domain.FileInfo{}}
}

// putObject simulates the browser's direct PUT completing.
func (s *fakeStorage) putObject(key string, size int64, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = domain.FileInfo{
		Key: key, SizeBytes: size, ContentType: contentType, ModifiedAt: time.Now().UTC(),
	}
}

func (s *fakeStorage) PresignUpload(_ context.Context, key, contentType string, ttl time.Duration) (string, error) {
	return "https://storage.test/upload/" + key + "?ct=" + contentType, nil
}

func (s *fakeStorage) PresignDownload(_ context.Context, key string, ttl time.Duration) (string, error) {
	return "https://storage.test/download/" + key, nil
}

func (s *fakeStorage) Stat(_ context.Context, key string) (domain.FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failStat != nil {
		return domain.FileInfo{}, s.failStat
	}
	info, ok := s.objects[key]
	if !ok {
		return domain.FileInfo{}, domain.ErrNotFound
	}
	return info, nil
}

func (s *fakeStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *fakeStorage) wasDeleted(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.deleted {
		if k == key {
			return true
		}
	}
	return false
}

func (s *fakeStorage) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

// countKeysWithPrefix supports assertions about key layout without hardcoding the format.
func (s *fakeStorage) countKeysWithPrefix(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}
