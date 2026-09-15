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
  coverSuggestionId?: string | null;
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
      cover_suggestion_id: input.coverSuggestionId || null,
    },
  });
}

export async function updateTrip(tripId: string, input: {
  name: string;
  description: string;
  timeZone: string;
  startDate?: string | null;
  endDate?: string | null;
  version: number;
  coverSuggestionId?: string | null;
}): Promise<Trip> {
  return apiFetch<Trip>(`/api/v1/trips/${tripId}`, {
    method: "PATCH",
    body: {
      name: input.name,
      description: input.description,
      time_zone: input.timeZone,
      start_date: input.startDate ? `${input.startDate}T00:00:00Z` : null,
      end_date: input.endDate ? `${input.endDate}T00:00:00Z` : null,
      version: input.version,
      cover_suggestion_id: input.coverSuggestionId || null,
    },
  });
}

export interface CoverSuggestion {
  id?: string;
  source: "neutral" | "suggested";
  url?: string;
  photographer_name?: string;
  photographer_url?: string;
  photo_url?: string;
}

export async function suggestTripCover(query: string, signal?: AbortSignal): Promise<CoverSuggestion> {
  return apiFetch<CoverSuggestion>(`/api/v1/cover-suggestions?query=${encodeURIComponent(query)}`, { signal });
}

interface CoverUploadTicket {
  id: string;
  status: "pending";
  upload_url: string;
  expires_at: string;
}

export async function uploadTripCover(tripId: string, file: File): Promise<Trip> {
  const ticket = await apiFetch<CoverUploadTicket>(`/api/v1/trips/${tripId}/cover/uploads`, {
    method: "POST",
    body: { content_type: file.type, original_name: file.name },
  });
  const upload = await fetch(ticket.upload_url, {
    method: "PUT",
    headers: { "Content-Type": file.type },
    body: file,
  });
  if (!upload.ok) throw new Error(`cover upload failed: ${upload.status}`);
  return apiFetch<Trip>(`/api/v1/trips/${tripId}/cover/uploads/${ticket.id}/confirm`, { method: "POST" });
}
