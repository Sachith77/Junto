"use client";

import { useEffect, useState } from "react";
import { listComments } from "@/lib/api/comments";
import { useAuth } from "@/context/AuthContext";
import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import type { Comment, OpFrame } from "@/lib/types";

// A row carries optimistic UI state alongside the real fields. `pending` means "sent, not yet
// acknowledged by the op broadcast" — the whole point of this slice's "optimistic
// reconciliation" requirement.
type CommentRow = Comment & { pending?: boolean };

export function CommentsList({ tripId, slotId }: { tripId: string; slotId: string }) {
  const { user } = useAuth();
  const { onOp, sendOp, resyncSignal } = useTripSocket();
  const names = useTripMembers(tripId);

  const [comments, setComments] = useState<CommentRow[]>([]);
  const [draft, setDraft] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [deletingIds, setDeletingIds] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    listComments(tripId, slotId)
      .then((fresh) => {
        if (!cancelled) setComments(fresh);
      })
      .catch(() => {
        if (!cancelled) setError("failed to load comments");
      });
    return () => {
      cancelled = true;
    };
  }, [tripId, slotId, resyncSignal]);

  useEffect(() => {
    return onOp((op: OpFrame) => {
      if (op.kind === "comment.create.v1") {
        const fields = op.payload.fields as { slot_id?: string; body?: string };
        if (fields.slot_id !== slotId) return;
        setComments((prev) => {
          // The client chooses entity_id for a create (D4), so our own optimistic entry and
          // the server's ack share the same id — reconciling is "clear pending", not a swap.
          if (prev.some((c) => c.id === op.entity_id)) {
            return prev.map((c) => (c.id === op.entity_id ? { ...c, pending: false } : c));
          }
          return [
            ...prev,
            {
              id: op.entity_id,
              slot_id: fields.slot_id ?? slotId,
              body: fields.body ?? "",
              author_id: op.actor_id,
              created_at: op.created_at,
            },
          ];
        });
        return;
      }
      if (op.kind === "comment.delete.v1") {
        setComments((prev) => prev.filter((c) => c.id !== op.entity_id));
      }
    });
  }, [onOp, slotId]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const body = draft.trim();
    if (!body || !user) return;
    setError(null);
    setSubmitting(true);
    setDraft("");

    // Optimistic: render immediately under the id the create will actually be stored under.
    const id = crypto.randomUUID();
    const optimistic: CommentRow = {
      id,
      slot_id: slotId,
      body,
      author_id: user.id,
      created_at: new Date().toISOString(),
      pending: true,
    };
    setComments((prev) => [...prev, optimistic]);

    try {
      await sendOp({
        kind: "comment.create.v1",
        entityId: id,
        fields: ["slot_id", "body"],
        values: { slot_id: slotId, body },
      });
    } catch (err) {
      // Reconciliation on failure: roll the optimistic row back out and hand the draft back.
      setComments((prev) => prev.filter((c) => c.id !== id));
      setDraft(body);
      setError(err instanceof Error ? err.message : "failed to post comment");
    } finally {
      setSubmitting(false);
    }
  };

  const remove = async (commentId: string) => {
    setError(null);
    setDeletingIds((prev) => new Set(prev).add(commentId));
    const removed = comments.find((c) => c.id === commentId);
    setComments((prev) => prev.filter((c) => c.id !== commentId));

    try {
      await sendOp({
        kind: "comment.delete.v1",
        entityId: commentId,
        fields: ["deleted_at"],
        values: {},
      });
    } catch (err) {
      if (removed) setComments((prev) => [...prev, removed]);
      setError(err instanceof Error ? err.message : "failed to delete comment");
    } finally {
      setDeletingIds((prev) => {
        const next = new Set(prev);
        next.delete(commentId);
        return next;
      });
    }
  };

  const sorted = [...comments].sort((a, b) => a.created_at.localeCompare(b.created_at));

  return (
    <div data-testid="comments-list" className="space-y-3">
      {error && (
        <p role="alert" className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
          {error}
        </p>
      )}
      <ul className="space-y-2">
        {sorted.map((c) => (
          <li
            key={c.id}
            data-testid="comment"
            data-pending={c.pending ?? false}
            className={`rounded-md border border-neutral-200 bg-white px-3 py-2 transition-opacity ${
              c.pending ? "opacity-60" : ""
            }`}
          >
            <div className="flex items-start justify-between gap-2">
              <div>
                <p className="text-xs text-neutral-500">
                  {c.author_id ? (names[c.author_id] ?? c.author_id.slice(0, 8)) : "unknown"}
                </p>
                <p className="text-sm text-neutral-800">{c.body}</p>
              </div>
              {user && c.author_id === user.id && !c.pending && (
                <button
                  type="button"
                  onClick={() => void remove(c.id)}
                  disabled={deletingIds.has(c.id)}
                  data-testid="delete-comment"
                  className="text-xs text-neutral-400 hover:text-red-600 disabled:opacity-50"
                >
                  delete
                </button>
              )}
            </div>
          </li>
        ))}
        {sorted.length === 0 && <p className="text-sm text-neutral-400">No comments yet.</p>}
      </ul>
      <form onSubmit={(e) => void submit(e)} className="flex gap-2">
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Add a comment"
          className="flex-1 rounded-md border border-neutral-300 px-3 py-1.5 text-sm"
        />
        <button
          type="submit"
          disabled={submitting || !draft.trim()}
          data-testid="post-comment"
          className="rounded-md bg-blue-600 px-3 py-1.5 text-sm text-white disabled:opacity-50"
        >
          Post
        </button>
      </form>
    </div>
  );
}
