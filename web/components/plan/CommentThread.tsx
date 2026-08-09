"use client";

import { useEffect, useRef, useState } from "react";
import { listComments } from "@/lib/api/comments";
import { useAuth } from "@/context/AuthContext";
import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import { useLiveFlash } from "@/hooks/useLiveFlash";
import { Button } from "@/components/ui/Button";
import { avatarColor, initials } from "@/lib/avatar";
import type { Comment, OpFrame } from "@/lib/types";

// Notion-style thread: quiet rows, no chat bubbles, no avatars-per-message competing with the
// text. Append-only by design (D98) — there is no edit affordance because there is no edit
// verb, and offering one would promise something the model cannot do.

type Row = Comment & { pending?: boolean };

function relativeTime(iso: string): string {
  const secs = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 60) return "just now";
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return new Date(iso).toLocaleDateString("en-GB", { day: "numeric", month: "short" });
}

export function CommentThread({ tripId, slotId }: { tripId: string; slotId: string }) {
  const { user } = useAuth();
  const { onOp, sendOp, resyncSignal } = useTripSocket();
  const { names } = useTripMembers(tripId);

  const [comments, setComments] = useState<Row[]>([]);
  const [draft, setDraft] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [deleting, setDeleting] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const { flashProps } = useLiveFlash((op: OpFrame) => {
    if (op.kind !== "comment.create.v1") return null;
    const f = op.payload.fields as { slot_id?: string };
    return f.slot_id === slotId ? op.entity_id : null;
  });

  useEffect(() => {
    let cancelled = false;
    listComments(tripId, slotId)
      .then((fresh) => !cancelled && setComments(fresh))
      .catch(() => !cancelled && setError("Couldn't load the discussion."));
    return () => {
      cancelled = true;
    };
  }, [tripId, slotId, resyncSignal]);

  useEffect(() => {
    return onOp((op: OpFrame) => {
      if (op.kind === "comment.create.v1") {
        const f = op.payload.fields as { slot_id?: string; body?: string };
        if (f.slot_id !== slotId) return;
        setComments((prev) => {
          // The client names the entity (D4), so our optimistic row and the server's echo
          // share an id — reconciling is "clear pending", never a swap.
          if (prev.some((c) => c.id === op.entity_id)) {
            return prev.map((c) => (c.id === op.entity_id ? { ...c, pending: false } : c));
          }
          return [
            ...prev,
            {
              id: op.entity_id,
              slot_id: f.slot_id ?? slotId,
              body: f.body ?? "",
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

    const id = crypto.randomUUID();
    setComments((prev) => [
      ...prev,
      {
        id,
        slot_id: slotId,
        body,
        author_id: user.id,
        created_at: new Date().toISOString(),
        pending: true,
      },
    ]);

    try {
      await sendOp({
        kind: "comment.create.v1",
        entityId: id,
        fields: ["slot_id", "body"],
        values: { slot_id: slotId, body },
      });
    } catch (err) {
      setComments((prev) => prev.filter((c) => c.id !== id));
      setDraft(body); // hand the text back rather than losing it
      setError(err instanceof Error ? err.message : "Couldn't post that comment.");
    } finally {
      setSubmitting(false);
    }
  };

  const remove = async (commentId: string) => {
    setError(null);
    setDeleting((prev) => new Set(prev).add(commentId));
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
      setError(err instanceof Error ? err.message : "Couldn't delete that comment.");
    } finally {
      setDeleting((prev) => {
        const next = new Set(prev);
        next.delete(commentId);
        return next;
      });
    }
  };

  const sorted = [...comments].sort((a, b) => a.created_at.localeCompare(b.created_at));

  return (
    <div data-testid="comments-list" className="rounded-card border border-line-subtle bg-surface-raised">
      {error && (
        <p role="alert" className="border-b border-line-subtle bg-critical-50 px-4 py-2.5 text-ui-sm text-critical-700">
          {error}
        </p>
      )}

      <ul className="divide-y divide-line-subtle">
        {sorted.map((c) => {
          const name = c.author_id ? (names[c.author_id] ?? c.author_id.slice(0, 8)) : "Unknown";
          const mine = Boolean(user && c.author_id === user.id);
          return (
            <li
              key={c.id}
              {...flashProps(c.id)}
              data-testid="comment"
              data-pending={c.pending ?? false}
              className={`group flex gap-3 px-4 py-3 ${c.pending ? "opacity-60" : ""}`}
            >
              <span
                aria-hidden
                className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-ui-2xs font-semibold text-fg-inverse"
                style={{ backgroundColor: c.author_id ? avatarColor(c.author_id) : "#a8a29a" }}
              >
                {initials(name)}
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex items-baseline gap-2">
                  <span className="text-ui-sm font-medium text-fg">{name}</span>
                  <span className="text-ui-2xs text-fg-subtle">
                    {c.pending ? "sending…" : relativeTime(c.created_at)}
                  </span>
                </div>
                <p className="mt-0.5 whitespace-pre-wrap break-words text-ui-md text-fg">
                  {c.body}
                </p>
              </div>
              {mine && !c.pending && (
                <button
                  type="button"
                  onClick={() => void remove(c.id)}
                  disabled={deleting.has(c.id)}
                  data-testid="delete-comment"
                  aria-label="Delete comment"
                  // Only the author can delete (D100) — the control does not exist for anyone
                  // else rather than existing and failing.
                  className="shrink-0 self-start rounded-sm px-1 text-ui-xs text-fg-subtle opacity-0 transition-opacity hover:text-critical-600 focus-visible:opacity-100 group-hover:opacity-100 disabled:opacity-50"
                >
                  Delete
                </button>
              )}
            </li>
          );
        })}

        {sorted.length === 0 && (
          <li className="px-4 py-8 text-center">
            <p className="text-ui-md text-fg-muted">No comments yet</p>
            <p className="mt-1 text-ui-sm text-fg-subtle">
              Say why one option beats another — it&rsquo;s easier than arguing in a group chat.
            </p>
          </li>
        )}
      </ul>

      <form onSubmit={(e) => void submit(e)} className="flex items-end gap-2 border-t border-line-subtle p-3">
        <textarea
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends, Shift+Enter breaks the line — the convention in every threaded
            // tool this sits next to.
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void submit(e);
            }
          }}
          rows={1}
          placeholder="Add a comment…"
          aria-label="Add a comment"
          className="max-h-32 min-h-9 flex-1 resize-y rounded-sm border border-line bg-surface px-3 py-2 text-ui-md text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none"
        />
        <Button type="submit" size="sm" disabled={submitting || !draft.trim()} data-testid="post-comment">
          Post
        </Button>
      </form>
    </div>
  );
}
