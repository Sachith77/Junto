import { test, type Page } from "@playwright/test";

// Screenshot harness for the Group B + C review, against the trip created by
// e2e/helpers/seedReviewTrip.ts. Pass the credentials and trip id in:
//   SEED_EMAIL=... SEED_TRIP=... npx playwright test groupBC.spec.ts

const EMAIL = process.env.SEED_EMAIL ?? "";
const TRIP = process.env.SEED_TRIP ?? "";
const PASSWORD = "correct horse battery staple";

async function loginViaUi(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Email").fill(EMAIL);
  await page.getByLabel("Password").fill(PASSWORD);
  for (let i = 0; i < 8; i++) {
    await page.getByRole("button", { name: "Log in" }).click();
    const r = await Promise.race([
      page.waitForURL(/\/trips$/, { timeout: 6000 }).then(() => "ok" as const),
      page
        .getByText("Too many attempts")
        .waitFor({ timeout: 6000 })
        .then(() => "rl" as const)
        .catch(() => "unknown" as const),
    ]);
    if (r === "ok") return;
    if (r === "rl") {
      await page.waitForTimeout(11_000);
      continue;
    }
    break;
  }
  await page.waitForURL(/\/trips$/, { timeout: 15_000 });
}

test("capture group B and C screens", async ({ page }) => {
  test.setTimeout(240_000);
  test.skip(!EMAIL || !TRIP, "SEED_EMAIL and SEED_TRIP must be set");
  await page.setViewportSize({ width: 1440, height: 950 });

  await loginViaUi(page);

  // Itinerary
  await page.goto(`/trips/${TRIP}/plan`);
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "shots/B1-itinerary.png", fullPage: true });

  // Slot detail — the D41 screen. The seeded trip resolves AGAINST the tally here.
  // Explicitly the slot the seed resolved AGAINST the tally — the D41 case.
  await page.getByTestId("slot-row").filter({ hasText: "Where are we staying" }).click();
  await page.waitForURL(/\/plan\/slots\//);
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "shots/B2-slot-detail.png", fullPage: true });

  // Budget
  await page.goto(`/trips/${TRIP}/plan/budget`);
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "shots/B3-budget.png", fullPage: true });

  // Budget with a split expanded — the "these numbers add up" moment.
  const split = page.getByRole("button", { name: "Split" }).first();
  if (await split.count()) {
    await split.click();
    await page.waitForTimeout(500);
    await page.screenshot({ path: "shots/B4-budget-split.png", fullPage: true });
  }

  // Members
  await page.goto(`/trips/${TRIP}/plan/members`);
  await page.waitForTimeout(1800);
  await page.screenshot({ path: "shots/B5-members.png", fullPage: true });

  // Memories
  await page.goto(`/trips/${TRIP}/memories`);
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "shots/C1-memories.png", fullPage: true });

  const card = page.getByTestId("memory-card").first();
  if (await card.count()) {
    await card.click();
    await page.waitForURL(/\/memories\//);
    await page.waitForTimeout(1800);
    await page.screenshot({ path: "shots/C2-destination.png", fullPage: true });
  }

  // Narrow — the stated responsive floor.
  await page.setViewportSize({ width: 720, height: 1100 });
  await page.goto(`/trips/${TRIP}/plan`);
  await page.waitForTimeout(1500);
  await page.screenshot({ path: "shots/B6-itinerary-narrow.png", fullPage: true });
});
