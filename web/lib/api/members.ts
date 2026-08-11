import { apiFetch } from "../http";
import type { Member, Trip } from "../types";

export async function listMembers(tripId: string): Promise<Member[]> {
  return apiFetch<Member[]>(`/api/v1/trips/${tripId}/members`);
}

export interface CreatedInvitation extends Invitation {
  /** Present for LINK invites only (no email), and only on this response — the server stores
   *  a hash, so nothing can produce this URL again. Show it now or the link is lost. */
  accept_url?: string;
}

export async function createInvitation(
  tripId: string,
  input: { email?: string; role: "editor" | "viewer"; maxUses?: number | null }
): Promise<CreatedInvitation> {
  return apiFetch(`/api/v1/trips/${tripId}/invitations`, {
    method: "POST",
    body: {
      email: input.email ?? null,
      role: input.role,
      // null means unlimited. Distinct from undefined, which would let the server apply its
      // own default (1 for an addressed invite) — so it is passed through deliberately.
      max_uses: input.maxUses ?? null,
    },
  });
}

export async function acceptInvitation(token: string): Promise<Trip> {
  return apiFetch<Trip>("/api/v1/invitations/accept", { method: "POST", body: { token } });
}

export interface Invitation {
  id: string;
  email?: string;
  role: "editor" | "viewer";
  max_uses?: number;
  use_count: number;
  expires_at: string;
  revoked_at?: string;
  created_at: string;
}

export async function listInvitations(tripId: string): Promise<Invitation[]> {
  return apiFetch<Invitation[]>(`/api/v1/trips/${tripId}/invitations`);
}

export async function revokeInvitation(tripId: string, invitationId: string): Promise<void> {
  await apiFetch<void>(`/api/v1/trips/${tripId}/invitations/${invitationId}`, {
    method: "DELETE",
  });
}
