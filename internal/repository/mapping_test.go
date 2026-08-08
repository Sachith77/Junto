package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// TestTimeOfDayRoundTripsThroughPostgres is the one mapping with real logic in it: Postgres
// stores `time` as microseconds since midnight, the domain models it as {Hour, Minute}.
//
// It goes through the database rather than testing the two conversion functions against each
// other, because a self-consistent pair of converters can still both be wrong.
//
// Time lives on the SLOT, not the option: the itinerary renders by time, and an undecided
// slot still occupies a schedule position.
func TestTimeOfDayRoundTripsThroughPostgres(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	day := makeDay(t, ctx, r, trip, ptr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))

	cases := []struct {
		name             string
		start, end       *domain.TimeOfDay
		wantStart, wantE string
	}{
		{"midnight", &domain.TimeOfDay{Hour: 0, Minute: 0}, &domain.TimeOfDay{Hour: 0, Minute: 1}, "00:00", "00:01"},
		{"morning", &domain.TimeOfDay{Hour: 9, Minute: 30}, &domain.TimeOfDay{Hour: 11, Minute: 0}, "09:30", "11:00"},
		{"last minute", &domain.TimeOfDay{Hour: 23, Minute: 59}, nil, "23:59", ""},
		{"unset", nil, nil, "", ""},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &domain.Slot{
				ID: domain.NewID(), TripID: trip.ID, DayID: &day.ID,
				Kind: domain.SlotKindActivity, Title: tc.name,
				Position:  string(rune('b' + i)),
				Status:    domain.SlotStatusPlanned,
				StartTime: tc.start, EndTime: tc.end,
			}
			if err := r.slots.Create(ctx, s); err != nil {
				t.Fatalf("creating slot: %v", err)
			}

			got, err := r.slots.GetByID(ctx, s.ID)
			if err != nil {
				t.Fatalf("reading slot: %v", err)
			}

			assertTimeOfDay(t, "start_time", got.StartTime, tc.wantStart)
			assertTimeOfDay(t, "end_time", got.EndTime, tc.wantE)
		})
	}
}

func assertTimeOfDay(t *testing.T, field string, got *domain.TimeOfDay, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Errorf("%s = %v, want nil", field, got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s = nil, want %q", field, want)
	}
	if got.String() != want {
		t.Errorf("%s = %q, want %q", field, got.String(), want)
	}
}

// TestPlaceNullabilityRoundTrip pins the "nullable only where NULL differs from the zero
// value" rule against the real columns.
//
// Place lives on the OPTION, not the slot: each candidate hotel has its own address.
func TestPlaceNullabilityRoundTrip(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Where are we staying")

	t.Run("null island survives", func(t *testing.T) {
		// (0, 0) is a real location. If coordinates were plain float64 with zero-means-absent,
		// this would come back as "no coordinates".
		zero := 0.0
		o := &domain.SlotOption{
			ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID, Title: "Null Island",
			Place: domain.Place{Name: "Null Island", Lat: &zero, Lng: &zero},
		}
		if err := r.options.Create(ctx, o); err != nil {
			t.Fatalf("creating: %v", err)
		}
		got, err := r.options.GetByID(ctx, o.ID)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		if !got.Place.HasCoordinates() {
			t.Fatal("(0,0) must round-trip as present coordinates")
		}
		if *got.Place.Lat != 0 || *got.Place.Lng != 0 {
			t.Errorf("coordinates = (%v, %v), want (0, 0)", *got.Place.Lat, *got.Place.Lng)
		}
	})

	t.Run("absent coordinates stay absent", func(t *testing.T) {
		o := &domain.SlotOption{
			ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID, Title: "No location",
		}
		if err := r.options.Create(ctx, o); err != nil {
			t.Fatalf("creating: %v", err)
		}
		got, err := r.options.GetByID(ctx, o.ID)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		if got.Place.HasCoordinates() {
			t.Error("absent coordinates must stay nil")
		}
		// Text place fields are NOT NULL DEFAULT '' — absent and empty are the same thing,
		// so they must come back as empty strings, never as a nil that callers must guard.
		if got.Place.Name != "" || got.Place.Address != "" || got.Place.ProviderID != "" {
			t.Errorf("unset text place fields should be empty strings, got %+v", got.Place)
		}
	})

	t.Run("a zero cost estimate is distinct from no estimate", func(t *testing.T) {
		// A free walking tour costs 0; an un-estimated option costs "unknown". Collapsing
		// them is exactly what the pointer exists to prevent.
		zero := int64(0)
		withZero := &domain.SlotOption{
			ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID, Title: "Free tour",
			EstimatedCostMinor: &zero,
		}
		noEstimate := &domain.SlotOption{
			ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID, Title: "Unknown cost",
		}
		for _, o := range []*domain.SlotOption{withZero, noEstimate} {
			if err := r.options.Create(ctx, o); err != nil {
				t.Fatalf("creating: %v", err)
			}
		}

		gotZero, err := r.options.GetByID(ctx, withZero.ID)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		if gotZero.EstimatedCostMinor == nil || *gotZero.EstimatedCostMinor != 0 {
			t.Errorf("a zero estimate must round-trip as 0, got %v", gotZero.EstimatedCostMinor)
		}

		gotNone, err := r.options.GetByID(ctx, noEstimate.ID)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		if gotNone.EstimatedCostMinor != nil {
			t.Errorf("an absent estimate must stay nil, got %v", *gotNone.EstimatedCostMinor)
		}
	})
}

