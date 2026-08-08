// Holds the access token in memory only (D30: never in localStorage/cookie — an XSS bug must
// not be able to read it). Lost on page reload by design; callers re-establish it via
// POST /auth/refresh, which rides the HttpOnly refresh cookie.
type Listener = () => void;

let token: string | null = null;
const listeners = new Set<Listener>();

export function getAccessToken(): string | null {
  return token;
}

export function setAccessToken(next: string | null): void {
  token = next;
  for (const l of listeners) l();
}

export function subscribeToken(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
