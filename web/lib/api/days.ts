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

export async function createDay(
  tripId: string,
  input: { label: string; date?: string | null }
): Promise<Day> {
  return apiFetch<Day>(`/api/v1/trips/${tripId}/days`, {
    method: "POST",
    body: {
      label: input.label,
      date: input.date ? `${input.date}T00:00:00Z` : null,
      after_day_id: null,
    },
  });
}
