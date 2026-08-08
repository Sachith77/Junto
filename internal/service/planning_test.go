package service

import (
	"context"
	"errors"
	"testing"

	"github.com/junto/junto/internal/domain"
)

// planningHarness wires every planning service against a shared set of fakes, so tests read
// as a story ("owner creates a trip, invites an editor, editor proposes a hotel...") instead
// of re-wiring dependencies in every test.
type planningHarness struct {
	trips    *TripService
	members  *MembershipService
	days     *DayService
	slots    *SlotService
	options  *SlotOptionService
	votes    *VoteService
	comments *CommentService
	budget   *BudgetService
	files    *AttachmentService

	membersFake     *fakeMembers
	commentsFake    *fakeComments
	budgetFake      *fakeBudget
	attachmentsFake *fakeAttachments
	storageFake     *fakeStorage
	usersFake       *fakeUsers
	mailerFake      *fakeMailer
	opsFake         *fakeOpLog
}

// ptr is a shorthand for the optional optimistic-concurrency preconditions (D69). Tests that
// pass one are asserting REST semantics; tests that pass nil are asserting merge semantics,
// and the distinction is now visible at every call site rather than implied.
func ptr[T any](v T) *T { return &v }

func newPlanningHarness(t *testing.T) *planningHarness {
	t.Helper()

	membersFake := newFakeMembers()
	tripsFake := newFakeTrips(membersFake)
	usersFake := newFakeUsers()
	invitationsFake := newFakeInvitations()
	daysFake := newFakeDays()
	slotsFake := newFakeSlots()
	optionsFake := newFakeSlotOptions()
	votesFake := newFakeVotes()
	commentsFake := newFakeComments()
	mailerFake := &fakeMailer{}
	opsFake := newFakeOpLog()
	budgetFake := newFakeBudget()
	attachmentsFake := newFakeAttachments()
	storageFake := newFakeStorage()

	// Every planning service shares ONE op log and ONE sequencer, exactly as production does.
	// That sharing is what lets a test assert that an intent touching two entities produced
	// two consecutively numbered operations in a single trip's total order.
	return &planningHarness{
		trips: NewTripService(TripDeps{Trips: tripsFake, Members: membersFake, Tx: &fakeTx{}}),
		members: NewMembershipService(MembershipDeps{
			Members: membersFake, Trips: tripsFake, Users: usersFake, Invitations: invitationsFake,
			Mailer: mailerFake, Tx: &fakeTx{}, Config: MembershipConfig{WebBaseURL: "https://junto.test"},
		}),
		days: NewDayService(DayDeps{
			Days: daysFake, Members: membersFake, Trips: tripsFake, Ops: opsFake, Tx: &fakeTx{},
		}),
		slots: NewSlotService(SlotDeps{
			Slots: slotsFake, Members: membersFake, Trips: tripsFake, Ops: opsFake, Tx: &fakeTx{},
		}),
		options: NewSlotOptionService(SlotOptionDeps{
			Options: optionsFake, Slots: slotsFake, Members: membersFake, Trips: tripsFake,
			Ops: opsFake, Tx: &fakeTx{},
		}),
		votes: NewVoteService(VoteDeps{
			Votes: votesFake, Slots: slotsFake, Members: membersFake, Trips: tripsFake,
			Ops: opsFake, Tx: &fakeTx{},
		}),
		comments: NewCommentService(CommentDeps{
			Comments: commentsFake, Slots: slotsFake, Members: membersFake, Trips: tripsFake,
			Ops: opsFake, Tx: &fakeTx{},
		}),
		budget: NewBudgetService(BudgetDeps{
			Budget: budgetFake, Members: membersFake, Trips: tripsFake, Ops: opsFake, Tx: &fakeTx{},
		}),
		files: NewAttachmentService(AttachmentDeps{
			Attachments: attachmentsFake, Storage: storageFake, Slots: slotsFake,
			Options: optionsFake, Budget: budgetFake, Members: membersFake, Trips: tripsFake,
			Ops: opsFake, Tx: &fakeTx{},
		}),

		budgetFake:      budgetFake,
		attachmentsFake: attachmentsFake,
		storageFake:     storageFake,
		membersFake:     membersFake,
		commentsFake:    commentsFake,
		usersFake:       usersFake,
		mailerFake:      mailerFake,
		opsFake:         opsFake,
	}
}

// makeUser registers a user directly in the fake, bypassing signup — these tests are about
// planning, not auth, and auth's own tests already cover account creation.
func (h *planningHarness) makeUser(t *testing.T, email string) *domain.User {
	t.Helper()
	u := &domain.User{ID: domain.NewID(), Email: email, PasswordHash: "x", DisplayName: "Test User"}
	if err := h.usersFake.Create(context.Background(), u); err != nil {
		t.Fatalf("creating user: %v", err)
	}
	return u
}

