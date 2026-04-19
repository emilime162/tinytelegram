package store

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/redis/go-redis/v9"
)

// ErrPTSNotDurable is returned when WAIT could not confirm replication within the
// configured timeout. Callers should map this to codes.Unavailable.
var ErrPTSNotDurable = errors.New("PTS not durable: Redis WAIT did not receive replica ack")

var RDB *redis.Client

func InitRedis() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	opts := &redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_AUTH"), // empty for local un-authenticated Redis
	}
	if os.Getenv("REDIS_TLS") == "true" {
		// ElastiCache in-transit encryption presents a valid cert signed by AWS CA.
		// Rely on system root CAs by default.
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		// When tunneling via SSM/bastion, clients reach Redis as
		// localhost:<port>; SNI/hostname validation against the ElastiCache
		// endpoint cert will fail. Gate skip behind an explicit env.
		if os.Getenv("REDIS_TLS_INSECURE") == "true" {
			tlsCfg.InsecureSkipVerify = true
		}
		opts.TLSConfig = tlsCfg
	}

	RDB = redis.NewClient(opts)
	if err := RDB.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}
	log.Println("Redis connected")
}

func NextUserPTS(userID string) (int64, error) {
	key := fmt.Sprintf("user:%s:pts", userID)
	return RDB.Incr(context.Background(), key).Result()
}

func GetUserPTS(userID string) (int64, error) {
	key := fmt.Sprintf("user:%s:pts", userID)
	val, err := RDB.Get(context.Background(), key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

func GetUserGateway(userID string) (string, error) {
	key := "presence:" + userID
	return RDB.Get(context.Background(), key).Result()
}

// AllocatePTSWithWait increments both the receiver's and sender's PTS counters,
// then issues a single WAIT to confirm at least one replica has acknowledged
// both writes. On timeout or WAIT error, returns ErrPTSNotDurable without
// attempting any subsequent work.
//
// This implements Layer 1 of the two-layer PTS defense (spec §5.3):
//   - WAIT blocks until all prior writes on this connection have been acked
//     by N replicas, so a single WAIT after both INCRs suffices.
//   - On timeout (no replica ack within timeoutMs), we return ErrPTSNotDurable
//     and the caller MUST NOT proceed with the Postgres INSERT. This is the
//     "reject writes rather than introduce out-of-order messages" behavior
//     per original design §11 (CAP Theorem).
func AllocatePTSWithWait(ctx context.Context, receiverID, senderID string, timeoutMs int) (receiverPTS, senderPTS int64, err error) {
	receiverPTS, err = RDB.Incr(ctx, fmt.Sprintf("user:%s:pts", receiverID)).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("incr receiver: %w", err)
	}
	senderPTS, err = RDB.Incr(ctx, fmt.Sprintf("user:%s:pts", senderID)).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("incr sender: %w", err)
	}
	acked, err := RDB.Do(ctx, "WAIT", 1, timeoutMs).Int64()
	if err != nil {
		return 0, 0, fmt.Errorf("wait: %w", err)
	}
	if acked < 1 {
		return 0, 0, ErrPTSNotDurable
	}
	return receiverPTS, senderPTS, nil
}
