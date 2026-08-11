import { apiFetch } from "../http";
import type { SlotOption } from "../types";

export async function listOptions(tripId: string, slotId: string): Promise<SlotOption[]> {
  return apiFetch<SlotOption[]>(`/api/v1/trips/${tripId}/slots/${slotId}/options`);
}

/** Proposes a candidate under a slot. Place fields are discrete columns, not a blob (D6),
 *  so they are sent as a nested object of real fields even when only the name is known. */
export async function createOption(
  tripId: string,
  slotId: string,
  input: { title: string; notes?: string; externalUrl?: string; placeName?: string }
): Promise<SlotOption> {
  return apiFetch<SlotOption>(`/api/v1/trips/${tripId}/slots/${slotId}/options`, {
    method: "POST",
    body: {
      title: input.title,
      notes: input.notes ?? "",
      external_url: input.externalUrl ?? "",
      estimated_cost_minor: null,
      place: {
        name: input.placeName ?? "",
        address: "",
        // Null rather than 0: (0,0) is a real location in the Gulf of Guinea, which is why
        // these columns are nullable in the first place (D18).
        lat: null,
        lng: null,
        provider_id: "",
      },
    },
  });
}
