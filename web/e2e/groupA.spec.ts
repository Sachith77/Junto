import { test, type Page } from "@playwright/test";
import { signupVerifiedUser } from "./helpers/fixtures";

// Screenshot harness for the Group A design review. Not a correctness test.
//
// Uses ONE account for every shot and walks the real product path
// (empty list -> create -> populated list -> mode picker). Two earlier
// mistakes are deliberately avoided here:
//   - `waitUntil: "networkidle"` never settles against Next's dev HMR socket,
//     which hung the first version of this file.
//   - Every extra account costs three calls against the auth rate limiter
//     (D35/D36) at ~11s of backoff each, so seeding three users to take five
//     screenshots took longer than the whole rest of the slice.

async function loginViaUi(page: Page, user: { email: string; password: string }) {
  await page.goto("/login");
  await page.getByLabel("Email").fill(user.email);
  await page.getByLabel("Password").fill(user.password);
  for (let i = 0; i < 8; i++) {
    await page.getByRole("button", { name: "Log in" }).click();
    const r = await Promise.race([
      page.waitForURL(/\/trips$/, { timeout: 6000 }).then(() => "ok" as const),
      page
        .getByText("Too many attempts")
        .waitFor({ timeout: 6000 })
        .then(() => "rate-limited" as const)
        .catch(() => "unknown" as const),
    ]);
    if (r === "ok") return;
    if (r === "rate-limited") {
      await page.waitForTimeout(11_000);
      continue;
    }
    break;
  }
  await page.waitForURL(/\/trips$/, { timeout: 15_000 });
}

test("capture group A screens", async ({ page }) => {
  test.setTimeout(240_000);
  await page.setViewportSize({ width: 1440, height: 900 });

  // 1. Landing, signed out.
  await page.goto("/");
  await page.waitForTimeout(1200);
  await page.screenshot({ path: "shots/A1-landing.png" });

  const user = await signupVerifiedUser("shots");

  // 2. Empty trips list — only visible before the account has any trips.
  await loginViaUi(page, user);
  await page.waitForTimeout(900);
  await page.screenshot({ path: "shots/A2-trips-empty.png" });

  // 3. Create trip, preview populated.
  await page.getByRole("link", { name: "Create your first trip" }).click();
  await page.waitForURL(/\/trips\/new$/);
  await page.getByLabel("Trip name").fill("Lisbon in autumn");
  await page
    .locator("#description")
    .fill("Tiled streets, long lunches, and one very contested dinner reservation.");
  await page.locator("#start").fill("2026-09-14");
  await page.locator("#end").fill("2026-09-21");
  await page.waitForTimeout(500);
  await page.screenshot({ path: "shots/A3-create-trip.png" });

  // Submitting lands on the mode picker for the trip just created.
  await page.getByRole("button", { name: "Create trip" }).click();
  await page.waitForURL(/\/trips\/[0-9a-f-]+$/, { timeout: 20_000 });
  await page.waitForTimeout(1600);
  await page.screenshot({ path: "shots/A5-mode-picker.png", fullPage: true });
  const tripUrl = page.url();

  // A second trip so the editorial grid has something to judge. Navigating by
  // link rather than page.goto keeps the in-memory access token alive (D30) —
  // a hard load would need /auth/refresh and burn limiter budget.
  await page.getByRole("link", { name: "← All trips" }).click();
  await page.waitForURL(/\/trips$/);
  await page.getByRole("link", { name: "New trip" }).click();
  await page.waitForURL(/\/trips\/new$/);
  await page.getByLabel("Trip name").fill("Hokkaido, deep winter");
  await page.locator("#description").fill("Powder, onsen, and an unreasonable amount of ramen.");
  await page.locator("#start").fill("2027-01-08");
  await page.locator("#end").fill("2027-01-16");
  await page.getByRole("button", { name: "Create trip" }).click();
  await page.waitForURL(/\/trips\/[0-9a-f-]+$/, { timeout: 20_000 });

  // 4. Populated trips list.
  await page.getByRole("link", { name: "← All trips" }).click();
  await page.waitForURL(/\/trips$/);
  await page.waitForTimeout(1200);
  await page.screenshot({ path: "shots/A4-trips-list.png" });

  // 5. Narrow viewport — the stated responsive floor for the outer shell.
  await page.setViewportSize({ width: 720, height: 1100 });
  await page.waitForTimeout(700);
  await page.screenshot({ path: "shots/A7-trips-narrow.png", fullPage: true });
  await page.locator(`a[href="${new URL(tripUrl).pathname}"]`).first().click();
  await page.waitForURL(tripUrl);
  await page.waitForTimeout(1400);
  await page.screenshot({ path: "shots/A6-mode-picker-narrow.png", fullPage: true });
});
