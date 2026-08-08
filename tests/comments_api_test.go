package tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Comments: Stage 3 Slice 2. Append-only (create + author-only delete, no edit), the same
// treatment as attachments (D46/D84) — decided before any of this was written. Unlike
// attachments there is no presign exchange to gate behind REST-only, so comments are WS-native
// and get the ordinary create/delete dispatch, same as votes and slots.
//
// This file does NOT need convergence-test rigor: there is nothing to merge, so there is no
// race to prove correct. What it proves instead: the REST surface and its authz (including the
// one delete path in the whole service layer that is author-gated rather than
// capability-gated), and that a WS-submitted comment reaches a second subscribed client and
// folds identically to what the REST API reports — Rule 3 exercised on the one entity in this
// slice that could most easily have skipped the op log by accident.

func TestCommentRESTLifecycleAndAuthorOnlyDelete(t *testing.T) {
	owner := newClient(t)
	ownerEmail := signupAndVerify(t, owner, "comment-owner")
	ownerToken := login(t, owner, ownerEmail)

	editor := newClient(t)
	editorEmail := signupAndVerify(t, editor, "comment-editor")

	viewer := newClient(t)
	viewerEmail := signupAndVerify(t, viewer, "comment-viewer")

	trip := createTrip(t, owner, ownerToken, "Comment Trip")
	tripID := trip["id"].(string)

	for _, invite := range []struct {
		client *client
		email  string
		role   string
	}{{editor, editorEmail, "editor"}, {viewer, viewerEmail, "viewer"}} {
		resp := owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID),
			map[string]any{"email": invite.email, "role": invite.role}, withBearer(ownerToken))
		assertStatus(t, resp, http.StatusCreated)
	}
	editorToken := login(t, editor, editorEmail)
	msg, ok := testMailer.lastTo(editorEmail)
	if !ok {
		t.Fatal("expected an invitation email for the editor")
	}
	resp := editor.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": extractToken(t, msg.TextBody, "invitations/accept")}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusOK)

	viewerToken := login(t, viewer, viewerEmail)
	msg, ok = testMailer.lastTo(viewerEmail)
	if !ok {
		t.Fatal("expected an invitation email for the viewer")
	}
	resp = viewer.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": extractToken(t, msg.TextBody, "invitations/accept")}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusOK)

	resp = owner.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots", tripID),
		map[string]any{"kind": "activity", "title": "Beach day"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var slot map[string]any
	resp.decode(t, &slot)
	slotID := slot["id"].(string)

	// A viewer may read but not comment — CapComment is owner/editor only, same as CapVote.
	resp = viewer.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments", tripID, slotID),
		map[string]string{"body": "sneaking in"}, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusForbidden)

	// The editor posts a comment.
	resp = editor.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments", tripID, slotID),
		map[string]string{"body": "I'll bring snacks"}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusCreated)
	var comment map[string]any
	resp.decode(t, &comment)
	commentID := comment["id"].(string)
	if comment["body"] != "I'll bring snacks" {
		t.Errorf("body = %v, want %q", comment["body"], "I'll bring snacks")
	}

	// Both the viewer and the owner can READ it.
	resp = viewer.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments", tripID, slotID), nil, withBearer(viewerToken))
	assertStatus(t, resp, http.StatusOK)
	var list []map[string]any
	resp.decode(t, &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(list))
	}

	// The OWNER — capability-wise the most privileged member on this trip — still cannot
	// delete the editor's comment. This is the one delete path in the whole service layer
	// that is author-gated rather than capability-gated.
	resp = owner.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments/%s", tripID, slotID, commentID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusForbidden)

	// The author can delete their own comment.
	resp = editor.do(http.MethodDelete, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments/%s", tripID, slotID, commentID), nil, withBearer(editorToken))
	assertStatus(t, resp, http.StatusNoContent)

	resp = owner.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments", tripID, slotID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	resp.decode(t, &list)
	if len(list) != 0 {
		t.Errorf("expected 0 comments after the author's delete, got %d", len(list))
	}

	// A non-member gets not-found, the same as everywhere else in this API (D53).
	outsider := newClient(t)
	outsiderEmail := signupAndVerify(t, outsider, "comment-outsider")
	outsiderToken := login(t, outsider, outsiderEmail)
	resp = outsider.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments", tripID, slotID), nil, withBearer(outsiderToken))
	assertStatus(t, resp, http.StatusNotFound)
}

