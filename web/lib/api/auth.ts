import { apiFetch } from "../http";
import { setAccessToken } from "../token-store";
import type { Session, User } from "../types";

export async function signup(email: string, password: string, displayName: string): Promise<User> {
  return apiFetch<User>("/api/v1/auth/signup", {
    method: "POST",
    auth: false,
    body: { email, password, display_name: displayName },
  });
}

export async function login(email: string, password: string): Promise<Session> {
  const session = await apiFetch<Session>("/api/v1/auth/login", {
    method: "POST",
    auth: false,
    body: { email, password },
  });
  setAccessToken(session.access_token);
  return session;
}

export async function logout(): Promise<void> {
  await apiFetch<void>("/api/v1/auth/logout", { method: "POST", auth: false });
  setAccessToken(null);
}

export async function verifyEmail(token: string): Promise<void> {
  await apiFetch<void>("/api/v1/auth/verify-email", { method: "POST", auth: false, body: { token } });
}

export async function me(): Promise<User> {
  return apiFetch<User>("/api/v1/me");
}

/** Always succeeds from the caller's point of view, whether or not the address exists —
 *  reporting "no such account" would turn this into a free account-enumeration oracle. The UI
 *  must therefore show the same confirmation either way. */
export async function requestPasswordReset(email: string): Promise<void> {
  await apiFetch<{ message: string }>("/api/v1/auth/request-password-reset", {
    method: "POST",
    auth: false,
    body: { email },
  });
}

export async function resetPassword(token: string, password: string): Promise<void> {
  await apiFetch<void>("/api/v1/auth/reset-password", {
    method: "POST",
    auth: false,
    body: { token, password },
  });
}
