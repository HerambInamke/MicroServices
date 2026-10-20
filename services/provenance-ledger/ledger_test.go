package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/google/uuid"
)

type fakeStore struct {
	pieces map[string]Piece
	events map[string][]Event
}

func newFakeStore() *fakeStore {
	return &fakeStore{pieces: map[string]Piece{}, events: map[string][]Event{}}
}

func (f *fakeStore) Create(_ context.Context, p Piece, first Event) error {
	if _, ok := f.pieces[p.ID]; ok {
		return errors.New("exists")
	}
	f.pieces[p.ID] = p
	f.events[p.ID] = []Event{first}
	return nil
}

func (f *fakeStore) Get(_ context.Context, id string) (Piece, error) {
	p, ok := f.pieces[id]
	if !ok {
		return Piece{}, ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) Update(_ context.Context, p Piece, e *Event) error {
	if _, ok := f.pieces[p.ID]; !ok {
		return ErrNotFound
	}
	if e != nil {
		if e.Index != len(f.events[p.ID]) {
			return errors.New("event index already written")
		}
		f.events[p.ID] = append(f.events[p.ID], *e)
	}
	f.pieces[p.ID] = p
	return nil
}

type fakeUsers map[string]User

func (f fakeUsers) Get(_ context.Context, _, _, userID string) (User, error) {
	u, ok := f[userID]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) History(_ context.Context, id string) ([]Event, error) {
	return f.events[id], nil
}

type fakeChain struct {
	recorded map[[32]byte]string
	pieces   map[[32]byte][]ChainEvent
	fail     bool
}

func (f *fakeChain) Record(_ context.Context, pieceID [32]byte, role string, dataHash [32]byte, sig []byte) (string, error) {
	if f.fail {
		return "", errors.New("rpc down")
	}
	if tx, ok := f.recorded[dataHash]; ok {
		return tx, nil
	}
	actor, _, err := recoverSigner(dataHash, hexutil.Encode(sig))
	if err != nil {
		return "", err
	}
	tx := "0xtx" + strconv.Itoa(len(f.recorded)+1)
	f.recorded[dataHash] = tx
	f.pieces[pieceID] = append(f.pieces[pieceID], ChainEvent{Actor: actor, Role: role, DataHash: dataHash})
	return tx, nil
}

func (f *fakeChain) History(_ context.Context, pieceID [32]byte) ([]ChainEvent, error) {
	if f.fail {
		return nil, errors.New("rpc down")
	}
	return f.pieces[pieceID], nil
}

type published struct{ topic, eventType, key string }

type fakePublisher struct{ sent []published }

func (f *fakePublisher) Publish(topic, eventType, key string, _ any) {
	f.sent = append(f.sent, published{topic, eventType, key})
}

func newTestServer() (*Server, *fakeStore, *fakePublisher) {
	st, pub := newFakeStore(), &fakePublisher{}
	chain := &fakeChain{recorded: map[[32]byte]string{}, pieces: map[[32]byte][]ChainEvent{}}
	return &Server{store: st, events: pub, users: fakeUsers{}, chain: chain}, st, pub
}

func do(t *testing.T, s *Server, method, path, userID, role, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
		req.Header.Set("X-User-Role", role)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return rec.Code, env
}

func TestRegisterPiece(t *testing.T) {
	s, st, pub := newTestServer()
	code, env := do(t, s, "POST", "/pieces", "w1", "weaver", `{"productId":42,"description":"Banarasi silk"}`)
	if code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %v", code, env)
	}
	data := env["data"].(map[string]any)
	id := data["id"].(string)
	if data["currentOwnerId"] != "w1" || data["currentOwnerRole"] != "weaver" || data["weaverId"] != "w1" || data["productId"] != float64(42) {
		t.Fatalf("unexpected piece: %v", data)
	}
	if evs := st.events[id]; len(evs) != 1 || evs[0].Type != "registered" || evs[0].UserID != "w1" {
		t.Fatalf("registration event not stored: %v", evs)
	}
	if len(pub.sent) != 1 || pub.sent[0] != (published{"piece.registered", "PieceRegistered", id}) {
		t.Fatalf("unexpected events: %v", pub.sent)
	}
}

