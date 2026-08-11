# Deploying the Junto API

Target: **Fly.io**, one app, rolling deploys. The frontend is not covered here — Stage 4
Slice 3 is backend only.

Everything below assumes `fly.toml` at the repo root, which is checked in and commented. Read
it alongside this document; the reasoning for each knob lives next to the knob.

---

## The probe contract

Three endpoints, and the differences between them are the whole design. Anything that treats
them as three names for one check will produce an outage eventually.

| Endpoint | Answers | Checks dependencies? | Fails when |
|---|---|---|---|
| `GET /livez` | "is this process wedged — restart it?" | **No. Never.** | the process cannot answer at all |
| `GET /readyz` | "should the load balancer route here?" | critical only | Postgres unreachable, **or** draining |
| `GET /healthz` | "what is actually wrong?" | all | same as readyz; also reports `degraded` |

All three are unauthenticated (an orchestrator cannot hold a credential), return
`Cache-Control: no-store`, are mounted at the root rather than under `/api/v1` so the rate
limiter cannot throttle them, and are excluded from request logging so a 2-second poll does
not bury the log.

### Why liveness checks nothing

A liveness probe that pings Postgres looks more thorough and is actively harmful. When the
database blips, *every* instance fails liveness at the same moment and the orchestrator
restarts all of them — and restarting the API cannot fix the database. It discards the warm
connection pool and every in-flight request, so recovery is strictly slower than doing
nothing. Liveness answers only "is this process itself stuck".

`tests/health_api_test.go::TestLivenessIgnoresDependencies` fails if anyone "improves" this.

### Why Redis does not fail readiness

Redis is probed and reported, but it is **not critical**. Two reasons:

1. If it were, a Redis outage would pull every instance out of rotation simultaneously — a
   degradation becomes a total outage.
2. The system is designed to absorb it. No `REDIS_URL` at all is a supported single-instance
   topology, and when it *is* configured and drops, D75's log-backed gap fill and D76's
   reconcile tick repair cross-instance delivery from the operation log. The cost is latency,
   not lost writes.

Postgres has no equivalent degraded mode — every read, every write, and every `op_seq`
allocation goes through it — which is what makes it the one critical probe.

Object storage is not probed at all: its failure affects attachments, not the API, and probing
it would make a third party's uptime silently become ours.

### `/healthz` example

```json
{
  "status": "degraded",
  "version": "a64349afe112",
  "checks": [
    { "name": "postgres", "status": "ok",   "critical": true  },
    { "name": "redis",    "status": "down", "critical": false }
  ]
}
```

`version` is the git revision Go stamps into the binary — the fastest way to confirm which
build is actually serving. `-dirty` means it was built from an uncommitted tree.

There is deliberately **no error detail** in any probe response. A driver error naming a host
and port is exactly the internal topology an unauthenticated endpoint must not publish; the
detail goes to the logs instead, at ERROR for a critical probe and WARN for a degraded one.

---

## The shutdown sequence, and the two timeouts that must agree

This is the part that breaks quietly, so it is written out in order:

1. Fly sends `SIGTERM`.
2. The process **fails readiness immediately** (`health.BeginDraining()`) and keeps serving.
3. It waits `HTTP_DRAIN_DELAY`. Fly's readiness check fails repeatedly during this window and
   takes the machine out of the pool — while it is still answering every request normally.
4. Only then does `server.Shutdown` run, bounded by `HTTP_SHUTDOWN_TIMEOUT`.

Skipping step 3 is the classic "graceful shutdown that still 502s": `Shutdown` stops accepting
new connections instantly, but the load balancer does not find out until its next probe, and
every request routed in between fails for no reason other than the deploy.

**Two constraints, both easy to violate by editing one number:**

```
HTTP_DRAIN_DELAY  >  readiness interval × failures needed     (8s > 2s × ~4)
fly kill_timeout  >  HTTP_DRAIN_DELAY + HTTP_SHUTDOWN_TIMEOUT (40s > 8s + 20s)
```

If `kill_timeout` is too low, Fly `SIGKILL`s the process partway through the drain — which is
worse than no drain at all, because connections are cut at an arbitrary moment instead of a
chosen one. Fly's default is 5s, so this **must** be set explicitly.

