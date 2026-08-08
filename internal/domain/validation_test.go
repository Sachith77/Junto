package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Validation tests focus on the boundaries and on the rules that exist for a reason, not on
// restating every constraint. Where a rule mirrors a database CHECK, the point of testing it
// here is that the user gets a field-level message instead of a constraint violation.

func TestValidationErrorAggregates(t *testing.T) {
	ve := &ValidationError{}
	if ve.OrNil() != nil {
		t.Fatal("an empty ValidationError must be nil-equivalent")
	}

	ve.Add("email", "required", "email is required")
	ve.AddIf(false, "name", "required", "should not appear")
	ve.AddIf(true, "password", "too_short", "password is too short")

	err := ve.OrNil()
	if err == nil {
		t.Fatal("expected a non-nil error once violations exist")
	}
	if !errors.Is(err, ErrValidation) {
		t.Error("a ValidationError must satisfy errors.Is(err, ErrValidation)")
	}
	if len(ve.Violations) != 2 {
		t.Fatalf("expected 2 violations, got %d", len(ve.Violations))
	}

	// The point of aggregating is that a client sees every problem in one round trip.
	msg := err.Error()
	if !strings.Contains(msg, "email") || !strings.Contains(msg, "password") {
		t.Errorf("error message should mention every failing field, got %q", msg)
	}

	extracted, ok := AsValidationError(err)
	if !ok || len(extracted.Violations) != 2 {
		t.Error("AsValidationError should recover the concrete type and its detail")
	}
}

