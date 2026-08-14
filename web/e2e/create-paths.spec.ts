// The create paths, driven entirely through the interface (Stage 3 Slice 4, D108–D109).
//
// Every other e2e file in this directory starts from a SEEDED trip — createVotingFixture posts
// the day, the slot and both options through the API before the browser opens. That is exactly
// why the gap these tests close survived Stage 3: the backend endpoints existed and were
// covered, the API calls worked, and nothing in the suite ever asked whether a human could
// reach any of them. A trip created by hand through the UI was a permanently empty screen with
// no control on it, and every test in the repository stayed green.
//
// So the rule for this file, and the reason it is separate rather than folded into
// voting.spec.ts: **it may not use createVotingFixture, and it may not POST itinerary content.**
// The only API calls it makes are account setup, which is not what is under test. Everything
// else is typed and clicked. A fixture here would reintroduce precisely the blind spot.
import { test, expect, type Page } from "@playwright/test";
import { signupVerifiedUser, type FixtureUser } from "./helpers/fixtures";

/** Shares one per-IP bucket with every other auth call in the suite (D35/D36), so retry. */
async function loginViaUi(page: Page, user: FixtureUser) {
  for (let attempt = 0; attempt < 6; attempt++) {
    await page.goto("/login");
    await page.getByLabel("Email").fill(user.email);
    await page.getByLabel("Password").fill(user.password);
    await page.getByRole("button", { name: "Log in" }).click();
    try {
      await page.waitForURL(/\/trips$/, { timeout: 12_000 });
      return;
    } catch {
      await page.waitForTimeout(12_000);
    }
  }
  throw new Error(`could not log in as ${user.email}`);
}

/** Creates a trip through the form and returns its id, read from the URL the app lands on. */
async function createTripViaUi(page: Page, name: string): Promise<string> {
  await page.goto("/trips/new");
  await page.getByLabel("Trip name").fill(name);
  await page.getByRole("button", { name: "Create trip" }).click();
  await page.waitForURL(/\/trips\/[0-9a-f-]{36}/, { timeout: 20_000 });
  const id = new URL(page.url()).pathname.split("/")[2];
  expect(id, "the create form should land on the new trip").toMatch(/^[0-9a-f-]{36}$/);
  return id;
}

/** Trip page -> Plan mode, the real two-hop path rather than a deep link. */
async function openPlanMode(page: Page, tripId: string) {
  await page.goto(`/trips/${tripId}`);
  await page.locator('[data-testid="mode-card"][data-mode="plan"]').click();
  await page.waitForURL(`/trips/${tripId}/plan`);
}

/** A brand-new trip must offer a way to start planning it.
 *
 *  This is the assertion that would have caught the whole class of bug in D109 on its own: the
 *  empty state used to describe what to do ("Add a day, then add the decisions...") without
 *  rendering a single control that did it. Copy that instructs and a screen that cannot comply
 *  is worse than an empty screen, because it reads as the feature being broken rather than
 *  absent.
 *
 *  Verified against a planted break: removing the `{canEdit ? <AddDay .../>}` branch from
 *  Itinerary's empty state fails here, and the page still shows the same encouraging
 *  paragraph — which is the exact state the app shipped in. */
test("a brand-new trip offers a way to start planning it", async ({ page }) => {
  test.setTimeout(120_000);

  const owner = await signupVerifiedUser("create-empty");
  await loginViaUi(page, owner);

  const tripId = await createTripViaUi(page, `Empty trip ${Date.now()}`);
  await openPlanMode(page, tripId);

  await expect(page.getByText("Nothing planned yet")).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId("add-day-open")).toBeVisible();
});

/** The whole itinerary, built from nothing through the interface.
 *
 *  Day -> slot -> option, plus the unscheduled-backlog path, then a vote on an option that this
 *  test created — which is what ties the new create paths to the surface the rest of the suite
 *  already covers. Voting on a seeded option proves the voting UI works; voting on one the
 *  browser just proposed proves the two halves meet.
 *
 *  Verified against three planted breaks, one per create path:
 *    - `createSlot` reverted to a no-op resolve: the day section keeps showing "No slots on
 *      this day yet" and the slot-row assertion fails.
 *    - `createOption` removed from SlotDetail: the "+ Propose an option" control is absent and
 *      the empty-options copy stays on screen.
 *    - `createDay`'s caller removed: fails at the first step, in the same place the previous
 *      test does. */
