package tests

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Full-stack tests for the two Slice 3 conflict classes: the budget (atomic) and attachments
// (broadcast-only).
//
// These run against real Postgres through real HTTP, and they exist to prove the things that
// only appear once every layer is assembled — that the coarse grain survives the round trip as
// a 409 rather than a silent overwrite, that an upload is invisible until it is confirmed, and
// that both entities land in the operation log so a resyncing client sees them. The last one is
// Rule 3's guarantee extended to the entities this slice added, and it is the reason attachments
// are logged at all despite having nothing to merge.

// --- helpers ---

type ledgerFixture struct {
	t      *testing.T
	owner  *client
	token  string
	tripID string
}

func newLedgerFixture(t *testing.T, prefix string) *ledgerFixture {
	t.Helper()
	owner := newClient(t)
	email := signupAndVerify(t, owner, prefix+"-owner")
	token := login(t, owner, email)
	trip := createTrip(t, owner, token, "Ledger Trip")
	return &ledgerFixture{t: t, owner: owner, token: token, tripID: trip["id"].(string)}
}

func (f *ledgerFixture) budgetPath(suffix string) string {
	if suffix == "" {
		return fmt.Sprintf("/api/v1/trips/%s/budget", f.tripID)
	}
	return fmt.Sprintf("/api/v1/trips/%s/budget/%s", f.tripID, suffix)
}

func (f *ledgerFixture) attachmentPath(suffix string) string {
	return fmt.Sprintf("/api/v1/trips/%s/attachments%s", f.tripID, suffix)
}

// makeSlotFor creates a slot to hang attachments off.
func (f *ledgerFixture) makeSlot(title string) string {
	f.t.Helper()
	resp := f.owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots", f.tripID),
		map[string]any{"kind": "lodging", "title": title}, withBearer(f.token))
	assertStatus(f.t, resp, http.StatusCreated)
	var slot map[string]any
	resp.decode(f.t, &slot)
	return slot["id"].(string)
}

// --- budget ---

