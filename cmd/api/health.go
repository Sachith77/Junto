package main

import (
	"context"
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
// Read from the embedded VCS stamp Go writes into every binary built from a git checkout, so
// it needs no ldflags in the Dockerfile and cannot drift from a hand-maintained constant. It
// degrades to "dev" when that information is absent, which is the honest answer for `go run`
// and for a build from a dirty or exported tree.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
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
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified == "true" {
		// An uncommitted build is worth flagging: "the deploy is on abc123" means something
		// different when abc123 is only approximately what is running.
		return revision + "-dirty"
	}
	return revision
}
