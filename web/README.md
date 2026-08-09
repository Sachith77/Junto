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
your machine, run `npm run dev -- -p 3001` and set `CORS_ALLOWED_ORIGINS`/`WEB_BASE_URL` on the
API to match — the API's CORS allowlist is exact-origin-match only (no wildcard), so the two
must agree exactly.

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
- `npm test` — Vitest + React Testing Library component tests.
- `npm run test:e2e` — Playwright, against a **real** running stack (Postgres, Redis, Mailpit,
  the Go API, and this app's dev server) — no mocks. `e2e/helpers/fixtures.ts` seeds a trip via
  the real REST API and reads real verification/invitation emails out of Mailpit's HTTP API
  (`http://localhost:8025`), the same way the backend's own Go tests do it, just over HTTP
  instead of in-process.

  The auth endpoints carry a deliberately strict per-IP rate limit in every real run of the API
  (signup/login/verify/refresh share one bucket — see the backend's D35/D36). A browser-driven
  e2e run generates several of these calls (one per signup, one per verify, one per login, and
  one `/auth/refresh` per **hard** navigation, since the access token is memory-only by design —
  D30). `helpers/fixtures.ts` and `voting.spec.ts` both back off and retry on 429 rather than
  fail, and the test itself avoids hard navigations where a normal `<Link>` click will do. This
  makes a full run take up to a couple of minutes in the worst case; that's the real production
  rate limiter doing its job, not a flaky test.

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
