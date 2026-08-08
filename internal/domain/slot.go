package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Day is a container within a trip. Slots belong to it.
//
// The alternative — deriving days from trip.StartDate + an offset and not storing them — is
// cheaper and is what many itinerary apps do. It was rejected because the sync engine
// addresses entities by stable identity: "move slot X to day Y" needs a Y that survives the
// trip's start date changing. Derived days would silently re-key every slot whenever someone
// edited the trip dates.
type Day struct {
	ID     ID
	TripID ID

	// Nil while the day is unscheduled.
	Date  *time.Time
	Label string

	// Position is a fractional index (see pkg/fracdex). Ordering is by (Position, ID); the
	// ID tiebreak is what makes concurrent same-slot inserts converge identically on every
	// replica.
	Position string

	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// IsDeleted reports whether the day is soft-deleted.
func (d *Day) IsDeleted() bool { return d.DeletedAt != nil }

// Validate checks the day's invariants.
func (d *Day) Validate() error {
	ve := &ValidationError{}
	ve.AddIf(d.TripID == NilID, "trip_id", "required", "trip id is required")
	ve.AddIf(utf8.RuneCountInString(d.Label) > 100, "label", "too_long", "label must be at most 100 characters")
	validatePosition(ve, "position", d.Position)
	return ve.OrNil()
}

// SlotKind classifies a slot. Kept in sync with the slots_kind CHECK constraint.
type SlotKind string

const (
	SlotKindPlace     SlotKind = "place"
	SlotKindActivity  SlotKind = "activity"
	SlotKindTransport SlotKind = "transport"
	SlotKindLodging   SlotKind = "lodging"
	SlotKindNote      SlotKind = "note"
)

// Valid reports whether k is a known kind.
func (k SlotKind) Valid() bool {
	switch k {
	case SlotKindPlace, SlotKindActivity, SlotKindTransport, SlotKindLodging, SlotKindNote:
		return true
	}
	return false
}

// SlotStatus is the Live-mode coverage state of a slot.
//
// A field on the slot rather than its own table: single-valued with no cardinality, so a
// table would be a join on every itinerary render and buy nothing until per-user or
// historical status is needed.
type SlotStatus string

const (
	// SlotStatusPlanned is the default: decided or not, it has not happened yet.
	SlotStatusPlanned SlotStatus = "planned"
	// SlotStatusCovered means the group actually did this.
	SlotStatusCovered SlotStatus = "covered"
	// SlotStatusSkipped means the group deliberately did not. Distinct from "not yet" —
	// conflating them loses the difference between a plan in progress and a plan abandoned.
	SlotStatusSkipped SlotStatus = "skipped"
)

// Valid reports whether s is a known status.
func (s SlotStatus) Valid() bool {
	switch s {
	case SlotStatusPlanned, SlotStatusCovered, SlotStatusSkipped:
		return true
	}
	return false
}

// Slot is a DECISION within a trip: "Where are we staying in Goa", "Day 2 activity".
//
// It is the addressable, ordered unit of the itinerary. What a flat model would call an
// item is here a slot carrying exactly one option; the shape exists so that "I don't like
// this hotel, here's an alternative" becomes another candidate under the same decision
// rather than a competing entry someone has to reconcile by hand.
//
// # What lives here and what lives on the option
//
// Time and placement live on the SLOT because the itinerary renders by time: an undecided
// slot with three candidate hotels still occupies a schedule position, which it could not
// if time belonged to the chosen option. Place data lives on the OPTION because each
// candidate has its own address and coordinates.
//
// # Stage 2 sync note
//
// A slot is FIELD-LEVEL MERGEABLE. Title, notes, times, status and selection are
// independent facts, and two members editing different fields concurrently must both
// succeed. The operation vocabulary needs per-field slot edits, plus move (day + position
// together) and soft delete. See SlotOption and Vote for the other two conflict classes,
// and internal/domain/budget.go and attachment.go for the two that are deliberately NOT
// field-mergeable.
type Slot struct {
	ID ID

	// TripID is denormalised from Day.TripID. Every authorization check and every sync-room
	// scoping query is by trip, so this removes a join from the hottest path in the system.
	// Drift is prevented by the composite FK (day_id, trip_id) -> days(id, trip_id), which
	// makes a slot pointing at another trip's day unrepresentable in the database.
	TripID ID

	// DayID is nil for backlog slots: decisions not yet placed on a day.
	DayID *ID

	Kind  SlotKind
	Title string
	Notes string

	StartTime *TimeOfDay
	EndTime   *TimeOfDay

	Position string

	// SelectedOptionID is the resolution. Stored rather than derived from the vote tally,
	// because a group routinely overrides its own vote ("we voted for A, but B had
	// availability") and a computed winner cannot represent that.
	SelectedOptionID *ID

	Status          SlotStatus
	StatusChangedAt *time.Time
	StatusChangedBy *ID

	CreatedBy *ID

	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time

	// DeletedAt is a tombstone, and it is load-bearing rather than stylistic. Concurrent
	// delete-versus-edit is one of the three required Stage 2 conflict cases, and you cannot
	// converge on a row that no longer exists — nor can the operation log reference a
	// hard-deleted foreign key.
	DeletedAt *time.Time
}

// IsDeleted reports whether the slot is tombstoned.
func (s *Slot) IsDeleted() bool { return s.DeletedAt != nil }

// IsScheduled reports whether the slot is assigned to a day.
func (s *Slot) IsScheduled() bool { return s.DayID != nil }

// IsResolved reports whether an option has been chosen.
func (s *Slot) IsResolved() bool { return s.SelectedOptionID != nil }

// Validate checks the slot's invariants.
func (s *Slot) Validate() error {
	ve := &ValidationError{}

	ve.AddIf(s.TripID == NilID, "trip_id", "required", "trip id is required")
	ve.AddIf(!s.Kind.Valid(), "kind", "invalid_kind",
		"must be one of: place, activity, transport, lodging, note")
	ve.AddIf(!s.Status.Valid(), "status", "invalid_status",
		"must be one of: planned, covered, skipped")

	title := strings.TrimSpace(s.Title)
	switch {
	case title == "":
		ve.Add("title", "required", "title is required")
	case utf8.RuneCountInString(title) > 200:
		ve.Add("title", "too_long", "title must be at most 200 characters")
	}

	ve.AddIf(utf8.RuneCountInString(s.Notes) > 10000, "notes", "too_long",
		"notes must be at most 10000 characters")

	if s.StartTime != nil && !s.StartTime.Valid() {
		ve.Add("start_time", "invalid_time", "must be a valid time of day")
	}
	if s.EndTime != nil && !s.EndTime.Valid() {
		ve.Add("end_time", "invalid_time", "must be a valid time of day")
	}
	if s.StartTime != nil && s.EndTime != nil && s.StartTime.Valid() && s.EndTime.Valid() &&
		s.EndTime.Minutes() < s.StartTime.Minutes() {
		// Mirrors slots_times. An overnight activity is modelled as two slots on consecutive
		// days rather than as an end time that wraps past midnight, because a wrapping range
		// cannot be ordered or compared without knowing it wraps.
		ve.Add("end_time", "before_start", "end time must not be before start time")
	}

	validatePosition(ve, "position", s.Position)
	return ve.OrNil()
}

// TimeOfDay is a wall-clock time with no date and no zone.
//
// This is a distinct type rather than a time.Time on purpose. A slot's time is "09:30 on
// this day, in the trip's timezone" — not an instant. Modelling it as a time.Time invites
// someone to call .UTC() or compare it across days, both of which are silently wrong.
// Absolute instants would also mean that shifting a trip by one day rewrites every slot,
// and that DST transitions corrupt durations.
//
// The date comes from Day.Date and the zone from Trip.TimeZone; only their combination
// produces an instant, and that combination happens explicitly via Resolve.
type TimeOfDay struct {
	Hour   int
	Minute int
}

// ParseTimeOfDay parses "HH:MM" (24-hour).
//
// Single-digit hours are accepted and canonicalised on output. Parsing leniently while
// storing canonically is the right split: rejecting "9:30" would fail a client for a
// difference that carries no meaning, and String guarantees everything downstream sees the
// zero-padded form.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return TimeOfDay{}, fmt.Errorf("%w: time must be HH:MM", ErrValidation)
	}
	return TimeOfDay{Hour: t.Hour(), Minute: t.Minute()}, nil
}

