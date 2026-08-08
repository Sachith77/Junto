package tests

import (
	"fmt"
	"net/http"
	"testing"
)

// TestPlanningCRUDSurfaceCompletes exercises the HTTP paths TestFullPlanningFlow does not
// touch: day listing/update/move/delete, slot update/delete, membership role changes and
// removal, invitation listing/revocation, and trip deletion. Kept as a separate test so each
// stays readable — this one is "the rest of CRUD", not the collaborative flow.
func TestPlanningCRUDSurfaceCompletes(t *testing.T) {
	owner := newClient(t)
	ownerEmail := signupAndVerify(t, owner, "crud-owner")
	ownerToken := login(t, owner, ownerEmail)

	editor := newClient(t)
	editorEmail := signupAndVerify(t, editor, "crud-editor")
	editorToken := login(t, editor, editorEmail)

	trip := createTrip(t, owner, ownerToken, "CRUD Trip")
	tripID := trip["id"].(string)

	resp := owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID),
		map[string]any{"email": editorEmail, "role": "editor"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	msg, ok := testMailer.lastTo(editorEmail)
	if !ok {
		t.Fatal("expected an invitation email")
	}
	acceptToken := extractToken(t, msg.TextBody, "invitations/accept")
	resp = editor.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": acceptToken}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusOK)

	// --- days: create, list, update, move, delete ---
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/days", tripID),
		map[string]string{"label": "Day 1"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var dayOne map[string]any
	resp.decode(t, &dayOne)
	dayOneID := dayOne["id"].(string)

	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/days", tripID),
		map[string]string{"label": "Day 2"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var dayTwo map[string]any
	resp.decode(t, &dayTwo)
	dayTwoID := dayTwo["id"].(string)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/days", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var days []map[string]any
	resp.decode(t, &days)
	if len(days) != 2 {
		t.Fatalf("expected 2 days, got %d", len(days))
	}

	// A viewer may read days but not write them.
	viewer := newClient(t)
	viewerEmail := signupAndVerify(t, viewer, "crud-viewer")
	viewerToken := login(t, viewer, viewerEmail)
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID),
		map[string]any{"email": viewerEmail, "role": "viewer"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	msg, ok = testMailer.lastTo(viewerEmail)
	if !ok {
		t.Fatal("expected a viewer invitation email")
	}
	viewerAcceptToken := extractToken(t, msg.TextBody, "invitations/accept")
	resp = viewer.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": viewerAcceptToken}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusOK)

	resp = viewer.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/days", tripID), nil, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusOK)
	resp = viewer.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/days/%s", tripID, dayOneID),
		map[string]string{"label": "Hijacked"}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	resp = owner.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/days/%s", tripID, dayOneID),
		map[string]any{"label": "Arrival Day", "version": int(dayOne["version"].(float64))}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var updatedDay map[string]any
	resp.decode(t, &updatedDay)
	if updatedDay["label"] != "Arrival Day" {
		t.Errorf("label = %v, want Arrival Day", updatedDay["label"])
	}

	// Move dayTwo to the front.
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/days/%s/move", tripID, dayTwoID),
		map[string]any{"after_id": nil, "version": int(dayTwo["version"].(float64))}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/days", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	resp.decode(t, &days)
	if days[0]["id"] != dayTwoID {
		t.Errorf("expected day two first after moving to the start, got %+v", days)
	}

	resp = owner.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/days/%s", tripID, dayTwoID),
		map[string]any{"version": int(dayTwo["version"].(float64)) + 1}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/days", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	resp.decode(t, &days)
	if len(days) != 1 {
		t.Errorf("expected 1 live day after delete, got %d", len(days))
	}

	// --- slots: create, move, update, delete ---
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots", tripID),
		map[string]any{"day_id": dayOneID, "kind": "activity", "title": "Original title"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var slot map[string]any
	resp.decode(t, &slot)
	slotID := slot["id"].(string)

	// Move it to the trip backlog (day_id: null) — the HTTP path for SlotService.Move,
	// distinct from Update.
	resp = viewer.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/move", tripID, slotID),
		map[string]any{"day_id": nil, "after_id": nil, "version": int(slot["version"].(float64))},
		withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/move", tripID, slotID),
		map[string]any{"day_id": nil, "after_id": nil, "version": int(slot["version"].(float64))},
		withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	// A fresh map, not a reuse of `slot`: json.Unmarshal into an already-populated map
	// MERGES rather than replaces, so decoding into the old variable would silently keep
	// its stale day_id key around (day_id is omitempty and absent for a backlog slot,
	// meaning the new JSON never mentions the key at all to overwrite it).
	var afterMove map[string]any
	resp.decode(t, &afterMove)
	if _, scheduled := afterMove["day_id"]; scheduled {
		t.Errorf("expected the slot in the backlog (no day_id) after the move, got %+v", afterMove)
	}
	slot = afterMove

	resp = viewer.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID),
		map[string]any{"kind": "activity", "title": "Hijacked", "version": int(slot["version"].(float64))},
		withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	resp = editor.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID),
		map[string]any{"kind": "activity", "title": "Updated title", "notes": "some notes",
			"version": int(slot["version"].(float64))},
		withBearer(editorToken))
	assertStatus(t, resp, http.StatusOK)
	var updatedSlot map[string]any
	resp.decode(t, &updatedSlot)
	if updatedSlot["title"] != "Updated title" {
		t.Errorf("title = %v, want Updated title", updatedSlot["title"])
	}

	// --- options: create, update ---
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/options", tripID, slotID),
		map[string]any{"title": "First draft title"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var option map[string]any
	resp.decode(t, &option)
	optionID := option["id"].(string)

	resp = viewer.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/slots/%s/options/%s", tripID, slotID, optionID),
		map[string]any{"title": "Hijacked", "version": int(option["version"].(float64))}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	resp = owner.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/slots/%s/options/%s", tripID, slotID, optionID),
		map[string]any{"title": "Revised title", "version": int(option["version"].(float64))}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var updatedOption map[string]any
	resp.decode(t, &updatedOption)
	if updatedOption["title"] != "Revised title" {
		t.Errorf("option title = %v, want Revised title", updatedOption["title"])
	}

	// --- votes: cast, then list every member's vote (distinct from the tally endpoint,
	// which TestFullPlanningFlow already covers) ---
	resp = editor.do(http.MethodPut, fmt.Sprintf("/api/v1/trips/%s/slots/%s/votes/me", tripID, slotID),
		map[string]any{"option_id": optionID}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusOK)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s/votes", tripID, slotID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var votes []map[string]any
	resp.decode(t, &votes)
	if len(votes) != 1 || votes[0]["option_id"] != optionID {
		t.Errorf("unexpected vote list: %+v", votes)
	}

	resp = viewer.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID),
		map[string]any{"version": int(updatedSlot["version"].(float64))}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	resp = owner.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID),
		map[string]any{"version": int(updatedSlot["version"].(float64))}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNotFound)

	// --- membership: role change, list, remove ---
	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/members", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var members []map[string]any
	resp.decode(t, &members)
	if len(members) != 3 {
		t.Fatalf("expected 3 members (owner, editor, viewer), got %d", len(members))
	}

	// Promote the viewer to editor.
	var viewerUserID string
	for _, m := range members {
		if m["role"] == "viewer" {
			viewerUserID = m["user_id"].(string)
		}
	}
	if viewerUserID == "" {
		t.Fatal("could not find the viewer in the member list")
	}

	resp = editor.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/members/%s", tripID, viewerUserID),
		map[string]any{"role": "editor", "version": 1}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusForbidden) // an editor cannot manage members

	resp = owner.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s/members/%s", tripID, viewerUserID),
		map[string]any{"role": "editor", "version": 1}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var promoted map[string]any
	resp.decode(t, &promoted)
	if promoted["role"] != "editor" {
		t.Errorf("role = %v, want editor", promoted["role"])
	}

	// Remove that member entirely.
	resp = owner.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/members/%s", tripID, viewerUserID),
		nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/members", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	resp.decode(t, &members)
	if len(members) != 2 {
		t.Errorf("expected 2 members after removal, got %d", len(members))
	}

	// --- invitations: list and revoke ---
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID),
		map[string]any{"role": "viewer"}, withBearer(ownerToken)) // a link invite: no email
	assertStatus(t, resp, http.StatusCreated)
	var linkInv map[string]any
	resp.decode(t, &linkInv)
	invID := linkInv["id"].(string)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var invitations []map[string]any
	resp.decode(t, &invitations)
	if len(invitations) == 0 {
		t.Fatal("expected at least the freshly created link invitation")
	}

	// CapInviteMembers is granted to editors too (not owner-only) — inviting collaborators
	// is treated as itinerary-planning work, unlike CapManageMembers (role changes,
	// removal) and CapDeleteTrip, which stay owner/manager-gated. So the editor revoking
	// their own trip's invitation is the POSITIVE case here, not a 403; the negative case
	// (a role with neither capability) is already covered at the unit level in
	// TestListAndRevokeInvitationsRequireCapability.
	resp = editor.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/invitations/%s", tripID, invID),
		nil, withBearer(editorToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	resp.decode(t, &invitations)
	for _, inv := range invitations {
		if inv["id"] == invID {
			t.Errorf("the revoked invitation must not be listed, got %+v", inv)
		}
	}

	// --- trip deletion, last ---
	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var current map[string]any
	resp.decode(t, &current)

	resp = editor.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s", tripID),
		map[string]any{"version": int(current["version"].(float64))}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusForbidden) // CapDeleteTrip is owner-only

	resp = owner.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s", tripID),
		map[string]any{"version": int(current["version"].(float64))}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNotFound)
}
