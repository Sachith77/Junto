"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { listOptions } from "@/lib/api/options";
import { listVotes } from "@/lib/api/votes";
import { useAuth } from "@/context/AuthContext";
import { useTripSocket } from "@/context/TripSocketContext";
import { useTripMembers } from "@/hooks/useTripMembers";
import { OptionCard } from "./OptionCard";
import type { OpFrame, Slot, SlotOption } from "@/lib/types";

const OPTION_OP_KINDS = new Set(["option.create.v1", "option.edit.v1", "option.delete.v1"]);

export function VotingSlot({ tripId, slot }: { tripId: string; slot: Slot }) {
  const { user } = useAuth();
  const { onOp, sendOp, resyncSignal } = useTripSocket();
  const names = useTripMembers(tripId);

  const [options, setOptions] = useState<SlotOption[]>([]);
  const [votes, setVotes] = useState<Map<string, string | null>>(new Map()); // userId -> optionId
  // Initialized once from the slot as first fetched; kept current afterwards only by the
  // slot.select_option.v1 branch below, never by re-syncing from the `slot` prop — see D41,
  // the group can override its own vote, and the live op is the source of truth for that.
  const [selectedOptionId, setSelectedOptionId] = useState(() => slot.selected_option_id);
  const [pendingOptionId, setPendingOptionId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const optionsRef = useRef<SlotOption[]>([]);

  const loadOptionsAndVotes = useCallback(async () => {
    const [freshOptions, freshVotes] = await Promise.all([
      listOptions(tripId, slot.id),
      listVotes(tripId, slot.id),
    ]);
    optionsRef.current = freshOptions;
    setOptions(freshOptions);
    setVotes(new Map(freshVotes.map((v) => [v.user_id, v.option_id ?? null])));
  }, [tripId, slot.id]);

  useEffect(() => {
    let cancelled = false;
    Promise.all([listOptions(tripId, slot.id), listVotes(tripId, slot.id)])
      .then(([freshOptions, freshVotes]) => {
        if (cancelled) return;
        optionsRef.current = freshOptions;
        setOptions(freshOptions);
        setVotes(new Map(freshVotes.map((v) => [v.user_id, v.option_id ?? null])));
      })
      .catch(() => {
        if (!cancelled) setError("failed to load options");
      });
    return () => {
      cancelled = true;
    };
  }, [tripId, slot.id, resyncSignal]);

  useEffect(() => {
    return onOp((op: OpFrame) => {
      if (op.kind === "vote.set.v1") {
        const fields = op.payload.fields as { slot_id: string; user_id: string; option_id: string | null };
        if (fields.slot_id !== slot.id) return;
        setVotes((prev) => {
          const next = new Map(prev);
          next.set(fields.user_id, fields.option_id ?? null);
          return next;
        });
        return;
      }
      if (op.kind === "slot.select_option.v1" && op.entity_id === slot.id) {
        const fields = op.payload.fields as { selected_option_id: string | null };
        setSelectedOptionId(fields.selected_option_id ?? undefined);
        return;
      }
      if (OPTION_OP_KINDS.has(op.kind)) {
        const fields = op.payload.fields as { slot_id?: string };
        const relevant =
          fields.slot_id === slot.id ||
          optionsRef.current.some((o) => o.id === op.entity_id);
        if (relevant) void loadOptionsAndVotes();
      }
    });
  }, [onOp, slot.id, loadOptionsAndVotes]);

  const castVote = async (optionId: string) => {
    if (!user) return;
    setError(null);
    setPendingOptionId(optionId);
    try {
      await sendOp({
        kind: "vote.set.v1",
        entityId: crypto.randomUUID(), // server ignores this for votes — identity is (slot_id, user_id)
        fields: ["slot_id", "user_id", "option_id"],
        values: { slot_id: slot.id, option_id: optionId },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to cast vote");
    } finally {
      setPendingOptionId(null);
    }
  };

  const retractVote = async () => {
    if (!user) return;
    setError(null);
    setPendingOptionId("__retract__");
    try {
      await sendOp({
        kind: "vote.set.v1",
        entityId: crypto.randomUUID(),
        fields: ["slot_id", "user_id", "option_id"],
        values: { slot_id: slot.id, option_id: null },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to retract vote");
    } finally {
      setPendingOptionId(null);
    }
  };

  const myVoteOptionId = user ? votes.get(user.id) ?? null : null;
  const knownOptionIds = new Set(options.map((o) => o.id));

  const tally = new Map<string, number>();
  for (const optionId of votes.values()) {
    if (optionId && knownOptionIds.has(optionId)) {
      tally.set(optionId, (tally.get(optionId) ?? 0) + 1);
    }
  }

  return (
    <div data-testid="voting-slot" className="space-y-3">
      {error && (
        <p role="alert" className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
          {error}
        </p>
      )}
      {options.length === 0 && (
        <p className="text-sm text-neutral-400">No candidate options for this slot yet.</p>
      )}
      {options.map((option) => (
        <OptionCard
          key={option.id}
          option={option}
          voteCount={tally.get(option.id) ?? 0}
          isSelected={option.id === selectedOptionId}
          hasMyVote={myVoteOptionId === option.id}
          proposerName={option.proposed_by ? names[option.proposed_by] : undefined}
          disabled={pendingOptionId !== null}
          onVote={() => void castVote(option.id)}
          onRetract={() => void retractVote()}
        />
      ))}
    </div>
  );
}
