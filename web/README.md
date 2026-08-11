# Junto web

Next.js (App Router, TypeScript) frontend for Junto. This is the first frontend slice —
Stage 3 (Collaboration UX) presence + voting UI — built directly against the Stage 1/2 backend
in `../`. No frontend existed before this slice.

## Running locally

1. Backend stack (from the repo root):
   ```bash
   docker compose up -d
   go run ./cmd/migrate up
   go run ./cmd/api
   ```
   The API needs `CORS_ALLOWED_ORIGINS` to match wherever this app actually runs (see below).

2. This app:
   ```bash
   npm install
   npm run dev
   ```
   Reads `NEXT_PUBLIC_API_URL` from `.env.local` (defaults to `http://localhost:8080`).

**Port note:** `next dev` defaults to :3000. If that port is already in use by something else on
your machine, run `npm run dev -- -p 3001`, set `CORS_ALLOWED_ORIGINS`/`WEB_BASE_URL` on the API
to match — the API's CORS allowlist is exact-origin-match only (no wildcard), so the two must
agree exactly — and point the e2e suite at it with `PLAYWRIGHT_BASE_URL`.

**The port conflict you will not diagnose in under an hour.** On Windows, two processes CAN both
hold :3000 — one bound to the IPv4 wildcard `0.0.0.0:3000` and another to the IPv6 loopback
`[::1]:3000`. A specific-address bind beats a wildcard, and `localhost` resolves to `::1` first,
so `http://localhost:3000` reaches the *other* program while `http://127.0.0.1:3000` reaches
this app. Symptom: the browser and the e2e suite silently drive an unrelated website, and
Playwright reports something maddening like "waiting for getByLabel('Email')". Diagnose with:

```bash
netstat -ano | findstr :3000     # two LISTENING rows for one port is the tell
curl -s http://127.0.0.1:3000 | grep -o "<title>[^<]*</title>"
curl -s http://localhost:3000   | grep -o "<title>[^<]*</title>"   # different answer = this bug
```

Do **not** work around it by switching everything to `127.0.0.1`: Next's dev server rejects its
own chunk requests with `403` when the origin is not an allowed dev origin, so the page loads,
no JavaScript runs, and every button silently does nothing. Free the port, or move this app to
one nothing else wants.

## Demo data

```bash
npm run seed                 # stable credentials — good for a rehearsed demo
SEED_UNIQUE=1 npm run seed   # throwaway accounts each run
```

Builds a populated trip (three members via the real invitation flow, two days, five slots,
competing options, split votes, comments, a four-entry budget) entirely through the public
API, then prints the logins and a step-by-step two-window collaboration script.

Override `JUNTO_API_URL`, `JUNTO_WEB_URL` and `JUNTO_MAILBOX_URL` to point it at another
environment. It cannot run against production by design — see D106 in the root CLAUDE.md.

## Testing

- `npm run lint` / `npx tsc --noEmit`
- `npm test` — Vitest is configured but **there are no component tests**, so this currently
  exits 1 with "No test files found". Frontend behaviour is covered end-to-end instead, by the
  Playwright suite below. See *Frontend test coverage, corrected* in the root `CLAUDE.md`.
- `npm run test:e2e` — Playwright, against a **real** running stack (Postgres, Redis, Mailpit,
  the Go API, and this app's dev server) — no mocks. `e2e/helpers/fixtures.ts` seeds a trip via
  the real REST API and reads real verification/invitation emails out of Mailpit's HTTP API
  (`http://localhost:8025`), the same way the backend's own Go tests do it, just over HTTP
  instead of in-process.

  The **credential** endpoints carry a deliberately strict per-IP rate limit in every real run
  of the API (signup/login/verify/reset share one bucket — see the backend's D35/D36). A
  browser-driven e2e run generates several of these calls, one per signup, verify and login, so
  `helpers/fixtures.ts` and the specs back off and retry on 429 rather than fail. This can make
  a full run take a couple of minutes in the worst case; that's the real production rate limiter
  doing its job, not a flaky test.

  A consequence worth stating, because it produced two tests that could not pass: **every spec
  must set its own `test.setTimeout`.** The config's 30s default does not survive one 429 backoff,
  let alone the several a spec makes while seeding accounts, and the failure it produces is a
  bare timeout with no assertion — which reads as the feature being broken rather than the budget
  being too small.

  The correctness specs are `voting.spec.ts` (presence, voting, comments — live across two
  browsers), `access-and-revocation.spec.ts` (the non-member gate and revoked-socket behaviour)
  and `create-paths.spec.ts` (building a trip from empty through the UI). `groupA`/`groupBC`/
  `groupD`/`shots` are screenshot harnesses for design review, not assertions.

  `/auth/refresh` is **no longer** in that bucket (D107). It has its own, far more permissive
  limit, because it is not a credential-guessing surface and the strict posture broke ordinary
  navigation: the access token is memory-only by design (D30), so every **hard** navigation
  costs one refresh, and a handful of quick reloads used to exhaust the bucket and render the
  app as signed-out.

## What's here

- `lib/` — typed REST client (`lib/api/*`), the envelope/auth-aware `fetch` wrapper
  (`lib/http.ts`), and `lib/socket.ts`, a framework-agnostic WebSocket client (ticket mint,
  subscribe/resync, reconnect with backoff, op ack/error correlation) — kept dependency-free so
  it's unit-testable without a browser.
- `context/` — `AuthContext` (session restore via the refresh cookie, D30) and
  `TripSocketContext` (owns one subscription per trip, fans presence + op frames out to
  whatever's mounted underneath so components don't each open their own connection).
- `components/PresenceBar.tsx`, `components/VotingSlot.tsx` + `OptionCard.tsx` — Part A and
  Part B of this slice.
- `app/` — enough plumbing to reach a trip and vote on something: signup/login/verify-email/
  invitation-accept, a trip list, and a trip page. Day/slot/option CRUD UI is out of scope for
  this slice; the e2e fixtures create that state directly via the REST API.

## Known limitations (by design, for this slice)

- Presence is trip-level only — the backend's WS hub doesn't track per-slot viewing, and
  building that would be new backend surface, out of scope for a frontend slice.
- `resync_required` is handled by resetting to "no prior state" and re-fetching REST state as
  the new baseline, then resubscribing at the head — correct, but there's a small window
  between the refetch and the resubscribe where a same-instant op could theoretically be missed
  or double-counted. Acceptable for this slice's stated risk tier (no concurrency/race rigor
  required); the backend's own resync guarantee (replay from `since_seq`) isn't fully replicated
  client-side.
