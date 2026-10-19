package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeStore struct{ users map[string]User }

func newFake() *fakeStore { return &fakeStore{users: map[string]User{}} }

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

var testSecret = []byte("test-secret")

func do(t *testing.T, s *Server, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	return rec.Code, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

const validSignup = `{"email":"a@b.com","password":"password123","role":"wholesaler"}`

func TestSignup(t *testing.T) {
	s := &Server{store: newFake(), secret: testSecret}

	if code, out := do(t, s, "/auth/signup", validSignup); code != 201 || out["error"] != nil {
		t.Fatalf("signup: %d %v", code, out)
	}
	if code, out := do(t, s, "/auth/signup", validSignup); code != 409 || errCode(out) != "USER_EXISTS" {
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
		if code, out := do(t, s, "/auth/signup", body); code != 400 || errCode(out) != want {
			t.Errorf("%s: got %d %q, want 400 %q", body, code, errCode(out), want)
		}
	}
}

func TestLogin(t *testing.T) {
	s := &Server{store: newFake(), secret: testSecret}
	do(t, s, "/auth/signup", `{"email":"a@b.com","phone":"+911234567890","password":"password123","role":"wholesaler"}`)

	for _, body := range []string{
		`{"email":"a@b.com","password":"password123"}`,
		`{"phone":"+911234567890","password":"password123"}`,
	} {
		code, out := do(t, s, "/auth/login", body)
		if code != 200 {
			t.Fatalf("login %s: %d %v", body, code, out)
		}
		data := out["data"].(map[string]any)
		c, err := parseToken(testSecret, data["access_token"].(string), tokenAccess)
		if err != nil || c.Subject != "id-a@b.com" || c.Role != "wholesaler" {
			t.Fatalf("access token: %v %+v", err, c)
		}
		if _, err := parseToken(testSecret, data["refresh_token"].(string), tokenRefresh); err != nil {
			t.Fatalf("refresh token: %v", err)
		}
		// a refresh token must not pass as an access token
		if _, err := parseToken(testSecret, data["refresh_token"].(string), tokenAccess); err == nil {
			t.Fatal("refresh token accepted as access token")
		}
	}

	for _, body := range []string{
		`{"email":"a@b.com","password":"wrong-password"}`,
		`{"email":"nobody@b.com","password":"password123"}`,
	} {
		if code, out := do(t, s, "/auth/login", body); code != 401 || errCode(out) != "INVALID_CREDENTIALS" {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	if code, _ := do(t, s, "/auth/login", `{"password":"password123"}`); code != 400 {
		t.Errorf("missing identifier: %d", code)
	}
}
