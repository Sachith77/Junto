package domain

import (
	"errors"
	"testing"
)

// TestSplitEvenlyPreservesTheTotal is the property that matters. Integer division leaves a
// remainder, and the whole point of storing explicit amounts rather than percentages is that
// the parts must sum to the whole exactly, for every amount and every group size.
func TestSplitEvenlyPreservesTheTotal(t *testing.T) {
	members := make([]ID, 7)
	for i := range members {
		members[i] = NewID()
	}

	for _, n := range []int{1, 2, 3, 4, 5, 6, 7} {
		for _, amount := range []int64{0, 1, 2, 7, 100, 999, 1000, 1001, 123456789} {
			splits := SplitEvenly(amount, members[:n])
			if len(splits) != n {
				t.Fatalf("amount %d across %d members produced %d splits", amount, n, len(splits))
			}

			var total int64
			for _, s := range splits {
				if s.AmountMinor < 0 {
					t.Fatalf("amount %d across %d members produced a negative share", amount, n)
				}
				total += s.AmountMinor
			}
			if total != amount {
				t.Fatalf("amount %d across %d members summed to %d", amount, n, total)
			}
		}
	}
}

// TestSplitEvenlyIsDeterministic is what makes the remainder assignment safe to recompute on
// any machine: the same amount and the same ordered member list must always give the same
// answer, or two clients rendering the same entry disagree about who owes the extra unit.
func TestSplitEvenlyIsDeterministic(t *testing.T) {
	a, b, c := NewID(), NewID(), NewID()
	members := []ID{a, b, c}

	first := SplitEvenly(1000, members)
	second := SplitEvenly(1000, members)

	for i := range first {
		if first[i].UserID != second[i].UserID || first[i].AmountMinor != second[i].AmountMinor {
			t.Fatalf("split %d differs between runs: %+v vs %+v", i, first[i], second[i])
		}
	}

	// 1000 / 3 = 333 remainder 1, so the first member carries the extra unit.
	if first[0].AmountMinor != 334 || first[1].AmountMinor != 333 || first[2].AmountMinor != 333 {
		t.Errorf("expected 334/333/333, got %d/%d/%d",
			first[0].AmountMinor, first[1].AmountMinor, first[2].AmountMinor)
	}
}

func TestSplitEvenlyEdgeCases(t *testing.T) {
	if s := SplitEvenly(100, nil); s != nil {
		t.Error("splitting between nobody yields nothing")
	}
	if s := SplitEvenly(-1, []ID{NewID()}); s != nil {
		t.Error("a negative amount must not produce splits")
	}
	// Fewer units than members: some shares are legitimately zero.
	splits := SplitEvenly(2, []ID{NewID(), NewID(), NewID()})
	var total int64
	for _, s := range splits {
		total += s.AmountMinor
	}
	if total != 2 {
		t.Errorf("2 across 3 members summed to %d, want 2", total)
	}
}

// TestBudgetEntryEnforcesTheSumInvariant covers the domain-level half of the guarantee. The
// deferred constraint trigger in migration 000003 is the database-level backstop; this one
// exists so the caller gets a field-level message instead of a constraint violation at COMMIT.
func TestBudgetEntryEnforcesTheSumInvariant(t *testing.T) {
	alice, bob := NewID(), NewID()
	base := func() *BudgetEntry {
		return &BudgetEntry{
			TripID: NewID(), Label: "Hotel", Category: BudgetCategoryLodging,
			AmountMinor: 1000,
		}
	}

	t.Run("unsplit entry is valid", func(t *testing.T) {
		// "Not split yet" is a normal state, distinct from "split wrong".
		e := base()
		if err := e.Validate(); err != nil {
			t.Errorf("an unsplit entry must be valid: %v", err)
		}
		if e.IsSplit() {
			t.Error("an entry with no splits is not split")
		}
	})

	t.Run("splits summing to the total are valid", func(t *testing.T) {
		e := base()
		e.Splits = SplitEvenly(e.AmountMinor, []ID{alice, bob})
		if err := e.Validate(); err != nil {
			t.Errorf("splits summing to the total must be valid: %v", err)
		}
		if e.SplitTotal() != 1000 {
			t.Errorf("split total = %d, want 1000", e.SplitTotal())
		}
	})

	t.Run("splits that do not sum are rejected", func(t *testing.T) {
		e := base()
		e.Splits = []BudgetSplit{
			{ID: NewID(), UserID: alice, AmountMinor: 400},
			{ID: NewID(), UserID: bob, AmountMinor: 400},
		}
		err := e.Validate()
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("expected a validation error, got %v", err)
		}
		ve, _ := AsValidationError(err)
		found := false
		for _, v := range ve.Violations {
			if v.Code == "sum_mismatch" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a sum_mismatch violation, got %+v", ve.Violations)
		}
	})

	t.Run("a member may appear at most once", func(t *testing.T) {
		e := base()
		e.Splits = []BudgetSplit{
			{ID: NewID(), UserID: alice, AmountMinor: 500},
			{ID: NewID(), UserID: alice, AmountMinor: 500},
		}
		if err := e.Validate(); err == nil {
			t.Error("a duplicated member in a split must be rejected")
		}
	})

	t.Run("negative amounts are rejected", func(t *testing.T) {
		e := base()
		e.AmountMinor = -1
		if err := e.Validate(); err == nil {
			t.Error("a negative entry total must be rejected")
		}
	})

	t.Run("unknown category is rejected", func(t *testing.T) {
		e := base()
		e.Category = BudgetCategory("bribes")
		if err := e.Validate(); err == nil {
			t.Error("an unknown category must be rejected (it is mirrored by a DB CHECK)")
		}
	})
}

