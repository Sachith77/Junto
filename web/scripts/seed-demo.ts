// Seeds a realistic, demo-ready trip through the PUBLIC HTTP API.
//
//   npm run seed                 # stable credentials, good for a rehearsed demo
//   SEED_UNIQUE=1 npm run seed   # fresh accounts each run, good for repeat testing
//
// Environment:
//   JUNTO_API_URL      default http://localhost:8080
//   JUNTO_WEB_URL      default http://localhost:3000   (only used to print links)
//   JUNTO_MAILBOX_URL  default http://localhost:8025   (Mailpit; see "verification" below)
//
// EVERYTHING goes through the public API — no direct database writes — so anything this
// script produces is something the product itself can produce.
//
// # Verification, and what this script needs from its environment
//
// Logging in requires a verified email (D29). This script handles BOTH ways that can be
// satisfied, and refuses to guess:
//
//   1. AUTH_AUTO_VERIFY_EMAIL=true (development only, D105) — signup returns an already
//      verified account and no mail is sent. Nothing else is needed.
//   2. Otherwise it reads the verification and invitation links out of a MAILBOX, which
//      locally is Mailpit.
//
// Neither is available in a production deployment: config validation refuses auto-verify
// there, and production SMTP goes to real inboxes this script cannot read. That is a
// deliberate limit rather than an oversight — a tool that could mint verified accounts
// against production would be a hole, not a feature. For a deployed DEMO environment, run it
// against a staging stack that has a readable mailbox, or point JUNTO_MAILBOX_URL at one.

const API = process.env.JUNTO_API_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
const WEB = process.env.JUNTO_WEB_URL ?? "http://localhost:3000";
const MAILBOX = process.env.JUNTO_MAILBOX_URL ?? "http://localhost:8025";
const UNIQUE = process.env.SEED_UNIQUE === "1";
const PASSWORD = "correct horse battery staple";

interface Envelope<T> {
  data: T;
}

async function call<T>(path: string, opts: RequestInit & { token?: string } = {}): Promise<T> {
  const { token, ...rest } = opts;
  for (let attempt = 0; attempt < 12; attempt++) {
    let res: Response;
    try {
      res = await fetch(`${API}${path}`, {
        ...rest,
        headers: {
          "Content-Type": "application/json",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
          ...(rest.headers ?? {}),
        },
      });
    } catch {
      throw new Error(`cannot reach the API at ${API} — is it running?`);
    }
    // The auth endpoints share a strict per-IP limiter (D35/D36) and a seed makes many calls
    // in a row, so back off rather than fail.
    if (res.status === 429) {
      const wait = Number(res.headers.get("retry-after")) || 6;
      await new Promise((r) => setTimeout(r, wait * 1000));
      continue;
    }
    const text = await res.text();
    if (!res.ok) throw new Error(`${opts.method ?? "GET"} ${path} -> ${res.status}: ${text}`);
    return text ? ((JSON.parse(text) as Envelope<T>).data ?? (undefined as T)) : (undefined as T);
  }
  throw new Error(`${path}: still rate-limited after retries`);
}

