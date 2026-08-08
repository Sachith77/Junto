"use client";

import { useEffect, useState } from "react";
import { listSlots } from "@/lib/api/slots";
import { useTripSocket } from "@/context/TripSocketContext";
import { CommentsList } from "./CommentsList";
import { VotingSlot } from "./VotingSlot";
import type { OpFrame, Slot } from "@/lib/types";

const SLOT_OP_KINDS = new Set(["slot.create.v1", "slot.edit.v1", "slot.move.v1", "slot.delete.v1"]);

export function SlotList({ tripId }: { tripId: string }) {
  const { onOp, resyncSignal } = useTripSocket();
  const [slots, setSlots] = useState<Slot[]>([]);

  useEffect(() => {
    void listSlots(tripId).then(setSlots);
  }, [tripId, resyncSignal]);

  useEffect(() => {
    return onOp((op: OpFrame) => {
      if (SLOT_OP_KINDS.has(op.kind)) void listSlots(tripId).then(setSlots);
    });
  }, [onOp, tripId]);

  if (slots.length === 0) {
    return (
      <p className="text-neutral-400">
        No slots on this trip yet. Add days and slots via the API to try voting.
      </p>
    );
  }

  return (
    <div className="space-y-6">
      {slots.map((slot) => (
        <section key={slot.id} data-testid="slot" className="space-y-3">
          <div>
            <h2 className="text-lg font-medium">{slot.title}</h2>
            {(slot.start_time || slot.end_time) && (
              <p className="text-sm text-neutral-500">
                {slot.start_time ?? ""}
                {slot.start_time && slot.end_time ? " – " : ""}
                {slot.end_time ?? ""}
              </p>
            )}
          </div>
          <VotingSlot tripId={tripId} slot={slot} />
          <div className="border-t border-neutral-100 pt-3">
            <h3 className="mb-2 text-sm font-medium text-neutral-500">Discussion</h3>
            <CommentsList tripId={tripId} slotId={slot.id} />
          </div>
        </section>
      ))}
    </div>
  );
}
