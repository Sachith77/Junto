"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  createInvitation,
  listInvitations,
  revokeInvitation,
  type Invitation,
} from "@/lib/api/members";
import { useAuth } from "@/context/AuthContext";
import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import { ApiError } from "@/lib/http";
import { Button } from "@/components/ui/Button";
import { avatarColor, initials } from "@/lib/avatar";
import { Skeleton } from "@/components/ui/Skeleton";

const ROLE_BLURB: Record<string, string> = {
  owner: "Full control, including deleting the trip",
  editor: "Can plan, vote, comment and manage the budget",
  viewer: "Read-only — cannot vote or comment",
};

export function MembersPanel({ tripId }: { tripId: string }) {
  const { user } = useAuth();
  const { presence } = useTripSocket();
  const { members, myRole, loading } = useTripMembers(tripId);

  const [invitations, setInvitations] = useState<Invitation[] | null>(null);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<"editor" | "viewer">("editor");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  // The freshly minted link, held in component state because there is nowhere else it could
  // come from: the server stores only a hash, so this URL exists in this browser tab or not
  // at all. Navigating away loses it, which is why the panel says so.
  const [link, setLink] = useState<{ url: string; role: string } | null>(null);
  const [copied, setCopied] = useState(false);
  const linkRef = useRef<HTMLInputElement>(null);

  // Inviting is granted to editors, but managing members and revoking is owner-only (D57).
  const canInvite = myRole === "owner" || myRole === "editor";
  const canManage = myRole === "owner";

  const online = new Set(presence.map((p) => p.user_id));

  const loadInvites = useCallback(
    () =>
      listInvitations(tripId)
        .then(setInvitations)
        .catch(() => setInvitations([])),
    [tripId]
  );

  useEffect(() => {
    if (!canInvite) return;
    let cancelled = false;
    listInvitations(tripId)
      .then((rows) => {
        if (!cancelled) setInvitations(rows);
      })
      .catch(() => {
        if (!cancelled) setInvitations([]);
      });
    return () => {
      cancelled = true;
    };
  }, [tripId, canInvite]);

  const invite = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!email.trim()) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await createInvitation(tripId, { email: email.trim(), role, maxUses: 1 });
      setNotice(`Invitation sent to ${email.trim()}.`);
      setEmail("");
      await loadInvites();
    } catch (err) {
      setError(
        err instanceof ApiError ? (err.violations[0]?.message ?? err.message) : "Couldn't send that invitation."
      );
    } finally {
      setBusy(false);
    }
  };

  // A link invite carries no address, so nothing is emailed and the token comes back on the
  // response instead. Unlimited uses by default — the point of a link is a group chat.
  const createLink = async () => {
    setBusy(true);
    setError(null);
    setNotice(null);
    setCopied(false);
    try {
      const inv = await createInvitation(tripId, { role, maxUses: null });
      if (!inv.accept_url) {
        // The invitation itself was created — it is in the pending list below — but without
        // the token nobody can ever redeem it. An API predating `accept_url` is the only way
        // to reach this, so the message names that rather than blaming the invite: the first
        // person to hit it lost time to "the server didn't return a link", which is true and
        // useless.
        setError(
          "That invite was created but came back without a link, so nothing can redeem it — " +
            "the API is likely running an older build. Restart it, then revoke the empty " +
            "invite below and create another."
        );
        await loadInvites();
        return;
      }
      setLink({ url: inv.accept_url, role });
      await loadInvites();
    } catch (err) {
      setError(
        err instanceof ApiError ? (err.violations[0]?.message ?? err.message) : "Couldn't create a link."
      );
    } finally {
      setBusy(false);
    }
  };

  const copyLink = async () => {
    if (!link) return;
    try {
      await navigator.clipboard.writeText(link.url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2500);
    } catch {
      // navigator.clipboard is undefined outside a secure context, which is the ordinary
      // case for a demo served over plain http on a LAN address. Select the text so the
      // keyboard still works rather than failing with nothing the user can do about it.
      linkRef.current?.select();
      setError("Couldn't copy automatically — the link is selected, press Ctrl+C.");
    }
  };

  const revoke = async (id: string) => {
    setBusy(true);
    setError(null);
    try {
      await revokeInvitation(tripId, id);
      await loadInvites();
    } catch {
      setError("Couldn't revoke that invitation.");
    } finally {
      setBusy(false);
    }
  };

  // "Pending" means genuinely still usable: not revoked, not expired, and not already spent.
  // Omitting the use_count check listed invitations that had already been accepted, which
  // made the panel claim people were still waiting when they were sitting in the list above.
  const pending = (invitations ?? []).filter(
    (i) =>
      !i.revoked_at &&
      new Date(i.expires_at) > new Date() &&
      (i.max_uses == null || i.use_count < i.max_uses)
  );

  return (
    <div className="space-y-10">
      <section>
        <h1 className="font-display text-display-lg text-fg">Members</h1>
        <p className="mt-1 text-ui-md text-fg-muted">
          Who is on this trip, and what they can do.
        </p>

        <ul className="mt-5 divide-y divide-line-subtle rounded-card border border-line-subtle bg-surface-raised">
          {loading &&
            [0, 1].map((i) => (
              <li key={i} className="flex items-center gap-3 px-4 py-3">
                <Skeleton className="h-9 w-9 rounded-full" />
                <div className="flex-1 space-y-1.5">
                  <Skeleton className="h-4 w-40" />
                  <Skeleton className="h-3 w-56" />
                </div>
              </li>
            ))}

          {members.map((m) => {
            const name = m.display_name || m.user_id.slice(0, 8);
            const isOnline = online.has(m.user_id);
            return (
              <li key={m.user_id} className="flex items-center gap-3 px-4 py-3">
                <span className="relative shrink-0">
                  <span
                    aria-hidden
                    className="flex h-9 w-9 items-center justify-center rounded-full text-ui-xs font-semibold text-fg-inverse"
                    style={{ backgroundColor: avatarColor(m.user_id) }}
                  >
                    {initials(name)}
                  </span>
                  {/* The same live language as the presence bar: a ring means "here now",
                      distinct from "is a member". */}
                  {isOnline && (
                    <span
                      aria-hidden
                      className="absolute -bottom-0.5 -right-0.5 h-3 w-3 rounded-full border-2 border-surface-raised bg-live"
                    />
                  )}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-ui-md font-medium text-fg">{name}</span>
                    {user?.id === m.user_id && (
                      <span className="shrink-0 text-ui-2xs text-fg-subtle">you</span>
                    )}
                    {isOnline && (
                      <span className="shrink-0 text-ui-2xs font-medium text-accent-text">
                        online
                      </span>
                    )}
                  </div>
                  <p className="truncate text-ui-xs text-fg-subtle">{ROLE_BLURB[m.role]}</p>
                </div>
                <span
                  className={`shrink-0 rounded-xs px-2 py-0.5 text-ui-2xs font-semibold uppercase tracking-[0.08em] ${
                    m.role === "owner"
                      ? "bg-accent text-fg-inverse"
                      : "border border-line text-fg-muted"
                  }`}
                >
                  {m.role}
                </span>
              </li>
            );
          })}
        </ul>

        {!canManage && !loading && (
          <p className="mt-2 text-ui-xs text-fg-subtle">
            Only the owner can change roles or remove members.
          </p>
        )}
      </section>

      {canInvite && (
        <section>
          <h2 className="font-display text-display-sm text-fg">Invite someone</h2>
          {/* One line. The expiry/revocation detail was permanent body text explaining a
              mechanism nobody needs to know until they use it — and both facts are already
              visible on each row in Pending invitations below ("expires 18 Aug", "Revoke").
              Body copy that restates what the interface shows is copy that gets skipped, and
              it was the longest paragraph on the screen. */}
          <p className="mt-1 flex flex-wrap items-center gap-x-1.5 text-ui-sm text-fg-muted">
            <span>Invite by email, or create a link you can paste anywhere.</span>
            <span
              tabIndex={0}
              role="note"
              aria-label="Invitations expire, and can be revoked before they are used."
              title="Invitations expire, and can be revoked before they are used."
              className="cursor-help rounded-sm border-b border-dotted border-line-strong text-fg-subtle focus-visible:outline-2 focus-visible:outline-offset-2"
            >
              How long do they last?
            </span>
          </p>

          {error && (
            <p role="alert" className="mt-3 rounded-md border border-critical-600/25 bg-critical-50 px-3 py-2 text-ui-sm text-critical-700">
              {error}
            </p>
          )}
          {notice && (
            <p className="mt-3 rounded-md border border-positive-600/25 bg-positive-50 px-3 py-2 text-ui-sm text-positive-700">
              {notice}
            </p>
          )}

          <form onSubmit={(e) => void invite(e)} className="mt-3 flex flex-wrap items-center gap-2">
            <input
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="name@example.com"
              aria-label="Email to invite"
              className="min-w-56 flex-1 rounded-sm border border-line bg-surface-raised px-3 py-2 text-ui-md text-fg placeholder:text-fg-subtle focus:border-accent focus:outline-none"
            />
            <select
              value={role}
              onChange={(e) => setRole(e.target.value as "editor" | "viewer")}
              aria-label="Role"
              className="rounded-sm border border-line bg-surface-raised px-3 py-2 text-ui-md text-fg focus:border-accent focus:outline-none"
            >
              <option value="editor">Editor</option>
              <option value="viewer">Viewer</option>
            </select>
            <Button type="submit" disabled={busy || !email.trim()}>
              Send invite
            </Button>
          </form>

          <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <Button
              type="button"
              variant="secondary"
              onClick={() => void createLink()}
              disabled={busy}
              data-testid="create-invite-link"
            >
              Or create a shareable link
            </Button>
            <p className="text-ui-xs text-fg-subtle">
              No address needed — anyone with the link joins as{" "}
              <span className="font-medium text-fg-muted">{role}</span>.
            </p>
          </div>

          {link && (
            <div
              data-testid="invite-link-panel"
              className="mt-3 rounded-card border border-accent/30 bg-surface-raised p-4"
            >
              <p className="text-ui-sm font-medium text-fg">
                Shareable link · joins as {link.role}
              </p>
              <p className="mt-0.5 text-ui-xs text-fg-subtle">
                Copy it now. Only a hash is stored, so this is the one and only time it can be
                shown — leave the page and you&rsquo;ll need to create another.
              </p>
              <div className="mt-3 flex flex-wrap items-center gap-2">
                <input
                  ref={linkRef}
                  readOnly
                  value={link.url}
                  aria-label="Invitation link"
                  data-testid="invite-link-value"
                  onFocus={(e) => e.target.select()}
                  className="min-w-56 flex-1 rounded-sm border border-line bg-surface-sunken px-3 py-2 text-ui-sm text-fg-muted focus:border-accent focus:outline-none"
                />
                <Button type="button" onClick={() => void copyLink()} data-testid="copy-invite-link">
                  {copied ? "Copied" : "Copy link"}
                </Button>
                <button
                  type="button"
                  onClick={() => {
                    setLink(null);
                    setCopied(false);
                  }}
                  className="rounded-sm text-ui-xs text-fg-subtle underline underline-offset-2 transition-colors hover:text-fg"
                >
                  Done
                </button>
              </div>
              {/* Politeness rather than an alert: confirming a copy must not interrupt a
                  screen reader mid-sentence, but silence would leave the button's label
                  change unannounced. */}
              <span role="status" aria-live="polite" className="sr-only">
                {copied ? "Invitation link copied to the clipboard" : ""}
              </span>
            </div>
          )}

          <div className="mt-5">
            <h3 className="text-ui-sm font-medium text-fg-muted">Pending invitations</h3>
            {pending.length === 0 ? (
              <p className="mt-2 text-ui-sm text-fg-subtle">None outstanding.</p>
            ) : (
              <ul className="mt-2 divide-y divide-line-subtle rounded-card border border-line-subtle bg-surface-raised">
                {pending.map((inv) => (
                  <li key={inv.id} className="flex items-center gap-3 px-4 py-2.5">
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-ui-md text-fg">{inv.email ?? "Shareable link"}</p>
                      <p className="text-ui-2xs text-fg-subtle">
                        {inv.role} · expires{" "}
                        {new Date(inv.expires_at).toLocaleDateString("en-GB", {
                          day: "numeric",
                          month: "short",
                        })}
                      </p>
                    </div>
                    {canManage && (
                      <button
                        type="button"
                        onClick={() => void revoke(inv.id)}
                        disabled={busy}
                        className="shrink-0 rounded-sm text-ui-xs text-fg-subtle underline underline-offset-2 transition-colors hover:text-critical-600 disabled:opacity-50"
                      >
                        Revoke
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