/** Pulls a tokenised link out of the mailbox, the way a user reads their inbox. */
async function linkFromMailbox(address: string, fragment: string): Promise<string> {
  const pattern = new RegExp(`${fragment}\\?token=([A-Za-z0-9_-]+)`);
  for (let attempt = 0; attempt < 40; attempt++) {
    let list: { messages: { ID: string; To: { Address: string }[] }[] };
    try {
      list = await fetch(`${MAILBOX}/api/v1/messages?limit=50`).then((r) => r.json());
    } catch {
      throw new Error(
        `cannot reach a mailbox at ${MAILBOX}.\n` +
          `  This environment sends real verification email, so the seed needs a readable\n` +
          `  mailbox. Either start Mailpit (docker compose up -d mailpit), set\n` +
          `  JUNTO_MAILBOX_URL, or run the API with AUTH_AUTO_VERIFY_EMAIL=true.`
      );
    }
    const msg = list.messages.find((m) => m.To.some((t) => t.Address === address));
    if (msg) {
      const full = (await fetch(`${MAILBOX}/api/v1/message/${msg.ID}`).then((r) => r.json())) as {
        Text: string;
      };
      const match = full.Text.match(pattern);
      if (match) return match[1];
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`timed out waiting for a "${fragment}" email to ${address}`);
}

interface SeedUser {
  email: string;
  name: string;
  token: string;
  id: string;
}

async function makeUser(name: string, tag: string): Promise<SeedUser> {
  const email = `${tag}@junto.local`;
  let alreadyVerified = false;
  let existed = false;

  try {
    const created = await call<{ email_verified_at: string | null }>("/api/v1/auth/signup", {
      method: "POST",
      body: JSON.stringify({ email, password: PASSWORD, display_name: name }),
    });
    alreadyVerified = Boolean(created.email_verified_at);
  } catch (err) {
    // Re-running with stable credentials should be harmless: the account exists and is
    // already verified from the first run.
    if (!/already exists|email_taken/i.test(String(err))) throw err;
    existed = true;
  }

  // Branch on what the server ACTUALLY returned rather than on an assumption about how it is
  // configured — this is what lets one script serve both modes.
  if (!existed && !alreadyVerified) {
    const token = await linkFromMailbox(email, "verify-email");
    await call("/api/v1/auth/verify-email", { method: "POST", body: JSON.stringify({ token }) });
  }

  const session = await call<{ access_token: string; user: { id: string } }>("/api/v1/auth/login", {
    method: "POST",
    body: JSON.stringify({ email, password: PASSWORD }),
  });
  return { email, name, token: session.access_token, id: session.user.id };
}

async function main() {
  const suffix = UNIQUE ? `-${Date.now().toString(36)}` : "";
  console.log(`Seeding demo trip against ${API}…\n`);

  const alice = await makeUser("Alice Moreau", `alice${suffix}`);
  const bruno = await makeUser("Bruno Alves", `bruno${suffix}`);
  const mira = await makeUser("Mira Sethi", `mira${suffix}`);

  const trip = await call<{ id: string }>("/api/v1/trips", {
    method: "POST",
    token: alice.token,
    body: JSON.stringify({
      name: "Lisbon in autumn",
      description: "Tiled streets, long lunches, and one very contested dinner reservation.",
      time_zone: "Europe/Lisbon",
      start_date: "2026-09-14T00:00:00Z",
      end_date: "2026-09-21T00:00:00Z",
      version: 0,
    }),
  });

  // Invite through the real flow so memberships and capabilities are genuine.
  for (const guest of [bruno, mira]) {
    await call(`/api/v1/trips/${trip.id}/invitations`, {
      method: "POST",
      token: alice.token,
      body: JSON.stringify({ email: guest.email, role: "editor", max_uses: 1 }),
    });
    const token = await linkFromMailbox(guest.email, "invitations/accept");
    await call("/api/v1/invitations/accept", {
      method: "POST",
      token: guest.token,
      body: JSON.stringify({ token }),
    });
  }

  // `after_*: null` means "at the START of the bucket" (fractional indexing, D2), so creating
  // in chronological order without chaining produces a REVERSED itinerary. Anchor each new
  // entry after the previous one.
  let lastDayId: string | null = null;
  const day = async (label: string, date: string) => {
    const created = await call<{ id: string }>(`/api/v1/trips/${trip.id}/days`, {
      method: "POST",
      token: alice.token,
      body: JSON.stringify({ label, date: `${date}T00:00:00Z`, after_day_id: lastDayId }),
    });
    lastDayId = created.id;
    return created;
  };

  const lastSlotByDay = new Map<string, string>();
  const slot = async (dayId: string, title: string, kind: string, start: string | null) => {
    const created = await call<{ id: string }>(`/api/v1/trips/${trip.id}/slots`, {
      method: "POST",
      token: alice.token,
      body: JSON.stringify({
        day_id: dayId,
        kind,
        title,
        notes: "",
        start_time: start,
        end_time: null,
        after_slot_id: lastSlotByDay.get(dayId) ?? null,
      }),
    });
    lastSlotByDay.set(dayId, created.id);
    return created;
  };

  const option = (
    slotId: string,
    token: string,
    title: string,
    notes: string,
    cost: number | null,
    place: string
  ) =>
    call<{ id: string }>(`/api/v1/trips/${trip.id}/slots/${slotId}/options`, {
      method: "POST",
      token,
      body: JSON.stringify({
        title,
        notes,
        external_url: "",
        estimated_cost_minor: cost,
        place: { name: place, address: "", lat: null, lng: null, provider_id: "" },
      }),
    });

  const vote = (slotId: string, token: string, optionId: string | null) =>
    call(`/api/v1/trips/${trip.id}/slots/${slotId}/votes/me`, {
      method: "PUT",
      token,
      body: JSON.stringify({ option_id: optionId }),
    });

  const comment = (slotId: string, token: string, body: string) =>
    call(`/api/v1/trips/${trip.id}/slots/${slotId}/comments`, {
      method: "POST",
      token,
      body: JSON.stringify({ body }),
    });

  const choose = (slotId: string, optionId: string) =>
    call(`/api/v1/trips/${trip.id}/slots/${slotId}/select`, {
      method: "POST",
      token: alice.token,
      body: JSON.stringify({ option_id: optionId, version: null }),
    });

  // --- Day 1 ---
  const d1 = await day("Day 1 — Arrival", "2026-09-14");

  const stay = await slot(d1.id, "Where are we staying", "lodging", "15:00");
  const alfama = await option(stay.id, alice.token, "Alfama guesthouse", "Steep walk, unbeatable view over the river.", 14800, "Alfama");
  const baixa = await option(stay.id, bruno.token, "Baixa apartment", "Flat, central, three bedrooms.", 13200, "Baixa");
  await option(stay.id, mira.token, "Chiado studio", "Smallest of the three but right by the metro.", 11000, "Chiado");
  await vote(stay.id, bruno.token, baixa.id);
  await vote(stay.id, mira.token, baixa.id);
  await vote(stay.id, alice.token, alfama.id);
  // Deliberately resolved AGAINST the tally: this is the D41 case the interface has to make
  // legible, and it is the single most demo-worthy screen in the product.
  await choose(stay.id, alfama.id);
  await comment(stay.id, bruno.token, "Baixa is cheaper and flatter — my knees have opinions.");
  await comment(stay.id, alice.token, "Booked Alfama in the end, it was the only one with three nights free.");
  await comment(stay.id, mira.token, "Fine by me. The view sells it.");

  // Left UNDECIDED on purpose, with two candidates and one vote — this is the slot to use for
  // a live two-window demo, because there is a vote still to change.
  const dinner = await slot(d1.id, "Dinner, first night", "activity", "20:00");
  const ramiro = await option(dinner.id, mira.token, "Cervejaria Ramiro", "Queue is real. So are the prawns.", 4500, "Intendente");
  await option(dinner.id, bruno.token, "Taberna da Rua das Flores", "Tiny, no bookings.", 3800, "Cais do Sodré");
  await vote(dinner.id, alice.token, ramiro.id);
  await comment(dinner.id, alice.token, "If we go at 18:30 the queue is survivable.");

  // --- Day 2 ---
  const d2 = await day("Day 2 — Sintra", "2026-09-15");
  const travel = await slot(d2.id, "Getting to Sintra", "transport", "09:15");
  const train = await option(travel.id, alice.token, "Train from Rossio", "40 minutes, runs constantly.", 260, "Rossio");
  await option(travel.id, bruno.token, "Rent a car", "Faster but parking in Sintra is famously grim.", 6500, "Sintra");
  await vote(travel.id, alice.token, train.id);
  await vote(travel.id, bruno.token, train.id);
  await vote(travel.id, mira.token, train.id);
  await choose(travel.id, train.id);

  const palace = await slot(d2.id, "Which palace", "place", "11:00");
  const pena = await option(palace.id, mira.token, "Pena Palace", "The famous one. Go early.", 1400, "Pena");
  await option(palace.id, alice.token, "Quinta da Regaleira", "Gardens and the initiation well.", 1200, "Regaleira");
  await vote(palace.id, mira.token, pena.id);
  await comment(palace.id, mira.token, "Pena first, Regaleira after lunch if we still have legs.");

  // An empty slot, so the "no options yet" state is visible too.
  await slot(d2.id, "Somewhere for lunch", "activity", "13:30");

  // --- Budget ---
  const members = await call<{ user_id: string }[]>(`/api/v1/trips/${trip.id}/members`, {
    token: alice.token,
  });
  const ids = members.map((m) => m.user_id);
  const splitEvenly = (total: number) => {
    const base = Math.floor(total / ids.length);
    let rem = total - base * ids.length;
    return ids.map((user_id) => {
      const extra = rem > 0 ? 1 : 0;
      rem -= extra;
      return { user_id, amount_minor: base + extra };
    });
  };

  for (const e of [
    { label: "Alfama guesthouse, 3 nights", category: "lodging", amount: 44400, payer: alice },
    { label: "Dinner at Ramiro", category: "food", amount: 13650, payer: mira },
    { label: "Train tickets to Sintra", category: "transport", amount: 780, payer: bruno },
    { label: "Pena Palace entry", category: "activity", amount: 4200, payer: bruno },
  ]) {
    await call(`/api/v1/trips/${trip.id}/budget`, {
      method: "POST",
      token: alice.token,
      body: JSON.stringify({
        label: e.label,
        category: e.category,
        amount_minor: e.amount,
        slot_option_id: null,
        paid_by: e.payer.id,
        incurred_on: null,
        splits: splitEvenly(e.amount),
        version: null,
      }),
    });
  }

  const dinnerUrl = `${WEB}/trips/${trip.id}/plan/slots/${dinner.id}`;

  console.log("Done.\n");
  console.log("  Accounts (all share the same password):");
  for (const u of [alice, bruno, mira]) console.log(`    ${u.name.padEnd(14)} ${u.email}`);
  console.log(`    password       ${PASSWORD}\n`);
  console.log("  Screens:");
  console.log(`    Trip       ${WEB}/trips/${trip.id}`);
  console.log(`    Plan       ${WEB}/trips/${trip.id}/plan`);
  console.log(`    Budget     ${WEB}/trips/${trip.id}/plan/budget`);
  console.log(`    Members    ${WEB}/trips/${trip.id}/plan/members`);
  console.log(`    Memories   ${WEB}/trips/${trip.id}/memories\n`);
  console.log("  Two-window collaboration demo:");
  console.log("    1. Window A (normal):  log in as alice, open");
  console.log(`       ${dinnerUrl}`);
  console.log("    2. Window B (private): log in as bruno, open the same URL.");
  console.log("    3. Both windows now show each other in the presence bar.");
  console.log("    4. In B, vote for 'Taberna' — A's tally updates live and the card flashes.");
  console.log("    5. In B, post a comment — it appears in A without a reload.");
  console.log("    6. In A, click 'Choose this' on either option — B sees the decision change.");
  if (!UNIQUE) {
    console.log("\n  Credentials are stable, so this demo can be re-run and rehearsed.");
    console.log("  Use SEED_UNIQUE=1 for throwaway accounts instead.");
  }
}

main().catch((err) => {
  console.error("\nSeed failed:", err instanceof Error ? err.message : err);
  process.exit(1);
});
