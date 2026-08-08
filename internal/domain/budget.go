package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Money is represented throughout as int64 MINOR UNITS (paise, cents) in the trip's base
// currency — never a float, and never a decimal string.
//
// Binary floating point cannot represent 0.10 exactly, and the error compounds across a
// split until the parts no longer sum to the whole. That is precisely the invariant the
// budget exists to protect, so the representation has to make the failure impossible rather
// than unlikely.
//
// One currency per trip, with no per-row currency field. Travel is genuinely multi-currency,
// but FX is a subsystem (rate source, rate-at-time-of-entry, re-conversion when rates move)
// and a currency field WITHOUT it produces sums that are silently meaningless. Deferring is
// the honest option; adding per-row currency later is additive.

// BudgetCategory classifies a ledger line. Kept in sync with budget_entries_category.
type BudgetCategory string

const (
	BudgetCategoryLodging   BudgetCategory = "lodging"
	BudgetCategoryTransport BudgetCategory = "transport"
	BudgetCategoryFood      BudgetCategory = "food"
	BudgetCategoryActivity  BudgetCategory = "activity"
	BudgetCategoryShopping  BudgetCategory = "shopping"
	BudgetCategoryOther     BudgetCategory = "other"
)

// Valid reports whether c is a known category.
func (c BudgetCategory) Valid() bool {
	switch c {
	case BudgetCategoryLodging, BudgetCategoryTransport, BudgetCategoryFood,
		BudgetCategoryActivity, BudgetCategoryShopping, BudgetCategoryOther:
		return true
	}
	return false
}

// BudgetSplit is one member's share of one entry.
//
// Explicit amounts, not percentages. Integer division has a remainder — 1000 across three
// people is 333/333/334 — and the extra unit must belong to a specific person
// deterministically, rather than being recomputed (and disagreed about) by each client.
type BudgetSplit struct {
	ID          ID
	UserID      ID
	AmountMinor int64
	CreatedAt   time.Time
}

// BudgetEntry is a ledger line for a trip, optionally attached to the proposal it pays for.
//
// # Stage 2 sync note — this entity is NOT field-mergeable
//
// The invariant is that the splits sum to the total at every point a client renders. Budget
// writes are therefore ATOMIC OPERATIONS carrying the total and the complete split set
// together, applied whole under optimistic version, with conflicts surfacing as an explicit
// 409 for a human to resolve.
//
// Field-level merging plus a post-merge check was considered and rejected: each individual
// merge is locally plausible and jointly wrong. A sets the total to 1000; B sets their own
// split to 600. Both operations are valid in isolation and the merged state violates the
// invariant. Detecting that is easy; REPAIRING it is not, because repair means choosing
// whose edit to discard — exactly the decision merging exists to avoid. Silently rewriting
// someone's number is worse for money than showing them a conflict.
//
// So the budget deliberately has a COARSER conflict grain than the itinerary. Text has no
// cross-field invariant; money does. A deferred constraint trigger in migration 000003 is
// the database-level backstop, so a violating state cannot be committed by any writer
// regardless of what the sync engine does.
type BudgetEntry struct {
	ID     ID
	TripID ID

	// SlotOptionID optionally ties this cost to the proposal it pays for.
	SlotOptionID *ID

	Label       string
	Category    BudgetCategory
	AmountMinor int64

	PaidBy     *ID
	IncurredOn *time.Time

	// Splits is the complete set. It is loaded and written with the entry, never
	// independently, because the two together are the atomic unit described above.
	Splits []BudgetSplit

	CreatedBy *ID
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// IsDeleted reports whether the entry is tombstoned.
func (e *BudgetEntry) IsDeleted() bool { return e.DeletedAt != nil }

// SplitTotal sums the splits.
func (e *BudgetEntry) SplitTotal() int64 {
	var total int64
	for _, s := range e.Splits {
		total += s.AmountMinor
	}
	return total
}

// IsSplit reports whether the entry has been divided at all. An entry with no splits is a
// legitimate state — "not split yet" is different from "split wrong".
func (e *BudgetEntry) IsSplit() bool { return len(e.Splits) > 0 }

// Validate checks the entry's invariants, including the sum.
func (e *BudgetEntry) Validate() error {
	ve := &ValidationError{}

	ve.AddIf(e.TripID == NilID, "trip_id", "required", "trip id is required")
	ve.AddIf(!e.Category.Valid(), "category", "invalid_category",
		"must be one of: lodging, transport, food, activity, shopping, other")

	label := strings.TrimSpace(e.Label)
	switch {
	case label == "":
		ve.Add("label", "required", "label is required")
	case utf8.RuneCountInString(label) > 200:
		ve.Add("label", "too_long", "label must be at most 200 characters")
	}

	ve.AddIf(e.AmountMinor < 0, "amount", "negative", "amount must not be negative")

	seen := make(map[ID]struct{}, len(e.Splits))
	for i, s := range e.Splits {
		if s.AmountMinor < 0 {
			ve.Add("splits", "negative", "a split must not be negative")
		}
		if s.UserID == NilID {
			ve.Add("splits", "missing_user", "every split must name a member")
		}
		if _, dup := seen[s.UserID]; dup {
			ve.Add("splits", "duplicate_member", "a member may appear at most once in a split")
		}
		seen[s.UserID] = struct{}{}
		_ = i
	}

	// The invariant, checked here so the caller gets a field-level message rather than a
	// constraint violation from the deferred trigger at COMMIT.
	if e.IsSplit() && e.SplitTotal() != e.AmountMinor {
		ve.Add("splits", "sum_mismatch",
			"splits must sum to exactly the entry total")
	}

	return ve.OrNil()
}

// SplitEvenly divides an amount between members, assigning the remainder deterministically.
//
// Integer division leaves a remainder that has to go somewhere. Handing it to the first
// members in the given order — rather than rounding each share — guarantees the parts sum to
// the whole exactly, and guarantees every client computing the same split from the same
// ordered member list gets the same answer.
//
// The caller is responsible for passing members in a stable order (sorted by id), which is
// what makes "deterministic" true across machines rather than merely within one.
func SplitEvenly(amountMinor int64, members []ID) []BudgetSplit {
	if len(members) == 0 || amountMinor < 0 {
		return nil
	}

	n := int64(len(members))
	base := amountMinor / n
	remainder := amountMinor % n

	splits := make([]BudgetSplit, 0, len(members))
	for i, userID := range members {
		share := base
		if int64(i) < remainder {
			share++
		}
		splits = append(splits, BudgetSplit{
			ID:          NewID(),
			UserID:      userID,
			AmountMinor: share,
		})
	}
	return splits
}
