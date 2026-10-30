package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/crypto/bcrypt"
)

// Admin is deliberately absent: it can't be self-registered.
var signupRoles = map[string]bool{"weaver": true, "dye-house": true, "wholesaler": true, "retailer": true}

var walletRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

type Server struct {
	store  Store
	secret []byte
}

func (s *Server) routes() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/auth/signup", s.signup).Methods("POST")
	r.HandleFunc("/auth/login", s.login).Methods("POST")
	r.HandleFunc("/auth/refresh", s.refresh).Methods("POST")
	r.HandleFunc("/auth/logout", s.authed(s.logout)).Methods("POST")
	r.HandleFunc("/auth/me", s.authed(s.me)).Methods("GET")
	r.HandleFunc("/auth/wallet/challenge", s.authed(s.walletChallenge)).Methods("POST")
	r.HandleFunc("/auth/wallet/link", s.authed(s.linkWallet)).Methods("POST")
	r.HandleFunc("/auth/wallet/unlink", s.authed(s.unlinkWallet)).Methods("DELETE")
	r.HandleFunc("/users/{id}", s.authed(s.getUser)).Methods("GET")
	r.HandleFunc("/users/{id}/role", s.authed(s.setRole)).Methods("PATCH")
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

func internal(w http.ResponseWriter) {
	fail(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return false
	}
	return true
}

type identity struct{ userID, role string }

// authed requires the identity headers the gateway sets after verifying the JWT.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := identity{userID: r.Header.Get("X-User-Id"), role: r.Header.Get("X-User-Role")}
		if id.userID == "" {
			fail(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
			return
		}
		h(w, r, id)
	}
}

func userView(u User) map[string]string {
	return map[string]string{"id": u.ID, "email": u.Email, "phone": u.Phone, "role": u.Role, "walletAddress": u.WalletAddress}
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
		internal(w)
		return
	}
	u, err := s.store.CreateUser(r.Context(), User{Email: req.Email, Phone: req.Phone, PasswordHash: string(hash), Role: req.Role})
	if errors.Is(err, ErrDuplicate) {
		fail(w, http.StatusConflict, "USER_EXISTS", "email or phone already registered")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	respond(w, http.StatusCreated, userView(u), "", "")
}

// issuePair signs an access/refresh pair and records the refresh token so it can be revoked.
func (s *Server) issuePair(w http.ResponseWriter, r *http.Request, u User) {
	access, _, err1 := issueToken(s.secret, u, tokenAccess, accessTTL)
	refresh, rc, err2 := issueToken(s.secret, u, tokenRefresh, refreshTTL)
	if err1 != nil || err2 != nil {
		internal(w)
		return
	}
	if err := s.store.SaveRefresh(r.Context(), rc.ID, u.ID, rc.ExpiresAt.Time); err != nil {
		internal(w)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"accessToken": access, "refreshToken": refresh, "expiresIn": int(accessTTL.Seconds()),
	}, "", "")
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
		internal(w)
		return
	}
	// Same response for unknown user and wrong password, so accounts can't be enumerated.
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		fail(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email/phone or password")
		return
	}
	s.issuePair(w, r, u)
}

// refresh rotates the refresh token: the presented one is revoked and a new pair issued.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := parseToken(s.secret, req.RefreshToken, tokenRefresh)
	if err != nil {
		fail(w, http.StatusUnauthorized, "INVALID_TOKEN", "refresh token is invalid or expired")
		return
	}
	userID, err := s.store.UseRefresh(r.Context(), c.ID)
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusUnauthorized, "INVALID_TOKEN", "refresh token is invalid or expired")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	// Reload so a role change since login is reflected in the new tokens.
	u, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		fail(w, http.StatusUnauthorized, "INVALID_TOKEN", "refresh token is invalid or expired")
		return
	}
	s.issuePair(w, r, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, id identity) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := parseToken(s.secret, req.RefreshToken, tokenRefresh)
	if err != nil || c.Subject != id.userID {
		fail(w, http.StatusBadRequest, "INVALID_TOKEN", "a valid refresh token for this user is required")
		return
	}
	if err := s.store.RevokeRefresh(r.Context(), c.ID, id.userID); err != nil && !errors.Is(err, ErrNotFound) {
		internal(w)
		return
	}
	respond(w, http.StatusOK, map[string]bool{"loggedOut": true}, "", "")
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, id identity) {
	u, err := s.store.GetUser(r.Context(), id.userID)
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "USER_NOT_FOUND", "user no longer exists")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	respond(w, http.StatusOK, userView(u), "", "")
}

