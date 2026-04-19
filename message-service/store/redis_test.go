package store

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// These tests require a local Redis reachable at REDIS_ADDR or localhost:6379.
// Run: docker run --rm -d --name tt-redis-test -p 16379:6379 redis:7
// Then: REDIS_ADDR=localhost:16379 go test ./store/...

func testRDB(t *testing.T) *redis.Client {
	t.Helper()
	addr := "localhost:16379"
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis at %s not reachable: %v", addr, err)
	}
	rdb.FlushDB(context.Background())
	return rdb
}

func TestInitRedis_WithAuthRequired(t *testing.T) {
	// Spin up a Redis with requirepass set to verify InitRedis honors REDIS_AUTH.
	// Preflight:
	//   docker run --rm -d --name tt-redis-auth -p 16380:6379 \
	//     redis:7 redis-server --requirepass testpass
	if os.Getenv("TT_REDIS_AUTH_TEST") != "1" {
		t.Skip("set TT_REDIS_AUTH_TEST=1 and run a redis with requirepass=testpass on localhost:16380")
	}
	os.Setenv("REDIS_ADDR", "localhost:16380")
	os.Setenv("REDIS_AUTH", "testpass")
	defer os.Unsetenv("REDIS_ADDR")
	defer os.Unsetenv("REDIS_AUTH")

	InitRedis() // must not Fatalf
	if err := RDB.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping after InitRedis with AUTH: %v", err)
	}
}

func TestAllocatePTSWithWait_HappyPath_NoReplicas(t *testing.T) {
	// Single-node Redis has 0 replicas; WAIT 1 <timeout> will time out.
	// We expect AllocatePTSWithWait to return an error that the caller can map
	// to codes.Unavailable. This exercises the "timeout" branch of the WAIT
	// outcome matrix (spec §5.3).
	RDB = testRDB(t)
	_, _, err := AllocatePTSWithWait(context.Background(), "alice", "bob", 100)
	if err == nil {
		t.Fatal("expected error on single-node Redis (no replicas to ack WAIT); got nil")
	}
}
