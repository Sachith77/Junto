// Budget live sync, and the coalescing window that keeps it cheap.
//
// Pins the fix made after the Budget-view performance investigation: BudgetPanel refetched the
// whole ledger on EVERY budget.* operation, with no coalescing, unlike Itinerary. The burst
// this matters for is not one intent fanning out into several ops — a budget write is atomic
// by construction (D44/D83), so one intent is one op — it is several PEOPLE settling up at
// once, which is exactly what the end of a trip looks like.
//
// Per-spec timeout is mandatory in this suite: the fixtures deliberately back off against the
// real auth rate limiter (D35/D36), so the config's 30s default is not enough (web/README.md).
import { test, expect } from "@playwright/test";
import { signupVerifiedUser } from "./helpers/fixtures";

const API = "http://localhost:8080";

/** How many expenses one member enters in quick succession. */
const BURST = 5;

test("a burst of budget writes lands live on another client as a coalesced refetch", async ({
  page,
}) => {
  test.setTimeout(180_000);

  const user = await signupVerifiedUser("budget-sync");
  const call = async (path: string, body: unknown) => {
    const res = await fetch(`${API}${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${user.accessToken}` },
      body: JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) throw new Error(`POST ${path} -> ${res.status}: ${text}`);
    return JSON.parse(text).data;
  };

  const trip = await call("/api/v1/trips", {
    name: `Budget sync ${Date.now()}`,
    description: "",
    time_zone: "UTC",
    start_date: null,
    end_date: null,
    version: 0,
  });
  const entry = (label: string, minor: number) => ({
    label,
    category: "food",
    amount_minor: minor,
    slot_option_id: null,
    paid_by: user.userId,
    incurred_on: null,
    splits: [{ user_id: user.userId, amount_minor: minor }],
    version: null,
  });
  // One entry up front, so the observer has real content to wait for before the burst starts
  // and cannot mistake "still loading" for "nothing arrived".
  await call(`/api/v1/trips/${trip.id}/budget`, entry("Seed expense", 1000));

  await page.goto("/login");
  await page.getByLabel("Email").fill(user.email);
  await page.getByLabel("Password").fill(user.password);
  await page.getByRole("button", { name: "Log in" }).click();
  await page.waitForURL(/\/trips$/, { timeout: 60_000 });

  await page.goto(`/trips/${trip.id}/plan/budget`);
  await expect(page.getByText("Seed expense")).toBeVisible({ timeout: 60_000 });
  // Let the socket finish subscribing and the initial load settle, so the count below covers
  // only refetches caused by the burst.
  await page.waitForTimeout(1500);

  let refetches = 0;
  page.on("request", (req) => {
    if (req.method() === "GET" && /\/api\/v1\/trips\/[^/]+\/budget$/.test(req.url())) refetches += 1;
  });

  // The burst: entered as fast as the API will take them, which is what several members
  // settling up simultaneously looks like from this client's socket.
  for (let i = 0; i < BURST; i++) {
    await call(`/api/v1/trips/${trip.id}/budget`, entry(`Burst expense ${i}`, 500 + i));
  }

  // FIRST assertion, and the one that makes the second meaningful: every write must actually
  // arrive. A refetch count of zero would otherwise pass this test perfectly while the socket
  // was dead — "few requests" is trivially satisfied by doing nothing at all.
  for (let i = 0; i < BURST; i++) {
    await expect(page.getByText(`Burst expense ${i}`)).toBeVisible({ timeout: 30_000 });
  }

  // Give any un-coalesced trailing refetch time to fire before counting, so a failure here is
  // a real one rather than a race the assertion won.
  await page.waitForTimeout(1000);

  // SECOND assertion: the burst collapsed into a small number of reads rather than one per op.
  //
  // The bound is loose on purpose. In development React StrictMode double-invokes effects, so
  // every fetch appears twice — coalesced is 1 logical refetch (2 requests), un-coalesced is
  // BURST logical refetches (10 requests at BURST=5). Anything at or below 4 is unambiguously
  // the coalesced path; anything approaching 10 is unambiguously not. Asserting an exact count
  // would be asserting StrictMode's behaviour, which is not what this test is about.
  expect(
    refetches,
    `${BURST} budget ops caused ${refetches} ledger refetches; coalescing should collapse them ` +
      `into ~1 (x2 in dev under StrictMode), not one per operation`
  ).toBeLessThanOrEqual(4);
});
