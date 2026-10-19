package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"

	"github.com/gorilla/mux"
	"golang.org/x/crypto/bcrypt"
)

// Admin is deliberately absent: it can't be self-registered.
var signupRoles = map[string]bool{"weaver": true, "dye-house": true, "wholesaler": true, "retailer": true}

type Server struct {
	store  Store
	secret []byte
}

func (s *Server) routes() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/auth/signup", s.signup).Methods("POST")
	r.HandleFunc("/auth/login", s.login).Methods("POST")
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

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return false
	}
	return true
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	if a, err := mail.ParseAddress(req.Email); err != nil || a.Address != req.Email {
		fail(w, http.StatusBadRequest, "INVALID_EMAIL", "a valid email is required")
		return
	}
	// bcrypt rejects inputs over 72 bytes
	if len(req.Password) < 8 || len(req.Password) > 72 {
		fail(w, http.StatusBadRequest, "INVALID_PASSWORD", "password must be 8-72 characters")
		return
	}
	if !signupRoles[req.Role] {
		fail(w, http.StatusBadRequest, "INVALID_ROLE", "role must be weaver, dye-house, wholesaler or retailer")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "could not create account")
		return
	}
	u, err := s.store.CreateUser(r.Context(), User{Email: req.Email, Phone: req.Phone, PasswordHash: string(hash), Role: req.Role})
	if errors.Is(err, ErrDuplicate) {
		fail(w, http.StatusConflict, "USER_EXISTS", "email or phone already registered")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "could not create account")
		return
	}
	respond(w, http.StatusCreated, map[string]string{"id": u.ID, "email": u.Email, "phone": u.Phone, "role": u.Role}, "", "")
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Email == "" && req.Phone == "" {
		fail(w, http.StatusBadRequest, "INVALID_BODY", "email or phone is required")
		return
	}
	u, err := s.store.FindUser(r.Context(), req.Email, req.Phone)
	if err != nil && !errors.Is(err, ErrNotFound) {
		fail(w, http.StatusInternalServerError, "INTERNAL", "could not log in")
		return
	}
	// Same response for unknown user and wrong password, so accounts can't be enumerated.
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		fail(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email/phone or password")
		return
	}
	access, err1 := issueToken(s.secret, u, tokenAccess, accessTTL)
	refresh, err2 := issueToken(s.secret, u, tokenRefresh, refreshTTL)
	if err1 != nil || err2 != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "could not log in")
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": refresh, "expires_in": int(accessTTL.Seconds()),
	}, "", "")
}