test("an itinerary can be built from empty: day, slot, unscheduled slot, option, vote", async ({
  page,
}) => {
  test.setTimeout(180_000);

  const owner = await signupVerifiedUser("create-build");
  await loginViaUi(page, owner);

  const tripId = await createTripViaUi(page, `Built by hand ${Date.now()}`);
  await openPlanMode(page, tripId);
  await expect(page.getByText("Nothing planned yet")).toBeVisible({ timeout: 20_000 });

  // ---- 1. A day -------------------------------------------------------------------------
  await page.getByTestId("add-day-open").click();
  await page.getByTestId("add-day-label").fill("Arrival day");
  await page.getByTestId("add-day-submit").click();

  const daySection = page.getByTestId("day-section").filter({ hasText: "Arrival day" });
  await expect(daySection).toHaveCount(1, { timeout: 15_000 });
  // The empty-state copy must be gone: the screen has moved from "nothing here" to an
  // itinerary, and both showing at once would mean the day landed somewhere unrendered.
  await expect(page.getByText("Nothing planned yet")).toHaveCount(0);
  await expect(daySection.getByText("No slots on this day yet")).toBeVisible();

  // ---- 2. A slot on that day ------------------------------------------------------------
  // Scoped to the day section on purpose. There is also an unscheduled trigger on screen, and
  // "click the first add-slot-open" would be a test that passes while adding the slot to the
  // wrong place.
  await daySection.getByTestId("add-slot-open").click();
  await daySection.getByTestId("add-slot-title").fill("Dinner in Alfama");
  await daySection.getByLabel("Start time (optional)").fill("19:30");
  await daySection.getByLabel("Kind").selectOption("activity");
  await daySection.getByTestId("add-slot-submit").click();

  const slotRow = daySection.getByTestId("slot-row").filter({ hasText: "Dinner in Alfama" });
  await expect(slotRow).toHaveCount(1, { timeout: 15_000 });
  // The time is asserted because it travels through the one conversion this create path has:
  // an <input type="time"> emits "19:30" and the API takes a TimeOfDay, not an instant (D7/D16).
  // A slot that arrived with a null time would still render a row and still pass a title-only
  // check.
  await expect(slotRow).toContainText("19:30");
  await expect(daySection.getByText("No slots on this day yet")).toHaveCount(0);

  // ---- 3. An unscheduled slot -----------------------------------------------------------
  // day_id null is a real destination, not a fallback — "we want to do this somewhere on the
  // trip" is how most planning starts — so it gets its own assertion that it landed in the
  // backlog rather than silently on the day above.
  await page.getByTestId("add-unscheduled-open").click();
  await page.getByTestId("add-slot-title").last().fill("Find a day for the castle");
  await page.getByTestId("add-slot-submit").last().click();

  const backlog = page.locator("section").filter({ hasText: "not yet placed on a day" });
  await expect(
    backlog.getByTestId("slot-row").filter({ hasText: "Find a day for the castle" })
  ).toHaveCount(1, { timeout: 15_000 });
  // ...and it must NOT have been added to the day.
  await expect(
    daySection.getByTestId("slot-row").filter({ hasText: "Find a day for the castle" })
  ).toHaveCount(0);

  // ---- 3b. Things added in order STAY in order -------------------------------------------
  //
  // This is the assertion that would have caught the bug twice now. `after_slot_id: null` and
  // `after_day_id: null` do not mean "append" — the repository reads a nil anchor as "insert
  // before the first", so a client that always sends null builds the list backwards. The seed
  // script had exactly this bug and it was fixed in e06fbf3; the UI create path reintroduced
  // it, and every existing test stayed green because they all add ONE slot per bucket, where
  // front and back are the same position.
  //
  // So: add two more slots to the day and assert the rendered sequence. Two is the minimum
  // that can tell append from prepend, which is precisely why one was not enough.
  for (const later of ["Late-night pastéis", "Walk back along the river"]) {
    // The form deliberately stays open after a submit (adding one thing to a day is usually
    // adding three), so the disclosure trigger is only present the first time.
    const opener = daySection.getByTestId("add-slot-open");
    if (await opener.count()) await opener.click();
    await daySection.getByTestId("add-slot-title").fill(later);
    await daySection.getByTestId("add-slot-submit").click();
    await expect(daySection.getByTestId("slot-row").filter({ hasText: later })).toHaveCount(1, {
      timeout: 15_000,
    });
  }
  await expect
    .poll(
      async () =>
        (await daySection.getByTestId("slot-row").allTextContents()).map((t) =>
          t.replace(/\s+/g, " ").trim()
        ),
      { timeout: 15_000 }
    )
    .toEqual([
      expect.stringContaining("Dinner in Alfama"),
      expect.stringContaining("Late-night pastéis"),
      expect.stringContaining("Walk back along the river"),
    ]);

  // Days too: a second day must come after the first, not before it.
  await page.getByTestId("add-day-open").click();
  await page.getByTestId("add-day-label").fill("Second day");
  await page.getByTestId("add-day-submit").click();
  await expect(page.getByTestId("day-section")).toHaveCount(2, { timeout: 15_000 });
  await expect
    .poll(async () => {
      const headings = await page.getByTestId("day-section").locator("h2").allTextContents();
      return headings.map((h) => h.trim());
    })
    .toEqual(["Arrival day", "Second day"]);

  // ---- 4. An option on the slot ---------------------------------------------------------
  await slotRow.click();
  await page.waitForURL(new RegExp(`/trips/${tripId}/plan/slots/`));
  await expect(page.getByText("No options proposed yet")).toBeVisible({ timeout: 15_000 });

  await page.getByTestId("add-option-open").click();
  await page.getByTestId("add-option-title").fill("Taberna Rua das Flores");
  await page.getByTestId("add-option-submit").click();

  const firstOption = page.getByTestId("option-card").filter({ hasText: "Taberna Rua das Flores" });
  await expect(firstOption).toHaveCount(1, { timeout: 15_000 });
  await expect(page.getByText("No options proposed yet")).toHaveCount(0);

  // A second candidate, because one option is not a decision. This also proves the form resets
  // and stays usable rather than needing a reload between proposals.
  await page.getByTestId("add-option-title").fill("Cervejaria Ramiro");
  await page.getByTestId("add-option-submit").click();
  await expect(page.getByTestId("option-card")).toHaveCount(2, { timeout: 15_000 });

  // ---- 5. The created option is a real, votable entity ------------------------------------
  // The join between this file and voting.spec.ts. Everything above could pass while producing
  // rows the rest of the app cannot use.
  await expect(firstOption.getByTestId("vote-count")).toHaveText("0 votes");
  await firstOption.getByTestId("cast-vote").click();
  await expect(firstOption.getByTestId("vote-count")).toHaveText("1 vote", { timeout: 15_000 });

  // And resolvable — D41's stored decision, on an option that did not exist five seconds ago.
  await firstOption.getByTestId("choose-option").click();
  await expect(firstOption.getByTestId("chosen-badge")).toBeVisible({ timeout: 15_000 });

  // Back on the itinerary, the row must reflect the decision rather than still reading
  // "undecided" — the list and the detail view read the same state through different queries.
  await page.goto(`/trips/${tripId}/plan`);
  await expect(
    page.getByTestId("slot-row").filter({ hasText: "Dinner in Alfama" })
  ).toContainText("Taberna Rua das Flores", { timeout: 20_000 });
});

