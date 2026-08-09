import { apiFetch } from "../http";
import type { Slot } from "../types";

export async function listSlots(tripId: string): Promise<Slot[]> {
  return apiFetch<Slot[]>(`/api/v1/trips/${tripId}/slots`);
}

export async function getSlot(tripId: string, slotId: string): Promise<Slot> {
  return apiFetch<Slot>(`/api/v1/trips/${tripId}/slots/${slotId}`);
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
