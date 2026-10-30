package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

type fakeStore struct {
	users   map[string]User
	refresh map[string]struct {
		userID  string
		revoked bool
	}
}

func newFake() *fakeStore {
	return &fakeStore{users: map[string]User{}, refresh: map[string]struct {
		userID  string
		revoked bool
	}{}}
}

func (f *fakeStore) CreateUser(_ context.Context, u User) (User, error) {
	for _, e := range f.users {
		if e.Email == u.Email || (u.Phone != "" && e.Phone == u.Phone) {
			return User{}, ErrDuplicate
		}
	}
	u.ID = "id-" + u.Email
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeStore) FindUser(_ context.Context, email, phone string) (User, error) {
	for _, u := range f.users {
		if (email != "" && u.Email == email) || (email == "" && u.Phone == phone) {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (f *fakeStore) GetUser(_ context.Context, id string) (User, error) {
	u, ok := f.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) SetWallet(_ context.Context, id, address string) error {
	u, ok := f.users[id]
	if !ok {
		return ErrNotFound
	}
	for _, e := range f.users {
		if address != "" && e.ID != id && e.WalletAddress == address {
			return ErrDuplicate
		}
	}
	u.WalletAddress = address
	f.users[id] = u
	return nil
}

func (f *fakeStore) SetRole(_ context.Context, id, role string) error {
	u, ok := f.users[id]
	if !ok {
		return ErrNotFound
	}
	u.Role = role
	f.users[id] = u
	return nil
}

func (f *fakeStore) SaveRefresh(_ context.Context, jti, userID string, _ time.Time) error {
	f.refresh[jti] = struct {
		userID  string
		revoked bool
	}{userID: userID}
	return nil
}

func (f *fakeStore) UseRefresh(_ context.Context, jti string) (string, error) {
	t, ok := f.refresh[jti]
	if !ok || t.revoked {
		return "", ErrNotFound
	}
	t.revoked = true
	f.refresh[jti] = t
	return t.userID, nil
}

func (f *fakeStore) RevokeRefresh(_ context.Context, jti, userID string) error {
	t, ok := f.refresh[jti]
	if !ok || t.userID != userID {
		return ErrNotFound
	}
	t.revoked = true
	f.refresh[jti] = t
	return nil
}

var testSecret = []byte("test-secret")

func newServer() *Server { return &Server{store: newFake(), secret: testSecret} }

// call sends a request; who is "userID:role" for the gateway identity headers, or "" for anonymous.
func call(t *testing.T, s *Server, method, path, who, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if who != "" {
		id, role, _ := strings.Cut(who, ":")
		req.Header.Set("X-User-Id", id)
		req.Header.Set("X-User-Role", role)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: response not JSON: %v", method, path, err)
	}
	return rec.Code, out
}

func post(t *testing.T, s *Server, path, body string) (int, map[string]any) {
	return call(t, s, http.MethodPost, path, "", body)
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func data(out map[string]any) map[string]any {
	d, _ := out["data"].(map[string]any)
	return d
}

const (
	validSignup = `{"email":"a@b.com","phone":"+911234567890","password":"password123","role":"wholesaler"}`
	validLogin  = `{"email":"a@b.com","password":"password123"}`
	userA       = "id-a@b.com:wholesaler"
)

func login(t *testing.T, s *Server) map[string]any {
	t.Helper()
	post(t, s, "/auth/signup", validSignup)
	code, out := post(t, s, "/auth/login", validLogin)
	if code != 200 {
		t.Fatalf("login: %d %v", code, out)
	}
	return data(out)
}

func TestSignup(t *testing.T) {
	s := newServer()

	if code, out := post(t, s, "/auth/signup", validSignup); code != 201 || out["error"] != nil {
		t.Fatalf("signup: %d %v", code, out)
	}
	if code, out := post(t, s, "/auth/signup", validSignup); code != 409 || errCode(out) != "USER_EXISTS" {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	bad := map[string]string{
		`{"email":"nope","password":"password123","role":"retailer"}`:    "INVALID_EMAIL",
		`{"email":"c@d.com","password":"short","role":"retailer"}`:       "INVALID_PASSWORD",
		`{"email":"c@d.com","password":"password123","role":"admin"}`:    "INVALID_ROLE",
		`{"email":"c@d.com","password":"password123","role":"nonsense"}`: "INVALID_ROLE",
		`not json`: "INVALID_BODY",
	}
	for body, want := range bad {
		if code, out := post(t, s, "/auth/signup", body); code != 400 || errCode(out) != want {
			t.Errorf("%s: got %d %q, want 400 %q", body, code, errCode(out), want)
		}
	}
}

func TestLogin(t *testing.T) {
	s := newServer()
	post(t, s, "/auth/signup", validSignup)

	for _, body := range []string{validLogin, `{"phone":"+911234567890","password":"password123"}`} {
		code, out := post(t, s, "/auth/login", body)
		if code != 200 {
			t.Fatalf("login %s: %d %v", body, code, out)
		}
		d := data(out)
		c, err := parseToken(testSecret, d["accessToken"].(string), tokenAccess)
		if err != nil || c.Subject != "id-a@b.com" || c.Role != "wholesaler" {
			t.Fatalf("access token: %v %+v", err, c)
		}
		if _, err := parseToken(testSecret, d["refreshToken"].(string), tokenRefresh); err != nil {
			t.Fatalf("refresh token: %v", err)
		}
		if _, err := parseToken(testSecret, d["refreshToken"].(string), tokenAccess); err == nil {
			t.Fatal("refresh token accepted as access token")
		}
	}

	for _, body := range []string{
		`{"email":"a@b.com","password":"wrong-password"}`,
		`{"email":"nobody@b.com","password":"password123"}`,
	} {
		if code, out := post(t, s, "/auth/login", body); code != 401 || errCode(out) != "INVALID_CREDENTIALS" {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	if code, _ := post(t, s, "/auth/login", `{"password":"password123"}`); code != 400 {
		t.Errorf("missing identifier: %d", code)
	}
}

func TestRefreshRotatesAndRevokes(t *testing.T) {
	s := newServer()
	tokens := login(t, s)
	body := `{"refreshToken":"` + tokens["refreshToken"].(string) + `"}`

	code, out := post(t, s, "/auth/refresh", body)
	if code != 200 || data(out)["accessToken"] == nil {
		t.Fatalf("refresh: %d %v", code, out)
	}
	// The old refresh token was rotated out, so replaying it must fail.
	if code, out := post(t, s, "/auth/refresh", body); code != 401 || errCode(out) != "INVALID_TOKEN" {
		t.Fatalf("replayed refresh: %d %v", code, out)
	}
	// An access token is not a refresh token.
	if code, _ := post(t, s, "/auth/refresh", `{"refreshToken":"`+tokens["accessToken"].(string)+`"}`); code != 401 {
		t.Fatalf("access as refresh: %d", code)
	}
	// Expired refresh token.
	expired, _, _ := issueToken(testSecret, User{ID: "id-a@b.com"}, tokenRefresh, -time.Minute)
	if code, _ := post(t, s, "/auth/refresh", `{"refreshToken":"`+expired+`"}`); code != 401 {
		t.Fatalf("expired refresh: %d", code)
	}
}

func TestLogout(t *testing.T) {
	s := newServer()
	tokens := login(t, s)
	body := `{"refreshToken":"` + tokens["refreshToken"].(string) + `"}`

	if code, _ := call(t, s, "POST", "/auth/logout", "", body); code != 401 {
		t.Fatalf("anonymous logout: %d", code)
	}
	if code, _ := call(t, s, "POST", "/auth/logout", "someone-else:retailer", body); code != 400 {
		t.Fatalf("logout with another user's token: %d", code)
	}
	if code, out := call(t, s, "POST", "/auth/logout", userA, body); code != 200 {
		t.Fatalf("logout: %d %v", code, out)
	}
	if code, _ := post(t, s, "/auth/refresh", body); code != 401 {
		t.Fatalf("refresh after logout: %d", code)
	}
}

func TestMe(t *testing.T) {
	s := newServer()
	login(t, s)
	if code, _ := call(t, s, "GET", "/auth/me", "", ""); code != 401 {
		t.Fatalf("anonymous me: %d", code)
	}
	code, out := call(t, s, "GET", "/auth/me", userA, "")
	if code != 200 || data(out)["email"] != "a@b.com" {
		t.Fatalf("me: %d %v", code, out)
	}
	if _, leaked := data(out)["passwordHash"]; leaked {
		t.Fatal("password hash exposed")
	}
	if code, _ := call(t, s, "GET", "/auth/me", "ghost:retailer", ""); code != 404 {
		t.Fatalf("deleted user me: %d", code)
	}
}

// Private key 1 has a well-known Ethereum address, which pins recoverAddress to the real derivation.
var (
	walletKey  = secp256k1.PrivKeyFromBytes([]byte{1})
	walletAddr = "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"
	otherKey   = secp256k1.PrivKeyFromBytes([]byte{2})
)

// personalSign signs like MetaMask: EIP-191 hash, 65 bytes r|s|v with v = 27+recid.
func personalSign(key *secp256k1.PrivateKey, msg string) string {
	c := ecdsa.SignCompact(key, keccak([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(msg), msg))), false)
	return "0x" + hex.EncodeToString(append(c[1:], c[0]))
}

func challenge(t *testing.T, s *Server, who, addr string) string {
	t.Helper()
	code, out := call(t, s, "POST", "/auth/wallet/challenge", who, `{"address":"`+addr+`"}`)
	if code != 200 {
		t.Fatalf("challenge: %d %v", code, out)
	}
	return data(out)["message"].(string)
}

func linkBody(addr, msg, sig string) string {
	b, _ := json.Marshal(map[string]string{"address": addr, "message": msg, "signature": sig})
	return string(b)
}

func TestWallet(t *testing.T) {
	s := newServer()
	login(t, s)
	post(t, s, "/auth/signup", `{"email":"r@b.com","password":"password123","role":"retailer"}`)
	retailer := "id-r@b.com:retailer"
	addr := walletAddr
	msg := challenge(t, s, userA, addr)

	// Proofs that must not link: each names the error code it should get.
	expired := challengeMessage(testSecret, "id-a@b.com", strings.ToLower(addr), time.Now().Add(-time.Second).Unix())
	other := "0x2B5AD5c4795c026514f8317c7a215E218DcCD6cF" // key 2's address
	rejects := []struct{ name, who, body, code string }{
		{"wrong signer", userA, linkBody(addr, msg, personalSign(otherKey, msg)), "INVALID_SIGNATURE"},
		{"garbage signature", userA, linkBody(addr, msg, "0x1234"), "INVALID_SIGNATURE"},
		{"tampered message", userA, linkBody(addr, msg+" ", personalSign(walletKey, msg+" ")), "INVALID_CHALLENGE"},
		{"expired", userA, linkBody(addr, expired, personalSign(walletKey, expired)), "INVALID_CHALLENGE"},
		{"another user's challenge", retailer, linkBody(addr, msg, personalSign(walletKey, msg)), "INVALID_CHALLENGE"},
		{"another address", userA, linkBody(other, msg, personalSign(otherKey, msg)), "INVALID_CHALLENGE"},
		{"no proof", userA, `{"address":"` + addr + `"}`, "INVALID_CHALLENGE"},
	}
	for _, r := range rejects {
		if code, out := call(t, s, "POST", "/auth/wallet/link", r.who, r.body); code != 400 || errCode(out) != r.code {
			t.Errorf("%s: %d %v, want 400 %s", r.name, code, out, r.code)
		}
	}
	// A high-s twin of a valid signature is the same proof in malleable form: refuse it like the ledger does.
	good := personalSign(walletKey, msg)
	sig, _ := hex.DecodeString(good[2:])
	var sv secp256k1.ModNScalar
	sv.SetByteSlice(sig[32:64])
	sv.Negate()
	sb := sv.Bytes()
	copy(sig[32:64], sb[:])
	sig[64] ^= 1 // 27 <-> 28
	if code, _ := call(t, s, "POST", "/auth/wallet/link", userA, linkBody(addr, msg, "0x"+hex.EncodeToString(sig))); code != 400 {
		t.Errorf("high-s signature: %d", code)
	}

	code, out := call(t, s, "POST", "/auth/wallet/link", userA, linkBody(addr, msg, good))
	if code != 200 || data(out)["walletAddress"] != strings.ToLower(addr) {
		t.Fatalf("link: %d %v", code, out)
	}
	// Same address in a different case is still the same wallet, even with a valid proof.
	rmsg := challenge(t, s, retailer, strings.ToLower(addr))
	if code, out := call(t, s, "POST", "/auth/wallet/link", retailer, linkBody(strings.ToLower(addr), rmsg, personalSign(walletKey, rmsg))); code != 409 || errCode(out) != "WALLET_IN_USE" {
		t.Fatalf("link taken wallet: %d %v", code, out)
	}
	for _, bad := range []string{"0x123", "AbCdEf0123456789abcdef0123456789ABCDEF0101", "0xZZCdEf0123456789abcdef0123456789ABCDEF01"} {
		if code, _ := call(t, s, "POST", "/auth/wallet/link", userA, `{"address":"`+bad+`"}`); code != 400 {
			t.Errorf("invalid address %s: %d", bad, code)
		}
		if code, _ := call(t, s, "POST", "/auth/wallet/challenge", userA, `{"address":"`+bad+`"}`); code != 400 {
			t.Errorf("challenge for invalid address %s: %d", bad, code)
		}
	}
	if code, _ := call(t, s, "POST", "/auth/wallet/link", "", `{"address":"`+addr+`"}`); code != 401 {
		t.Fatalf("anonymous link: %d", code)
	}
	if code, _ := call(t, s, "POST", "/auth/wallet/challenge", "", `{"address":"`+addr+`"}`); code != 401 {
		t.Fatalf("anonymous challenge: %d", code)
	}
	if code, _ := call(t, s, "DELETE", "/auth/wallet/unlink", userA, ""); code != 200 {
		t.Fatalf("unlink: %d", code)
	}
	if _, out := call(t, s, "GET", "/auth/me", userA, ""); data(out)["walletAddress"] != "" {
		t.Fatalf("wallet still linked: %v", out)
	}
}

func TestUsersAndRoles(t *testing.T) {
	s := newServer()
	login(t, s)

	code, out := call(t, s, "GET", "/users/id-a@b.com", "x:retailer", "")
	if code != 200 || data(out)["role"] != "wholesaler" || data(out)["email"] != nil {
		t.Fatalf("get user: %d %v", code, out)
	}
	if code, _ := call(t, s, "GET", "/users/missing", "x:retailer", ""); code != 404 {
		t.Fatalf("missing user: %d", code)
	}
	if code, _ := call(t, s, "PATCH", "/users/id-a@b.com/role", userA, `{"role":"admin"}`); code != 403 {
		t.Fatalf("non-admin role change: %d", code)
	}
	if code, _ := call(t, s, "PATCH", "/users/id-a@b.com/role", "boss:admin", `{"role":"emperor"}`); code != 400 {
		t.Fatalf("invalid role: %d", code)
	}
	if code, _ := call(t, s, "PATCH", "/users/id-a@b.com/role", "boss:admin", `{"role":"retailer"}`); code != 200 {
		t.Fatalf("admin role change: %d", code)
	}
	if _, out := call(t, s, "GET", "/auth/me", userA, ""); data(out)["role"] != "retailer" {
		t.Fatalf("role not changed: %v", out)
	}
}