// String renders as "HH:MM".
func (t TimeOfDay) String() string { return fmt.Sprintf("%02d:%02d", t.Hour, t.Minute) }

// Minutes returns minutes since midnight — the comparable form.
func (t TimeOfDay) Minutes() int { return t.Hour*60 + t.Minute }

// Valid reports whether the value is a real clock time.
func (t TimeOfDay) Valid() bool {
	return t.Hour >= 0 && t.Hour <= 23 && t.Minute >= 0 && t.Minute <= 59
}

// Resolve combines the wall-clock time with a date and a location to produce an instant.
// This is the only place the three parts come together, which keeps the conversion auditable
// instead of scattered.
func (t TimeOfDay) Resolve(date time.Time, loc *time.Location) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), t.Hour, t.Minute, 0, 0, loc)
}

// MaxPositionLength mirrors the 128-character CHECK constraint on position columns.
//
// Fractional index keys grow under pathological repeated insertion at the same slot. This
// bound is the backstop that turns unbounded growth into a loud, catchable error instead of
// silent truncation — and reaching it is the signal that a rebalance pass is needed.
const MaxPositionLength = 128

func validatePosition(ve *ValidationError, field, position string) {
	switch {
	case position == "":
		ve.Add(field, "required", "position is required")
	case len(position) > MaxPositionLength:
		ve.Add(field, "too_long", "position key exceeded its maximum length; the list needs rebalancing")
	}
}
