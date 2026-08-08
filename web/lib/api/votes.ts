import { apiFetch } from "../http";
import type { Vote } from "../types";

// Tallies are derived client-side from listVotes() + the current (non-deleted) option set —
// see VotingSlot. That reproduces the backend's D71 filtering for free (a vote for a
// soft-deleted option simply isn't counted, because the option isn't in the set) without a
// second round trip or a second source of truth to keep in sync with live vote.set.v1 ops.
export async function listVotes(tripId: string, slotId: string): Promise<Vote[]> {
  return apiFetch<Vote[]>(`/api/v1/trips/${tripId}/slots/${slotId}/votes`);
}
