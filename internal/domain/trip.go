package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	// Embeds the IANA timezone database into the binary.
	//
	// Without this, time.LoadLocation depends on tz files existing on the host. They are
	// absent from scratch/distroless containers and from stock Windows, so trip timezone
	// validation would pass in development and fail in production — or worse, differ
	// between the API instance and a worker. ~450KB to make behaviour identical everywhere
	// is a trade this project's priority order makes easily.
	_ "time/tzdata"
)

// Trip is the root aggregate of the Trips module and the unit of collaboration: it is the
// permission boundary, and in Stage 2 it becomes the sync room (one operation log, one
// total order, one set of connected clients).
//
// There is deliberately no OwnerID field. Ownership is the trip_members row with
// role='owner', enforced by a partial unique index. Storing it here as well would create
// two sources of truth for one fact, and they would drift.
type Trip struct {
	ID          ID
	Name        string
	Description string

	// Nil until the dates are decided. Planning routinely starts before they are.
	StartDate *time.Time
	EndDate   *time.Time

	// TimeZone is the IANA name the trip's wall-clock item times are interpreted in.
	// It belongs to the trip, not the viewer: a collaborator in another zone must see the
	// same itinerary, or the schedule means different things to different members.
	TimeZone string

	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// IsDeleted reports whether the trip is soft-deleted.
func (t *Trip) IsDeleted() bool { return t.DeletedAt != nil }

// Location resolves the trip's timezone, falling back to UTC if it is somehow unset.
// Validation should have rejected an unknown zone long before this is called.
func (t *Trip) Location() *time.Location {
	if t.TimeZone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(t.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Validate checks the trip's invariants.
func (t *Trip) Validate() error {
	ve := &ValidationError{}

	name := strings.TrimSpace(t.Name)
	switch {
	case name == "":
		ve.Add("name", "required", "trip name is required")
	case utf8.RuneCountInString(name) > 200:
		ve.Add("name", "too_long", "trip name must be at most 200 characters")
	}

	if utf8.RuneCountInString(t.Description) > 5000 {
		ve.Add("description", "too_long", "description must be at most 5000 characters")
	}

	if t.TimeZone == "" {
		ve.Add("time_zone", "required", "time zone is required")
	} else if _, err := time.LoadLocation(t.TimeZone); err != nil {
		ve.Add("time_zone", "unknown_timezone", "must be a valid IANA time zone name, e.g. Europe/Lisbon")
	}

	// Mirrors the trips_dates_ordered CHECK constraint. Both exist on purpose: the
	// database guarantees the invariant can never be violated by any writer, the domain
	// check turns it into a useful field-level message instead of a constraint violation.
	if t.StartDate != nil && t.EndDate != nil && t.EndDate.Before(*t.StartDate) {
		ve.Add("end_date", "before_start", "end date must not be before start date")
	}

	return ve.OrNil()
}

// Member is a user's membership of a trip.
//
// This is the seam between core and domain. The MembershipRepository interface is
// reusable; this table is not. A polymorphic memberships(resource_type, resource_id) table
// would be reusable but cannot be enforced by Postgres — trading real referential
// integrity for a generality with no second consumer is a bad deal today.
type Member struct {
	ID        ID
	TripID    ID
	UserID    ID
	Role      Role
	InvitedBy *ID
	JoinedAt  time.Time

	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// MemberProfile is a membership AS RENDERED: the authorization row plus the one user field
// every collaborative surface needs to show a person instead of a UUID.
//
// A separate read model rather than a field on Member, because Member is loaded by every
// authorization check on every request and does not need a join to answer "may this caller
// do this". Email is deliberately absent — a display name is enough to render an author, a
// voter or a budget split, and showing every member's address to every viewer is a
// disclosure the interface has no use for.
type MemberProfile struct {
	Member
	DisplayName string
}

// Actor projects the membership into the authorization type used by services.
func (m *Member) Actor() Actor {
	return Actor{UserID: m.UserID, TripID: m.TripID, Role: m.Role}
}

// Validate checks the membership's invariants.
func (m *Member) Validate() error {
	ve := &ValidationError{}
	ve.AddIf(m.TripID == NilID, "trip_id", "required", "trip id is required")
	ve.AddIf(m.UserID == NilID, "user_id", "required", "user id is required")
	ve.AddIf(!m.Role.Valid(), "role", "invalid_role", "must be one of: owner, editor, viewer")
	return ve.OrNil()
}
