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

async function refreshAccessToken(): Promise<boolean> {
  if (!refreshInFlight) {
    refreshInFlight = (async () => {
      try {
        const res = await fetch(`${API_URL}/api/v1/auth/refresh`, {
          method: "POST",
          credentials: "include",
        });
        if (!res.ok) {
          setAccessToken(null);
          return false;
        }
        const body = (await res.json()) as Envelope<{ access_token: string }>;
        setAccessToken(body.data.access_token);
        return true;
      } catch {
        setAccessToken(null);
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

/** Returns the parsed JSON payload with no envelope unwrapping, for the one route that
 * deliberately doesn't use it (the WS ticket endpoint — see ws/ticket.go). */
export async function apiFetchRaw<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, auth = true } = opts;
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (auth) {
    const token = getAccessToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
  }
  const res = await fetch(`${API_URL}${path}`, {
    method,
    headers,
    credentials: "include",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`request failed: ${res.status} ${text}`);
  }
  return res.json() as Promise<T>;
}

export { refreshAccessToken };
