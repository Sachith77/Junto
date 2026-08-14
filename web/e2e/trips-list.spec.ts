// The trips list's layout, which is the app's front door and the only screen where a
// regression is purely geometric — nothing errors, nothing fails to load, the grid just looks
// wrong. That is exactly the kind of defect a test suite built around behaviour misses, and
// this one was reported by eye rather than caught here.
//
// Per-spec timeout is mandatory in this suite: the fixtures deliberately back off against the
// real auth rate limiter (D35/D36), so the config's 30s default is not enough (web/README.md).
import { test, expect } from "@playwright/test";
import { signupVerifiedUser } from "./helpers/fixtures";

const API = "http://localhost:8080";

/** THREE trips, and the odd count is the point.
 *
 *  The bug this pins gave the first two cards a taller treatment than the rest. In a
 *  two-column grid that is invisible at 2 or 4 trips — the tall ones fill whole rows — and
 *  only shows up at an odd count, where it leaves one short card alongside a tall one. A
 *  fixture of two would have been green against the broken build. */
const TRIP_NAMES = ["Lisbon in autumn", "Kerala backwaters", "Hokkaido in winter"];

test("every trip card on the list is the same size", async ({ page }) => {
  test.setTimeout(180_000);

  const user = await signupVerifiedUser("grid");
  for (const name of TRIP_NAMES) {
    const res = await fetch(`${API}/api/v1/trips`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${user.accessToken}` },
      body: JSON.stringify({
        name,
        // Deliberately mixed: one trip with no dates renders a shorter eyebrow ("Dates not
        // set") than one with a range. Card height must not follow its content.
        description: name === "Kerala backwaters" ? "" : "Houseboats, tea country, and rain.",
        time_zone: "UTC",
        start_date: name === "Hokkaido in winter" ? null : "2026-09-14T00:00:00Z",
        end_date: name === "Hokkaido in winter" ? null : "2026-09-21T00:00:00Z",
        version: 0,
      }),
    });
    if (!res.ok) throw new Error(`creating ${name}: ${res.status} ${await res.text()}`);
  }

  // Backoff-and-retry, like every other spec here.
  //
  // The first version of this test omitted it and passed standalone, then failed the moment it
  // ran as part of the suite: by then the shared per-IP auth bucket (D35/D36) is empty, login
  // answers "Too many attempts", and a bare waitForURL just times out with no hint of why. A
  // real client backs off; so does this one.
  for (let attempt = 0; attempt < 6; attempt++) {
    await page.goto("/login");
    await page.getByLabel("Email").fill(user.email);
    await page.getByLabel("Password").fill(user.password);
    await page.getByRole("button", { name: "Log in" }).click();
    try {
      await page.waitForURL(/\/trips$/, { timeout: 12_000 });
      break;
    } catch {
      await page.waitForTimeout(12_000);
    }
  }
  await expect(page).toHaveURL(/\/trips$/);

  const cards = page.getByTestId("trip-card");
  await expect(cards).toHaveCount(TRIP_NAMES.length, { timeout: 30_000 });

  const sizes = await cards.evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect();
      return { w: Math.round(r.width), h: Math.round(r.height) };
    })
  );

  // Height to the pixel. "Looks even" is what failed to catch this the first time; a set of
  // one is the only assertion an eye cannot talk itself out of.
  const heights = sizes.map((s) => s.h);
  expect(
    new Set(heights).size,
    `trip cards have different heights (${heights.join(", ")}px) — the grid reads as broken, ` +
      `not as emphasis`
  ).toBe(1);

  // Width too, since a card that spans two columns would also break the grid's rhythm while
  // leaving every height identical.
  const widths = sizes.map((s) => s.w);
  expect(new Set(widths).size, `trip cards have different widths (${widths.join(", ")}px)`).toBe(1);

  // And the cards must actually have been laid out — a list of three zero-height elements
  // would satisfy both assertions above perfectly.
  expect(heights[0], "cards rendered with no height").toBeGreaterThan(200);
});
