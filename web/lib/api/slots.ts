import { apiFetch } from "../http";
import type { Slot } from "../types";

export async function listSlots(tripId: string): Promise<Slot[]> {
  return apiFetch<Slot[]>(`/api/v1/trips/${tripId}/slots`);
}

export async function getSlot(tripId: string, slotId: string): Promise<Slot> {
  return apiFetch<Slot>(`/api/v1/trips/${tripId}/slots/${slotId}`);
}