// TestMoveSlotIsASingleAtomicChange covers the operation Stage 2 will map a move onto: day
// and position change together, with exactly one version bump.
func TestMoveSlotIsASingleAtomicChange(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	dayOne := makeDay(t, ctx, r, trip, ptr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
	dayTwo := makeDay(t, ctx, r, trip, ptr(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)))

	slot := makeSlot(t, ctx, r, trip, dayOne, "movable")
	staysPut := makeSlot(t, ctx, r, trip, dayOne, "stays put")

	if err := r.slots.Move(ctx, slot.ID, &dayTwo.ID, "a5", slot.Version, time.Now().UTC()); err != nil {
		t.Fatalf("moving slot: %v", err)
	}

	moved, err := r.slots.GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("reading moved slot: %v", err)
	}
	if moved.DayID == nil || *moved.DayID != dayTwo.ID {
		t.Errorf("day = %v, want %v", moved.DayID, dayTwo.ID)
	}
	if moved.Position != "a5" {
		t.Errorf("position = %q, want a5", moved.Position)
	}
	if moved.Version != slot.Version+1 {
		t.Errorf("version = %d, want exactly one bump to %d", moved.Version, slot.Version+1)
	}
	// The slot must never be observable as deleted, even momentarily.
	if moved.IsDeleted() {
		t.Error("a move must not tombstone the slot")
	}

	// The neighbour it left behind must be untouched.
	other, err := r.slots.GetByID(ctx, staysPut.ID)
	if err != nil {
		t.Fatalf("reading neighbour: %v", err)
	}
	if other.Version != staysPut.Version || other.Position != staysPut.Position {
		t.Error("moving one slot must not rewrite its former neighbours")
	}

	// A stale version must be refused rather than silently relocating the slot.
	err = r.slots.Move(ctx, slot.ID, &dayOne.ID, "a1", slot.Version, time.Now().UTC())
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("moving with a stale version must conflict, got %v", err)
	}

	// Moving to the backlog is a nil day, not a deletion.
	if err := r.slots.Move(ctx, slot.ID, nil, "a9", moved.Version, time.Now().UTC()); err != nil {
		t.Fatalf("moving to backlog: %v", err)
	}
	backlog, err := r.slots.ListBacklog(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing backlog: %v", err)
	}
	if len(backlog) != 1 || backlog[0].ID != slot.ID {
		t.Errorf("backlog = %v, want just the moved slot", titles(backlog))
	}
}

func TestCursorRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 8, 7, 12, 34, 56, 123456000, time.UTC)
	id := domain.NewID()

	cursor := encodeTripCursor(createdAt, id)
	gotTime, gotID, err := decodeTripCursor(cursor)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	// Must round-trip to the exact microsecond: truncating to seconds would make the boundary
	// comparison ambiguous for rows created in the same second, which is precisely when
	// pagination correctness matters.
	if !gotTime.Equal(createdAt) {
		t.Errorf("time = %v, want %v", gotTime, createdAt)
	}
	if gotID != id {
		t.Errorf("id = %v, want %v", gotID, id)
	}

	// A hand-edited cursor is user input, so it must be a validation error rather than a 500.
	for _, bad := range []domain.Cursor{"not-base64!!", "", "YWJj", "bm90LWEtdGltZXxub3QtYS11dWlk"} {
		_, _, err := decodeTripCursor(bad)
		if err == nil {
			t.Errorf("cursor %q should be rejected", bad)
			continue
		}
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("cursor %q should give a validation error, got %v", bad, err)
		}
	}
}

// TestNeighbourPositionsOnEmptyBucket covers the boundary the fixtures rely on: an empty list
// must report unbounded on both sides so KeyBetween produces the first key.
func TestNeighbourPositionsOnEmptyBucket(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	prev, next, err := r.slots.NeighbourPositions(ctx, trip.ID, nil, nil)
	if err != nil {
		t.Fatalf("neighbour positions on empty backlog: %v", err)
	}
	if prev != "" || next != "" {
		t.Errorf("empty bucket brackets = (%q, %q), want two empty strings", prev, next)
	}

	prev, next, err = r.days.NeighbourPositions(ctx, trip.ID, nil)
	if err != nil {
		t.Fatalf("neighbour positions on empty trip: %v", err)
	}
	if prev != "" || next != "" {
		t.Errorf("empty trip brackets = (%q, %q), want two empty strings", prev, next)
	}

	// An anchor that does not exist must be an error, not a silent append: the caller asked
	// to insert after a specific slot, and putting it somewhere else is worse than failing.
	ghost := domain.NewID()
	if _, _, err := r.slots.NeighbourPositions(ctx, trip.ID, nil, &ghost); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a missing anchor must report not-found, got %v", err)
	}
}
