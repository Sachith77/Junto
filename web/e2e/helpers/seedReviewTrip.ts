// Seeds ONE richly-populated trip through the real HTTP API, so the Plan and Memories
// screens can be reviewed against real data instead of empty states.
//
// This is a REVIEW FIXTURE, not the Stage 4 seed script. It exists because the previous
// checkpoint ended with "another empty state" — every screen here needs days, slots, several
// competing options, votes from more than one person, comments and budget entries before it
// shows what it was designed to show.
//
// Everything goes through the public API: no direct database writes, so anything this script
// can produce is something the product can produce.
//
//   npx tsx e2e/helpers/seedReviewTrip.ts

const API = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
const PASSWORD = "correct horse battery staple";

interface Envelope<T> {
  data: T;
}

async function call<T>(path: string, opts: RequestInit & { token?: string } = {}): Promise<T> {
  const { token, ...rest } = opts;
  for (let attempt = 0; attempt < 10; attempt++) {
    const res = await fetch(`${API}${path}`, {
      ...rest,
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(rest.headers ?? {}),
      },
    });
    // The auth endpoints share a strict per-IP limiter (D35/D36); a seed makes many calls in
    // a row, so back off rather than fail.
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

async function makeUser(name: string, tag: string) {
  const email = `${tag}@junto.local`;
  try {
    await call("/api/v1/auth/signup", {
      method: "POST",
      body: JSON.stringify({ email, password: PASSWORD, display_name: name }),
    });
  } catch (err) {
    // Re-running the seed should be harmless; an existing account is fine.
    if (!String(err).includes("already exists")) throw err;
  }
  const session = await call<{ access_token: string; user: { id: string } }>(
    "/api/v1/auth/login",
    { method: "POST", body: JSON.stringify({ email, password: PASSWORD }) }
  );
  return { email, name, token: session.access_token, id: session.user.id };
}

async function main() {
  const stamp = Date.now().toString(36);
  console.log("Seeding review trip…\n");

  const alice = await makeUser("Alice Moreau", `alice-${stamp}`);
  const bruno = await makeUser("Bruno Alves", `bruno-${stamp}`);
  const mira = await makeUser("Mira Sethi", `mira-${stamp}`);
  console.log(`  owner   ${alice.email}`);
  console.log(`  editor  ${bruno.email}`);
  console.log(`  editor  ${mira.email}`);
  console.log(`  password: ${PASSWORD}\n`);

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
    const msgs = (await fetch("http://localhost:8025/api/v1/messages?limit=50").then((r) =>
      r.json()
    )) as { messages: { ID: string; To: { Address: string }[] }[] };
    const msg = msgs.messages.find((m) => m.To.some((t) => t.Address === guest.email));
    if (!msg) throw new Error(`no invitation email for ${guest.email} — is Mailpit running?`);
    const full = (await fetch(`http://localhost:8025/api/v1/message/${msg.ID}`).then((r) =>
      r.json()
    )) as { Text: string };
    const token = full.Text.match(/invitations\/accept\?token=([A-Za-z0-9_-]+)/)?.[1];
    if (!token) throw new Error(`no accept link for ${guest.email}`);
    await call("/api/v1/invitations/accept", {
      method: "POST",
      token: guest.token,
      body: JSON.stringify({ token }),
    });
  }

  // `after_*: null` means "at the START of the bucket" (fractional indexing, D2) — so
  // creating in chronological order without chaining produces a REVERSED itinerary. Each
  // create is anchored after the previous one instead.
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

  const option = async (
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

  const vote = async (slotId: string, token: string, optionId: string | null) =>
    call(`/api/v1/trips/${trip.id}/slots/${slotId}/votes/me`, {
      method: "PUT",
      token,
      body: JSON.stringify({ option_id: optionId }),
    });

  const comment = async (slotId: string, token: string, body: string) =>
    call(`/api/v1/trips/${trip.id}/slots/${slotId}/comments`, {
      method: "POST",
      token,
      body: JSON.stringify({ body }),
    });

  const choose = async (slotId: string, optionId: string) =>
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
  // Deliberately resolve AGAINST the tally — this is the D41 case the interface must make
  // legible: the group chose the guesthouse even though the apartment had more votes.
  await choose(stay.id, alfama.id);
  await comment(stay.id, bruno.token, "Baixa is cheaper and flatter — my knees have opinions.");
  await comment(stay.id, alice.token, "Booked Alfama in the end, it was the only one with three nights free.");
  await comment(stay.id, mira.token, "Fine by me. The view sells it.");

  const dinner = await slot(d1.id, "Dinner, first night", "activity", "20:00");
  const ramiro = await option(dinner.id, mira.token, "Cervejaria Ramiro", "Queue is real. So are the prawns.", 4500, "Intendente");
  await option(dinner.id, bruno.token, "Taberna da Rua das Flores", "Tiny, no bookings.", 3800, "Cais do Sodré");
  await vote(dinner.id, alice.token, ramiro.id);
  await vote(dinner.id, mira.token, ramiro.id);
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

  // An undecided slot with no options at all, so the empty state is visible too.
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

  const expenses = [
    { label: "Alfama guesthouse, 3 nights", category: "lodging", amount: 44400, payer: alice },
    { label: "Dinner at Ramiro", category: "food", amount: 13650, payer: mira },
    { label: "Train tickets to Sintra", category: "transport", amount: 780, payer: bruno },
    { label: "Pena Palace entry", category: "activity", amount: 4200, payer: bruno },
  ];
  for (const e of expenses) {
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

  console.log("Done.\n");
  console.log(`  Trip:      http://localhost:3000/trips/${trip.id}`);
  console.log(`  Plan:      http://localhost:3000/trips/${trip.id}/plan`);
  console.log(`  Budget:    http://localhost:3000/trips/${trip.id}/plan/budget`);
  console.log(`  Members:   http://localhost:3000/trips/${trip.id}/plan/members`);
  console.log(`  Memories:  http://localhost:3000/trips/${trip.id}/memories`);
  console.log(`\n  Log in as ${alice.email} / ${PASSWORD}`);
}

main().catch((err) => {
  console.error("\nSeed failed:", err instanceof Error ? err.message : err);
  process.exit(1);
});