// makeTrip creates a trip owned by owner.
func (h *planningHarness) makeTrip(t *testing.T, owner *domain.User) *domain.Trip {
	t.Helper()
	trip, err := h.trips.Create(context.Background(), owner.ID, CreateTripInput{
		Name: "Goa Trip", TimeZone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatalf("creating trip: %v", err)
	}
	return trip
}

// addMember adds user to trip directly (bypassing invitations) at the given role.
func (h *planningHarness) addMember(t *testing.T, tripID domain.ID, user *domain.User, role domain.Role) {
	t.Helper()
	if err := h.membersFake.Add(context.Background(), &domain.Member{
		ID: domain.NewID(), TripID: tripID, UserID: user.ID, Role: role,
	}); err != nil {
		t.Fatalf("adding member: %v", err)
	}
}

// --- trips ---

func TestCreateTripAddsCallerAsOwner(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")

	trip := h.makeTrip(t, owner)

	member, err := h.membersFake.Get(ctx, trip.ID, owner.ID)
	if err != nil {
		t.Fatalf("expected the creator to be a member: %v", err)
	}
	if member.Role != domain.RoleOwner {
		t.Errorf("role = %q, want owner", member.Role)
	}
}

func TestNonMemberCannotGetTrip(t *testing.T) {
	h := newPlanningHarness(t)
	owner := h.makeUser(t, "owner@example.com")
	outsider := h.makeUser(t, "outsider@example.com")
	trip := h.makeTrip(t, owner)

	// A non-member gets the SAME error as a nonexistent trip. Distinguishing them would
	// disclose that the trip exists to someone with no access to it.
	_, err := h.trips.Get(context.Background(), trip.ID, outsider.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for a non-member, got %v", err)
	}
}

func TestViewerCannotEditOrDeleteTrip(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)

	// A viewer CAN read.
	if _, err := h.trips.Get(ctx, trip.ID, viewer.ID); err != nil {
		t.Errorf("a viewer should be able to read the trip: %v", err)
	}

	// A viewer may NOT edit or delete. This is Actor.Can() enforced by a real handler path
	// (service -> domain), not just exercised as a standalone unit.
	_, err := h.trips.Update(ctx, trip.ID, viewer.ID, UpdateTripInput{
		Name: "Hijacked", TimeZone: "UTC", Version: trip.Version,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("expected ErrForbidden for a viewer editing, got %v", err)
	}

	err = h.trips.Delete(ctx, trip.ID, viewer.ID, trip.Version)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("expected ErrForbidden for a viewer deleting, got %v", err)
	}
}

// TestEditingTheTripContainerIsOwnerOnly covers a real, deliberate distinction: CapEditTrip
// and CapDeleteTrip (renaming, changing dates/timezone, deleting the trip itself) are
// owner-only. CapEditSlots and friends (planning the itinerary's CONTENT) are granted to
// editors. An editor who can restructure the whole itinerary still cannot rename the trip
// or delete it — those are container-level operations, not content ones.
func TestEditingTheTripContainerIsOwnerOnly(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	editor := h.makeUser(t, "editor@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, editor, domain.RoleEditor)

	_, err := h.trips.Update(ctx, trip.ID, editor.ID, UpdateTripInput{
		Name: "Renamed", TimeZone: trip.TimeZone, Version: trip.Version,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("an editor renaming the trip container must be forbidden, got %v", err)
	}
	if err := h.trips.Delete(ctx, trip.ID, editor.ID, trip.Version); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("an editor deleting the trip must be forbidden, got %v", err)
	}

	// The owner can do both.
	updated, err := h.trips.Update(ctx, trip.ID, owner.ID, UpdateTripInput{
		Name: "Renamed", TimeZone: trip.TimeZone, Version: trip.Version,
	})
	if err != nil {
		t.Fatalf("the owner should be able to rename the trip: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", updated.Name)
	}
}

func TestListForUserOnlyReturnsMemberTrips(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	alice := h.makeUser(t, "alice@example.com")
	bob := h.makeUser(t, "bob@example.com")

	h.makeTrip(t, alice)
	h.makeTrip(t, alice)
	h.makeTrip(t, bob)

	page, err := h.trips.ListForUser(ctx, alice.ID, domain.PageRequest{})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("expected alice's 2 trips, got %d", len(page.Items))
	}
}

// --- membership: owner protection ---

func TestOwnerRoleCannotBeChangedOrRemovedThroughGenericPaths(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	trip := h.makeTrip(t, owner)

	ownerMember, err := h.membersFake.Get(ctx, trip.ID, owner.ID)
	if err != nil {
		t.Fatalf("reading owner membership: %v", err)
	}

	_, err = h.members.UpdateRole(ctx, trip.ID, owner.ID, owner.ID, domain.RoleEditor, ownerMember.Version)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("changing the owner's role must be forbidden, got %v", err)
	}

	if err := h.members.RemoveMember(ctx, trip.ID, owner.ID, owner.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("removing the owner must be forbidden, got %v", err)
	}
}

