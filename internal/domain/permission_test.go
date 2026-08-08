package domain

import (
	"errors"
	"testing"
)

// TestRoleCapabilityMatrix pins the full role -> capability mapping.
//
// An exhaustive matrix rather than spot checks, because the failure mode being guarded
// against is a *widening* of permissions â€” someone adds a capability to the owner set and
// accidentally pastes it into viewer's. A spot check would not notice; this does.
func TestRoleCapabilityMatrix(t *testing.T) {
	all := []Capability{
		CapViewTrip, CapEditTrip, CapDeleteTrip,
		CapCreateSlots, CapEditSlots, CapDeleteSlots, CapReorderSlots, CapManageDays,
		CapProposeOptions, CapManageBudget, CapUploadAttachments,
		CapInviteMembers, CapManageMembers, CapTransferOwnership,
		CapComment, CapVote,
	}

	expected := map[Role]map[Capability]bool{
		RoleOwner: {
			CapViewTrip: true, CapEditTrip: true, CapDeleteTrip: true,
			CapCreateSlots: true, CapEditSlots: true, CapDeleteSlots: true,
			CapReorderSlots: true, CapManageDays: true,
			CapProposeOptions: true, CapManageBudget: true, CapUploadAttachments: true,
			CapInviteMembers: true, CapManageMembers: true, CapTransferOwnership: true,
			CapComment: true, CapVote: true,
		},
		RoleEditor: {
			CapViewTrip: true, CapEditTrip: false, CapDeleteTrip: false,
			CapCreateSlots: true, CapEditSlots: true, CapDeleteSlots: true,
			CapReorderSlots: true, CapManageDays: true,
			CapProposeOptions: true, CapManageBudget: true, CapUploadAttachments: true,
			CapInviteMembers: true, CapManageMembers: false, CapTransferOwnership: false,
			CapComment: true, CapVote: true,
		},
		RoleViewer: {
			CapViewTrip: true, CapEditTrip: false, CapDeleteTrip: false,
			CapCreateSlots: false, CapEditSlots: false, CapDeleteSlots: false,
			CapReorderSlots: false, CapManageDays: false,
			// A viewer may not propose an alternative, manage the ledger, or upload.
			// CapProposeOptions and CapManageBudget being separate capabilities is what will
			// let those be granted independently later â€” see the notes on their declarations.
			CapProposeOptions: false, CapManageBudget: false, CapUploadAttachments: false,
			CapInviteMembers: false, CapManageMembers: false, CapTransferOwnership: false,
			// Viewer cannot vote. This is the concrete case that motivates the eventual
			// capability migration: "may vote on options but may not edit the itinerary"
			// is a genuinely useful role for trip planning and cannot be expressed with
			// three fixed roles. If this assertion is ever flipped, it should be because
			// capabilities became per-member â€” not because someone widened the viewer role.
			CapComment: false, CapVote: false,
		},
	}

	for role, want := range expected {
		for _, cap := range all {
			actor := Actor{UserID: NewID(), TripID: NewID(), Role: role}
			if got := actor.Can(cap); got != want[cap] {
				t.Errorf("role %s: Can(%s) = %v, want %v", role, cap, got, want[cap])
			}
		}
	}
}

func TestAuthorizeReturnsForbidden(t *testing.T) {
	viewer := Actor{UserID: NewID(), TripID: NewID(), Role: RoleViewer}

	if err := viewer.Authorize(CapViewTrip); err != nil {
		t.Errorf("viewer should be able to view: %v", err)
	}

	err := viewer.Authorize(CapEditSlots)
	if err == nil {
		t.Fatal("viewer should not be able to edit items")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestUnknownRoleHasNoCapabilities(t *testing.T) {
	// Fail closed. A role that somehow reaches the code without being in the matrix â€”
	// bad data, a partially applied migration â€” must grant nothing rather than defaulting
	// to something permissive.
	rogue := Actor{UserID: NewID(), TripID: NewID(), Role: Role("superadmin")}
	if rogue.Can(CapViewTrip) {
		t.Error("an unrecognised role must not grant any capability")
	}
	if Role("superadmin").Valid() {
		t.Error("an unrecognised role must not validate")
	}
	if n := len(Role("").Capabilities().List()); n != 0 {
		t.Errorf("empty role granted %d capabilities, want 0", n)
	}
}

func TestAssignableRolesExcludeOwner(t *testing.T) {
	// Mirrors the trip_invitations_role CHECK constraint. Ownership transfer is a distinct
	// operation; no invitation should be able to grant it.
	for _, r := range AssignableRoles {
		if r == RoleOwner {
			t.Fatal("owner must not be assignable via invitation")
		}
		if !r.Valid() {
			t.Errorf("assignable role %q is not valid", r)
		}
	}
}

func TestCapabilitySetIsNotAccidentallyShared(t *testing.T) {
	// Capabilities() returns a shared, immutable map. This test documents that contract:
	// two calls must observe the same content, and callers must not be handed something
	// they can corrupt for every other actor holding the role.
	a := RoleEditor.Capabilities()
	b := RoleEditor.Capabilities()
	if len(a) != len(b) {
		t.Fatalf("capability set is unstable across calls: %d vs %d", len(a), len(b))
	}
	for c := range a {
		if !b.Has(c) {
			t.Errorf("capability %s missing from second call", c)
		}
	}
}

func TestMemberActorProjection(t *testing.T) {
	tripID, userID := NewID(), NewID()
	m := &Member{TripID: tripID, UserID: userID, Role: RoleEditor}
	actor := m.Actor()

	if actor.UserID != userID || actor.TripID != tripID || actor.Role != RoleEditor {
		t.Errorf("Actor() lost information: %+v", actor)
	}
	if !actor.Can(CapEditSlots) {
		t.Error("editor projected from membership should be able to edit items")
	}
}
