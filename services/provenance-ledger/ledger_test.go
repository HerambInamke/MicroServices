package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

type fakeStore struct {
	pieces map[string]Piece
	events map[string][]Event
	outbox map[string]Outgoing // store key -> event
}

func newFakeStore() *fakeStore {
	return &fakeStore{pieces: map[string]Piece{}, events: map[string][]Event{}, outbox: map[string]Outgoing{}}
}

func (f *fakeStore) Create(_ context.Context, p Piece, first Event, out Outgoing) error {
	if _, ok := f.pieces[p.ID]; ok {
		return errors.New("exists")
	}
	f.pieces[p.ID] = p
	f.events[p.ID] = []Event{first}
	f.outbox[string(outboxKey(p.ID, first.Index))] = out
	return nil
}

func (f *fakeStore) Pending(_ context.Context, limit int) ([]Pending, error) {
	keys := make([]string, 0, len(f.outbox))
	for k := range f.outbox {
		keys = append(keys, k)
	}
	sort.Strings(keys) // immudb scans in key order too
	var out []Pending
	for _, k := range keys {
		if len(out) == limit {
			break
		}
		out = append(out, Pending{StoreKey: []byte(k), Outgoing: f.outbox[k]})
	}
	return out, nil
}

func (f *fakeStore) Sent(_ context.Context, storeKey []byte) error {
	delete(f.outbox, string(storeKey))
	return nil
}

// published lists the outbox as {topic, eventType, key}, in relay order.
func (f *fakeStore) published() []published {
	pending, _ := f.Pending(context.Background(), len(f.outbox))
	var out []published
	for _, p := range pending {
		var env struct{ EventType string }
		json.Unmarshal(p.Value, &env)
		out = append(out, published{p.Topic, env.EventType, p.Key})
	}
	return out
}