func TestNormalizeEmailMatchesTheUniqueIndex(t *testing.T) {
	// This function must agree exactly with `lower(email)` in users_email_lower_uq.
	// If they ever diverge, duplicate accounts become possible: the application looks up
	// one form while the database enforces uniqueness on another.
	cases := map[string]string{
		"Alice@Example.com":   "alice@example.com",
		"  bob@example.com  ": "bob@example.com",
		"CAPS@EXAMPLE.COM":    "caps@example.com",
		"already@lower.com":   "already@lower.com",
	}
	for in, want := range cases {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	valid := []string{"a@b.co", "first.last+tag@sub.example.com"}
	for _, e := range valid {
		ve := &ValidationError{}
		ValidateEmail(ve, "email", e)
		if ve.HasViolations() {
			t.Errorf("%q should be valid, got %v", e, ve.Violations)
		}
	}

	// 320 characters exactly is the RFC-derived maximum and must be accepted; one more
	// must not. Checking both sides of the boundary, because an off-by-one here silently
	// rejects legitimate addresses.
	atLimit := strings.Repeat("a", 320-len("@b.co")) + "@b.co"
	if len(atLimit) != 320 {
		t.Fatalf("test fixture is %d chars, expected exactly 320", len(atLimit))
	}
	ve := &ValidationError{}
	ValidateEmail(ve, "email", atLimit)
	if ve.HasViolations() {
		t.Errorf("an address of exactly 320 characters must be accepted, got %v", ve.Violations)
	}

	invalid := []string{
		"",
		"no-at-sign",
		"Bob <bob@example.com>", // display-name form is not an identity
		"trailing@",
		strings.Repeat("a", 321-len("@b.co")+1) + "@b.co", // 321 chars
	}
	for _, e := range invalid {
		ve := &ValidationError{}
		ValidateEmail(ve, "email", e)
		if !ve.HasViolations() {
			t.Errorf("%q should be rejected", e)
		}
	}
}

func TestValidatePasswordLengthOnly(t *testing.T) {
	ve := &ValidationError{}
	ValidatePassword(ve, "password", strings.Repeat("a", MinPasswordLength))
	if ve.HasViolations() {
		t.Error("a 12-character password must be accepted: length is the only rule")
	}

	// No composition rules by design (NIST SP 800-63B). A long all-lowercase passphrase
	// is stronger than a short one with a digit and a symbol bolted on.
	ve = &ValidationError{}
	ValidatePassword(ve, "password", "correcthorsebatterystaple")
	if ve.HasViolations() {
		t.Errorf("a passphrase must be accepted, got %v", ve.Violations)
	}

	ve = &ValidationError{}
	ValidatePassword(ve, "password", "short")
	if !ve.HasViolations() {
		t.Error("a password under the minimum must be rejected")
	}

	// The upper bound is a denial-of-service guard: Argon2id burns 64MB per call, so an
	// unbounded password is free memory-hard work for an attacker.
	ve = &ValidationError{}
	ValidatePassword(ve, "password", strings.Repeat("a", MaxPasswordLength+1))
	if !ve.HasViolations() {
		t.Error("an oversized password must be rejected before it reaches the hasher")
	}
}

func TestTripValidation(t *testing.T) {
	base := func() *Trip {
		return &Trip{Name: "Lisbon", TimeZone: "Europe/Lisbon"}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a minimal valid trip should pass: %v", err)
	}

	t.Run("rejects unknown timezone", func(t *testing.T) {
		tr := base()
		tr.TimeZone = "Mars/Olympus_Mons"
		if err := tr.Validate(); err == nil {
			t.Error("an unknown IANA zone must be rejected")
		}
	})

	t.Run("accepts real zones without host tzdata", func(t *testing.T) {
		// Passes because time/tzdata is embedded. Without it this would depend on tz files
		// existing on the host, which they do not in scratch containers or stock Windows.
		for _, z := range []string{"UTC", "Europe/Lisbon", "Asia/Tokyo", "America/Sao_Paulo"} {
			tr := base()
			tr.TimeZone = z
			if err := tr.Validate(); err != nil {
				t.Errorf("zone %q should be valid: %v", z, err)
			}
		}
	})

	t.Run("rejects end before start", func(t *testing.T) {
		start := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
		end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		tr := base()
		tr.StartDate, tr.EndDate = &start, &end
		if err := tr.Validate(); err == nil {
			t.Error("end date before start date must be rejected")
		}
	})

	t.Run("allows undated trips", func(t *testing.T) {
		// Planning routinely starts before dates are fixed.
		tr := base()
		tr.StartDate, tr.EndDate = nil, nil
		if err := tr.Validate(); err != nil {
			t.Errorf("an undated trip must be valid: %v", err)
		}
	})

	t.Run("rejects blank name", func(t *testing.T) {
		tr := base()
		tr.Name = "   "
		if err := tr.Validate(); err == nil {
			t.Error("a whitespace-only name must be rejected")
		}
	})
}

// TestSlotValidation covers the DECISION. Time, ordering and coverage live here; place data
// lives on the option and is covered by TestSlotOptionValidation below.
func TestSlotValidation(t *testing.T) {
	base := func() *Slot {
		return &Slot{
			TripID: NewID(), Kind: SlotKindPlace, Title: "Where are we staying",
			Position: "a0", Status: SlotStatusPlanned,
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a minimal valid slot should pass: %v", err)
	}

	t.Run("rejects unknown kind", func(t *testing.T) {
		it := base()
		it.Kind = SlotKind("teleport")
		if err := it.Validate(); err == nil {
			t.Error("an unknown kind must be rejected")
		}
	})

	t.Run("rejects unknown status", func(t *testing.T) {
		it := base()
		it.Status = SlotStatus("maybe")
		if err := it.Validate(); err == nil {
			t.Error("an unknown coverage status must be rejected")
		}
	})

	t.Run("rejects end before start", func(t *testing.T) {
		it := base()
		it.StartTime = &TimeOfDay{Hour: 18}
		it.EndTime = &TimeOfDay{Hour: 9}
		if err := it.Validate(); err == nil {
			t.Error("end time before start time must be rejected")
		}
	})

	t.Run("requires a position", func(t *testing.T) {
		it := base()
		it.Position = ""
		if err := it.Validate(); err == nil {
			t.Error("a slot without a position has no defined order and must be rejected")
		}
	})

	t.Run("rejects an overlong position key", func(t *testing.T) {
		// Mirrors the 128-char CHECK. Reaching this means the list needs rebalancing;
		// failing loudly beats silent truncation, which would corrupt ordering.
		it := base()
		it.Position = strings.Repeat("a", MaxPositionLength+1)
		if err := it.Validate(); err == nil {
			t.Error("an overlong position key must be rejected")
		}
	})
}

// TestSlotOptionValidation covers the CANDIDATE. Place data lives here because each proposal
// has its own address and coordinates — the whole reason the model splits decision from
// candidate.
func TestSlotOptionValidation(t *testing.T) {
	base := func() *SlotOption {
		return &SlotOption{SlotID: NewID(), TripID: NewID(), Title: "Taj Exotica"}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a minimal valid option should pass: %v", err)
	}

	t.Run("requires a title", func(t *testing.T) {
		o := base()
		o.Title = "   "
		if err := o.Validate(); err == nil {
			t.Error("a whitespace-only title must be rejected")
		}
	})

	t.Run("requires coordinates in pairs", func(t *testing.T) {
		lat := 38.69
		o := base()
		o.Place.Lat = &lat
		if err := o.Validate(); err == nil {
			t.Error("a latitude without a longitude must be rejected")
		}

		lng := -9.21
		o.Place.Lng = &lng
		if err := o.Validate(); err != nil {
			t.Errorf("a complete coordinate pair must be accepted: %v", err)
		}
	})

	t.Run("rejects out-of-range coordinates", func(t *testing.T) {
		bad, ok := 200.0, 10.0
		o := base()
		o.Place.Lat, o.Place.Lng = &bad, &ok
		if err := o.Validate(); err == nil {
			t.Error("latitude 200 must be rejected")
		}
	})

	t.Run("accepts null island", func(t *testing.T) {
		// (0, 0) is a real location. This is exactly why Lat/Lng are pointers rather than
		// plain float64s with a zero-means-absent convention.
		zero := 0.0
		o := base()
		o.Place.Lat, o.Place.Lng = &zero, &zero
		if err := o.Validate(); err != nil {
			t.Errorf("(0,0) is a real coordinate and must be accepted: %v", err)
		}
		if !o.Place.HasCoordinates() {
			t.Error("(0,0) must count as having coordinates")
		}
	})

	t.Run("cost estimate", func(t *testing.T) {
		// Zero is a real estimate — a free walking tour — which is why the field is a pointer
		// rather than using 0 to mean "unknown".
		zero := int64(0)
		o := base()
		o.EstimatedCostMinor = &zero
		if err := o.Validate(); err != nil {
			t.Errorf("a zero estimate is meaningful and must be accepted: %v", err)
		}

		negative := int64(-1)
		o.EstimatedCostMinor = &negative
		if err := o.Validate(); err == nil {
			t.Error("a negative estimate must be rejected")
		}
	})
}

// TestVoteIsARegister pins the shape Stage 2 depends on: one row per (slot, user), with
// retraction expressed as a value rather than a deletion.
func TestVoteIsARegister(t *testing.T) {
	optionID := NewID()
	v := &Vote{SlotID: NewID(), TripID: NewID(), UserID: NewID(), OptionID: &optionID}

	if err := v.Validate(); err != nil {
		t.Fatalf("a cast vote should validate: %v", err)
	}
	if v.IsRetracted() {
		t.Error("a vote with an option is not retracted")
	}

	v.OptionID = nil
	if !v.IsRetracted() {
		t.Error("a vote with no option is retracted")
	}
	// A retracted vote is still a valid row. If this ever fails, someone has started
	// modelling retraction as deletion and the register property is gone.
	if err := v.Validate(); err != nil {
		t.Errorf("a retracted vote is still a valid row: %v", err)
	}
}

func TestLeadersReturnsEveryTie(t *testing.T) {
	a, b, c := NewID(), NewID(), NewID()

	leaders, count := Leaders([]VoteTally{{a, 3}, {b, 1}, {c, 3}})
	if count != 3 {
		t.Errorf("winning count = %d, want 3", count)
	}
	// Both tied options are returned. Silently picking one would hide the fact that the
	// group has not actually agreed, which is the opposite of what voting is for.
	if len(leaders) != 2 {
		t.Fatalf("expected 2 tied leaders, got %d", len(leaders))
	}

	if leaders, count := Leaders(nil); leaders != nil || count != 0 {
		t.Error("no votes means no leader")
	}
	if leaders, count := Leaders([]VoteTally{{a, 0}, {b, 0}}); leaders != nil || count != 0 {
		t.Error("all-zero tallies means no leader")
	}
}

func TestTimeOfDay(t *testing.T) {
	tod, err := ParseTimeOfDay("09:30")
	if err != nil {
		t.Fatalf("parsing 09:30: %v", err)
	}
	if tod.Hour != 9 || tod.Minute != 30 {
		t.Errorf("parsed %+v, want 09:30", tod)
	}
	if got := tod.String(); got != "09:30" {
		t.Errorf("String() = %q, want %q", got, "09:30")
	}
	if got := tod.Minutes(); got != 570 {
		t.Errorf("Minutes() = %d, want 570", got)
	}

	// Single-digit hours are accepted and canonicalised on output. Parsing leniently while
	// storing canonically is the right split: rejecting "9:30" would fail a client for a
	// difference that carries no meaning, and String() guarantees everything downstream
	// sees the zero-padded form regardless.
	lenient, err := ParseTimeOfDay("9:30")
	if err != nil {
		t.Errorf("ParseTimeOfDay(\"9:30\") should be accepted: %v", err)
	} else if lenient.String() != "09:30" {
		t.Errorf("a leniently parsed time must canonicalise: got %q, want %q", lenient.String(), "09:30")
	}

	for _, bad := range []string{"25:00", "09:60", "", "noon", "09:30:00", "9:3"} {
		if _, err := ParseTimeOfDay(bad); err == nil {
			t.Errorf("ParseTimeOfDay(%q) should fail", bad)
		}
	}

	if (TimeOfDay{Hour: 24}).Valid() || (TimeOfDay{Minute: 60}).Valid() || (TimeOfDay{Hour: -1}).Valid() {
		t.Error("out-of-range clock values must not validate")
	}
}

// TestTimeOfDayResolveUsesTripZone is the reason TimeOfDay exists as its own type.
func TestTimeOfDayResolveUsesTripZone(t *testing.T) {
	lisbon, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		t.Fatalf("embedded tzdata should provide Europe/Lisbon: %v", err)
	}
	tokyo, _ := time.LoadLocation("Asia/Tokyo")

	date := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	nineThirty := TimeOfDay{Hour: 9, Minute: 30}

	inLisbon := nineThirty.Resolve(date, lisbon)
	inTokyo := nineThirty.Resolve(date, tokyo)

	// Same wall-clock time, different instants. If item times were stored as absolute
	// instants, a collaborator in another zone would see a different itinerary.
	if inLisbon.Equal(inTokyo) {
		t.Error("09:30 in Lisbon and 09:30 in Tokyo must be different instants")
	}
	if inLisbon.Hour() != 9 || inTokyo.Hour() != 9 {
		t.Error("Resolve must preserve the wall-clock hour in its own zone")
	}
}

// TestDSTDoesNotShiftWallClockTimes is the concrete failure that timestamptz would cause.
func TestDSTDoesNotShiftWallClockTimes(t *testing.T) {
	lisbon, _ := time.LoadLocation("Europe/Lisbon")
	nineAM := TimeOfDay{Hour: 9}

	beforeDST := nineAM.Resolve(time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC), lisbon)
	afterDST := nineAM.Resolve(time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC), lisbon)

	// Both are 09:00 local, even though the UTC offset changed between them. Storing
	// absolute instants would have made one of them 08:00 or 10:00 to the user.
	if beforeDST.Hour() != 9 || afterDST.Hour() != 9 {
		t.Errorf("wall-clock hour shifted across a DST boundary: %v / %v", beforeDST, afterDST)
	}
	if beforeDST.UTC().Hour() == afterDST.UTC().Hour() {
		t.Log("note: no DST transition between these dates in this tzdata version")
	}
}

