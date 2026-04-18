package store

import (
	"context"
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