func (f *fakeStore) Get(_ context.Context, id string) (Piece, error) {
	p, ok := f.pieces[id]
	if !ok {
		return Piece{}, ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) Update(_ context.Context, pp *Piece, e *Event, out *Outgoing) error {
	p := *pp
	if _, ok := f.pieces[p.ID]; !ok {
		return ErrNotFound
	}
	if e != nil {
		if e.Index != len(f.events[p.ID]) {
			return errors.New("event index already written")
		}
		f.events[p.ID] = append(f.events[p.ID], *e)
		f.outbox[string(outboxKey(p.ID, e.Index))] = *out
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

func newTestServer() (*Server, *fakeStore) {
	st := newFakeStore()
	chain := &fakeChain{recorded: map[[32]byte]string{}, pieces: map[[32]byte][]ChainEvent{}}
	return &Server{store: st, users: fakeUsers{}, chain: chain}, st
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
	s, st := newTestServer()
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
	if sent := st.published(); len(sent) != 1 || sent[0] != (published{"piece.registered", "PieceRegistered", id}) {
		t.Fatalf("unexpected events: %v", sent)
	}
}

func TestRegisterRequiresWeaver(t *testing.T) {
	s, st := newTestServer()
	if code, _ := do(t, s, "POST", "/pieces", "", "", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("no identity: want 401, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/pieces", "r1", "retailer", `{}`); code != http.StatusForbidden {
		t.Fatalf("retailer: want 403, got %d", code)
	}
	if sent := st.published(); len(sent) != 0 {
		t.Fatalf("no event expected, got %v", sent)
	}
}

func TestRegisterValidatesBody(t *testing.T) {
	s, _ := newTestServer()
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
	s, _ := newTestServer()
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
	registered := newOutgoing("piece.registered", "PieceRegistered", p.ID, map[string]any{"pieceId": p.ID})
	if err := st.Create(ctx, p, Event{Type: "registered", Role: "weaver", UserID: "w1"}, registered); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, p, Event{}, registered); err == nil {
		t.Fatal("creating the same piece twice must fail")
	}
	got, err := st.Get(ctx, p.ID)
	if err != nil || got.ID != p.ID || got.OwnerID != "w1" {
		t.Fatalf("get: %v %v", got, err)
	}
	p.OwnerID, p.Rev = "d1", got.Rev
	handoff := newOutgoing("piece.handoff.signed", "PieceHandoffSigned", p.ID, map[string]any{"pieceId": p.ID})
	if err := st.Update(ctx, &p, &Event{Index: 1, Type: "handoff", UserID: "d1"}, &handoff); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(ctx, &p, &Event{Index: 1, Type: "handoff", UserID: "x"}, &handoff); err == nil {
		t.Fatal("an event index must not be written twice")
	}
	// The outbox holds this piece's two events in history order (other runs may have left their own).
	mine := func() []Pending {
		all, err := st.Pending(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var out []Pending
		for _, e := range all {
			if strings.HasPrefix(string(e.StoreKey), "outbox:"+p.ID+":") {
				out = append(out, e)
			}
		}
		return out
	}
	if pending := mine(); len(pending) != 2 || string(pending[0].Value) != string(registered.Value) || string(pending[1].Value) != string(handoff.Value) {
		t.Fatalf("outbox: %+v", pending)
	}
	if err := st.Sent(ctx, outboxKey(p.ID, 0)); err != nil {
		t.Fatal(err)
	}
	if pending := mine(); len(pending) != 1 || pending[0].Topic != "piece.handoff.signed" {
		t.Fatalf("sent entry still pending: %+v", pending)
	}
	if err := st.Sent(ctx, outboxKey(p.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Get(ctx, p.ID); got.OwnerID != "d1" {
		t.Fatalf("update not applied: %+v", got)
	}
	if evs, err := st.History(ctx, p.ID); err != nil || len(evs) != 2 || evs[0].Type != "registered" || evs[1].UserID != "d1" {
		t.Fatalf("history: %+v (%v)", evs, err)
	}
	if err := st.Update(ctx, &Piece{ID: uuid.NewString()}, nil, nil); err == nil {
		t.Fatal("updating a missing piece must fail")
	}
	if _, err := st.Get(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing piece: want ErrNotFound, got %v", err)
	}
}

// barrierStore holds every Get until both requests have read the piece, so both act on the same state.
type barrierStore struct {
	Store
	read *sync.WaitGroup
}

func (b barrierStore) Get(ctx context.Context, id string) (Piece, error) {
	p, err := b.Store.Get(ctx, id)
	b.read.Done()
	b.read.Wait()
	return p, err
}

// hookChain runs before ahead of every Record, i.e. after accept has claimed the offer.
type hookChain struct {
	Chain
	before func()
}

func (h hookChain) Record(ctx context.Context, pieceID [32]byte, role string, dataHash [32]byte, sig []byte) (string, error) {
	h.before()
	return h.Chain.Record(ctx, pieceID, role, dataHash, sig)
}

// immuHandoff is a piece registered by w1 in a real immudb (TEST_IMMUDB_HOST), with a Server on top of it.
type immuHandoff struct {
	st      *immuStore
	s       *Server
	pieceID string
	dyeKey  *ecdsa.PrivateKey
}

func newImmuHandoff(t *testing.T) *immuHandoff {
	t.Helper()
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
	if err := st.Create(ctx, p, Event{Type: "registered", Role: "weaver", UserID: "w1"}, newOutgoing("piece.registered", "PieceRegistered", p.ID, map[string]any{})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Sent(ctx, outboxKey(p.ID, 0)); st.Sent(ctx, outboxKey(p.ID, 1)) })
	key, _ := crypto.GenerateKey()
	users := fakeUsers{
		"w1": {ID: "w1", Role: "weaver"},
		"d1": {ID: "d1", Role: "dye-house", WalletAddress: crypto.PubkeyToAddress(key.PublicKey).Hex()},
		"h1": {ID: "h1", Role: "wholesaler"},
	}
	chain := &fakeChain{recorded: map[[32]byte]string{}, pieces: map[[32]byte][]ChainEvent{}}
	return &immuHandoff{st: st, s: &Server{store: st, users: users, chain: chain}, pieceID: p.ID, dyeKey: key}
}

// race sends both requests through their own Server (one per replica, sharing immudb) once both have read the piece,
// and returns the status codes sorted.
func (f *immuHandoff) race(reqs ...*http.Request) []int {
	read := &sync.WaitGroup{}
	read.Add(len(reqs))
	var wg sync.WaitGroup
	codes := make([]int, len(reqs))
	for i, req := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &Server{store: barrierStore{f.st, read}, users: f.s.users, chain: f.s.chain}
			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, req)
			codes[i] = rec.Code
		}()
	}
	wg.Wait()
	sort.Ints(codes)
	return codes
}

func authedReq(path, userID, role, body string) *http.Request {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-User-Role", role)
	return req
}

// Two replicas offering the same piece at once: exactly one commits, the other gets 409.
func TestConcurrentHandoffsOneWins(t *testing.T) {
	f := newImmuHandoff(t)
	path := "/pieces/" + f.pieceID + "/handoffs"
	codes := f.race(authedReq(path, "w1", "weaver", `{"toUserId":"d1"}`), authedReq(path, "w1", "weaver", `{"toUserId":"h1"}`))
	if codes[0] != http.StatusCreated || codes[1] != http.StatusConflict {
		t.Fatalf("want one 201 and one 409, got %v", codes)
	}
	if got, err := f.st.Get(context.Background(), f.pieceID); err != nil || got.Pending == nil {
		t.Fatalf("the winning offer was not stored: %+v (%v)", got, err)
	}
}

func (f *immuHandoff) offer(t *testing.T, to string) string {
	t.Helper()
	code, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs", "w1", "weaver", `{"toUserId":"`+to+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("offer: want 201, got %d: %v", code, env)
	}
	return env["data"].(map[string]any)["pendingHandoff"].(map[string]any)["dataHash"].(string)
}

// Two replicas accepting the same offer at once: one claims it and mirrors the handoff, the other gets 409.
func TestConcurrentAcceptsOneWins(t *testing.T) {
	f := newImmuHandoff(t)
	body := `{"signature":"` + personalSign(t, f.dyeKey, f.offer(t, "d1")) + `"}`
	path := "/pieces/" + f.pieceID + "/handoffs/accept"
	codes := f.race(authedReq(path, "d1", "dye-house", body), authedReq(path, "d1", "dye-house", body))
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict {
		t.Fatalf("want one 200 and one 409, got %v", codes)
	}
	if evs, err := f.st.History(context.Background(), f.pieceID); err != nil || len(evs) != 2 {
		t.Fatalf("want the handoff mirrored once: %+v (%v)", evs, err)
	}
}

// Once accept has claimed the offer, the owner cannot redirect it while the tx is mined.
func TestRedirectDuringAcceptConflicts(t *testing.T) {
	f := newImmuHandoff(t)
	body := `{"signature":"` + personalSign(t, f.dyeKey, f.offer(t, "d1")) + `"}`
	redirect := 0
	f.s.chain = hookChain{Chain: f.s.chain, before: func() {
		redirect, _ = do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs", "w1", "weaver", `{"toUserId":"h1"}`)
	}}
	if code, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", body); code != http.StatusOK {
		t.Fatalf("accept: want 200, got %d: %v", code, env)
	}
	if redirect != http.StatusConflict {
		t.Fatalf("redirect during accept: want 409, got %d", redirect)
	}
	if got, err := f.st.Get(context.Background(), f.pieceID); err != nil || got.OwnerID != "d1" || got.Pending != nil {
		t.Fatalf("handoff not mirrored: %+v (%v)", got, err)
	}
}
