import { test } from "@playwright/test";

// Screenshot harness for the Group D auth screens. No assertions — auth behaviour is already
// covered by the Go suite and voting.spec.ts; this slice was styling only.
test("capture auth screens", async ({ page }) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 1280, height: 860 });

  const shots: [string, string][] = [
    ["D1-login", "/login"],
    ["D2-signup", "/signup"],
    ["D3-forgot-password", "/forgot-password"],
    ["D4-reset-password", "/reset-password?token=demo-token-for-screenshot"],
    ["D5-verify-email-invalid", "/verify-email?token=not-a-real-token"],
    ["D6-invitation-anonymous", "/invitations/accept?token=not-a-real-token"],
  ];

  for (const [name, path] of shots) {
    await page.goto(path);
    await page.waitForTimeout(900);
    await page.screenshot({ path: `shots/${name}.png` });
  }

  // The signup confirmation state, which is a different screen from the form.
  await page.goto("/signup");
  await page.getByLabel("Name").fill("Alice Moreau");
  await page.getByLabel("Email").fill(`shot-${Date.now()}@junto.local`);
  await page.getByLabel("Password").fill("correct horse battery staple");
  await page.getByRole("button", { name: "Create account" }).click();
  await page.waitForTimeout(2500);
  await page.screenshot({ path: "shots/D7-signup-done.png" });
});
