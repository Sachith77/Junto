// Seeds real state through the real Go API + real Mailpit — no mocks, matching the backend's
// own "real stack" testing bar. This only sets up data; the actual proof that the UI is wired
// to the sync engine happens in the browser (see voting.spec.ts).

const API_URL = "http://localhost:8080";
const MAILPIT_URL = "http://localhost:8025";

interface Envelope<T> {
  data: T;
}

// The auth endpoints carry a deliberately strict per-IP rate limit in every real run of the
// API (D35/D36 — burst 5, refilling at 1 per 10s). Every fixture in this file shares one
// source IP, so a real client here has to do what any well-behaved real client does: back off
// and retry rather than treat it as a hard failure. This is not a workaround for a test-only
// problem — it's correct behavior for anything calling this API from outside the Go test
// harness's own permissive-limiter wiring (tests/stack_test.go), which is unavailable to a
// browser-driven e2e run against the real `cmd/api` binary.
async function api<T>(path: string, opts: RequestInit & { token?: string } = {}): Promise<T> {
  const { token, headers, ...rest } = opts;
  for (let attempt = 0; attempt < 8; attempt++) {
    const res = await fetch(`${API_URL}${path}`, {
      ...rest,
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...headers,
      },
    });
    if (res.status === 429) {
      await new Promise((r) => setTimeout(r, 11_000));
      continue;
    }
    const text = await res.text();
    if (!res.ok) {
      throw new Error(`${opts.method ?? "GET"} ${path} -> ${res.status}: ${text}`);
    }
    if (!text) return undefined as T;
    const body = JSON.parse(text) as Envelope<T>;
    return body.data;
  }
  throw new Error(`${opts.method ?? "GET"} ${path} -> still rate-limited after retries`);
}

interface MailpitMessage {
  ID: string;
  To: { Address: string }[];
}

async function waitForToken(address: string, linkFragment: string): Promise<string> {
  const pattern = new RegExp(`${linkFragment}\\?token=([A-Za-z0-9_-]+)`);
  for (let attempt = 0; attempt < 40; attempt++) {
    const list = (await fetch(`${MAILPIT_URL}/api/v1/messages?limit=50`).then((r) =>
      r.json()
    )) as { messages: MailpitMessage[] };
    // Most recent match for this address — later entries are pushed to the front by Mailpit.
    const msg = list.messages.find((m) => m.To.some((t) => t.Address === address));
    if (msg) {
      const full = (await fetch(`${MAILPIT_URL}/api/v1/message/${msg.ID}`).then((r) =>
        r.json()
      )) as { Text: string };
      const match = full.Text.match(pattern);
      if (match) return match[1];
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`timed out waiting for an email to ${address} containing "${linkFragment}"`);
}

export interface FixtureUser {
  email: string;
  password: string;
  displayName: string;
  accessToken: string;
  userId: string;
}

export async function signupVerifiedUser(prefix: string): Promise<FixtureUser> {
  const unique = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const email = `${prefix}-${unique}@example.com`;
  const password = "correct horse battery staple";
  const displayName = `${prefix} ${unique.slice(0, 4)}`;

  await api("/api/v1/auth/signup", {
    method: "POST",
    body: JSON.stringify({ email, password, display_name: displayName }),
  });

  const token = await waitForToken(email, "verify-email");
  await api("/api/v1/auth/verify-email", { method: "POST", body: JSON.stringify({ token }) });

  const session = await api<{ access_token: string; user: { id: string } }>(
    "/api/v1/auth/login",
    { method: "POST", body: JSON.stringify({ email, password }) }
  );

  return { email, password, displayName, accessToken: session.access_token, userId: session.user.id };
}

export interface VotingFixture {
  tripId: string;
  slotId: string;
  optionAId: string;
  optionATitle: string;
  optionBId: string;
  optionBTitle: string;
}

/** A trip with owner + one accepted member, one slot, and two candidate options — the minimum
 * state VotingSlot needs to render something worth clicking on. */
export async function createVotingFixture(
  owner: FixtureUser,
  member: FixtureUser
): Promise<VotingFixture> {
  const trip = await api<{ id: string }>("/api/v1/trips", {
    method: "POST",
    token: owner.accessToken,
    body: JSON.stringify({
      name: `Playwright trip ${Date.now()}`,
      description: "",
      time_zone: "UTC",
      start_date: null,
      end_date: null,
      version: 0,
    }),
  });

  await api(`/api/v1/trips/${trip.id}/invitations`, {
    method: "POST",
    token: owner.accessToken,
    body: JSON.stringify({ email: member.email, role: "editor", max_uses: 1 }),
  });
  const inviteToken = await waitForToken(member.email, "invitations/accept");
  await api("/api/v1/invitations/accept", {
    method: "POST",
    token: member.accessToken,
    body: JSON.stringify({ token: inviteToken }),
  });

  const day = await api<{ id: string }>(`/api/v1/trips/${trip.id}/days`, {
    method: "POST",
    token: owner.accessToken,
    body: JSON.stringify({ date: null, label: "Day 1", after_day_id: null }),
  });

  const slot = await api<{ id: string }>(`/api/v1/trips/${trip.id}/slots`, {
    method: "POST",
    token: owner.accessToken,
    body: JSON.stringify({
      day_id: day.id,
      kind: "activity",
      title: "Pick a restaurant",
      notes: "",
      start_time: null,
      end_time: null,
      after_slot_id: null,
    }),
  });

  const emptyPlace = { name: "", address: "", lat: null, lng: null, provider_id: "" };
  const optionATitle = "Trattoria Uno";
  const optionBTitle = "Ramen House";
  const optionA = await api<{ id: string }>(`/api/v1/trips/${trip.id}/slots/${slot.id}/options`, {
    method: "POST",
    token: owner.accessToken,
    body: JSON.stringify({
      title: optionATitle,
      notes: "",
      external_url: "",
      estimated_cost_minor: 2500,
      place: emptyPlace,
    }),
  });
  const optionB = await api<{ id: string }>(`/api/v1/trips/${trip.id}/slots/${slot.id}/options`, {
    method: "POST",
    token: owner.accessToken,
    body: JSON.stringify({
      title: optionBTitle,
      notes: "",
      external_url: "",
      estimated_cost_minor: 1400,
      place: emptyPlace,
    }),
  });

  return {
    tripId: trip.id,
    slotId: slot.id,
    optionAId: optionA.id,
    optionATitle,
    optionBId: optionB.id,
    optionBTitle,
  };
}
