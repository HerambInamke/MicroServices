package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
)

type Server struct {
	store Store

	mu      sync.Mutex
	sockets map[string]int // open WebSockets per user
}

func (s *Server) routes() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/notifications", s.authed(s.list)).Methods("GET")
	r.HandleFunc("/notifications/{id}/read", s.authed(s.markRead)).Methods("PATCH")
	r.HandleFunc("/ws/notifications", s.authed(s.live)).Methods("GET")
	return r
}

// respond writes the {data, error, meta} envelope from api-architecture.md.
func respond(w http.ResponseWriter, status int, data any, code, msg string) {
	var apiErr any
	if code != "" {
		apiErr = map[string]string{"code": code, "message": msg}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"data": data, "error": apiErr, "meta": map[string]any{}})
}

func fail(w http.ResponseWriter, status int, code, msg string) { respond(w, status, nil, code, msg) }

func internal(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	fail(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
}

// authed requires the identity header the gateway sets after verifying the JWT.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-Id")
		if userID == "" {
			fail(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
			return
		}
		h(w, r, userID)
	}
}

// list returns the caller's own inbox, newest first; there is no userId parameter to spoof.
func (s *Server) list(w http.ResponseWriter, r *http.Request, userID string) {
	ns, err := s.store.List(r.Context(), userID, r.URL.Query().Get("unread") == "true")
	if err != nil {
		internal(w, err)
		return
	}
	respond(w, http.StatusOK, ns, "", "")
}

func (s *Server) markRead(w http.ResponseWriter, r *http.Request, userID string) {
	err := s.store.MarkRead(r.Context(), userID, mux.Vars(r)["id"])
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "notification not found")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]bool{"read": true}, "", "")
}
