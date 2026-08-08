import { test } from "@playwright/test";

// Not an assertion test — a screenshot harness for design review. Kept in e2e/
// because it needs a real browser against the real dev server, but it is skipped
// by default so `npm run test:e2e` stays a correctness suite. Run explicitly:
//   npx playwright test shots.spec.ts --grep-invert @never
test.describe("design specimens", () => {
  test("capture design token page", async ({ page }) => {
    test.setTimeout(120_000);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto("/design", { waitUntil: "networkidle" });
    await page.waitForTimeout(1200); // let webfonts settle so specimens are real

    await page.screenshot({ path: "shots/tokens-full.png", fullPage: true });

    const sections = [
      ["header", "header"],
      ["01-two-languages", "section:nth-of-type(1)"],
      ["02-colour", "section:nth-of-type(2)"],
      ["03-type", "section:nth-of-type(3)"],
      ["04-form", "section:nth-of-type(4)"],
      ["05-scrim", "section:nth-of-type(5)"],
      ["06-realtime", "section:nth-of-type(6)"],
    ] as const;

    for (const [name, selector] of sections) {
      const el = page.locator(`main ${selector}`).first();
      if (await el.count()) {
        await el.screenshot({ path: `shots/${name}.png` });
      }
    }
  });
});
