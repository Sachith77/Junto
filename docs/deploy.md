# Deploying Junto

Backend on **Render** (Docker web service + free Postgres), frontend on **Vercel**. No Redis in
this deploy — see *Redis: a deliberate scope decision*, below, before assuming that is a gap.

This was originally written for Fly.io. It was retargeted because Fly's free allowance still
asks for a card at signup; Render's free tier genuinely does not. `render.yaml` at the repo
root is the source of truth for the backend's platform config — read it alongside this
document, since the reasoning for each field lives in its comments.

---

## The probe contract (unchanged from the Fly version)

`/livez`, `/readyz`, `/healthz` and the reasoning behind each (D110–D113) are **platform-
independent application design** and did not change in this retarget. If you want the "why",
it is in `CLAUDE.md` and in the doc comments on `internal/transport/http/health_handler.go`.
What changed is which endpoint Render's *single* health-check slot points at, and why —
covered next, because it is the one decision that genuinely differs from Fly and is easy to
get backwards.

### Why Render's health check points at `/livez`, not `/readyz`

Fly runs two independently-configured checks — a fast `/readyz` for routing and a slow,
forgiving `/livez` for "is this process wedged". Render has **one** health-check slot per
service, and it does double duty: during a deploy it gates whether the new instance receives
traffic, and on an already-running instance, "if an instance fails consecutive health checks
for 60 seconds, Render automatically restarts it."

That second behavior is the trap. `/readyz` deliberately fails on a Postgres outage (D111) —
that is correct for a load balancer deciding where to route traffic, but on Render it would
mean a transient database blip, sustained for a minute, causes Render to **restart the whole
process**. That is exactly the outcome D110 exists to prevent: restarting cannot fix the
database, it only discards the connection pool and every in-flight request, making recovery
slower than doing nothing.

`/livez` never consults a dependency, so `render.yaml` points `healthCheckPath` there instead.
This does not open a deploy-time gap: the app pings Postgres and fails fast **before** it
starts listening (`cmd/api/main.go`), so if Postgres is unreachable at boot, `/livez` is never
reachable at all and Render correctly stalls the deploy rather than routing traffic to a
broken instance.

**This is a genuine platform-specific decision, not a preference.** If Junto is ever deployed
somewhere with real multi-instance load-balanced readiness routing again, revisit it — `/readyz`
was the right choice there for a reason, and that reason has not gone away.

### Shutdown timing, retuned for Render specifically

Render's equivalent of Fly's `kill_timeout` is `maxShutdownDelaySeconds` (default 30s, set to
`60` in `render.yaml`). `HTTP_DRAIN_DELAY` is set to `5s` here — shorter than the `8s` Fly used
— and that number is **not** carried over with the same justification. Fly's 8s was sized
against a load balancer polling `/readyz` every 2 seconds and routing away from a failing
instance; Render's free tier is one instance with no equivalent continuous-readiness-based
routing to wait out, so the "go unready, then keep serving" choreography from D112 buys less
here. It is left non-zero as a small, cheap defensive margin — stated honestly as unproven on
this platform, not ported over pretending Fly's reasoning still applies unchanged.

`5s` (`HTTP_DRAIN_DELAY`) + `15s` (`HTTP_SHUTDOWN_TIMEOUT`) = 20s of intended shutdown time
against a 60s budget. Generous margin, not a tight fit — verify this on a real deploy rather
than trusting the arithmetic alone; see *What I could not verify*, below.

---

## Redis: a deliberate scope decision

**Redis is absent from `render.yaml` on purpose.** Not an oversight, not deferred for later —
the multi-instance / Redis pub/sub claim in this project is proven **in CI**, not demonstrated
by this live deploy:

> `tests/multi_instance_api_test.go` — two fully wired instances, two `httptest` servers, one
> Postgres, one Redis container. `TestAnOperationPublishedOnInstanceAReachesAClientOnInstanceB`,
> `TestAHandshakeTicketMintedOnOneInstanceIsRedeemableOnAnother` and
> `TestConcurrentWritesOnBothInstancesConverge` all pass, and the first of those is verified to
> **fail** when instance B's transport is replaced with `NoopTransport` — so it is not a test
> that would pass vacuously.