// TestAttachmentExclusiveArc covers the ownership rule that replaces a polymorphic FK.
func TestAttachmentExclusiveArc(t *testing.T) {
	optionID, slotID := NewID(), NewID()
	base := func() *Attachment {
		return &Attachment{
			TripID: NewID(), SlotOptionID: &optionID,
			Kind: AttachmentKindFile, Status: AttachmentStatusPending,
			StorageKey: "trips/a/opt/b/ticket.png", ContentType: "image/png",
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a well-formed pending file attachment should validate: %v", err)
	}

	t.Run("no owner is rejected", func(t *testing.T) {
		a := base()
		a.SlotOptionID = nil
		if a.OwnerCount() != 0 {
			t.Fatal("fixture is wrong")
		}
		if err := a.Validate(); err == nil {
			t.Error("an ownerless attachment must be rejected")
		}
	})

	t.Run("two owners are rejected", func(t *testing.T) {
		a := base()
		a.SlotID = &slotID
		if a.OwnerCount() != 2 {
			t.Fatal("fixture is wrong")
		}
		if err := a.Validate(); err == nil {
			t.Error("an attachment with two owners must be rejected")
		}
	})

	t.Run("a link is ready immediately and carries no key", func(t *testing.T) {
		a := base()
		a.Kind = AttachmentKindLink
		a.StorageKey = ""
		a.ExternalURL = "https://example.test/booking"
		a.Status = AttachmentStatusReady
		if err := a.Validate(); err != nil {
			t.Errorf("a link attachment should validate: %v", err)
		}

		// There is nothing to upload, so pending is meaningless for a link.
		a.Status = AttachmentStatusPending
		if err := a.Validate(); err == nil {
			t.Error("a pending link attachment must be rejected")
		}
	})

	t.Run("a file must not carry a URL", func(t *testing.T) {
		a := base()
		a.ExternalURL = "https://example.test/x"
		if err := a.Validate(); err == nil {
			t.Error("a file attachment with a URL must be rejected")
		}
	})

	t.Run("oversized uploads are rejected", func(t *testing.T) {
		// The size limit cannot be enforced by the presigned URL, so this check on the
		// confirm path is the only thing standing between us and an unbounded upload.
		a := base()
		tooBig := MaxAttachmentBytes + 1
		a.SizeBytes = &tooBig
		if err := a.Validate(); err == nil {
			t.Error("an oversized attachment must be rejected")
		}

		atLimit := MaxAttachmentBytes
		a.SizeBytes = &atLimit
		if err := a.Validate(); err != nil {
			t.Errorf("an attachment exactly at the limit must be accepted: %v", err)
		}
	})
}

func TestAttachmentOwnerValidity(t *testing.T) {
	id := NewID()
	if (AttachmentOwner{}).Valid() {
		t.Error("no owner is invalid")
	}
	if !(AttachmentOwner{SlotOptionID: &id}).Valid() {
		t.Error("exactly one owner is valid")
	}
	if (AttachmentOwner{SlotOptionID: &id, SlotID: &id}).Valid() {
		t.Error("two owners are invalid")
	}
}