// walletChallenge returns the message the wallet must personal_sign to prove it owns the address.
func (s *Server) walletChallenge(w http.ResponseWriter, r *http.Request, id identity) {
	var req struct {
		Address string `json:"address"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !walletRe.MatchString(req.Address) {
		fail(w, http.StatusBadRequest, "INVALID_ADDRESS", "address must be a 0x-prefixed 40-hex-digit wallet address")
		return
	}
	expires := time.Now().Add(challengeTTL).Unix()
	respond(w, http.StatusOK, map[string]any{
		"message":   challengeMessage(s.secret, id.userID, strings.ToLower(req.Address), expires),
		"expiresAt": time.Unix(expires, 0).UTC().Format(time.RFC3339),
	}, "", "")
}

// linkWallet links the address only if it signed a fresh challenge issued to this user for it.
func (s *Server) linkWallet(w http.ResponseWriter, r *http.Request, id identity) {
	var req struct {
		Address   string `json:"address"`
		Message   string `json:"message"`
		Signature string `json:"signature"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !walletRe.MatchString(req.Address) {
		fail(w, http.StatusBadRequest, "INVALID_ADDRESS", "address must be a 0x-prefixed 40-hex-digit wallet address")
		return
	}
	// Lowercase so checksum/non-checksum forms of one address can't be linked twice.
	addr := strings.ToLower(req.Address)
	if !checkChallenge(s.secret, req.Message, id.userID, addr, time.Now()) {
		fail(w, http.StatusBadRequest, "INVALID_CHALLENGE", "message must be an unexpired challenge from /auth/wallet/challenge for this address")
		return
	}
	if signer, err := recoverAddress(req.Message, req.Signature); err != nil || signer != addr {
		fail(w, http.StatusBadRequest, "INVALID_SIGNATURE", "signature must be the address's personal_sign of the challenge message")
		return
	}
	switch err := s.store.SetWallet(r.Context(), id.userID, addr); {
	case errors.Is(err, ErrDuplicate):
		fail(w, http.StatusConflict, "WALLET_IN_USE", "wallet is already linked to another account")
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "USER_NOT_FOUND", "user no longer exists")
	case err != nil:
		internal(w)
	default:
		respond(w, http.StatusOK, map[string]string{"walletAddress": addr}, "", "")
	}
}

func (s *Server) unlinkWallet(w http.ResponseWriter, r *http.Request, id identity) {
	switch err := s.store.SetWallet(r.Context(), id.userID, ""); {
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "USER_NOT_FOUND", "user no longer exists")
	case err != nil:
		internal(w)
	default:
		respond(w, http.StatusOK, map[string]string{"walletAddress": ""}, "", "")
	}
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request, _ identity) {
	u, err := s.store.GetUser(r.Context(), mux.Vars(r)["id"])
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "USER_NOT_FOUND", "user not found")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	// Public-ish lookup for other services: no email or phone.
	respond(w, http.StatusOK, map[string]string{"id": u.ID, "role": u.Role, "walletAddress": u.WalletAddress}, "", "")
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request, id identity) {
	if id.role != "admin" {
		fail(w, http.StatusForbidden, "FORBIDDEN", "only admins can change roles")
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !signupRoles[req.Role] && req.Role != "admin" {
		fail(w, http.StatusBadRequest, "INVALID_ROLE", "unknown role")
		return
	}
	switch err := s.store.SetRole(r.Context(), mux.Vars(r)["id"], req.Role); {
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "USER_NOT_FOUND", "user not found")
	case err != nil:
		internal(w)
	default:
		respond(w, http.StatusOK, map[string]string{"id": mux.Vars(r)["id"], "role": req.Role}, "", "")
	}
}
