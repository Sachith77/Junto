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

### Shutdown timing, retuned for Render specifically — and unconfigurable on the free plan

Render's equivalent of Fly's `kill_timeout` is `maxShutdownDelaySeconds`. **It is rejected by
the free-plan Blueprint validator** — confirmed by the actual rejection when first deploying
this Blueprint, not by anything in Render's docs, which don't mention the restriction at all.
`render.yaml` no longer sets it, which means the window between SIGTERM and SIGKILL is
whatever Render's platform default is, with no way on this plan to raise it.

That default is documented plainly, independent of tier: "a configurable shutdown delay
(default 30 seconds)... if the process is still running after the shutdown delay, Render sends
SIGKILL." Nothing found states a *different* default specifically for the free tier, and the
field being paid-only reads most naturally as "you may only pay to raise the ceiling," not "the
ceiling itself differs by tier" — but that is an inference from the general product docs, not a
free-tier-specific confirmation, and it is stated here as exactly that rather than asserted as
fact. **Treat 30s as the working assumption, not a verified number, until a real deploy proves
it out** — watching a real shutdown complete cleanly (see step 5, below) is the only way to
close this gap for certain.

`HTTP_DRAIN_DELAY` is `5s` — shorter than the `8s` Fly used, and that number was never carried
over with Fly's justification attached: Fly's 8s was sized against a load balancer polling
`/readyz` every 2 seconds and routing away from a failing instance, which Render's single
free-tier instance has no equivalent of, so the "go unready, then keep serving" choreography
from D112 buys less here. It is left non-zero as a small, cheap defensive margin — stated
honestly as unproven on this platform, not ported over pretending Fly's reasoning still
applies unchanged.

`5s` (`HTTP_DRAIN_DELAY`) + `15s` (`HTTP_SHUTDOWN_TIMEOUT`) = **20s of intended shutdown time
against an assumed 30s budget — 10s of margin**, down from the 40s of margin the earlier,
now-rejected `maxShutdownDelaySeconds: 60` gave. Still real margin, not a tight fit, but
genuinely smaller than the first version of this deploy planned for. If a real deploy ever
shows shutdown taking longer than expected, this is the number that needs revisiting — and
there is no lever left on this plan to buy more room by raising Render's side of the equation,
only by lowering `HTTP_DRAIN_DELAY` and `HTTP_SHUTDOWN_TIMEOUT` further.

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

