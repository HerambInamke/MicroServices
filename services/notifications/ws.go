package main

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pingEvery = 30 * time.Second
	pongWait  = 70 * time.Second
	writeWait = 10 * time.Second

	maxSocketsPerUser = 5
)

// Auth is the gateway-verified token, not cookies, so a cross-origin page cannot ride a
// session here; any origin may connect.
var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// live streams the caller's new notifications. The socket is receive-only; history comes from GET /notifications.
func (s *Server) live(w http.ResponseWriter, r *http.Request, userID string) {
	if !s.openSocket(userID) {
		fail(w, http.StatusTooManyRequests, "TOO_MANY_CONNECTIONS", "too many open notification streams")
		return
	}
	defer s.closeSocket(userID)
	msgs, stop, err := s.store.Subscribe(r.Context(), userID)
	if err != nil {
		internal(w, err)
		return
	}
	defer stop()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}
	defer conn.Close()

	// Reading is only for control frames and noticing the client went away.
	closed := make(chan struct{})
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(m)); err != nil {
				log.Printf("ws write to %s failed: %v", userID, err)
				return
			}
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				return
			}
		case <-closed:
			return
		}
	}
}

// Each socket holds its own Redis connection, so cap them per user to protect Redis maxclients.
func (s *Server) openSocket(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sockets == nil {
		s.sockets = map[string]int{}
	}
	if s.sockets[userID] >= maxSocketsPerUser {
		return false
	}
	s.sockets[userID]++
	return true
}

func (s *Server) closeSocket(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sockets[userID]--; s.sockets[userID] <= 0 {
		delete(s.sockets, userID)
	}
}
