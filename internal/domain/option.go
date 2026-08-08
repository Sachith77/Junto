package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Place holds location data for a slot option.
//
// Discrete fields rather than a JSONB blob. Stage 2 resolves conflicts at field level, so
// two users concurrently editing Title and Address should both succeed. An opaque blob would
// coarsen that back to whole-entity conflicts — the same mistake as storing a whole day as
// one document, one level further down.
type Place struct {
	Name    string
	Address string

	// Lat/Lng are pointers because (0, 0) is a real coordinate in the Gulf of Guinea. The
	// general rule in this schema: a column is nullable only where NULL is semantically
	// distinct from the zero value. Name and Address are plain strings for exactly that
	// reason — an absent name and an empty name are the same thing.
	Lat *float64
	Lng *float64

	// ProviderID is an opaque external reference (e.g. a maps provider place id).
	ProviderID string
}

// HasCoordinates reports whether both coordinates are set.
func (p Place) HasCoordinates() bool { return p.Lat != nil && p.Lng != nil }

// SlotOption is one candidate proposal under a slot: "Taj Exotica" under "Where are we
// staying in Goa".
//
// # Stage 2 sync note
//
// An option is FIELD-LEVEL MERGEABLE, like a slot. Title, notes, place fields and the cost
// estimate are independent facts. The operation vocabulary needs per-field option edits,
// create, and soft delete.
type SlotOption struct {
	ID     ID
	SlotID ID

	// TripID is denormalised two levels for authorization and sync-room scoping, kept
	// consistent by the composite FK (slot_id, trip_id) -> slots(id, trip_id).
	TripID ID

	Title       string
	Notes       string
	ExternalURL string

	// EstimatedCostMinor is the PROPOSER'S ESTIMATE, in the trip's base currency and in
	// minor units (paise, cents). Deliberately distinct from a BudgetEntry, which is the
	// ledger: this answers "roughly what would this cost", not "what did we spend".
	//
	// Nil means no estimate was given. Zero is a real value — a free walking tour — so the
	// pointer is carrying a genuine third state.
	EstimatedCostMinor *int64

	Place Place

	ProposedBy *ID

	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// IsDeleted reports whether the option is tombstoned.
func (o *SlotOption) IsDeleted() bool { return o.DeletedAt != nil }

// Validate checks the option's invariants.
func (o *SlotOption) Validate() error {
	ve := &ValidationError{}

	ve.AddIf(o.SlotID == NilID, "slot_id", "required", "slot id is required")
	ve.AddIf(o.TripID == NilID, "trip_id", "required", "trip id is required")

	title := strings.TrimSpace(o.Title)
	switch {
	case title == "":
		ve.Add("title", "required", "title is required")
	case utf8.RuneCountInString(title) > 200:
		ve.Add("title", "too_long", "title must be at most 200 characters")
	}

	ve.AddIf(utf8.RuneCountInString(o.Notes) > 10000, "notes", "too_long",
		"notes must be at most 10000 characters")
	ve.AddIf(o.EstimatedCostMinor != nil && *o.EstimatedCostMinor < 0,
		"estimated_cost", "negative", "an estimate must not be negative")

	if (o.Place.Lat == nil) != (o.Place.Lng == nil) {
		ve.Add("place", "incomplete_coordinates", "latitude and longitude must be provided together")
	}
	if o.Place.Lat != nil && (*o.Place.Lat < -90 || *o.Place.Lat > 90) {
		ve.Add("place.lat", "out_of_range", "latitude must be between -90 and 90")
	}
	if o.Place.Lng != nil && (*o.Place.Lng < -180 || *o.Place.Lng > 180) {
		ve.Add("place.lng", "out_of_range", "longitude must be between -180 and 180")
	}

	return ve.OrNil()
}

// Vote is one member's choice within one slot.
//
// # Why this shape matters for Stage 2
//
// There is exactly one row per (SlotID, UserID), with exactly one mutable field. That makes
// a vote a LAST-WRITER-WINS REGISTER — the simplest convergent structure there is. It has no
// tombstone to reconcile and no delete-versus-edit case, so it should be the easiest entity
// in the system to prove convergent, and its whole operation vocabulary is one verb:
//
//	set_vote(slot_id, option_id | null)
//
// Retraction sets OptionID to nil rather than deleting the row. That is the one deliberate
// break from the tombstone convention in this codebase, and it exists precisely to preserve
// the register shape: a soft-deleted vote row would reintroduce the delete-versus-edit
// conflict the shape avoids.
//
// Single choice per slot rather than approval voting: "a slot resolves to a selected option"
// implies picking one, the tally is then unambiguous, and changing your mind is a single-row
// update rather than a delete plus an insert.
type Vote struct {
	ID     ID
	SlotID ID
	TripID ID
	UserID ID

	// OptionID is nil when the member has explicitly retracted their vote.
	OptionID *ID

	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsRetracted reports whether the member has withdrawn their choice.
func (v *Vote) IsRetracted() bool { return v.OptionID == nil }

// Validate checks the vote's invariants.
func (v *Vote) Validate() error {
	ve := &ValidationError{}
	ve.AddIf(v.SlotID == NilID, "slot_id", "required", "slot id is required")
	ve.AddIf(v.TripID == NilID, "trip_id", "required", "trip id is required")
	ve.AddIf(v.UserID == NilID, "user_id", "required", "user id is required")
	return ve.OrNil()
}

// VoteTally counts the votes for one option within a slot.
type VoteTally struct {
	OptionID ID
	Count    int
}

// Leaders returns the option ids with the highest count, and that count.
//
// Returns every tied option rather than picking one. A tie is a real state that the group
// has to resolve — silently choosing a winner would hide the fact that they have not agreed,
// which is the opposite of what a voting feature is for.
func Leaders(tallies []VoteTally) ([]ID, int) {
	best := 0
	for _, t := range tallies {
		if t.Count > best {
			best = t.Count
		}
	}
	if best == 0 {
		return nil, 0
	}
	var leaders []ID
	for _, t := range tallies {
		if t.Count == best {
			leaders = append(leaders, t.OptionID)
		}
	}
	return leaders, best
}
