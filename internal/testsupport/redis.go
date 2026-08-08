package testsupport

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Redis is a running test Redis.
//
// A real server rather than a fake, for the same reason the repository tests run real
// Postgres: the behaviour under test IS the server's. GETDEL's atomicity, pub/sub's
// fire-and-forget delivery, and the fact that a subscriber receives its own publishes are all
// properties of Redis, and a hand-written double would encode whatever the author assumed
// about them — which is exactly the assumption the two-instance test exists to check.
type Redis struct {
	URL       string
	terminate func() error
}

// StartRedis launches Redis 7 and waits for it to answer PING.
//
// Pinned to 7 to match docker-compose.yml. GETDEL, which the multi-instance ticket store
// depends on for single-use redemption, needs 6.2+.
func StartRedis(ctx context.Context) (*Redis, error) {
	container, err := tcredis.Run(ctx, "redis:7-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").WithStartupTimeout(time.Minute),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("starting redis container: %w", err)
	}
	terminate := func() error { return testcontainers.TerminateContainer(container) }

	url, err := container.ConnectionString(ctx)
	if err != nil {
		_ = terminate()
		return nil, fmt.Errorf("building redis connection string: %w", err)
	}

	// The wait strategy proves the log line appeared; this proves the port is actually
	// serving, which is the thing the tests need.
	opts, err := redis.ParseURL(url)
	if err != nil {
		_ = terminate()
		return nil, fmt.Errorf("parsing redis url %q: %w", url, err)
	}
	client := redis.NewClient(opts)
	defer func() { _ = client.Close() }()

	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = terminate()
		return nil, fmt.Errorf("pinging redis: %w", err)
	}

	return &Redis{URL: url, terminate: terminate}, nil
}

// Close terminates the container.
func (r *Redis) Close() error {
	if r == nil {
		return nil
	}
	return r.terminate()
}