**Render will immediately try to build and boot `junto-api`, and it will fail.** Expect this —
the database Blueprint just created is empty, no migration hook exists on this plan (see
below), and the app's own boot sequence deliberately refuses to start listening against an
unmigrated schema (`cmd/api/main.go`'s `verifySchema` checks for the `attachments` table — the
last one migrations create — before the server ever binds a port, and fails with `"database
schema is not initialised: run `go run ./cmd/migrate up`"` if it's missing). That crash is the
app behaving correctly, not a broken deploy. Step 4 fixes it.

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

### 4. Run migrations — manual, every time, no automatic hook on this plan

`preDeployCommand` (Render's `release_command` equivalent) is **rejected outright by the
free-plan Blueprint validator** — confirmed by the actual rejection when first setting this up,
not documented anywhere findable on Render's side. There is no automatic migration hook on
this plan at all. This is not a one-time setup gap: **it applies to every future deploy that
changes the schema, not just this first one.**

Shell/SSH access, which would be the other obvious way to run a command against the running
instance, is also confirmed off the free tier entirely ("Free web services cannot... offer
SSH/shell access"). So the only remaining path is running the migration tool from your own
machine, against Render Postgres's **external** connection string — a different value from the
`DATABASE_URL` the API service uses internally, and the distinction matters:

1. In the Render dashboard, open the `junto-db` Postgres instance (not the `junto-api`
   service) → its **Info** page → the **Connections** section. Copy the **External Database
   URL** (or the **PSQL Command**, if you'd rather connect with `psql` directly to poke around
   — the migration tool needs the URL form). This is *not* the same string
   `render.yaml`'s `DATABASE_URL` resolves to: that one (`fromDatabase` → `connectionString`)
   is Render's **internal** URL, reachable only from inside Render's own network, and will not
   resolve from your machine.
2. From the repo root, run the migration tool locally with that external URL:
   ```bash
   DATABASE_URL="<the external connection string you just copied>" go run ./cmd/migrate up
   ```
   Render Postgres external connections use TLS by default and are reachable from any IP with
   valid credentials (nothing found suggests the free tier restricts or disables this — it
   appears to be the same behavior as paid tiers), so no extra flags should be needed for a
   `postgresql://...` URL in that form.
3. Confirm it worked — the tool prints the version it landed on:
   ```
   msg="migrations applied" version=<N>
   ```
4. **Only now** does the service have a schema to boot against. If the first deploy attempt
   already crash-looped per step 2's warning, go to the `junto-api` service in the dashboard
   and trigger a manual deploy/restart (Render's dashboard has a control for this — exact
   wording not independently confirmed here, look for "Manual Deploy" or "Restart" near the
   top of the service page) so it retries booting now that `verifySchema` will pass.

**For every deploy after this one that changes the schema**, the safe order is **migrate
first, deploy second** — run step 4's command against the external URL while the *old* code is
still live and serving, then push/trigger the new deploy. This is safe specifically because
migrations in this project are additive by convention (the previous build stays compatible
with a newer schema, stated already in `render.yaml`'s own comments on `release_command`-style
reasoning) — deploying new code that expects a new column or table *before* that column or
table exists is the ordering that breaks.

### 5. Verify

```bash
curl -s https://junto-api.onrender.com/healthz | jq
```

Expect `"status": "ok"`, `postgres` reporting `"status": "ok"`, **no** `redis` entry in
`checks` at all (not "down" — absent, confirming the probe correctly skipped it), and
`"version"` showing a real git hash, not `"dev"`. If it says `dev`, the builder lost the VCS
stamp — check `.dockerignore` has not started excluding `.git`.

Also worth confirming here, since it's now unverified rather than configured: watch a deploy's
old instance actually shut down cleanly within Render's (now unconfigurable) default window
rather than getting killed abruptly — see *Shutdown timing*, above.

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

- **`preDeployCommand` turned out to be moot** — superseded, not resolved. It doesn't exist in
  `render.yaml` any more: the free-plan Blueprint validator rejects it outright, confirmed by
  the actual rejection rather than by anything Render documents. The open question is no
  longer "does it behave correctly for a Docker runtime," it's "does the manual replacement
  procedure (*Run migrations — manual*, above) actually work as described" — specifically,
  whether the `junto-api` service's dashboard genuinely offers a manual redeploy/restart
  control while it's crash-looping on a missing schema, since that exact scenario (a service
  that has never once booted successfully) wasn't something a documentation search could
  confirm one way or the other. **Verify on the first real deploy.**
- **Whether Render's free tier shares the platform's general 30s default shutdown delay, or
  has a shorter unconfirmed one of its own.** `maxShutdownDelaySeconds` — the field that would
  have let this be stated with certainty — is also rejected on the free plan. Render's docs
  state the 30s default without a tier caveat either way, and the field being paid-only reads
  most naturally as "you may pay to raise the ceiling," not "the ceiling itself differs by
  tier" — but that is an inference, not a confirmation. See *Shutdown timing*, above, for the
  arithmetic this assumption feeds into (10s of margin if 30s holds).
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
2. **Re-migrate — manually, same as every other migration on this plan.** The new database is
   empty and nothing runs migrations for you (see *Run migrations — manual*, above). Get the
   new instance's external connection string from its Info page and run
   `DATABASE_URL="<external URL>" go run ./cmd/migrate up` locally before the `junto-api`
   service tries to serve against it — `verifySchema` will keep it crash-looping until you do.
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
