package main

import (
	"context"
	"os"
	"runtime/debug"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	junto "github.com/junto/junto/internal/transport/http"
)

// healthProbes builds the dependency checks the probe endpoints run.
//
// They live in cmd rather than in the transport package because this is the only layer that
// is allowed to know a pgxpool and a redis.Client exist — transport may not import a
// repository or a driver, and junto.Probe is deliberately shaped as a name plus a closure so
// the composition root can supply them without leaking either type upward.
//
// Which probes are CRITICAL is the load-bearing part, and it is not "all of them":
//
//   - Postgres is critical. There is no degraded mode without it. Every read, every write and
//     every op_seq allocation (D60) goes through it, so an instance that cannot reach it has
//     nothing useful to serve and should leave the pool.
//   - Redis is NOT critical, and its absence is not even a fault — no REDIS_URL is a
//     supported single-instance topology. When it IS configured and unreachable, the system
//     degrades exactly as designed: local fan-out keeps working, and D75's log-backed gap
//     fill plus D76's reconcile tick repair cross-instance delivery from the operation log,
//     so the cost is latency rather than lost writes. Failing readiness on it would pull
//     EVERY instance out of rotation at the same moment and convert a degradation into a
//     total outage.
//
// Object storage is deliberately not probed at all. Attachments mount only when it is
// configured, its failure affects one feature rather than the API, and a probe on it would be
// a third-party dependency's uptime silently becoming ours.
func healthProbes(pool *pgxpool.Pool, redisClient *redis.Client) []junto.Probe {
	probes := []junto.Probe{{
		Name:     "postgres",
		Critical: true,
		Check: func(ctx context.Context) error {
			// Ping rather than a query: it takes a pooled connection, round-trips, and
			// returns it. A SELECT would add nothing except a plan to cache, and anything
			// heavier would make the probe itself a load source at the platform's polling
			// rate.
			return pool.Ping(ctx)
		},
	}}

	// Nil when no REDIS_URL was configured. Reporting "down" for a dependency the operator
	// deliberately did not configure would be a permanent false alarm, so it is simply absent
	// from the list — /healthz then shows what this instance actually has.
	if redisClient != nil {
		probes = append(probes, junto.Probe{
			Name:     "redis",
			Critical: false,
			Check: func(ctx context.Context) error {
				return redisClient.Ping(ctx).Err()
			},
		})
	}

	return probes
}

// buildVersion reports the running build, for /healthz.
//
// Two sources, tried in order — and the reason there are two rather than one is a real finding
// from the first Render deploy, not a hedge added in advance.
//
//  1. The embedded VCS stamp Go writes into every binary built from a `git checkout` with
//     `.git` present alongside the source it is compiling. This needs no ldflags and cannot
//     drift from a hand-maintained constant, which is why it stays the PRIMARY source — it is
//     confirmed correct for every build path this project controls directly: `go run`,
//     `docker build` run locally (verified against a real image: a fresh `--no-cache` build
//     produced `vcs.revision=9e2c50e6e757...` matching `HEAD` exactly), and CI.
//  2. `RENDER_GIT_COMMIT`, which Render sets to the deployed commit SHA. This is the fallback,
//     used only when (1) comes back empty — which is exactly what happened on the first real
//     Render deploy: `/healthz` reported "dev" even though the Dockerfile and `.dockerignore`
//     were independently proven correct by the same local rebuild referenced above. That
//     isolates the cause to Render's build pipeline specifically: whatever Render hands to
//     `docker build` as context, it does not appear to include a `.git` directory Go's
//     toolchain can read — Render does not document this either way, so it was confirmed by
//     elimination (our Dockerfile works everywhere `.git` is present; it stops working only
//     on Render) rather than found stated anywhere.
//
// RENDER_GIT_COMMIT is documented as available at both build time AND runtime, with no
// exception noted for Docker-runtime services specifically and no plan-tier restriction
// mentioned — unlike `preDeployCommand` and `maxShutdownDelaySeconds`, both of which turned
// out to be free-tier-rejected despite identically silent docs. Reading it here at RUNTIME via
// os.Getenv, rather than threading it through as a Docker build-arg and baking it in via
// ldflags, is deliberately the simpler of the two available mechanisms: it needed zero
// Dockerfile changes and zero new build-time plumbing, only this fallback. **Not yet confirmed
// by an actual Render deploy showing a real hash** — the absence of a documented tier
// restriction is not the same as a deploy proving it works, and that is the next thing to
// check after this ships.
//
// A build with neither source available (a `go run` with no git repo present, or a context
// that also lacks RENDER_GIT_COMMIT) still degrades to "dev" — the same honest fallback as
// before, not a new failure mode.
func buildVersion() string {
	if v := gitBuildVersion(); v != "" {
		return v
	}
	if commit := os.Getenv("RENDER_GIT_COMMIT"); commit != "" {
		return truncateRevision(commit)
	}
	return "dev"
}

// gitBuildVersion reads Go's embedded VCS stamp. Returns "" — not "dev" — when unavailable, so
// buildVersion can tell "nothing here" apart from "checked and there is truly nothing at all"
// and fall through to RENDER_GIT_COMMIT before giving up.
func gitBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return ""
	}
	revision = truncateRevision(revision)
	if modified == "true" {
		// An uncommitted build is worth flagging: "the deploy is on abc123" means something
		// different when abc123 is only approximately what is running. RENDER_GIT_COMMIT has
		// no equivalent signal — a platform deploying a pushed commit has no "dirty tree" to
		// report — so this suffix is specific to the git-stamp path and does not carry over.
		return revision + "-dirty"
	}
	return revision
}

func truncateRevision(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