This deploy runs a single Render instance with no `REDIS_URL` configured at all — confirmed
directly against `cmd/api/main.go` and `cmd/api/health.go` before making this change, not
assumed:

- With no `REDIS_URL`, the process boots normally: in-memory op fan-out, in-memory handshake
  tickets, purely local session revocation, and one `WARN`-level log line at startup. It is a
  supported topology (`configs.RedisConfig`'s own doc comment says so), not a degraded or
  error path.
- The health probes reflect this correctly: `healthProbes()` only registers a Redis check
  `if redisClient != nil`, so `/healthz` never reports a false "redis: down" when Redis was
  never configured in the first place.

**State this plainly wherever the resume claim is stated**, not just here — see the note added
to `CLAUDE.md`'s claims-discipline table. "Redis pub/sub for horizontal scaling" is true and
tested; it is not something a visitor to the live URL can observe, because the live URL is one
instance. Saying so up front is worth more than hoping nobody asks.

---

## First deploy

### 1. Push to a Git host Render can read

Render deploys from a connected GitHub/GitLab repo (or a Blueprint sync from one) — unlike
Fly's CLI-driven `fly deploy`, there is no local push-an-image step. If this repo is not yet on
GitHub, that has to happen first.

### 2. Create the Blueprint

In the Render dashboard: **New → Blueprint**, point it at the repo, let it read `render.yaml`.
Render will offer to create both `junto-api` (the web service) and `junto-db` (the Postgres
instance) from the file. Confirm the service name is available — like Fly's `.fly.dev`
subdomains, Render's `.onrender.com` hostnames are global, and `junto-api` may already be
taken; rename it in `render.yaml` first if so, since `PUBLIC_BASE_URL` in the same file has to
match whatever name you land on.

### 3. Fill in the prompted secrets

Render prompts for every `envVars` entry marked `sync: false` during this first Blueprint
creation, and (per Render's own docs) **ignores them on later blueprint syncs** — so get these
right now rather than expecting to edit `render.yaml` to change them later:

| Key | What to put |
|---|---|
| `JWT_SECRET` | `openssl rand -base64 48` — at least 32 bytes, config validation refuses shorter |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM` | Real SMTP credentials. **Not optional in production** — see below |
| `WEB_BASE_URL`, `CORS_ALLOWED_ORIGINS` | The Vercel frontend's URL, once you know it (step 6) — a placeholder is fine for now, update after |

**SMTP is load-bearing, not a nice-to-have.** `AUTH_AUTO_VERIFY_EMAIL` is refused outright by
config validation in production (D105) — confirmed as pure application logic, gated only on
`cfg.Env.IsProduction()`, with no platform dependency at all. Without working SMTP, **nobody
can complete signup**, because email verification is required to log in (D29). Brevo's free
tier (300 emails/day, no card) is a reasonable choice if you don't have a provider already.

### 4. Deploy

Render deploys automatically once the Blueprint is created and secrets are filled in. Watch the
build log for the `preDeployCommand: /migrate up` step — confirm it actually runs and applies
migrations before the service starts serving. See *What I could not verify* below for why this
specific step deserves a first-time check rather than trust.

### 5. Verify

```bash
curl -s https://junto-api.onrender.com/healthz | jq
```

Expect `"status": "ok"`, `postgres` reporting `"status": "ok"`, **no** `redis` entry in
`checks` at all (not "down" — absent, confirming the probe correctly skipped it), and
`"version"` showing a real git hash, not `"dev"`. If it says `dev`, the builder lost the VCS
stamp — check `.dockerignore` has not started excluding `.git`.

### 6. Deploy the frontend to Vercel

```bash
cd web
vercel # first run links/creates the project and prompts for settings
```

Set `NEXT_PUBLIC_API_URL` to `https://junto-api.onrender.com` in Vercel's project environment
variables. Once Vercel gives you the frontend's URL, go back to Render's dashboard and set
`WEB_BASE_URL` and `CORS_ALLOWED_ORIGINS` to it (both — they must match, or invitation links
resolve to a different origin than the one CORS allows to call the API). A manual redeploy
picks up the change; Render env var updates alone do not restart the service.

---

## What I could not verify

Written down rather than glossed over, per this project's own standing rule about not
asserting confidence that was not earned:

- **`preDeployCommand`'s exact interaction with `runtime: docker` services.** Render's docs
  describe it as running "after the build command but before the start command," which is
  buildpack-shaped phrasing. It is documented as the recommended place for migrations
  regardless of runtime, and the built image contains both `/api` and `/migrate`, so it should
  work — but this was not something I could confirm executes identically for a Docker-runtime
  service from documentation alone. **Verify on the first real deploy** by reading the build
  log for the migration step, not by trusting this document.
- **Whether Render's edge overwrites or appends `X-Forwarded-For` on an untrusted (client-
  supplied) header.** This matters because `cmd/api/main.go` sets `TrustProxyHeaders:
  cfg.Env.IsProduction()` unconditionally — true for any production environment, not written
  with a specific platform in mind — and chi's `middleware.RealIP` (which that flag enables)
  is **explicitly marked deprecated and spoofable by its own maintainers**: it takes the
  *leftmost* `X-Forwarded-For` value, which is safe only if every hop between the internet and
  this code either strips or overwrites client-supplied values rather than blindly appending
  to them. A Render community feature-request thread titled "Send the correct
  X_FORWARDED_FOR" was the one signal found during research, and it was not enough to either
  confirm or rule out the safe behavior with confidence.

  Turning `TrustProxyHeaders` off is not a clean fix either: without it, `RemoteAddr` is
  Render's own proxy IP for every request, which collapses every real user into one shared
  rate-limit bucket — precisely the "everyone shares one IP" problem that shaped D107's
  refresh-endpoint limiter split earlier in this project. Left enabled, on balance, because
  the failure mode of leaving it off (real users throttling each other) is certain and the
  failure mode of leaving it on (spoofing, if Render's edge does not sanitize the header) is
  unconfirmed either way — but **verify before trusting the rate limiter under adversarial
  load**: send a request with a fabricated `X-Forwarded-For` header from outside Render's
  network and confirm the observed limiter bucket is not the spoofed value.

---

## Re-provisioning: the free Postgres will expire

**Render's free PostgreSQL databases expire 30 days after creation** (changed from 90 days in
Render's own May 2024 changelog — if you find an older guide online quoting 90, it is stale).
After expiry there is a 14-day grace period to upgrade to a paid instance before Render deletes
it, data included. Render emails a warning before both deadlines.

When it expires and you want to keep running on the free tier rather than upgrade:

1. **Recreate the database.** In the Render dashboard, delete the expired `junto-db` and create
   a new free Postgres instance with the same name (or update `render.yaml` and re-sync the
   Blueprint, which does the same thing).
2. **Re-migrate.** The new database is empty. Either trigger a redeploy (the
   `preDeployCommand` runs `/migrate up` automatically), or run it manually against the new
   `DATABASE_URL` if the service is already up and you don't want a full redeploy.
3. **Re-seed, if you want demo data back.** `npm run seed` from `web/` builds a demo trip
   through the public API — but **it cannot run against this production deployment**, by
   design (D106): signing in requires a verified email, which only auto-verify (refused in
   production, D105) or a readable mailbox can provide, and production SMTP goes to real
   inboxes. Point the seed script at a staging deployment with a readable mailbox
   (`JUNTO_MAILBOX_URL`), or sign up for real and skip seeding entirely.

This is a real, recurring maintenance cost of the free tier, not a one-time setup step — put a
reminder somewhere you'll actually see it.

---

## What this deployment does not do

- **No Redis, deliberately** — see above. Horizontal scaling is proven in CI, not demonstrated
  live.
- **The demo seed cannot run against production** (D106). See re-provisioning, above, for the
  same reasoning applied to a fresh database.
- **No metrics or tracing.** The probes are health signals, not observability — `/healthz`
  gives a point-in-time answer and nothing historical.
- **Single region for both tiers.** Postgres is the sequencer for every write (D60); a second
  API region would put the row lock an ocean away from half the writers, and Render's free
  Postgres is one instance regardless.
- **The free Postgres is not backed up.** Render's paid tiers add automated backups; the free
  tier does not. Treat anything on it as disposable — which the 30-day expiry already forces.
