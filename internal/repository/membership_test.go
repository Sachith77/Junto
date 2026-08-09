package repository

import (
	"testing"

	"github.com/junto/junto/internal/domain"
)

// TestListMemberProfilesReturnsDisplayNames covers the join the UI depends on.
//
// Worth its own test because the read model is the only place membership meets `users`, and
// the failure it guards is silent: a broken join returns rows with empty names, and every
// collaborative surface renders blanks where a person should be rather than erroring.
func TestListMemberProfilesReturnsDisplayNames(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	editor := makeUser(t, ctx, r)
	if err := r.members.Add(ctx, &domain.Member{
		ID: domain.NewID(), TripID: trip.ID, UserID: editor.ID, Role: domain.RoleEditor,
	}); err != nil {
		t.Fatalf("adding member: %v", err)
	}

	profiles, err := r.members.ListProfiles(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing profiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("listed %d profiles, want 2", len(profiles))
	}
	// Owner first, matching ListMembers' ordering.
	if profiles[0].Role != domain.RoleOwner {
		t.Errorf("first profile role = %q, want owner", profiles[0].Role)
	}
	for _, p := range profiles {
		if p.DisplayName == "" {
			t.Errorf("member %s came back with no display name", p.UserID)
		}
		if p.UserID == domain.NilID {
			t.Error("profile lost its membership fields in the join")
		}
	}
}
