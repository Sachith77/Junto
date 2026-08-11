import { API_URL } from "./config";
import { getAccessToken, setAccessToken } from "./token-store";

export interface ProblemDetail {
  type: string;
  title: string;
  status: number;
  detail: string;
  instance?: string;
  errors?: { field: string; code: string; message: string }[];
}

export class ApiError extends Error {
  status: number;
  code: string;
  violations: { field: string; code: string; message: string }[];

  constructor(problem: ProblemDetail) {
    super(problem.detail || problem.title);
    this.name = "ApiError";
    this.status = problem.status;
    this.code = problem.type.replace(/^\/problems\//, "");
    this.violations = problem.errors ?? [];
  }
}

interface Envelope<T> {
  data: T;
  meta?: { next_cursor?: string; has_more?: boolean };
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  auth?: boolean;
  credentials?: RequestCredentials;
  signal?: AbortSignal;
}

// Refresh rides the HttpOnly cookie (D30) — the body is empty, the cookie does the work.
let refreshInFlight: Promise<boolean> | null = null;

const REFRESH_ATTEMPTS = 3;
const MAX_BACKOFF_MS = 12_000;

async function refreshAccessToken(): Promise<boolean> {
  if (!refreshInFlight) {
    refreshInFlight = (async () => {
      try {
        for (let attempt = 0; attempt < REFRESH_ATTEMPTS; attempt++) {
          const res = await fetch(`${API_URL}/api/v1/auth/refresh`, {
            method: "POST",
            credentials: "include",
          });

          if (res.ok) {
            const body = (await res.json()) as Envelope<{ access_token: string }>;
            setAccessToken(body.data.access_token);
            return true;
          }

          // Only 401/403 means "you are genuinely not signed in". Everything else
          // — a 429 from the auth limiter, a 5xx, an API restart — is TRANSIENT,
          // and treating it as a logout signs a user out for being unlucky.
          //
          // This was a real bug, not a hypothetical: the access token is
          // memory-only by design (D30), so every hard reload needs a refresh;
          // reloading while the strict auth limiter (D35/D36) was throttling
          // bounced a perfectly valid session to /login.
          if (res.status === 401 || res.status === 403) {
            setAccessToken(null);
            return false;
          }
          if (res.status !== 429 && res.status < 500) return false;

          const retryAfter = Number(res.headers.get("retry-after"));
          const waitMs = Math.min(
            Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter * 1000 : 800 * 2 ** attempt,
            MAX_BACKOFF_MS
          );
          await new Promise((r) => setTimeout(r, waitMs));
        }
        return false;
      } catch {
        // Network error — the server may simply be down. Keep whatever token is
        // held rather than destroying a session over a dropped connection.
        return false;
      } finally {
        refreshInFlight = null;
      }
    })();
  }
  return refreshInFlight;
}

async function parseResponse<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;

  const contentType = res.headers.get("content-type") ?? "";
  if (!contentType.includes("json")) {
    if (!res.ok) throw new Error(`request failed: ${res.status}`);
    return undefined as T;
  }

  const body = await res.json();
  if (!res.ok) {
    throw new ApiError(body as ProblemDetail);
  }
  return (body as Envelope<T>).data;
}

/** Envelope-aware, auth-aware fetch. Retries once on 401 after a silent token refresh. */
export async function apiFetch<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, auth = true, credentials, signal } = opts;

  const doFetch = async (): Promise<Response> => {
    const headers: Record<string, string> = {};
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (auth) {
      const token = getAccessToken();
      if (token) headers["Authorization"] = `Bearer ${token}`;
    }
    return fetch(`${API_URL}${path}`, {
      method,
      headers,
      credentials: credentials ?? "include",
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal,
    });
  };

  let res = await doFetch();
  if (res.status === 401 && auth && getAccessToken() !== null) {
    const refreshed = await refreshAccessToken();
    if (refreshed) res = await doFetch();
  }
  return parseResponse<T>(res);
}

/** A non-envelope request that failed, carrying the status so callers can tell an expired
 * credential (retryable after a refresh, or terminal if the refresh also fails) from a
 * server fault. Without the status the only thing a caller can do is retry blindly. */
export class RawHttpError extends Error {
  readonly status: number;
  constructor(status: number, body: string) {
    super(`request failed: ${status} ${body}`);
    this.name = "RawHttpError";
    this.status = status;
  }
}

/** Returns the parsed JSON payload with no envelope unwrapping, for the one route that
 * deliberately doesn't use it (the WS ticket endpoint — see ws/ticket.go).
 *
 * Retries once on 401 after a silent refresh, exactly like apiFetch. That parity is not
 * cosmetic: the access token is held in memory and expires on a timer (D30), so WITHOUT the
 * retry the ticket call is the one request in the app that cannot survive its own token
 * expiring — and since the socket's reconnect loop is what calls it, a single expiry turned
 * into an unbounded 401 retry storm rather than a refresh. */
export async function apiFetchRaw<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, auth = true } = opts;

  const doFetch = async (): Promise<Response> => {
    const headers: Record<string, string> = {};
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (auth) {
      const token = getAccessToken();
      if (token) headers["Authorization"] = `Bearer ${token}`;
    }
    return fetch(`${API_URL}${path}`, {
      method,
      headers,
      credentials: "include",
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  };

  let res = await doFetch();
  if (res.status === 401 && auth && getAccessToken() !== null) {
    const refreshed = await refreshAccessToken();
    if (refreshed) res = await doFetch();
  }
  if (!res.ok) {
    throw new RawHttpError(res.status, await res.text());
  }
  return res.json() as Promise<T>;
}

export { refreshAccessToken };
