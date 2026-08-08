package domain

import (
	"testing"
	"time"
)

// Entity-level tests for the validation paths not exercised by validation_test.go.
// Deliberately not testing one-line accessors like IsDeleted: a test that asserts
// `x.DeletedAt != nil` returns true when DeletedAt is non-nil inflates the coverage number
// without protecting anything.

func TestUserValidate(t *testing.T) {
	valid := &User{Email: "a@b.co", DisplayName: "Alice", PasswordHash: "argon2id$..."}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a complete user should validate: %v", err)
	}

	t.Run("requires a password hash", func(t *testing.T) {
		// Guards against persisting a user whose password was never hashed — an empty hash
		// would otherwise be a valid-looking row that no password can ever match, or worse,
		// one that a bug in comparison logic treats as matching.
		u := *valid
		u.PasswordHash = ""
		if err := u.Validate(); err == nil {
			t.Error("a user without a password hash must be rejected")
		}
	})

	t.Run("requires a display name", func(t *testing.T) {
		u := *valid
		u.DisplayName = "  "
		if err := u.Validate(); err == nil {
			t.Error("a whitespace-only display name must be rejected")
		}
	})

	t.Run("rejects a bad email", func(t *testing.T) {
		u := *valid
		u.Email = "not-an-email"
		if err := u.Validate(); err == nil {
			t.Error("an invalid email must be rejected")
		}
	})
}

func TestValidateDisplayNameBoundary(t *testing.T) {
	ve := &ValidationError{}
	ValidateDisplayName(ve, "display_name", "abcdefghijklmnopqrstuvwxyz0123456789") // 36 chars
	if ve.HasViolations() {
		t.Errorf("a normal display name must be accepted: %v", ve.Violations)
	}

	// Counted in runes, not bytes: 100 emoji is 100 characters to a user even though it is
	// 400 bytes. A byte-based limit would reject names that look short.
	ve = &ValidationError{}
	long := ""
	for i := 0; i < 100; i++ {
		long += "é"
	}
	ValidateDisplayName(ve, "display_name", long)
	if ve.HasViolations() {
		t.Errorf("100 multi-byte runes must be accepted (limit is in runes): %v", ve.Violations)
	}

	ve = &ValidationError{}
	ValidateDisplayName(ve, "display_name", long+"é")
	if !ve.HasViolations() {
		t.Error("101 runes must be rejected")
	}
}

func TestDayValidate(t *testing.T) {
	valid := &Day{TripID: NewID(), Position: "a0"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a minimal day should validate: %v", err)
	}

	t.Run("requires a trip", func(t *testing.T) {
		d := *valid
		d.TripID = NilID
		if err := d.Validate(); err == nil {
			t.Error("an orphan day must be rejected")
		}
	})

	t.Run("requires a position", func(t *testing.T) {
		d := *valid
		d.Position = ""
		if err := d.Validate(); err == nil {
			t.Error("a day with no position has no defined order and must be rejected")
		}
	})

	t.Run("allows an unscheduled day", func(t *testing.T) {
		d := *valid
		d.Date = nil
		if err := d.Validate(); err != nil {
			t.Errorf("a day may exist before it is scheduled: %v", err)
		}
	})
}

func TestMemberValidate(t *testing.T) {
	valid := &Member{TripID: NewID(), UserID: NewID(), Role: RoleEditor}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a complete membership should validate: %v", err)
	}

	t.Run("rejects an unknown role", func(t *testing.T) {
		// Mirrors the trip_members_role CHECK. Because capability lookup fails closed, an
		// unknown role would grant nothing rather than everything — but it should never
		// reach persistence in the first place.
		m := *valid
		m.Role = Role("admin")
		if err := m.Validate(); err == nil {
			t.Error("an unknown role must be rejected")
		}
	})

	t.Run("rejects missing ids", func(t *testing.T) {
		m := *valid
		m.UserID = NilID
		if err := m.Validate(); err == nil {
			t.Error("a membership without a user must be rejected")
		}
	})
}

