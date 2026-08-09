import { apiFetch } from "../http";
import type { Member, Trip } from "../types";

export async function listMembers(tripId: string): Promise<Member[]> {
  return apiFetch<Member[]>(`/api/v1/trips/${tripId}/members`);
}

export async function createInvitation(
  tripId: string,
  input: { email?: string; role: "editor" | "viewer"; maxUses?: number }
): Promise<{ id: string }> {
  return apiFetch(`/api/v1/trips/${tripId}/invitations`, {
    method: "POST",
    body: { email: input.email ?? null, role: input.role, max_uses: input.maxUses ?? null },
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
