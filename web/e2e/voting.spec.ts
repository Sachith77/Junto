import { test, expect, type Page } from "@playwright/test";
import { createVotingFixture, signupVerifiedUser, type FixtureUser } from "./helpers/fixtures";

// Logging in through the UI is itself an auth-rate-limited call (D35/D36) sharing the same
// per-IP bucket as every signup/verify/login the fixtures already made — see the comment in
// helpers/fixtures.ts. Retry with the same backoff rather than treat a 429 as a real failure.
async function loginViaUi(page: Page, user: FixtureUser) {
  await page.goto("/login");
  await page.getByLabel("Email").fill(user.email);
  await page.getByLabel("Password").fill(user.password);
  for (let attempt = 0; attempt < 8; attempt++) {
    await page.getByRole("button", { name: "Log in" }).click();
    const result = await Promise.race([
      page.waitForURL(/\/trips$/, { timeout: 5_000 }).then(() => "ok" as const),
      page
        .getByText("Too many attempts")
        .waitFor({ timeout: 5_000 })
        .then(() => "rate-limited" as const)
        .catch(() => "unknown" as const),
    ]);
    if (result === "ok") return;
    if (result === "rate-limited") {
      await page.waitForTimeout(11_000);
      continue;
    }
    break;
  }
  await expect(page).toHaveURL(/\/trips$/);
}

test("presence shows both members, and votes/comments cast by one client update a second client live", async ({
  browser,
}) => {
  // Fixture setup makes several auth calls that share the API's strict per-IP rate limit
  // (D35/D36) with backoff-and-retry on 429 — see helpers/fixtures.ts — so this can take a
  // while in the worst case.
  test.setTimeout(180_000);

  const owner = await signupVerifiedUser("owner");
  const member = await signupVerifiedUser("member");
  const fixture = await createVotingFixture(owner, member);

  const ownerContext = await browser.newContext();
  const memberContext = await browser.newContext();
  const ownerPage = await ownerContext.newPage();
  const memberPage = await memberContext.newPage();

  await loginViaUi(ownerPage, owner);
  await loginViaUi(memberPage, member);

  // A client-side <Link> click, not page.goto() — a hard navigation would remount AuthContext
  // and cost another call against the auth rate limit for no reason (D30: the access token is
  // memory-only, so a hard reload legitimately needs /auth/refresh; a client transition does
  // not, since the in-memory token is still there).
  await ownerPage.locator(`a[href="/trips/${fixture.tripId}"]`).click();
  await memberPage.locator(`a[href="/trips/${fixture.tripId}"]`).click();
  await ownerPage.waitForURL(`/trips/${fixture.tripId}`);
  await memberPage.waitForURL(`/trips/${fixture.tripId}`);

  // Part A: presence. Both sockets are subscribed once each page's avatar stack shows both
  // members — this is the live WS presence signal, not a static member list.
  await expect(ownerPage.getByTestId("presence-avatar")).toHaveCount(2, { timeout: 15_000 });
  await expect(memberPage.getByTestId("presence-avatar")).toHaveCount(2, { timeout: 15_000 });

  // Part B: voting, proven live. The member votes; the OWNER's screen — which never reloads —
  // must show the updated tally. Playwright's expect() polls, so this only passes if the
  // number actually changes after the initial render, not merely on first paint.
  const ownerOptionA = ownerPage.getByTestId("option-card").filter({ hasText: fixture.optionATitle });
  const memberOptionA = memberPage
    .getByTestId("option-card")
    .filter({ hasText: fixture.optionATitle });

  await expect(ownerOptionA.getByTestId("vote-count")).toHaveText("0 votes");

  await memberOptionA.getByTestId("cast-vote").click();
  await expect(memberOptionA.getByTestId("retract-vote")).toBeVisible();

  await expect(ownerOptionA.getByTestId("vote-count")).toHaveText("1 vote", { timeout: 10_000 });

  // Retraction propagates live too, and maps to option_id = NULL (D42) rather than a delete.
  await memberOptionA.getByTestId("retract-vote").click();
  await expect(ownerOptionA.getByTestId("vote-count")).toHaveText("0 votes", { timeout: 10_000 });

  // Part C: comments, proven live over the SAME sockets — no extra fixture, no extra
  // rate-limited signup. The member posts a comment; the OWNER's screen must show it without
  // a reload, and the member's own copy must have already reconciled out of its pending state
  // by the time the owner sees it (comment.create.v1 is WS-native, D103, so both are driven by
  // the same op broadcast).
  const ownerComments = ownerPage.getByTestId("comments-list");
  const memberComments = memberPage.getByTestId("comments-list");

  await memberComments.getByPlaceholder("Add a comment").fill("Should we book the early flight?");
  await memberComments.getByTestId("post-comment").click();

  const memberComment = memberComments.getByTestId("comment").filter({ hasText: "Should we book the early flight?" });
  await expect(memberComment).toHaveAttribute("data-pending", "false", { timeout: 10_000 });

  const ownerComment = ownerComments.getByTestId("comment").filter({ hasText: "Should we book the early flight?" });
  await expect(ownerComment).toBeVisible({ timeout: 10_000 });

  // Author-only delete (D100): the OWNER — the most privileged member on this trip — must not
  // even see a delete control on the member's comment.
  await expect(ownerComment.getByTestId("delete-comment")).toHaveCount(0);

  // The member deletes their own comment; it must disappear from the owner's screen live too.
  await memberComment.getByTestId("delete-comment").click();
  await expect(ownerComment).toHaveCount(0, { timeout: 10_000 });

  await ownerContext.close();
  await memberContext.close();
});
