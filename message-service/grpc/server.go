package grpc

import (
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"

	pb "tinytelegram/message-service/proto"
	"tinytelegram/message-service/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type messageServer struct {
	pb.UnimplementedMessageServiceServer
}

const ptsWaitTimeoutMs = 100

func (s *messageServer) PersistMessage(ctx context.Context, msg *pb.ChatMessage) (*pb.PersistedMessage, error) {
	receiverPTS, senderPTS, err := store.AllocatePTSWithWait(ctx, msg.ReceiverId, msg.SenderId, ptsWaitTimeoutMs)
	if err != nil {
		// All four WAIT outcome rows from spec §5.3 table must surface as
		// codes.Unavailable so the client retries:
		//   - ErrPTSNotDurable      (explicit timeout, acked < 1)
		//   - INCR/WAIT transport   (primary crashed mid-call)
		//   - context timeout       (caller timeout)
		// Only catastrophic-unexpected errors should be codes.Internal; we
		// don't have a way to distinguish here, so we err on the side of
		// Unavailable + let Layer 2 catch any duplicate that sneaks through.
		_ = errors.Is(err, store.ErrPTSNotDurable) // retained as a breadcrumb in case logging wants it
		return nil, status.Errorf(codes.Unavailable, "PTS not durable: %v", err)
	}

	msgID := uuid.NewString()
	_, err = store.DB.ExecContext(
		ctx,
		`INSERT INTO messages (id, sender_id, receiver_id, content, sender_pts, receiver_pts)
         VALUES ($1, $2, $3, $4, $5, $6)`,
		msgID, msg.SenderId, msg.ReceiverId, msg.Content, senderPTS, receiverPTS,
	)
	if err != nil {
		if isUniqueViolation(err) {
			// Layer 2 backstop: Redis produced a duplicate PTS across a failover
			// window. The INSERT correctly rejected it; surface as Unavailable.
			return nil, status.Error(codes.Unavailable, "PTS uniqueness violation, retry")
		}
		return nil, status.Errorf(codes.Internal, "insert message: %v", err)
	}

	return &pb.PersistedMessage{
		Id:              msgID,
		Message:         msg,
		ReceiverPts:     receiverPTS,
		SenderPts:       senderPTS,
		ServerTimestamp: time.Now().UnixMilli(),
	}, nil
}

// isUniqueViolation returns true if err is a Postgres UNIQUE constraint violation.
// We use string matching on the lib/pq error rather than adding a dependency on
// a richer error package — simpler and sufficient for this check.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate key value violates unique constraint") ||
		strings.Contains(err.Error(), "uniq_receiver_pts") ||
		strings.Contains(err.Error(), "uniq_sender_pts")
}

func (s *messageServer) GetDiff(ctx context.Context, req *pb.GetDiffRequest) (*pb.GetDiffResponse, error) {
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	rows, err := store.DB.QueryContext(
		ctx,
		`SELECT id, sender_id, receiver_id, content,
		        CASE WHEN receiver_id = $1 THEN receiver_pts ELSE sender_pts END AS pts,
		        created_at
		 FROM messages
		 WHERE (receiver_id = $1 OR sender_id = $1)
		   AND CASE WHEN receiver_id = $1 THEN receiver_pts ELSE sender_pts END > $2
		 ORDER BY pts ASC
		 LIMIT $3`,
		req.UserId,
		req.ClientPts,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]*pb.PersistedMessage, 0)
	for rows.Next() {
		var (
			id         string
			senderID   string
			receiverID string
			content    string
			pts        int64
			createdAt  time.Time
		)

		if err := rows.Scan(&id, &senderID, &receiverID, &content, &pts, &createdAt); err != nil {
			return nil, err
		}

		pm := &pb.PersistedMessage{
			Id: id,
			Message: &pb.ChatMessage{
				SenderId:   senderID,
				ReceiverId: receiverID,
				Content:    content,
			},
			ServerTimestamp: createdAt.UnixMilli(),
		}
		if receiverID == req.UserId {
			pm.ReceiverPts = pts
		} else {
			pm.SenderPts = pts
		}
		messages = append(messages, pm)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	currentPTS, err := store.GetUserPTS(req.UserId)
	if err != nil {
		return nil, err
	}

	return &pb.GetDiffResponse{
		Messages:   messages,
		CurrentPts: currentPTS,
	}, nil
}

func (s *messageServer) GetUserPts(ctx context.Context, req *pb.PtsRequest) (*pb.PtsResponse, error) {
	pts, err := store.GetUserPTS(req.UserId)
	if err != nil {
		return nil, err
	}
	return &pb.PtsResponse{Pts: pts}, nil
}

func StartGRPCServer(port string) {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("MessageService gRPC listen error: %v", err)
	}

	server := grpc.NewServer()
	pb.RegisterMessageServiceServer(server, &messageServer{})

	log.Printf("MessageService gRPC server starting on port %s", port)
	if err := server.Serve(lis); err != nil {
		log.Fatalf("MessageService gRPC serve error: %v", err)
	}
}