// TestCommentDeliveredLiveOverWebSocketAndFolds proves comments are WS-native (unlike
// attachments) and that Rule 3 holds for them: a comment submitted by one client reaches a
// second subscribed client as a live op frame, and both replicas end up matching what the
// REST API reports — fold(trip_ops) == database == client state, the same equality the
// convergence tests hold slots/options/votes to, without needing a race to prove it.
func TestCommentDeliveredLiveOverWebSocketAndFolds(t *testing.T) {
	ownerHTTP := newClient(t)
	ownerEmail := signupAndVerify(t, ownerHTTP, "comment-ws-owner")
	ownerToken := login(t, ownerHTTP, ownerEmail)

	editorHTTP := newClient(t)
	editorEmail := signupAndVerify(t, editorHTTP, "comment-ws-editor")

	trip := createTrip(t, ownerHTTP, ownerToken, "Comment WS Trip")
	tripIDStr := trip["id"].(string)
	tripID, err := domain.ParseID("trip_id", tripIDStr)
	if err != nil {
		t.Fatalf("parsing trip id: %v", err)
	}

	resp := ownerHTTP.do(http.MethodPost, "/api/v1/trips/"+tripIDStr+"/invitations",
		map[string]any{"email": editorEmail, "role": "editor"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	msg, ok := testMailer.lastTo(editorEmail)
	if !ok {
		t.Fatal("expected an invitation email")
	}
	editorToken := login(t, editorHTTP, editorEmail)
	resp = editorHTTP.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": extractToken(t, msg.TextBody, "invitations/accept")}, withBearer(editorToken))
	assertStatus(t, resp, http.StatusOK)

	resp = ownerHTTP.do(http.MethodPost, "/api/v1/trips/"+tripIDStr+"/slots",
		map[string]any{"kind": "lodging", "title": "Where are we staying"}, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusCreated)
	var slotResp map[string]any
	resp.decode(t, &slotResp)
	slotID, err := domain.ParseID("slot_id", slotResp["id"].(string))
	if err != nil {
		t.Fatalf("parsing slot id: %v", err)
	}

	ownerID := meID(t, ownerHTTP, ownerToken)
	editorID := meID(t, editorHTTP, editorToken)
	ownerWS := dialWS(t, "owner", ownerHTTP, ownerToken, ownerID)
	editorWS := dialWS(t, "editor", editorHTTP, editorToken, editorID)
	ownerWS.subscribe(tripID)
	editorWS.subscribe(tripID)

	commentID := domain.NewID()
	body := "Should we book the earlier flight?"
	clientOpID := ownerWS.submit(tripID, domain.OpCommentCreate, commentID,
		[]string{"slot_id", "body"},
		map[string]any{"slot_id": slotID.String(), "body": body})
	ownerWS.awaitResolution(clientOpID, 5*time.Second)
	if errs := ownerWS.errorFrames(); len(errs) > 0 {
		t.Fatalf("owner received error frames: %v", errs)
	}

	// The editor — a DIFFERENT connection that never submitted anything — must receive the
	// comment live. This is the actual proof the entity is wired to the sync engine, not a
	// REST-only broadcast like attachments.
	editorWS.waitForSeq(ownerWS.snapshot().Seq, 5*time.Second)

	for name, snap := range map[string]*domain.Replica{"owner": ownerWS.snapshot(), "editor": editorWS.snapshot()} {
		c, ok := snap.Comments[commentID]
		if !ok {
			t.Fatalf("%s's replica is missing the comment", name)
		}
		if c.Body != body {
			t.Errorf("%s folded body = %q, want %q", name, c.Body, body)
		}
		if c.SlotID != slotID {
			t.Errorf("%s folded slot_id = %s, want %s", name, c.SlotID, slotID)
		}
		if c.AuthorID == nil || *c.AuthorID != ownerID {
			t.Errorf("%s folded author_id = %v, want %s", name, c.AuthorID, ownerID)
		}
	}

	// fold(trip_ops) == database: the REST API, reading straight from Postgres, must agree.
	resp = ownerHTTP.do(http.MethodGet, fmt.Sprintf("/api/v1/trips/%s/slots/%s/comments", tripIDStr, slotID), nil, withBearer(ownerToken))
	assertStatus(t, resp, http.StatusOK)
	var list []map[string]any
	resp.decode(t, &list)
	if len(list) != 1 || list[0]["id"] != commentID.String() || list[0]["body"] != body {
		t.Errorf("database state does not match the folded replicas: %+v", list)
	}
}
