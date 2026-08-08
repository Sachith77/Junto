import { apiFetch } from "../http";
import type { Trip } from "../types";

export async function listTrips(): Promise<Trip[]> {
  return apiFetch<Trip[]>("/api/v1/trips");
}

export async function getTrip(tripId: string): Promise<Trip> {
  return apiFetch<Trip>(`/api/v1/trips/${tripId}`);
}

export async function createTrip(input: {
  name: string;
  description: string;
  timeZone: string;
  startDate?: string | null;
  endDate?: string | null;
}): Promise<Trip> {
  return apiFetch<Trip>("/api/v1/trips", {
    method: "POST",
    body: {
      name: input.name,
      description: input.description,
      time_zone: input.timeZone,
      // The API takes RFC3339; a date input gives "2026-09-14". Sent as UTC
      // midnight because these are calendar dates, not instants (D7's reasoning
      // one level up) — formatDateRange reads the date part back the same way.
      start_date: input.startDate ? `${input.startDate}T00:00:00Z` : null,
      end_date: input.endDate ? `${input.endDate}T00:00:00Z` : null,
      version: 0,
    },
  });
}
