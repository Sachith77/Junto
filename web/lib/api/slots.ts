import { apiFetch } from "../http";
import type { Slot } from "../types";

export async function listSlots(tripId: string): Promise<Slot[]> {
  return apiFetch<Slot[]>(`/api/v1/trips/${tripId}/slots`);
}

export async function getSlot(tripId: string, slotId: string): Promise<Slot> {
  return apiFetch<Slot>(`/api/v1/trips/${tripId}/slots/${slotId}`);
}

export type SlotKind = "place" | "activity" | "transport" | "lodging" | "note";

/** Creates a decision. day_id null puts it in the unscheduled backlog, which is a real
 *  destination rather than a fallback — "we want to do this somewhere on the trip" is how
 *  most planning starts.
 *
 *  `afterSlotId` is the anchor to insert AFTER, and callers adding to the end of a day must
 *  pass the last slot in it. `null` does NOT mean "append" — the repository reads it as
 *  "insert before the first slot" (NeighbourPositions returns prev="" when the anchor is
 *  nil), so a caller that always sends null builds the list backwards. This is the second
 *  time that has happened in this codebase: the seed script had the identical bug and it was
 *  fixed in e06fbf3. Pinned now by create-paths.spec.ts rather than by remembering. */
export async function createSlot(
  tripId: string,
  input: {
    dayId: string | null;
    kind: SlotKind;
    title: string;
    startTime?: string | null;
    notes?: string;
    /** The slot to insert after — normally the last one currently in this bucket. */
    afterSlotId?: string | null;
  }
): Promise<Slot> {
  return apiFetch<Slot>(`/api/v1/trips/${tripId}/slots`, {
    method: "POST",
    body: {
      day_id: input.dayId,
      kind: input.kind,
      title: input.title,
      notes: input.notes ?? "",
      // The backend takes a time-of-day, not an instant (D7/D16). An empty field is absent,
      // not midnight — the distinction the nullable column exists for (D18).
      start_time: input.startTime || null,
      end_time: null,
      after_slot_id: input.afterSlotId ?? null,
    },
  });
}

/** Records the group's resolution. Stored, never derived from the tally (D41) — a group
 *  routinely overrides its own vote, and a computed winner cannot represent that.
 *  optionId null un-resolves the slot. */
export async function selectOption(
  tripId: string,
  slotId: string,
  optionId: string | null
): Promise<void> {
  await apiFetch<void>(`/api/v1/trips/${tripId}/slots/${slotId}/select`, {
    method: "POST",
    // version null asks for merge semantics (D69) — resolving is a single-field decision and
    // should not fail because someone edited the slot's title meanwhile.
    body: { option_id: optionId, version: null },
  });
}
