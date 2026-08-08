package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// Full-stack proof that Junto does something a user would call "planning": real router,
// real services, real Postgres, real authorization decisions made by Actor.Can() inside the
// service layer and surfaced as real HTTP status codes. This is the slice that closes
// Stage 1 — these tests are the evidence for that claim, not the auth-only tests above.

// createTrip is a small helper mirroring login/signupAndVerify's style.
func createTrip(t *testing.T, c *client, token, name string) map[string]any {
	t.Helper()
	resp := c.do(http.MethodPost, "/api/v1/trips", map[string]string{
		"name": name, "time_zone": "Asia/Kolkata",
	}, withBearer(token))
	assertStatus(t, resp, http.StatusCreated)
	var trip map[string]any
	resp.decode(t, &trip)
	return trip
}

// TestFullPlanningFlow walks the whole surface this slice adds: create a trip, invite a
// collaborator by email, that collaborator redeems the link, the owner builds a day and a
// slot with two candidate options, the collaborator votes, the owner resolves the slot —
// and along the way, a viewer is shown to be able to read but not write, and a non-member is
// shown to be unable to see the trip at all.
func TestFullPlanningFlow(t *testing.T) {
	owner := newClient(t)
	ownerEmail := signupAndVerify(t, owner, "planflow-owner")
	ownerToken := login(t, owner, ownerEmail)

	editor := newClient(t)
	editorEmail := signupAndVerify(t, editor, "planflow-editor")

	trip := createTrip(t, owner, ownerToken, "Goa Trip")
	tripID := trip["id"].(string)

	// --- invite + redeem ---
	resp := owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID),
		map[string]any{"email": editorEmail, "role": "editor"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)

	msg, ok := testMailer.lastTo(editorEmail)
	if !ok {
		t.Fatal("expected an invitation email")
	}
	token := extractToken(t, msg.TextBody, "invitations/accept")

	editorToken := login(t, editor, editorEmail)
	resp = editor.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": token}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusOK)
	var redeemedTrip map[string]any
	resp.decode(t, &redeemedTrip)
	if redeemedTrip["id"] != tripID {
		t.Errorf("redeemed trip id = %v, want %v", redeemedTrip["id"], tripID)
	}

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/members", tripID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var members []map[string]any
	resp.decode(t, &members)
	if len(members) != 2 {
		t.Fatalf("expected 2 members after redemption, got %d", len(members))
	}

	// --- day ---
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/days", tripID),
		map[string]string{"label": "Day 1"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var day map[string]any
	resp.decode(t, &day)
	dayID := day["id"].(string)

	// --- slot with two options ---
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots", tripID),
		map[string]any{"day_id": dayID, "kind": "lodging", "title": "Where are we staying"},
		withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var slot map[string]any
	resp.decode(t, &slot)
	slotID := slot["id"].(string)
	if slot["status"] != "planned" {
		t.Errorf("a new slot must start planned, got %v", slot["status"])
	}

	resp = editor.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/options", tripID, slotID),
		map[string]any{"title": "Taj Exotica"}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusCreated)
	var taj map[string]any
	resp.decode(t, &taj)
	tajID := taj["id"].(string)

	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/options", tripID, slotID),
		map[string]any{"title": "Airbnb in Anjuna"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s/options", tripID, slotID),
		nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var options []map[string]any
	resp.decode(t, &options)
	if len(options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(options))
	}

	// --- voting ---
	resp = editor.do(http.MethodPut, fmt.Sprintf("/api/v1/trips/%s/slots/%s/votes/me", tripID, slotID),
		map[string]any{"option_id": tajID}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusOK)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s/votes/tally", tripID, slotID),
		nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var tallies []map[string]any
	resp.decode(t, &tallies)
	if len(tallies) != 1 || tallies[0]["option_id"] != tajID || int(tallies[0]["count"].(float64)) != 1 {
		t.Errorf("unexpected tally: %+v", tallies)
	}

	// --- resolution: the owner overrides the vote, which is allowed by design ---
	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/select", tripID, slotID),
		map[string]any{"option_id": tajID, "version": int(slot["version"].(float64))}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var resolved map[string]any
	resp.decode(t, &resolved)
	if resolved["selected_option_id"] != tajID {
		t.Errorf("selected_option_id = %v, want %v", resolved["selected_option_id"], tajID)
	}

	// --- Live-mode coverage ---
	resp = editor.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/status", tripID, slotID),
		map[string]any{"status": "covered", "version": int(resolved["version"].(float64))}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusNoContent)

	// --- THE FIX, proven end to end: deleting the selected option clears the selection ---
	resp = owner.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/slots/%s/options/%s", tripID, slotID, tajID),
		map[string]any{"version": int(taj["version"].(float64))}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s", tripID, slotID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var afterDelete map[string]any
	resp.decode(t, &afterDelete)
	if _, stillSet := afterDelete["selected_option_id"]; stillSet {
		t.Errorf("selected_option_id must be cleared after its option was deleted, got %+v", afterDelete)
	}

	// --- a viewer can read, not write ---
	viewer := newClient(t)
	viewerEmail := signupAndVerify(t, viewer, "planflow-viewer")
	viewerToken := login(t, viewer, viewerEmail)

	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID),
		map[string]any{"email": viewerEmail, "role": "viewer"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	msg, ok = testMailer.lastTo(viewerEmail)
	if !ok {
		t.Fatal("expected a viewer invitation email")
	}
	viewerToken2 := extractToken(t, msg.TextBody, "invitations/accept")
	resp = viewer.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": viewerToken2}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusOK)

	resp = viewer.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s", tripID), nil, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusOK)

	resp = viewer.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots", tripID),
		map[string]any{"kind": "note", "title": "Sneaky slot"}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	resp = viewer.do(http.MethodPatch, fmt.Sprintf("/api/v1/trips/%s", tripID),
		map[string]any{"name": "Hijacked", "time_zone": "UTC", "version": 1}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	// --- a non-member sees nothing, not even that the trip exists ---
	outsider := newClient(t)
	outsiderEmail := signupAndVerify(t, outsider, "planflow-outsider")
	outsiderToken := login(t, outsider, outsiderEmail)

	resp = outsider.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s", tripID), nil, withBearer(outsiderToken))
	assertStatus(t, resp, http.StatusNotFound)

	resp = outsider.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots", tripID), nil, withBearer(outsiderToken))
	assertStatus(t, resp, http.StatusNotFound)
}

// TestTripListPaginationIsKeyset proves GET /trips actually paginates with a cursor rather
// than silently returning everything, and that walking pages visits each trip exactly once.
func TestTripListPaginationIsKeyset(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "paginate-owner")
	token := login(t, c, email)

	const total = 7
	created := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		trip := createTrip(t, c, token, fmt.Sprintf("Trip %d", i))
		created[trip["id"].(string)] = true
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/api/v1/trips?limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		resp := c.do(http.MethodGet, path, nil, withBearer(token))
		assertStatus(t, resp, http.StatusOK)

		var env struct {
			Data []map[string]any `json:"data"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(resp.body, &env); err != nil {
			t.Fatalf("decoding page: %v", err)
		}
		pages++
		if len(env.Data) > 3 {
			t.Fatalf("page returned %d items, want at most 3", len(env.Data))
		}
		for _, tr := range env.Data {
			id := tr["id"].(string)
			if seen[id] {
				t.Errorf("trip %s was returned on more than one page", id)
			}
			seen[id] = true
		}
		if !env.Meta.HasMore {
			break
		}
		cursor = env.Meta.NextCursor
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
	}

	for id := range created {
		if !seen[id] {
			t.Errorf("trip %s was never returned while paginating", id)
		}
	}
	if pages < 2 {
		t.Errorf("expected pagination to span multiple pages for %d items at limit 3, got %d pages", total, pages)
	}
}
