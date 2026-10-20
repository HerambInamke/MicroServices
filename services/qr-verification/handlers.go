package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/skip2/go-qrcode"
)

type Server struct {
	store   Store
	ledger  Ledger
	baseURL string // public gateway origin encoded into each QR code
}

func (s *Server) routes() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/qr/{pieceId}/generate", s.authed(s.generate)).Methods("POST")
	r.HandleFunc("/qr/{code}/resolve", s.resolve).Methods("GET")
	r.HandleFunc("/qr/{code}/scan-log", s.scanLog).Methods("POST")
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

func ledgerDown(w http.ResponseWriter, err error) {
	log.Printf("ledger call failed: %v", err)
	fail(w, http.StatusBadGateway, "LEDGER_UNAVAILABLE", "could not reach the provenance ledger")
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

// newCode is an opaque, unguessable token, so a code reveals nothing about the piece id.
func newCode() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) resolveURL(code string) string { return s.baseURL + "/api/v1/qr/" + code + "/resolve" }

func (s *Server) generate(w http.ResponseWriter, r *http.Request, userID string) {
	pieceID := mux.Vars(r)["pieceId"]
	p, err := s.ledger.Piece(r.Context(), pieceID)
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "piece not found")
		return
	}
	if err != nil {
		ledgerDown(w, err)
		return
	}
	if p.OwnerID != userID {
		fail(w, http.StatusForbidden, "NOT_OWNER", "only the piece's current owner can generate its QR code")
		return
	}

	// One code per piece for life: it is printed on the tag, so asking again returns the same code.
	status := http.StatusOK
	rec, err := s.store.GetByPiece(r.Context(), pieceID)
	if errors.Is(err, ErrNotFound) {
		status = http.StatusCreated
		rec, err = s.store.Create(r.Context(), Record{
			PieceID: p.ID, ProductID: p.ProductID, Code: newCode(), OwnerID: p.OwnerID, LedgerRef: p.LastHash, EventCount: p.EventCount,
		})
	}
	if err != nil {
		internal(w, err)
		return
	}
	png, err := qrcode.Encode(s.resolveURL(rec.Code), qrcode.Medium, 512)
	if err != nil {
		internal(w, err)
		return
	}
	respond(w, status, map[string]any{
		"pieceId":    rec.PieceID,
		"code":       rec.Code,
		"resolveUrl": s.resolveURL(rec.Code),
		"pngBase64":  base64.StdEncoding.EncodeToString(png),
	}, "", "")
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	rec, err := s.store.GetByCode(r.Context(), mux.Vars(r)["code"])
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "UNKNOWN_CODE", "this code is not registered; the piece may be counterfeit")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	history, err := s.ledger.History(r.Context(), rec.PieceID)
	if err != nil {
		ledgerDown(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"code":      rec.Code,
		"pieceId":   rec.PieceID,
		"history":   history,
		"verifyUrl": "/api/v1/pieces/" + rec.PieceID + "/verify",
	}, "", "")
}

func (s *Server) scanLog(w http.ResponseWriter, r *http.Request) {
	err := s.store.LogScan(r.Context(), mux.Vars(r)["code"])
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "UNKNOWN_CODE", "this code is not registered")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]bool{"logged": true}, "", "")
}