func TestTripLocation(t *testing.T) {
	tokyo := &Trip{TimeZone: "Asia/Tokyo"}
	if got := tokyo.Location().String(); got != "Asia/Tokyo" {
		t.Errorf("Location() = %q, want Asia/Tokyo", got)
	}

	// Fail safe, not closed: Location is called on the read path when rendering an
	// itinerary. Validation should have rejected a bad zone long before this, so falling
	// back to UTC keeps a display bug from becoming an outage.
	for _, bad := range []*Trip{{TimeZone: ""}, {TimeZone: "Mars/Olympus_Mons"}} {
		if got := bad.Location(); got != time.UTC {
			t.Errorf("Location() for %q = %v, want UTC fallback", bad.TimeZone, got)
		}
	}
}

func TestTripDescriptionLimit(t *testing.T) {
	tr := &Trip{Name: "Lisbon", TimeZone: "UTC"}
	tr.Description = string(make([]rune, 5000))
	if err := tr.Validate(); err != nil {
		t.Errorf("a 5000-character description must be accepted: %v", err)
	}
	tr.Description = string(make([]rune, 5001))
	if err := tr.Validate(); err == nil {
		t.Error("a description over the limit must be rejected")
	}
}

func TestSlotNotesLimit(t *testing.T) {
	s := &Slot{TripID: NewID(), Kind: SlotKindNote, Title: "Notes", Position: "a0", Status: SlotStatusPlanned}
	s.Notes = string(make([]rune, 10000))
	if err := s.Validate(); err != nil {
		t.Errorf("10000 characters of notes must be accepted: %v", err)
	}
	s.Notes = string(make([]rune, 10001))
	if err := s.Validate(); err == nil {
		t.Error("notes over the limit must be rejected")
	}
}

func TestSlotKindValidity(t *testing.T) {
	for _, k := range []SlotKind{SlotKindPlace, SlotKindActivity, SlotKindTransport, SlotKindLodging, SlotKindNote} {
		if !k.Valid() {
			t.Errorf("%q should be a valid kind", k)
		}
	}
	for _, k := range []SlotKind{"", "PLACE", "meal", "flight"} {
		if k.Valid() {
			t.Errorf("%q should not be a valid kind (it is mirrored by a DB CHECK)", k)
		}
	}
}

func TestSlotStatusValidity(t *testing.T) {
	for _, s := range []SlotStatus{SlotStatusPlanned, SlotStatusCovered, SlotStatusSkipped} {
		if !s.Valid() {
			t.Errorf("%q should be a valid status", s)
		}
	}
	for _, s := range []SlotStatus{"", "done", "COVERED", "maybe"} {
		if s.Valid() {
			t.Errorf("%q should not be a valid status (it is mirrored by a DB CHECK)", s)
		}
	}
}

func TestSlotScheduling(t *testing.T) {
	s := &Slot{TripID: NewID(), Kind: SlotKindPlace, Title: "Backlog decision", Position: "a0", Status: SlotStatusPlanned}
	if s.IsScheduled() {
		t.Error("a slot with no day is unscheduled (backlog)")
	}
	if s.IsResolved() {
		t.Error("a slot with no selected option is unresolved")
	}
	if err := s.Validate(); err != nil {
		t.Errorf("a backlog slot must be valid: %v", err)
	}

	day := NewID()
	s.DayID = &day
	if !s.IsScheduled() {
		t.Error("a slot with a day is scheduled")
	}

	option := NewID()
	s.SelectedOptionID = &option
	if !s.IsResolved() {
		t.Error("a slot with a selected option is resolved")
	}
}

func TestCapabilitySetList(t *testing.T) {
	got := RoleViewer.Capabilities().List()
	if len(got) != 1 || got[0] != CapViewTrip {
		t.Errorf("viewer capabilities = %v, want exactly [view_trip]", got)
	}
	// An exact count, updated deliberately. The failure mode being guarded against is a
	// capability being added to a role by accident, which a "greater than" check would miss.
	if n := len(RoleOwner.Capabilities().List()); n != 16 {
		t.Errorf("owner has %d capabilities, want 16 — update this count deliberately when adding one", n)
	}
}