func TestInvitationRedeemability(t *testing.T) {
	now := time.Now().UTC()
	one := 1
	base := func() *Invitation {
		return &Invitation{
			TripID: NewID(), Role: RoleEditor, CreatedBy: NewID(),
			TokenHash: make([]byte, 32), ExpiresAt: now.Add(time.Hour),
			MaxUses: &one,
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a minimal valid invitation should pass: %v", err)
	}
	if !base().IsRedeemable(now) {
		t.Error("a fresh invitation must be redeemable")
	}

	t.Run("expired", func(t *testing.T) {
		inv := base()
		inv.ExpiresAt = now.Add(-time.Minute)
		if inv.IsRedeemable(now) {
			t.Error("an expired invitation must not be redeemable")
		}
	})

	t.Run("revoked", func(t *testing.T) {
		inv := base()
		inv.RevokedAt = &now
		if inv.IsRedeemable(now) {
			t.Error("a revoked invitation must not be redeemable")
		}
	})

	t.Run("exhausted", func(t *testing.T) {
		inv := base()
		inv.UseCount = 1
		if inv.IsRedeemable(now) {
			t.Error("an invitation at its use limit must not be redeemable")
		}
	})

	t.Run("unlimited link invite", func(t *testing.T) {
		inv := base()
		inv.MaxUses = nil
		inv.UseCount = 500
		if !inv.IsRedeemable(now) {
			t.Error("a link invite with no use limit must stay redeemable")
		}
		if !inv.IsLinkInvite() {
			t.Error("an invitation with no email is a link invite")
		}
	})

	t.Run("rejects owner role", func(t *testing.T) {
		inv := base()
		inv.Role = RoleOwner
		if err := inv.Validate(); err == nil {
			t.Error("an invitation must never grant ownership")
		}
	})
}

func TestSessionAndTokenLifecycle(t *testing.T) {
	now := time.Now().UTC()

	s := &AuthSession{ExpiresAt: now.Add(time.Hour)}
	if !s.IsActive(now) {
		t.Error("a fresh session must be active")
	}
	s.RevokedAt = &now
	if s.IsActive(now) {
		t.Error("a revoked session must not be active")
	}

	expired := &AuthSession{ExpiresAt: now.Add(-time.Second)}
	if expired.IsActive(now) {
		t.Error("an expired session must not be active")
	}

	rt := &RefreshToken{ExpiresAt: now.Add(time.Hour)}
	if rt.IsUsed() || rt.IsExpired(now) {
		t.Error("a fresh refresh token is neither used nor expired")
	}
	rt.UsedAt = &now
	if !rt.IsUsed() {
		t.Error("a token with UsedAt set is used; presenting it again is a replay")
	}

	// Boundary: expiry is exclusive, so a token is dead exactly at ExpiresAt.
	atExpiry := &RefreshToken{ExpiresAt: now}
	if !atExpiry.IsExpired(now) {
		t.Error("a token must be expired at exactly its expiry instant")
	}

	ut := &UserToken{Purpose: TokenPurposeEmailVerify, ExpiresAt: now.Add(time.Hour)}
	if !ut.IsUsable(now) {
		t.Error("a fresh user token must be usable")
	}
	ut.ConsumedAt = &now
	if ut.IsUsable(now) {
		t.Error("a consumed single-use token must not be usable again")
	}

	if !TokenPurposeEmailVerify.Valid() || !TokenPurposePasswordReset.Valid() {
		t.Error("known purposes must validate")
	}
	if TokenPurpose("admin_escalate").Valid() {
		t.Error("an unknown purpose must not validate: it is mirrored by a DB CHECK")
	}
}

func TestPageRequestNormalize(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, DefaultPageSize},
		{-5, DefaultPageSize},
		{10, 10},
		{MaxPageSize, MaxPageSize},
		{MaxPageSize + 1000, MaxPageSize}, // a client cannot turn a list into a table scan
	}
	for _, c := range cases {
		if got := (PageRequest{Limit: c.in}).Normalize().Limit; got != c.want {
			t.Errorf("Normalize(limit=%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNewPageTrimsTheProbeRow(t *testing.T) {
	cursorOf := func(s string) Cursor { return Cursor(s) }

	// Fetching limit+1 rows is how HasMore is determined without a second COUNT query,
	// which would be both slower and wrong (the total can change between the two queries).
	full := NewPage([]string{"a", "b", "c", "d"}, 3, cursorOf)
	if !full.HasMore {
		t.Error("an extra row must set HasMore")
	}
	if len(full.Items) != 3 {
		t.Errorf("the probe row must be trimmed, got %d items", len(full.Items))
	}
	if full.NextCursor != "c" {
		t.Errorf("NextCursor should anchor to the last returned row, got %q", full.NextCursor)
	}

	last := NewPage([]string{"a", "b"}, 3, cursorOf)
	if last.HasMore {
		t.Error("a short page must not set HasMore")
	}
	if last.NextCursor != "" {
		t.Error("a final page must not advertise a next cursor")
	}

	empty := NewPage([]string{}, 3, cursorOf)
	if empty.HasMore || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Error("an empty page must be empty in every respect")
	}
}

func TestNewIDIsTimeOrdered(t *testing.T) {
	// UUIDv7 is time-ordered, which is what gives primary-key inserts B-tree locality
	// instead of scattering them the way random v4 keys do.
	prev := NewID()
	for i := 0; i < 500; i++ {
		next := NewID()
		if next.String() <= prev.String() {
			t.Fatalf("id %d is not greater than its predecessor: %s <= %s", i, next, prev)
		}
		if next.Version() != 7 {
			t.Fatalf("expected UUID version 7, got %d", next.Version())
		}
		prev = next
	}
}

func TestParseIDReturnsFieldLevelError(t *testing.T) {
	if _, err := ParseID("trip_id", "not-a-uuid"); err == nil {
		t.Fatal("a malformed UUID must be rejected")
	} else if !errors.Is(err, ErrValidation) {
		t.Errorf("a malformed UUID should be a field-level validation error, got %v", err)
	}

	id := NewID()
	got, err := ParseID("trip_id", id.String())
	if err != nil || got != id {
		t.Errorf("round trip failed: %v / %v", got, err)
	}
}

func TestSystemClockIsUTC(t *testing.T) {
	// Normalising here means a local-time value can never leak into a timestamptz column
	// or a token expiry comparison.
	if loc := (SystemClock{}).Now().Location(); loc != time.UTC {
		t.Errorf("SystemClock must return UTC, got %v", loc)
	}
}
