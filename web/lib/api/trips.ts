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
}): Promise<Trip> {
  return apiFetch<Trip>("/api/v1/trips", {
    method: "POST",
    body: {
      name: input.name,
      description: input.description,
      time_zone: input.timeZone,
      start_date: null,
      end_date: null,
      version: 0,
    },
  });
}