func TestRegisterRequiresWeaver(t *testing.T) {
	s, _, pub := newTestServer()
	if code, _ := do(t, s, "POST", "/pieces", "", "", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("no identity: want 401, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/pieces", "r1", "retailer", `{}`); code != http.StatusForbidden {
		t.Fatalf("retailer: want 403, got %d", code)
	}
	if len(pub.sent) != 0 {
		t.Fatalf("no event expected, got %v", pub.sent)
	}
}

func TestRegisterValidatesBody(t *testing.T) {
	s, _, _ := newTestServer()
	if code, _ := do(t, s, "POST", "/pieces", "w1", "weaver", `not json`); code != http.StatusBadRequest {
		t.Fatalf("bad JSON: want 400, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/pieces", "w1", "weaver", `{"productId":"p1"}`); code != http.StatusBadRequest {
		t.Fatalf("string productId: want 400, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/pieces", "w1", "weaver", `{"productId":-1}`); code != http.StatusBadRequest {
		t.Fatalf("negative productId: want 400, got %d", code)
	}
	long := `{"description":"` + string(bytes.Repeat([]byte("x"), 1001)) + `"}`
	if code, _ := do(t, s, "POST", "/pieces", "w1", "weaver", long); code != http.StatusBadRequest {
		t.Fatalf("long description: want 400, got %d", code)
	}
}

func TestGetPieceIsPublic(t *testing.T) {
	s, _, _ := newTestServer()
	_, env := do(t, s, "POST", "/pieces", "w1", "weaver", `{}`)
	id := env["data"].(map[string]any)["id"].(string)

	code, env := do(t, s, "GET", "/pieces/"+id, "", "", "")
	if code != http.StatusOK || env["data"].(map[string]any)["id"] != id {
		t.Fatalf("want 200 with the piece, got %d: %v", code, env)
	}
	if code, _ := do(t, s, "GET", "/pieces/nope", "", "", ""); code != http.StatusNotFound {
		t.Fatalf("unknown piece: want 404, got %d", code)
	}
}

// Runs against a real immudb when TEST_IMMUDB_HOST is set (e.g. localhost with the compose stack up).
func TestImmuStore(t *testing.T) {
	host := os.Getenv("TEST_IMMUDB_HOST")
	if host == "" {
		t.Skip("TEST_IMMUDB_HOST not set")
	}
	ctx := context.Background()
	st, err := newImmuStore(ctx, host, "immudb", "immudb", "defaultdb")
	if err != nil {
		t.Fatal(err)
	}
	p := Piece{ID: uuid.NewString(), WeaverID: "w1", OwnerID: "w1", OwnerRole: "weaver", EventCount: 1, CreatedAt: time.Now().UTC()}
	if err := st.Create(ctx, p, Event{Type: "registered", Role: "weaver", UserID: "w1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, p, Event{}); err == nil {
		t.Fatal("creating the same piece twice must fail")
	}
	got, err := st.Get(ctx, p.ID)
	if err != nil || got.ID != p.ID || got.OwnerID != "w1" {
		t.Fatalf("get: %v %v", got, err)
	}
	p.OwnerID = "d1"
	if err := st.Update(ctx, p, &Event{Index: 1, Type: "handoff", UserID: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(ctx, p, &Event{Index: 1, Type: "handoff", UserID: "x"}); err == nil {
		t.Fatal("an event index must not be written twice")
	}
	if got, _ := st.Get(ctx, p.ID); got.OwnerID != "d1" {
		t.Fatalf("update not applied: %+v", got)
	}
	if evs, err := st.History(ctx, p.ID); err != nil || len(evs) != 2 || evs[0].Type != "registered" || evs[1].UserID != "d1" {
		t.Fatalf("history: %+v (%v)", evs, err)
	}
	if err := st.Update(ctx, Piece{ID: uuid.NewString()}, nil); err == nil {
		t.Fatal("updating a missing piece must fail")
	}
	if _, err := st.Get(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing piece: want ErrNotFound, got %v", err)
	}
}
