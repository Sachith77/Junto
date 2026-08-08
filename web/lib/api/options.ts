import { apiFetch } from "../http";
import type { SlotOption } from "../types";

export async function listOptions(tripId: string, slotId: string): Promise<SlotOption[]> {
  return apiFetch<SlotOption[]>(`/api/v1/trips/${tripId}/slots/${slotId}/options`);
}
