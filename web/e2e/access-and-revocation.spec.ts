// Regression cover for two bugs found by direct testing on 2026-08-09, neither of which had
// any test on it. Both are browser-observable behaviours of the real stack, so they live here
// rather than as component tests: what broke in each case was the interaction between the API's
// response and what the UI did with it, which a mocked component test cannot see.
import { test, expect, type Page } from "@playwright/test";
import { signupVerifiedUser, createVotingFixture, type FixtureUser } from "./helpers/fixtures";

/** POST/GET against the real API, backing off on the shared per-IP auth limiter (D35/D36). */
async function api<T>(path: string, token: string | null, body?: unknown, method = "POST"): Promise<T> {
  for (let attempt = 0; attempt < 8; attempt++) {
    const res = await fetch(`http://localhost:8080${path}`, {
      method,
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 429) {
      await new Promise((r) => setTimeout(r, 11_000));
      continue;
    }
    const text = await res.text();
    if (!res.ok) throw new Error(`${method} ${path} -> ${res.status}: ${text}`);
    return (text ? JSON.parse(text).data : undefined) as T;
  }
  throw new Error(`${path}: still rate-limited`);
}

/** Navigates and waits for real content, reloading if it doesn't arrive.
 *
 *  A hard navigation restores the in-memory access token via /auth/refresh (D30), and refresh
 *  sits inside the STRICT auth limiter — so a suite that has just made several auth calls can
 *  have its first page load 429 and render nothing. Reloading is what a person does, and the
 *  bucket refills while we wait. */
async function gotoUntilVisible(page: Page, url: string, expected: string) {
  for (let attempt = 0; attempt < 5; attempt++) {
    await page.goto(url);
    try {
      await expect(page.getByText(expected)).toBeVisible({ timeout: 12_000 });
      return;
    } catch {
      await page.waitForTimeout(11_000);
    }
  }
  throw new Error(`"${expected}" never appeared at ${url}`);
}

// Shares one per-IP bucket with every other auth call in the suite (D35/D36), so retry.
async function loginViaUi(page: Page, user: { email: string; password: string }) {
  for (let attempt = 0; attempt < 6; attempt++) {
    await page.goto("/login");
    await page.getByLabel("Email").fill(user.email);
    await page.getByLabel("Password").fill(user.password);
    await page.getByRole("button", { name: "Log in" }).click();
    try {
      await page.waitForURL(/\/trips/, { timeout: 12_000 });
      return;
    } catch {
      await page.waitForTimeout(12_000);
    }
  }
  throw new Error(`could not log in as ${user.email}`);
}

/** A non-member must be TOLD they have no access, not shown an empty trip.
 *
 *  The backend answers every trip-scoped route with 404 for a non-member by design (D53).
 *  Each screen used to swallow that and fall back to its own empty state, so Plan mode
 *  rendered a trip with no days under a title stuck at "…" — visually identical to a trip
 *  nobody has filled in yet, and reported as "Plan mode doesn't load".
 *
 *  Verified against a planted break: restoring TripShell's unconditional render (dropping the
 *  access gate) fails this test on the heading assertion, and the page shows "Nothing planned
 *  yet" — the exact symptom that was reported. */
test("a non-member is told the trip is unavailable, not shown an empty one", async ({ page }) => {
  // Three accounts plus a fixture, all through auth endpoints sharing one strict per-IP bucket
  // (D35/D36) that backs off ~11s per 429. The config's 30s default is not survivable once
  // another spec has already drawn that bucket down, which made this fail with a bare timeout
  // and no assertion — indistinguishable, at a glance, from the behaviour under test breaking.
  test.setTimeout(180_000);

  const owner = await signupVerifiedUser("gate-owner");
  const member = await signupVerifiedUser("gate-member");
  const fixture = await createVotingFixture(owner, member);

  const stranger = await signupVerifiedUser("gate-stranger");
  await loginViaUi(page, stranger);
  await page.goto(`/trips/${fixture.tripId}/plan`);

  await expect(page.getByRole("heading", { name: /isn't available/i })).toBeVisible({
    timeout: 15_000,
  });
  // The negative is what makes this precise: the empty-itinerary copy must NOT be what a
  // stranger sees, because that is the failure being guarded against.
  await expect(page.getByText("Nothing planned yet")).toHaveCount(0);
  await expect(page.getByRole("link", { name: /go to your trips/i })).toBeVisible();
});

/** A revoked session must stop the socket cleanly rather than retry 401s forever.
 *
 *  Session revocation closes open sockets on every instance (D91). The client then tried to
 *  reconnect, and its ticket request 401'd forever: apiFetchRaw had no refresh-and-retry (so
 *  it could not recover) and connect() rethrew into `void this.connect()` (so every attempt
 *  became an unhandled rejection). The observable result was a dev-overlay error counter
 *  climbing by two every few seconds, indefinitely.
 *
 *  This asserts BOTH halves, because they are guarded by different code and an early version
 *  of this test proved it: with only the uncaught-error assertion, restoring `throw err`
 *  did NOT fail the test — the defensive `.catch()` on the reconnect timer swallowed it, so
 *  the test was measuring the belt and reporting on the braces. The ticket-request count is
 *  what actually pins "stops retrying".
 *
 *  Verified against two planted breaks:
 *    - dropping the 401 early-return (so it falls through to scheduleReconnect) fails the
 *      ticket-request assertion, which climbs past the bound;
 *    - restoring `throw err` AND reverting scheduleReconnect's `.catch()` to `void` fails
 *      the uncaught-error assertion. */
test("a revoked session stops the socket cleanly instead of a 401 retry storm", async ({
  page,
}) => {
  // This test contains an unconditional 45s observation window (see below), so at the config's
  // 30s default it could never pass on any machine — it is not a slow-CI flake but arithmetic.
  // Sized here at the window plus account setup plus the auth limiter's backoff.
  test.setTimeout(240_000);

  // Deliberately ONE user and a trip built directly: every extra account costs two calls
  // against the strict auth limiter, and this test's subject is the socket, not membership.
  const user: FixtureUser = await signupVerifiedUser("revoke");
  const trip = await api<{ id: string }>("/api/v1/trips", user.accessToken, {
    name: `Revocation trip ${Date.now()}`,
    description: "",
    time_zone: "UTC",
    start_date: null,
    end_date: null,
    version: 0,
  });
  const day = await api<{ id: string }>(`/api/v1/trips/${trip.id}/days`, user.accessToken, {
    date: null,
    label: "Day 1",
    after_day_id: null,
  });
  const SLOT_TITLE = "Pick a restaurant";
  await api(`/api/v1/trips/${trip.id}/slots`, user.accessToken, {
    day_id: day.id,
    kind: "activity",
    title: SLOT_TITLE,
    notes: "",
    start_time: null,
    end_time: null,
    after_slot_id: null,
  });

  const pageErrors: string[] = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  let ticketRequests = 0;
  page.on("request", (r) => {
    if (r.url().includes("/api/v1/ws/ticket")) ticketRequests += 1;
  });

  await loginViaUi(page, user);
  await gotoUntilVisible(page, `/trips/${trip.id}/plan`, SLOT_TITLE);
  expect(pageErrors, "no errors before the session is revoked").toHaveLength(0);

  // Revoke the session this page is using — the same thing as logging out in another tab.
  const status = await page.evaluate(async () => {
    const res = await fetch("http://localhost:8080/api/v1/auth/logout", {
      method: "POST",
      credentials: "include",
    });
    return res.status;
  });
  expect(status).toBe(204);
  const ticketsBefore = ticketRequests;

  // Long enough for several reconnect attempts at the client's backoff: the pre-fix loop
  // produced roughly one attempt every 3-9s, so a storm would be unmistakable by now.
  await page.waitForTimeout(45_000);

  const ticketsAfter = ticketRequests - ticketsBefore;
  // One retry is legitimate — the socket closes, the client tries once, the ticket endpoint
  // refuses it and the client gives up. A storm is what this is guarding against, so the
  // bound is loose on purpose; the pre-fix loop produced well over a dozen in this window.
  expect(
    ticketsAfter,
    `revoked session kept requesting handshake tickets (${ticketsAfter} in 45s) instead of stopping`
  ).toBeLessThanOrEqual(3);

  expect(
    pageErrors,
    `revoked session produced ${pageErrors.length} uncaught errors: ${pageErrors.slice(0, 3).join(" | ")}`
  ).toHaveLength(0);
});