/** A shareable invite link, created in the UI and redeemed by a second person (D108).
 *
 *  This is the path that had never worked at all: CreateInvitation minted a token, hashed it,
 *  stored the hash and dropped the raw value, so a link invite produced a row that listed as
 *  pending, showed an expiry, and could never be redeemed by anyone.
 *
 *  The test redeems the URL end to end rather than asserting it is non-empty, because from the
 *  invitee's side a link that is present but malformed fails identically to an absent one — the
 *  weaker assertion would have passed against several wrong implementations.
 *
 *  It then reuses the second browser it had to create anyway to prove the other half of the
 *  create paths: a slot added through one client's interface reaches another client's screen
 *  live, over the socket, with no reload. That is the claim REST-only coverage cannot make.
 *
 *  Verified against a planted break: returning CreatedInvitation with AcceptURL unset (the
 *  state this code shipped in) fails at the copy-panel assertion, and the panel shows the
 *  "came back without a link" error instead. */
test("a shareable link invite is created in the UI, redeemed by a second person, and their screens stay in sync", async ({
  browser,
}) => {
  test.setTimeout(240_000);

  const owner = await signupVerifiedUser("link-owner");
  const joiner = await signupVerifiedUser("link-joiner");

  const ownerContext = await browser.newContext();
  const joinerContext = await browser.newContext();
  const ownerPage = await ownerContext.newPage();
  const joinerPage = await joinerContext.newPage();

  await loginViaUi(ownerPage, owner);
  const tripId = await createTripViaUi(ownerPage, `Shared trip ${Date.now()}`);

  // ---- 1. Create the link ----------------------------------------------------------------
  await ownerPage.goto(`/trips/${tripId}/plan/members`);
  await ownerPage.getByTestId("create-invite-link").click();

  const panel = ownerPage.getByTestId("invite-link-panel");
  await expect(panel).toBeVisible({ timeout: 20_000 });

  const acceptUrl = await ownerPage.getByTestId("invite-link-value").inputValue();
  expect(acceptUrl, "the panel must show a redeemable URL").toContain("/invitations/accept?token=");

  // ---- 2. A second person redeems it -------------------------------------------------------
  await loginViaUi(joinerPage, joiner);
  await joinerPage.goto(acceptUrl);
  // Redemption lands the joiner on the trip they were invited to.
  await joinerPage.waitForURL(new RegExp(`/trips/${tripId}`), { timeout: 30_000 });

  // The roster is the durable proof: the owner's members panel must now list two people, and
  // the joiner must hold the role the link granted.
  await ownerPage.reload();
  await expect(ownerPage.getByText(joiner.displayName)).toBeVisible({ timeout: 20_000 });

  // ---- 3. A UI-created slot reaches the other client live ----------------------------------
  for (const page of [ownerPage, joinerPage]) {
    await page.goto(`/trips/${tripId}/plan`);
  }
  await expect(ownerPage.getByTestId("add-day-open")).toBeVisible({ timeout: 20_000 });

  await ownerPage.getByTestId("add-day-open").click();
  await ownerPage.getByTestId("add-day-label").fill("Day one");
  await ownerPage.getByTestId("add-day-submit").click();

  const ownerDay = ownerPage.getByTestId("day-section").filter({ hasText: "Day one" });
  await expect(ownerDay).toHaveCount(1, { timeout: 15_000 });

  await ownerDay.getByTestId("add-slot-open").click();
  await ownerDay.getByTestId("add-slot-title").fill("Tram 28 at dawn");
  await ownerDay.getByTestId("add-slot-submit").click();
  await expect(
    ownerDay.getByTestId("slot-row").filter({ hasText: "Tram 28 at dawn" })
  ).toHaveCount(1, { timeout: 15_000 });

  // The joiner's page has NOT been reloaded since before the day existed. Playwright polls, so
  // this can only pass if the row arrives after the initial render — which means it came over
  // the socket as day.create.v1 / slot.create.v1 rather than from the page load.
  await expect(
    joinerPage.getByTestId("slot-row").filter({ hasText: "Tram 28 at dawn" })
  ).toHaveCount(1, { timeout: 20_000 });

  await ownerContext.close();
  await joinerContext.close();
});
