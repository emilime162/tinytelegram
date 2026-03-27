package handler

import (
	"log"
	"net/http"
	"os"

	"tinytelegram/gateway/store"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func WebSocketHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "user_id required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// register user presence in Redis
	gatewayAddr := os.Getenv("GATEWAY_ADDR")
	if err := store.RegisterUser(userID, gatewayAddr); err != nil {
		log.Printf("Failed to register user %s: %v", userID, err)
	}
	log.Printf("User %s connected to gateway %s", userID, gatewayAddr)

	defer func() {
		store.UnregisterUser(userID)
		log.Printf("User %s disconnected", userID)
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		log.Printf("Message from %s: %s", userID, msg)

		// echo back for now, gRPC routing comes next
		conn.WriteMessage(websocket.TextMessage, msg)
	}
}
