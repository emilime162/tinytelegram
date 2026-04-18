package grpc

import (
	"context"
	"fmt"
	"os"
	"testing"

	pb "tinytelegram/message-service/proto"
	"tinytelegram/message-service/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Integration test: requires local Postgres + Redis.
//
// Preflight:
//
//	docker run --rm -d --name tt-pg-test  -p 15432:5432 \
//	  -e POSTGRES_USER=tt_user -e POSTGRES_PASSWORD=tt_pass -e POSTGRES_DB=tinytelegram postgres:16
//	docker run --rm -d --name tt-redis-test -p 16379:6379 redis:7
//
// Then:
//
//	POSTGRES_DSN='postgres://tt_user:tt_pass@localhost:15432/tinytelegram?sslmode=disable' \
//	REDIS_ADDR=localhost:16379 \
//	go test ./grpc/... -v
func initTestStores(t *testing.T) {
	t.Helper()
	if os.Getenv("POSTGRES_DSN") == "" || os.Getenv("REDIS_ADDR") == "" {
		t.Skip("set POSTGRES_DSN and REDIS_ADDR to run integration tests")
	}
	store.InitRedis()
	store.InitPostgres()
	store.RDB.FlushDB(context.Background())
	if _, err := store.DB.Exec("TRUNCATE messages"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestPersistMessage_DuplicatePTSReturnsUnavailable(t *testing.T) {
	initTestStores(t)
	ctx := context.Background()
	srv := &messageServer{}
	msg := &pb.ChatMessage{
		SenderId:   "alice",
		ReceiverId: "bob",
		Content:    "hello",
	}

	// First persist should succeed.
	first, err := srv.PersistMessage(ctx, msg)
	if err != nil {
		// Single-node Redis → WAIT 1 times out → Unavailable. If that happens,
		// the environment is not what this test expects. Surface it clearly.
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
			t.Skipf("WAIT failed on single-node Redis; use a replicated Redis for this test: %v", err)
		}
		t.Fatalf("first persist: %v", err)
	}

	// Forcibly reset Bob's PTS counter to simulate a failover that produced a
	// duplicate. The next PersistMessage will INCR back to first.ReceiverPts,
	// the INSERT will hit uniq_receiver_pts, and we should get Unavailable.
	key := fmt.Sprintf("user:%s:pts", msg.ReceiverId)
	if err := store.RDB.Set(ctx, key, first.ReceiverPts-1, 0).Err(); err != nil {
		t.Fatalf("reset pts: %v", err)
	}
	senderKey := fmt.Sprintf("user:%s:pts", msg.SenderId)
	if err := store.RDB.Set(ctx, senderKey, first.SenderPts, 0).Err(); err != nil {
		t.Fatalf("reset sender pts: %v", err)
	}

	_, err = srv.PersistMessage(ctx, msg)
	if err == nil {
		t.Fatal("expected Unavailable for duplicate PTS; got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected codes.Unavailable, got %v", err)
	}
}