// TestBudgetSurfaceCompletes walks the ledger through HTTP: create, list, get, replace, delete.
func TestBudgetSurfaceCompletes(t *testing.T) {
	f := newLedgerFixture(t, "budget-crud")

	// Create, with a split covering the whole cost.
	ownerID := meID(t, f.owner, f.token).String()
	resp := f.owner.do(http.MethodPost, f.budgetPath(""), map[string]any{
		"label": "Beach house", "category": "lodging", "amount_minor": 45000,
		"incurred_on": "2026-09-14",
		"splits":      []map[string]any{{"user_id": ownerID, "amount_minor": 45000}},
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)

	var entry map[string]any
	resp.decode(t, &entry)
	entryID := entry["id"].(string)
	if entry["incurred_on"] != "2026-09-14" {
		t.Errorf("incurred_on round-tripped as %v, want a plain date", entry["incurred_on"])
	}
	if splits, ok := entry["splits"].([]any); !ok || len(splits) != 1 {
		t.Fatalf("created entry has splits %v, want one", entry["splits"])
	}

	// List.
	resp = f.owner.do(http.MethodGet, f.budgetPath(""), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
	var ledger []map[string]any
	resp.decode(t, &ledger)
	if len(ledger) != 1 {
		t.Fatalf("ledger has %d entries, want 1", len(ledger))
	}

	// Replace. PUT, because the entry and its complete split set are written together.
	resp = f.owner.do(http.MethodPut, f.budgetPath(entryID), map[string]any{
		"label": "Beach house, four nights", "category": "lodging", "amount_minor": 60000,
		"splits":  []map[string]any{{"user_id": ownerID, "amount_minor": 60000}},
		"version": entry["version"],
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
	var updated map[string]any
	resp.decode(t, &updated)
	if updated["amount_minor"].(float64) != 60000 {
		t.Errorf("amount is %v after the replace, want 60000", updated["amount_minor"])
	}

	// Get.
	resp = f.owner.do(http.MethodGet, f.budgetPath(entryID), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)

	// Delete.
	resp = f.owner.do(http.MethodDelete, f.budgetPath(entryID),
		map[string]any{"version": updated["version"]}, withBearer(f.token))
	assertStatus(t, resp, http.StatusNoContent)

	resp = f.owner.do(http.MethodGet, f.budgetPath(entryID), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusNotFound)
}

// TestBudgetEditWithoutAVersionIsRejectedOverHTTP is D85 as a client actually experiences it.
//
// The rest of the planning API accepts a versionless write and merges it. The budget does not,
// and the difference has to be visible at the API boundary — otherwise a client would discover
// the coarse grain by silently losing someone's number.
//
// Verified against a planted break: replacing requireVersion with versionOrCurrent here — the
// treatment every mergeable entity gets — turns this 422 into a 200.
func TestBudgetEditWithoutAVersionIsRejectedOverHTTP(t *testing.T) {
	f := newLedgerFixture(t, "budget-noversion")

	resp := f.owner.do(http.MethodPost, f.budgetPath(""), map[string]any{
		"label": "Dinner", "category": "food", "amount_minor": 5000,
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)
	var entry map[string]any
	resp.decode(t, &entry)
	entryID := entry["id"].(string)

	// No version key at all.
	resp = f.owner.do(http.MethodPut, f.budgetPath(entryID), map[string]any{
		"label": "Dinner", "category": "food", "amount_minor": 9000,
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusUnprocessableEntity)
	assertProblemField(t, resp, "version")

	// The same omission on a SLOT is fine — that is the contrast worth pinning, because it is
	// what makes the budget's requirement a deliberate exception rather than a global rule.
	slotID := f.makeSlot("Where are we staying")
	resp = f.owner.do(http.MethodPatch,
		fmt.Sprintf("/api/v1/trips/%s/slots/%s", f.tripID, slotID),
		map[string]any{"title": "Where are we staying, really", "fields": []string{"title"}},
		withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
}

// TestUnbalancedSplitsAreRejectedOverHTTP — the client gets a field-level message naming
// `splits`, not a 500 from a constraint firing at commit.
func TestUnbalancedSplitsAreRejectedOverHTTP(t *testing.T) {
	f := newLedgerFixture(t, "budget-unbalanced")
	ownerID := meID(t, f.owner, f.token).String()

	resp := f.owner.do(http.MethodPost, f.budgetPath(""), map[string]any{
		"label": "Does not add up", "category": "other", "amount_minor": 1000,
		"splits": []map[string]any{{"user_id": ownerID, "amount_minor": 600}},
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusUnprocessableEntity)
	assertProblemField(t, resp, "splits")
}

// TestConcurrentBudgetEditsLeaveOneWinnerAndOneConflict is the budget's convergence story, and
// it is deliberately DIFFERENT from the itinerary's.
//
// Two members editing different fields of a slot both succeed — that is the whole point of
// field-level merge. Two members editing one budget entry cannot both succeed, because the
// entry carries a cross-field invariant and merging their halves would produce a ledger neither
// of them wrote (D44). So the correct outcome here is exactly one success and exactly one 409,
// and the loser's numbers are untouched rather than half-applied.
//
// This is what "coarse by design" has to look like from outside, and asserting it is how the
// design stays honest: a future change that quietly made budget writes merge would pass every
// other test in this suite.
//
// Verified against a planted break — the optimistic version predicate defeated in
// BudgetRepository.Save — which turns this into "2 successes and 0 conflicts".
//
// Worth recording what it does NOT catch, because that was found the same way: planting merge
// semantics in the SERVICE (a nil version silently substituting the stored one) leaves this test
// green, because both racers here do send a version. That half of D85 is covered by
// TestBudgetEditWithoutAVersionIsRejectedOverHTTP, which fails on exactly that plant. Two claims,
// two tests; neither one covers the other.
func TestConcurrentBudgetEditsLeaveOneWinnerAndOneConflict(t *testing.T) {
	f := newLedgerFixture(t, "budget-race")
	ownerID := meID(t, f.owner, f.token).String()

	resp := f.owner.do(http.MethodPost, f.budgetPath(""), map[string]any{
		"label": "Shared cost", "category": "other", "amount_minor": 1000,
		"splits": []map[string]any{{"user_id": ownerID, "amount_minor": 1000}},
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)
	var entry map[string]any
	resp.decode(t, &entry)
	entryID := entry["id"].(string)
	version := entry["version"]

	// Both writers hold the SAME version — they read the entry at the same moment.
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []int
		start   = make(chan struct{})
	)
	for i, amount := range []int{2000, 3000} {
		wg.Add(1)
		go func(i, amount int) {
			defer wg.Done()
			c := newClient(t)
			<-start
			r := c.do(http.MethodPut, f.budgetPath(entryID), map[string]any{
				"label": "Shared cost", "category": "other", "amount_minor": amount,
				"splits":  []map[string]any{{"user_id": ownerID, "amount_minor": amount}},
				"version": version,
			}, withBearer(f.token))
			mu.Lock()
			results = append(results, r.status)
			mu.Unlock()
		}(i, amount)
	}
	close(start)
	wg.Wait()

	var ok, conflicts int
	for _, status := range results {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected status %d from a racing budget write", status)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("racing budget writes produced %d successes and %d conflicts, want exactly one "+
			"of each — a budget entry is replaced whole, so both cannot win", ok, conflicts)
	}

	// The survivor is internally consistent: whichever writer won, the entry and its splits
	// agree. A merge of the two would have left them disagreeing.
	stored, err := testBudget.GetByID(context.Background(), mustParseID(t, entryID))
	if err != nil {
		t.Fatalf("reading the entry: %v", err)
	}
	if stored.SplitTotal() != stored.AmountMinor {
		t.Fatalf("the surviving entry violates its own invariant: splits total %d, entry total %d",
			stored.SplitTotal(), stored.AmountMinor)
	}
	if stored.AmountMinor != 2000 && stored.AmountMinor != 3000 {
		t.Errorf("the surviving amount is %d, which is neither writer's value", stored.AmountMinor)
	}
}

// --- attachments ---

// TestUploadIsInvisibleUntilConfirmed walks the two-phase upload end to end and pins the
// property that makes the phases worth having.
//
// A presigned PUT cannot be intercepted, so between the presign and the confirmation the server
// knows nothing about the object. Announcing the attachment at presign time would advertise a
// photo that may never arrive; the row therefore stays invisible, and nothing reaches the
// operation log, until the server has stat'd real bytes.
func TestUploadIsInvisibleUntilConfirmed(t *testing.T) {
	f := newLedgerFixture(t, "upload-flow")
	slotID := f.makeSlot("Where are we staying")
	tripID := mustParseID(t, f.tripID)
	ctx := context.Background()

	seqBefore := currentTripSeq(t, tripID)

	resp := f.owner.do(http.MethodPost, f.attachmentPath("/uploads"), map[string]any{
		"owner":         map[string]any{"slot_id": slotID},
		"content_type":  "image/png",
		"original_name": "booking.png",
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)

	var ticket struct {
		Attachment map[string]any `json:"attachment"`
		UploadURL  string         `json:"upload_url"`
	}
	resp.decode(t, &ticket)
	attachmentID := ticket.Attachment["id"].(string)
	if ticket.Attachment["status"] != "pending" {
		t.Errorf("a reserved attachment is %v, want pending", ticket.Attachment["status"])
	}
	if ticket.UploadURL == "" {
		t.Fatal("no upload URL was issued")
	}
	// The object key never reaches the client: it is an internal identifier, useless without a
	// signature, and it would leak the bucket layout.
	if _, leaked := ticket.Attachment["storage_key"]; leaked {
		t.Error("the response exposed the storage key")
	}

	// Nothing has been announced: the sequencer has not moved.
	if got := currentTripSeq(t, tripID); got != seqBefore {
		t.Errorf("presigning advanced the trip sequence from %d to %d; a pending upload must "+
			"not be broadcast", seqBefore, got)
	}
	// And it is not listed to the trip either.
	resp = f.owner.do(http.MethodGet, f.attachmentPath("?slot_id="+slotID), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
	var listed []map[string]any
	resp.decode(t, &listed)
	for _, a := range listed {
		if a["id"] == attachmentID && a["status"] == "ready" {
			t.Error("a pending upload is already listed as ready")
		}
	}

	// Confirming before the object exists fails: the server trusts storage, not the client.
	resp = f.owner.do(http.MethodPost, f.attachmentPath("/"+attachmentID+"/confirm"), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusUnprocessableEntity)

	// The browser's direct PUT lands.
	stored, err := testAttachments.GetByID(ctx, mustParseID(t, attachmentID))
	if err != nil {
		t.Fatalf("reading the reserved attachment: %v", err)
	}
	testStorage.Put(stored.StorageKey, "image/png", make([]byte, 4096))

	resp = f.owner.do(http.MethodPost, f.attachmentPath("/"+attachmentID+"/confirm"), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
	var confirmed map[string]any
	resp.decode(t, &confirmed)
	if confirmed["status"] != "ready" {
		t.Errorf("status after confirmation is %v, want ready", confirmed["status"])
	}
	if confirmed["size_bytes"].(float64) != 4096 {
		t.Errorf("size after confirmation is %v, want 4096", confirmed["size_bytes"])
	}

	// NOW it is in the log, so an absent member will learn about it on resync.
	if got := currentTripSeq(t, tripID); got != seqBefore+1 {
		t.Errorf("trip sequence is %d after confirmation, want %d — a confirmed upload must be "+
			"logged, or a resyncing client never learns it exists", got, seqBefore+1)
	}

	// A signed read URL is minted per request and is not the stored key.
	resp = f.owner.do(http.MethodGet, f.attachmentPath("/"+attachmentID+"/url"), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
	var download map[string]any
	resp.decode(t, &download)
	if download["url"] == "" {
		t.Error("no download URL was returned")
	}

	// Deleting tombstones the row and logs a removal, but keeps the object.
	resp = f.owner.do(http.MethodDelete, f.attachmentPath("/"+attachmentID), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusNoContent)
	if !testStorage.Has(stored.StorageKey) {
		t.Error("a soft delete destroyed the stored object")
	}
}

// TestTwoConcurrentUploadsBothExistOverHTTP is the entire attachment conflict story at the API
// level: there is nothing to merge, so both simply exist.
func TestTwoConcurrentUploadsBothExistOverHTTP(t *testing.T) {
	f := newLedgerFixture(t, "upload-race")
	slotID := f.makeSlot("Where are we staying")
	ctx := context.Background()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		ids   []string
		start = make(chan struct{})
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := newClient(t)
			<-start
			r := c.do(http.MethodPost, f.attachmentPath("/uploads"), map[string]any{
				"owner":         map[string]any{"slot_id": slotID},
				"content_type":  "image/png",
				"original_name": fmt.Sprintf("photo-%d.png", i),
			}, withBearer(f.token))
			if r.status != http.StatusCreated {
				t.Errorf("upload request %d returned %d", i, r.status)
				return
			}
			var ticket struct {
				Attachment map[string]any `json:"attachment"`
			}
			r.decode(t, &ticket)
			mu.Lock()
			ids = append(ids, ticket.Attachment["id"].(string))
			mu.Unlock()
		}(i)
	}
	close(start)
	wg.Wait()

	if len(ids) != 2 {
		t.Fatalf("got %d reserved attachments, want 2", len(ids))
	}
	for _, id := range ids {
		stored, err := testAttachments.GetByID(ctx, mustParseID(t, id))
		if err != nil {
			t.Fatalf("reading attachment %s: %v", id, err)
		}
		testStorage.Put(stored.StorageKey, "image/png", make([]byte, 512))
		resp := f.owner.do(http.MethodPost, f.attachmentPath("/"+id+"/confirm"), nil, withBearer(f.token))
		assertStatus(t, resp, http.StatusOK)
	}

	resp := f.owner.do(http.MethodGet, f.attachmentPath("?slot_id="+slotID), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
	var listed []map[string]any
	resp.decode(t, &listed)
	if len(listed) != 2 {
		t.Fatalf("%d attachments on the slot, want 2 — concurrent uploads must not displace "+
			"each other", len(listed))
	}
}

// TestAttachmentOwnerMustBeExactlyOne pins the exclusive arc at the API boundary, where a
// client can send anything.
func TestAttachmentOwnerMustBeExactlyOne(t *testing.T) {
	f := newLedgerFixture(t, "owner-arc")
	slotID := f.makeSlot("Where are we staying")

	for _, tc := range []struct {
		name  string
		owner map[string]any
	}{
		{"no owner", map[string]any{}},
		{"two owners", map[string]any{"slot_id": slotID, "budget_entry_id": slotID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.owner.do(http.MethodPost, f.attachmentPath("/links"), map[string]any{
				"owner": tc.owner, "url": "https://example.test/x",
			}, withBearer(f.token))
			assertStatus(t, resp, http.StatusUnprocessableEntity)
			assertProblemField(t, resp, "owner")
		})
	}
}

// TestLedgerAndAttachmentsFoldBackToTheDatabase is the Slice 3 extension of the invariant the
// whole sync design rests on:
//
//	fold(trip_ops) == database state
//
// It matters most for these two entities, precisely BECAUSE they do not merge. It would have
// been easy to conclude that an entity with no conflict resolution does not need to be in the
// log at all — and that conclusion would have left a member who was offline permanently unaware
// of every photo and every ledger line added while they were away, with every other test still
// green. This is the test that would fail.
//
// Verified against a planted break: dropping the rec.budget and rec.attachment calls from
// BudgetService.Create and AttachmentService.ConfirmUpload. The fold then dies on
// "op budget.delete.v1 targets unknown budget entry", and TestUploadIsInvisibleUntilConfirmed
// catches the attachment half by watching the trip sequencer fail to advance.
func TestLedgerAndAttachmentsFoldBackToTheDatabase(t *testing.T) {
	f := newLedgerFixture(t, "ledger-fold")
	ctx := context.Background()
	tripID := mustParseID(t, f.tripID)
	ownerID := meID(t, f.owner, f.token).String()
	slotID := f.makeSlot("Where are we staying")

	// A ledger line, edited, plus a second one deleted — so the fold has to handle a replace
	// and a tombstone, not just creates.
	resp := f.owner.do(http.MethodPost, f.budgetPath(""), map[string]any{
		"label": "Beach house", "category": "lodging", "amount_minor": 45000,
		"splits": []map[string]any{{"user_id": ownerID, "amount_minor": 45000}},
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)
	var kept map[string]any
	resp.decode(t, &kept)

	resp = f.owner.do(http.MethodPut, f.budgetPath(kept["id"].(string)), map[string]any{
		"label": "Beach house, four nights", "category": "lodging", "amount_minor": 60000,
		"splits":  []map[string]any{{"user_id": ownerID, "amount_minor": 60000}},
		"version": kept["version"],
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)
	resp.decode(t, &kept)

	resp = f.owner.do(http.MethodPost, f.budgetPath(""), map[string]any{
		"label": "Cancelled tour", "category": "activity", "amount_minor": 2000,
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)
	var removed map[string]any
	resp.decode(t, &removed)
	resp = f.owner.do(http.MethodDelete, f.budgetPath(removed["id"].(string)),
		map[string]any{"version": removed["version"]}, withBearer(f.token))
	assertStatus(t, resp, http.StatusNoContent)

	// A link attachment (ready immediately) and a confirmed file upload.
	resp = f.owner.do(http.MethodPost, f.attachmentPath("/links"), map[string]any{
		"owner": map[string]any{"slot_id": slotID},
		"url":   "https://example.test/booking", "title": "Booking confirmation",
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)

	resp = f.owner.do(http.MethodPost, f.attachmentPath("/uploads"), map[string]any{
		"owner":        map[string]any{"slot_id": slotID},
		"content_type": "image/png", "original_name": "room.png",
	}, withBearer(f.token))
	assertStatus(t, resp, http.StatusCreated)
	var ticket struct {
		Attachment map[string]any `json:"attachment"`
	}
	resp.decode(t, &ticket)
	uploadID := ticket.Attachment["id"].(string)
	stored, err := testAttachments.GetByID(ctx, mustParseID(t, uploadID))
	if err != nil {
		t.Fatalf("reading the reserved attachment: %v", err)
	}
	testStorage.Put(stored.StorageKey, "image/png", make([]byte, 2048))
	resp = f.owner.do(http.MethodPost, f.attachmentPath("/"+uploadID+"/confirm"), nil, withBearer(f.token))
	assertStatus(t, resp, http.StatusOK)

	// --- the fold ---
	ops, err := testOpLog.ListSince(ctx, tripID, 0, 0)
	if err != nil {
		t.Fatalf("reading the operation log: %v", err)
	}
	folded, err := domain.Fold(ops)
	if err != nil {
		t.Fatalf("folding the operation log: %v", err)
	}
	for i, op := range ops {
		if op.Seq != int64(i+1) {
			t.Fatalf("operation log is not gapless: position %d has seq %d", i, op.Seq)
		}
	}

	// Budget: every live entry in the database must appear in the fold with the same total,
	// the same version, and the same split set.
	dbEntries, err := testBudget.ListForTrip(ctx, tripID)
	if err != nil {
		t.Fatalf("listing the ledger: %v", err)
	}
	if len(dbEntries) != 1 {
		t.Fatalf("database has %d live entries, want 1", len(dbEntries))
	}
	for _, want := range dbEntries {
		got, ok := folded.Budgets[want.ID]
		if !ok {
			t.Fatalf("entry %s is in the database but not in fold(log)", want.ID)
		}
		if got.IsDeleted() {
			t.Errorf("entry %s folded to a tombstone but is live in the database", want.ID)
		}
		if got.AmountMinor != want.AmountMinor {
			t.Errorf("entry %s: fold has amount %d, database has %d",
				want.ID, got.AmountMinor, want.AmountMinor)
		}
		if got.Version != want.Version {
			t.Errorf("entry %s: fold has version %d, database has %d",
				want.ID, got.Version, want.Version)
		}
		if got.Label != want.Label {
			t.Errorf("entry %s: fold has label %q, database has %q", want.ID, got.Label, want.Label)
		}
		if len(got.Splits) != len(want.Splits) {
			t.Fatalf("entry %s: fold has %d splits, database has %d",
				want.ID, len(got.Splits), len(want.Splits))
		}
		for i := range want.Splits {
			if got.Splits[i].UserID != want.Splits[i].UserID ||
				got.Splits[i].AmountMinor != want.Splits[i].AmountMinor {
				t.Errorf("entry %s split %d: fold has %+v, database has %+v",
					want.ID, i, got.Splits[i], want.Splits[i])
			}
		}
		if got.SplitTotal() != got.AmountMinor {
			t.Errorf("entry %s: the FOLDED entry does not balance (%d vs %d)",
				want.ID, got.SplitTotal(), got.AmountMinor)
		}
	}
	// The deleted entry must be a tombstone in the fold, not simply missing — that is how a
	// resyncing client learns to drop it rather than keeping a stale row forever.
	deletedID := mustParseID(t, removed["id"].(string))
	if tomb, ok := folded.Budgets[deletedID]; !ok {
		t.Error("the deleted entry is absent from fold(log); a replaying client would never " +
			"learn it was removed")
	} else if !tomb.IsDeleted() {
		t.Error("the deleted entry folded without its tombstone")
	}

	// Attachments: same comparison, minus the version they do not have.
	dbAttachments, err := testAttachments.ListForTrip(ctx, tripID)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	if len(dbAttachments) != 2 {
		t.Fatalf("database has %d live attachments, want 2", len(dbAttachments))
	}
	for _, want := range dbAttachments {
		got, ok := folded.Attachments[want.ID]
		if !ok {
			t.Fatalf("attachment %s is in the database but not in fold(log)", want.ID)
		}
		if got.Kind != want.Kind || got.Status != want.Status {
			t.Errorf("attachment %s: fold has %s/%s, database has %s/%s",
				want.ID, got.Kind, got.Status, want.Kind, want.Status)
		}
		if got.StorageKey != want.StorageKey || got.ExternalURL != want.ExternalURL {
			t.Errorf("attachment %s: fold and database disagree on its location", want.ID)
		}
		if (got.SizeBytes == nil) != (want.SizeBytes == nil) ||
			(got.SizeBytes != nil && *got.SizeBytes != *want.SizeBytes) {
			t.Errorf("attachment %s: fold has size %v, database has %v",
				want.ID, got.SizeBytes, want.SizeBytes)
		}
		if got.SlotID == nil || want.SlotID == nil || *got.SlotID != *want.SlotID {
			t.Errorf("attachment %s: fold and database disagree on the owner", want.ID)
		}
	}
}

// TestAttachmentIntentsAreRefusedOverTheSocket pins the deliberate boundary (D86).
//
// Attachments are broadcast-only in the most literal sense: the engine delivers their
// operations and never accepts them, because an upload is a presign plus a direct PUT plus a
// confirmation and a frame cannot express that. A client that tries gets an error saying so
// rather than silence, because a socket that accepted the frame and did nothing would be far
// worse than one that refuses.
func TestAttachmentIntentsAreRefusedOverTheSocket(t *testing.T) {
	f := newLedgerFixture(t, "attachment-ws")
	slotID := f.makeSlot("Where are we staying")
	tripID := mustParseID(t, f.tripID)
	userID := meID(t, f.owner, f.token)

	ws := dialWS(t, "owner", f.owner, f.token, userID)
	defer ws.close()
	ws.subscribe(tripID)

	clientOpID := ws.submit(tripID, domain.OpAttachmentAdd, domain.NewID(),
		domain.OpAttachmentAdd.AllowedFields(), map[string]any{
			"kind": "link", "status": "ready", "slot_id": slotID,
			"external_url": "https://example.test/x",
		})
	ws.awaitResolution(clientOpID, 3*time.Second)

	if errs := ws.errorFrames(); len(errs) == 0 {
		t.Fatal("the socket accepted an attachment operation; attachments are written through " +
			"REST and only broadcast here")
	}

	// And nothing was written: a refusal that still created the row would be the worst of both.
	attachments, err := testAttachments.ListForTrip(context.Background(), tripID)
	if err != nil {
		t.Fatalf("listing attachments: %v", err)
	}
	if len(attachments) != 0 {
		t.Errorf("%d attachments exist after a refused socket operation, want 0", len(attachments))
	}
}

// --- local helpers ---

// mustParseID converts an id from a JSON response body.
func mustParseID(t *testing.T, raw string) domain.ID {
	t.Helper()
	id, err := domain.ParseID("id", raw)
	if err != nil {
		t.Fatalf("parsing id %q: %v", raw, err)
	}
	return id
}

// currentTripSeq reads the trip's sequencer without allocating.
//
// Used to assert that an operation was — or crucially was NOT — logged. Counting operations of
// a given kind would miss the case this is guarding: a presign that logged something under a
// different kind is still a presign that announced a pending upload.
func currentTripSeq(t *testing.T, tripID domain.ID) int64 {
	t.Helper()
	seq, err := testTrips.CurrentOpSeq(context.Background(), tripID)
	if err != nil {
		t.Fatalf("reading the trip sequence: %v", err)
	}
	return seq
}
