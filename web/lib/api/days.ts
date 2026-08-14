import { apiFetch } from "../http";

export interface Day {
  id: string;
  date: string | null;
  label: string;
  position: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export async function listDays(tripId: string): Promise<Day[]> {
  return apiFetch<Day[]>(`/api/v1/trips/${tripId}/days`);
}

/** `afterDayId` is the anchor to insert AFTER; pass the last existing day to append.
 *  `null` inserts at the START, not the end — see the note on createSlot. */
export async function createDay(
  tripId: string,
  input: { label: string; date?: string | null; afterDayId?: string | null }
): Promise<Day> {
  return apiFetch<Day>(`/api/v1/trips/${tripId}/days`, {
    method: "POST",
    body: {
      label: input.label,
      date: input.date ? `${input.date}T00:00:00Z` : null,
      after_day_id: input.afterDayId ?? null,
    },
  });
}