`HTTP_DRAIN_DELAY` defaults to `0` in development (nobody wants Ctrl+C to pause for eight
seconds) and to `8s` in production, keyed off whether the variable was set at all — an
operator who deliberately writes `HTTP_DRAIN_DELAY=0` in production keeps that decision.

---

## First deploy

### 1. Provision

```bash
fly launch --no-deploy              # or: fly apps create junto-api
fly postgres create --name junto-db
fly postgres attach junto-db        # sets DATABASE_URL
```

Redis is **optional**. Skip it and the app runs single-instance, which is a supported
topology and says so at WARN on startup. Add it before scaling past one machine — without it
a handshake ticket minted on one instance cannot be redeemed on another, and neither
instance's subscribers see the other's writes.

```bash
fly redis create                    # then: fly secrets set REDIS_URL=...
```

### 2. Secrets

Config has no defaults for secrets: the process refuses to start rather than run with a
well-known key.

```bash
fly secrets set \
  JWT_SECRET="$(openssl rand -base64 48)" \
  SMTP_HOST=... SMTP_PORT=587 SMTP_USERNAME=... SMTP_PASSWORD=... \
  SMTP_FROM="Junto <no-reply@yourdomain>" SMTP_USE_TLS=true \
  PUBLIC_BASE_URL=https://junto-api.fly.dev \
  WEB_BASE_URL=https://your-frontend \
  CORS_ALLOWED_ORIGINS=https://your-frontend
```

Production validation will reject the deploy at boot if any of these is wrong, which is the
intent — see `configs/config.go`. In particular it refuses:

- `JWT_SECRET` shorter than 32 bytes
- `sslmode=disable` in `DATABASE_URL`
- a non-`https://` `PUBLIC_BASE_URL` or `WEB_BASE_URL`
- `AUTH_AUTO_VERIFY_EMAIL=true` (D105) — this one is refused outright, not ignored, because an
  operator who set it believes signups are auto-verified
- `STORAGE_ENDPOINT` set without its credentials

Attachments need object storage. Point `STORAGE_*` at S3 or R2 — `minio-go` presigns
identically against all three (D50). Leave `STORAGE_ENDPOINT` unset and the attachment routes
simply are not mounted.

### 3. Deploy

```bash
fly deploy
```

`release_command = "/migrate up"` runs the embedded migrations from the image being deployed,
before any new machine takes traffic — so the schema cannot be applied by a different build
than the one about to serve it. A failing release aborts the deploy and the old machines keep
serving.

### 4. Verify

```bash
curl -s https://junto-api.fly.dev/healthz | jq
```

Check `version` matches `git rev-parse --short=12 HEAD`. If it says `dev`, the builder lost
the git stamp — usually `.git` got excluded from the Docker context.

---

## Runbook

**A machine is failing readiness.** `fly logs` — a critical probe failure is logged at ERROR
with the underlying error the HTTP response withholds. Almost always Postgres.

**`/healthz` says `degraded`.** Redis is unreachable. Cross-instance fan-out is running on the
log-repair path, so writes still converge but cross-instance delivery is slower. Not urgent at
one machine; urgent at more than one.

**Deploy 502s.** Check the two timeout constraints above. Most likely `kill_timeout` was
lowered below `HTTP_DRAIN_DELAY + HTTP_SHUTDOWN_TIMEOUT`, or the readiness interval was raised
without raising the drain delay.

**Rolling back.** `fly releases` then `fly deploy --image <previous>`. Migrations are additive
by convention, so the previous build runs against the newer schema. A migration that is *not*
additive breaks this property and needs a plan of its own before it ships.

---

## What this deployment does not do

Stated so nobody has to find out at an awkward moment:

- **The demo seed cannot run against it** (D106). Seeding needs a verified account, which needs
  either auto-verify (refused in production) or a readable mailbox (production SMTP goes to
  real inboxes). Point `npm run seed` at a staging stack with `JUNTO_MAILBOX_URL` instead. The
  script fails with that instruction rather than a confusing error.
- **No frontend.** `web/` is deployed separately and is not part of this slice.
- **No metrics or tracing.** The probes are health signals, not observability. `/healthz` gives
  a point-in-time answer and nothing historical.
- **Single region.** `primary_region` only. Postgres is the sequencer for every write (D60), so
  a second region would put the row lock an ocean away from half the writers.
