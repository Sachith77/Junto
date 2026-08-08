package domain

import "fmt"

// Role is a member's role on a trip.
//
// Roles are what is persisted today. Authorization checks, however, are written in terms
// of capabilities (see Capability) rather than roles. That indirection costs nothing now
// and means the eventual migration to per-member capabilities is a change to one function
// plus one column — not an audit of every handler and service method in the codebase.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// AssignableRoles are the roles an invitation may grant.
//
// Owner is excluded, and the database CHECK on trip_invitations.role enforces the same
// thing independently: ownership transfer is a distinct operation with distinct rules, and
// no invite link should be able to grant it.
var AssignableRoles = []Role{RoleEditor, RoleViewer}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	_, ok := roleCapabilities[r]
	return ok
}

func (r Role) String() string { return string(r) }

// Capability is a single granular permission.
//
// Deliberately string-valued rather than a bitmask: when these become per-member and are
// persisted, a readable text[] column is far easier to inspect, debug, and migrate than an
// opaque integer, and the lookup cost is irrelevant at this granularity.
type Capability string

const (
	CapViewTrip   Capability = "view_trip"
	CapEditTrip   Capability = "edit_trip"
	CapDeleteTrip Capability = "delete_trip"

	// Slots are the decisions in the itinerary.
	CapCreateSlots  Capability = "create_slots"
	CapEditSlots    Capability = "edit_slots"
	CapDeleteSlots  Capability = "delete_slots"
	CapReorderSlots Capability = "reorder_slots"
	CapManageDays   Capability = "manage_days"

	// CapProposeOptions is separate from CapEditSlots on purpose: "may suggest an
	// alternative hotel" is a lower bar than "may restructure the itinerary", and keeping
	// them apart is what will let a future viewer-who-participates role exist.
	CapProposeOptions Capability = "propose_options"

	// CapManageBudget is separate for a trust reason, not a tidiness one. "May propose where
	// we stay" and "may edit the ledger that decides who owes what" are different levels of
	// trust, and collapsing them would force a group to choose between an unhelpful member
	// and an over-privileged one. This is the second concrete case — alongside
	// viewers-who-vote — that motivates the eventual capability migration.
	CapManageBudget Capability = "manage_budget"

	CapUploadAttachments Capability = "upload_attachments"

	CapInviteMembers     Capability = "invite_members"
	CapManageMembers     Capability = "manage_members"
	CapTransferOwnership Capability = "transfer_ownership"

	CapComment Capability = "comment"
	CapVote    Capability = "vote"
)

// CapabilitySet is an immutable set of capabilities.
//
// The sets below are built once at package init and shared. Never mutate a set returned by
// Capabilities(); it is shared across every actor holding that role.
type CapabilitySet map[Capability]struct{}

// Has reports whether the set contains c.
func (s CapabilitySet) Has(c Capability) bool {
	_, ok := s[c]
	return ok
}

// List returns the capabilities as a slice. Order is not guaranteed; sort if you need
// determinism (e.g. for API output).
func (s CapabilitySet) List() []Capability {
	out := make([]Capability, 0, len(s))
	for c := range s {
		out = append(out, c)
	}
	return out
}

func newCapabilitySet(caps ...Capability) CapabilitySet {
	s := make(CapabilitySet, len(caps))
	for _, c := range caps {
		s[c] = struct{}{}
	}
	return s
}

// roleCapabilities is the single place where a role is translated into what it may do.
// When capabilities become per-member, this map becomes the default seed for new members
// and nothing else in the codebase needs to change.
var roleCapabilities = map[Role]CapabilitySet{
	RoleOwner: newCapabilitySet(
		CapViewTrip, CapEditTrip, CapDeleteTrip,
		CapCreateSlots, CapEditSlots, CapDeleteSlots, CapReorderSlots, CapManageDays,
		CapProposeOptions, CapManageBudget, CapUploadAttachments,
		CapInviteMembers, CapManageMembers, CapTransferOwnership,
		CapComment, CapVote,
	),
	RoleEditor: newCapabilitySet(
		CapViewTrip,
		CapCreateSlots, CapEditSlots, CapDeleteSlots, CapReorderSlots, CapManageDays,
		CapProposeOptions, CapManageBudget, CapUploadAttachments,
		CapInviteMembers,
		CapComment, CapVote,
	),
	// Viewer is strictly read-only. Note that "a viewer who may vote but not edit" is a
	// genuinely useful role for trip planning — you invite people specifically to vote on
	// options — and it is exactly the case that cannot be expressed with three fixed roles.
	// That is the concrete motivation for the capability migration, rather than a
	// hypothetical one.
	RoleViewer: newCapabilitySet(
		CapViewTrip,
	),
}

// Capabilities returns the (shared, immutable) capability set for the role.
func (r Role) Capabilities() CapabilitySet {
	if s, ok := roleCapabilities[r]; ok {
		return s
	}
	return CapabilitySet{}
}

// Actor is an authenticated user in the context of one trip.
//
// Services construct it from a membership lookup and then reason only about capabilities.
// It exists so that no service method has to remember which roles imply which powers.
type Actor struct {
	UserID ID
	TripID ID
	Role   Role
}

// Can reports whether the actor holds the capability.
func (a Actor) Can(c Capability) bool { return a.Role.Capabilities().Has(c) }

// Authorize returns ErrForbidden (wrapped with the missing capability, for logs) when the
// actor lacks c. Services call this rather than branching on Can, so the failure path is
// uniform and impossible to forget to return.
func (a Actor) Authorize(c Capability) error {
	if a.Can(c) {
		return nil
	}
	return fmt.Errorf("%w: role %q lacks capability %q", ErrForbidden, a.Role, c)
}
