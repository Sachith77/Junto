"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { getSlot, selectOption } from "@/lib/api/slots";
import { listOptions } from "@/lib/api/options";
import { listVotes } from "@/lib/api/votes";
import { useAuth } from "@/context/AuthContext";
import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import { useLiveFlash } from "@/hooks/useLiveFlash";
import { Skeleton, LoadingRegion } from "@/components/ui/Skeleton";
import { OptionCard } from "./OptionCard";
import { CommentThread } from "./CommentThread";
import type { OpFrame, Slot, SlotOption, Vote } from "@/lib/types";

const OPTION_OPS = new Set(["option.create.v1", "option.edit.v1", "option.delete.v1"]);

export function SlotDetail({ tripId, slotId }: { tripId: string; slotId: string }) {
  const { user } = useAuth();
  const { onOp, sendOp, resyncSignal, status: socket } = useTripSocket();
  const { names, myRole } = useTripMembers(tripId);

  const [slot, setSlot] = useState<Slot | null>(null);
  const [options, setOptions] = useState<SlotOption[] | null>(null);
  const [votes, setVotes] = useState<Map<string, string | null>>(new Map());
  const [chosenId, setChosenId] = useState<string | undefined>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // A vote flashes the OPTION it was cast for, not the vote row — the vote itself is not on
  // screen, and "this candidate just gained a vote" is the thing a reader cares about.
  const { flashProps } = useLiveFlash((op: OpFrame) => {
    if (op.kind === "vote.set.v1") {
      const f = op.payload.fields as { slot_id?: string; option_id?: string | null };
      return f.slot_id === slotId ? (f.option_id ?? null) : null;
    }
    if (OPTION_OPS.has(op.kind)) return op.entity_id;
    return null;
  });

  // Fetch returns data; state is set in a promise callback rather than synchronously from
  // the effect body (see the same pattern in Itinerary).
  const fetchAll = useCallback(async () => {
    const [s, o, v] = await Promise.all([
      getSlot(tripId, slotId),
      listOptions(tripId, slotId),
      listVotes(tripId, slotId),
    ]);
    return { slot: s, options: o, votes: v };
  }, [tripId, slotId]);

  const apply = useCallback((d: { slot: Slot; options: SlotOption[]; votes: Vote[] }) => {
    setSlot(d.slot);
    setChosenId(d.slot.selected_option_id);
    setOptions(d.options);
    setVotes(new Map(d.votes.map((x) => [x.user_id, x.option_id ?? null])));
  }, []);

  useEffect(() => {
    let cancelled = false;
    fetchAll()
      .then((d) => {
        if (!cancelled) apply(d);
      })
      .catch(() => {
        if (!cancelled) setError("Couldn't load this slot.");
      });
    return () => {
      cancelled = true;
    };
  }, [fetchAll, apply, resyncSignal]);

  useEffect(() => {
    return onOp((op) => {
      if (op.kind === "vote.set.v1") {
        const f = op.payload.fields as { slot_id: string; user_id: string; option_id: string | null };
        if (f.slot_id !== slotId) return;
        setVotes((prev) => new Map(prev).set(f.user_id, f.option_id ?? null));
        return;
      }
      if (op.kind === "slot.select_option.v1" && op.entity_id === slotId) {
        const f = op.payload.fields as { selected_option_id: string | null };
        setChosenId(f.selected_option_id ?? undefined);
        return;
      }
      if (OPTION_OPS.has(op.kind)) void fetchAll().then(apply).catch(() => {});
    });
  }, [onOp, slotId, fetchAll, apply]);

  const myVote = user ? (votes.get(user.id) ?? null) : null;
  const known = new Set((options ?? []).map((o) => o.id));

  const tally = new Map<string, number>();
  for (const optionId of votes.values()) {
    // Votes for a soft-deleted option do not count (D71) — filtered here by "is it still in
    // the visible option set", which reproduces the server's rule without a second query.
    if (optionId && known.has(optionId)) tally.set(optionId, (tally.get(optionId) ?? 0) + 1);
  }
  const totalVotes = [...tally.values()].reduce((a, b) => a + b, 0);
  const topCount = Math.max(0, ...tally.values());

  const castVote = async (optionId: string | null) => {
    setError(null);
    setPending(true);
    try {
      await sendOp({
        kind: "vote.set.v1",
        entityId: crypto.randomUUID(), // ignored for votes — identity is (slot_id, user_id)
        fields: ["slot_id", "user_id", "option_id"],
        values: { slot_id: slotId, option_id: optionId },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Couldn't record that vote.");
    } finally {
      setPending(false);
    }
  };

  const resolve = async (optionId: string | null) => {
    setError(null);
    setPending(true);
    const previous = chosenId;
    setChosenId(optionId ?? undefined); // optimistic
    try {
      await selectOption(tripId, slotId, optionId);
    } catch (err) {
      setChosenId(previous);
      setError(err instanceof Error ? err.message : "Couldn't change the decision.");
    } finally {
      setPending(false);
    }
  };

  if (error && !slot) {
    return (
      <p role="alert" className="rounded-card border border-critical-600/25 bg-critical-50 px-4 py-3 text-ui-md text-critical-700">
        {error}
      </p>
    );
  }

  if (!slot || options === null) {
    return (
      <>
        <LoadingRegion label="Loading this decision" />
        <div className="space-y-4">
          <Skeleton className="h-8 w-64" />
          <Skeleton className="h-28 w-full rounded-card" />
          <Skeleton className="h-28 w-full rounded-card" />
        </div>
      </>
    );
  }

  const canResolve = myRole === "owner" || myRole === "editor";
  const canVote = canResolve; // CapVote is granted to owner and editor, not viewer

  return (
    <div className="space-y-8">
      <header>
        <Link
          href={`/trips/${tripId}/plan`}
          className="rounded-sm text-ui-sm text-fg-subtle transition-colors hover:text-fg"
        >
          ← Itinerary
        </Link>
        <div className="mt-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <h1 className="font-display text-display-lg text-fg">{slot.title}</h1>
          {(slot.start_time || slot.end_time) && (
            <span data-numeric className="text-ui-md text-fg-muted">
              {slot.start_time ?? ""}
              {slot.start_time && slot.end_time ? "–" : ""}
              {slot.end_time ?? ""}
            </span>
          )}
        </div>
        {slot.notes && <p className="mt-2 max-w-2xl text-ui-md text-fg-muted">{slot.notes}</p>}
      </header>

      {error && (
        <p role="alert" className="rounded-md border border-critical-600/25 bg-critical-50 px-4 py-2.5 text-ui-md text-critical-700">
          {error}
        </p>
      )}

      <section>
        <div className="mb-3 flex items-baseline justify-between gap-4">
          <h2 className="font-display text-display-sm text-fg">Options</h2>
          <span className="text-ui-xs text-fg-subtle" data-numeric>
            {totalVotes} {totalVotes === 1 ? "vote" : "votes"} cast
            {socket === "open" && " · live"}
          </span>
        </div>

        {options.length === 0 ? (
          <div className="rounded-card border border-dashed border-line bg-surface-raised px-6 py-12 text-center">
            <p className="text-ui-md font-medium text-fg">No options proposed yet</p>
            <p className="mx-auto mt-1 max-w-sm text-ui-sm text-fg-muted">
              Someone needs to suggest a candidate before the group has anything to vote on.
            </p>
          </div>
        ) : (
          <div className="space-y-3">
            {options.map((option) => (
              <OptionCard
                key={option.id}
                option={option}
                voteCount={tally.get(option.id) ?? 0}
                totalVotes={totalVotes}
                isChosen={option.id === chosenId}
                isMostVoted={topCount > 0 && (tally.get(option.id) ?? 0) === topCount}
                hasMyVote={myVote === option.id}
                proposerName={option.proposed_by ? names[option.proposed_by] : undefined}
                disabled={pending || !canVote}
                canResolve={canResolve}
                onVote={() => void castVote(option.id)}
                onRetract={() => void castVote(null)}
                onChoose={() => void resolve(option.id)}
                onClearChoice={() => void resolve(null)}
                flash={flashProps(option.id)}
              />
            ))}
          </div>
        )}

        {/* Spelling out the D41 distinction in words, once, where the decision is made. */}
        {options.length > 0 && (
          <p className="mt-3 text-ui-xs text-fg-subtle">
            The chosen option is the group&rsquo;s decision — it does not have to be the one
            with the most votes.
          </p>
        )}
      </section>

      <section>
        <h2 className="mb-3 font-display text-display-sm text-fg">Discussion</h2>
        <CommentThread tripId={tripId} slotId={slotId} />
      </section>
    </div>
  );
}