func TestUpdateRoleRejectsGrantingOwnership(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	editor := h.makeUser(t, "editor@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, editor, domain.RoleEditor)

	member, _ := h.membersFake.Get(ctx, trip.ID, editor.ID)
	_, err := h.members.UpdateRole(ctx, trip.ID, owner.ID, editor.ID, domain.RoleOwner, member.Version)
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("granting ownership through UpdateRole must be a validation error, got %v", err)
	}
}

func TestViewerCannotManageMembers(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	target := h.makeUser(t, "target@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	h.addMember(t, trip.ID, target, domain.RoleViewer)

	targetMember, _ := h.membersFake.Get(ctx, trip.ID, target.ID)
	_, err := h.members.UpdateRole(ctx, trip.ID, viewer.ID, target.ID, domain.RoleEditor, targetMember.Version)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer must not manage members, got %v", err)
	}
}

// --- invitations ---

func TestInvitationRedemptionAddsMembership(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	email := "joiner@example.com"
	joiner := h.makeUser(t, email)
	trip := h.makeTrip(t, owner)

	// CreateInvitation deliberately never returns the raw token — only its hash is stored,
	// by design (D9's reasoning applied here too: a stored secret is a liability). The raw
	// value exists exactly once, in the mailed link, which is where this test recovers it —
	// the same path a real invitee follows.
	if _, err := h.members.CreateInvitation(ctx, trip.ID, owner.ID, CreateInvitationInput{
		Email: &email, Role: domain.RoleEditor,
	}); err != nil {
		t.Fatalf("creating invitation: %v", err)
	}
	msg, ok := h.mailerFake.Last()
	if !ok {
		t.Fatal("expected an invitation email")
	}
	token := extractTokenFromBody(t, msg.TextBody)

	trip2, err := h.members.RedeemInvitation(ctx, joiner.ID, token)
	if err != nil {
		t.Fatalf("redeeming: %v", err)
	}
	if trip2.ID != trip.ID {
		t.Errorf("redeemed trip = %v, want %v", trip2.ID, trip.ID)
	}

	member, err := h.membersFake.Get(ctx, trip.ID, joiner.ID)
	if err != nil {
		t.Fatalf("expected the joiner to be a member: %v", err)
	}
	if member.Role != domain.RoleEditor {
		t.Errorf("role = %q, want editor (the invitation's granted role)", member.Role)
	}
}

func TestRedeemInvitationRejectsEmailMismatch(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	trip := h.makeTrip(t, owner)
	targetEmail := "invitee@example.com"
	wrongUser := h.makeUser(t, "someone-else@example.com")

	if _, err := h.members.CreateInvitation(ctx, trip.ID, owner.ID, CreateInvitationInput{
		Email: &targetEmail, Role: domain.RoleEditor,
	}); err != nil {
		t.Fatalf("creating invitation: %v", err)
	}
	msg, ok := h.mailerFake.Last()
	if !ok {
		t.Fatal("expected an invitation email to be sent")
	}
	token := extractTokenFromBody(t, msg.TextBody)

	_, err := h.members.RedeemInvitation(ctx, wrongUser.ID, token)
	if !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("redeeming a targeted invite as the wrong user must fail, got %v", err)
	}
}

// TestRedeemInvitationIsIdempotentForAnAlreadyJoinedMember covers an unlimited-use link
// invite redeemed twice by the same person — a double click, a stale tab reopened. The
// second redemption must succeed rather than error, and must not create a duplicate
// membership row.
func TestRedeemInvitationIsIdempotentForAnAlreadyJoinedMember(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	email := "joiner@example.com"
	joiner := h.makeUser(t, email)
	trip := h.makeTrip(t, owner)

	if _, err := h.members.CreateInvitation(ctx, trip.ID, owner.ID, CreateInvitationInput{
		Email: &email, Role: domain.RoleEditor,
		MaxUses: nil, // unlimited: a double redemption should not be rejected on use count alone
	}); err != nil {
		t.Fatalf("creating invitation: %v", err)
	}
	msg, ok := h.mailerFake.Last()
	if !ok {
		t.Fatal("expected an email")
	}
	token := extractTokenFromBody(t, msg.TextBody)

	if _, err := h.members.RedeemInvitation(ctx, joiner.ID, token); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	if _, err := h.members.RedeemInvitation(ctx, joiner.ID, token); err != nil {
		t.Fatalf("second redemption of an already-joined member must succeed idempotently: %v", err)
	}

	members, err := h.membersFake.List(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing members: %v", err)
	}
	count := 0
	for _, m := range members {
		if m.UserID == joiner.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one membership row for the joiner, got %d", count)
	}
}

// extractTokenFromBody pulls the raw token out of a mailed link, the way a user would click
// it, rather than reaching past the service's deliberate refusal to hand back a raw secret.
func extractTokenFromBody(t *testing.T, body string) string {
	t.Helper()
	const marker = "token="
	idx := indexOf(body, marker)
	if idx < 0 {
		t.Fatalf("no token in email body:\n%s", body)
	}
	rest := body[idx+len(marker):]
	end := 0
	for end < len(rest) && rest[end] != '\n' && rest[end] != ' ' && rest[end] != '\r' {
		end++
	}
	return rest[:end]
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
