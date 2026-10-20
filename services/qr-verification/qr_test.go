package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeStore struct {
	byPiece map[string]Record
	scans   []string
	down    bool
}

func (f *fakeStore) GetByPiece(_ context.Context, id string) (Record, error) {
	r, ok := f.byPiece[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) GetByCode(_ context.Context, code string) (Record, error) {
	for _, r := range f.byPiece {
		if r.Code == code {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

func (f *fakeStore) Create(_ context.Context, rec Record) (Record, error) {
	if r, ok := f.byPiece[rec.PieceID]; ok {
		return r, nil
	}
	f.byPiece[rec.PieceID] = rec
	return rec, nil
}

func (f *fakeStore) LogScan(ctx context.Context, code string) error {
	if _, err := f.GetByCode(ctx, code); err != nil {
		return err
	}
	f.scans = append(f.scans, code)
	return nil
}

func (f *fakeStore) ApplyHandoff(_ context.Context, pieceID, ownerID string, eventCount int, ledgerRef string) error {
	if f.down {
		return errors.New("db down")
	}
	r, ok := f.byPiece[pieceID]
	if ok && r.EventCount < eventCount {
		r.OwnerID, r.EventCount, r.LedgerRef = ownerID, eventCount, ledgerRef
		f.byPiece[pieceID] = r
	}
	return nil
}

type fakeLedger struct {
	pieces map[string]LedgerPiece
	down   bool
}

func (f *fakeLedger) Piece(_ context.Context, id string) (LedgerPiece, error) {
	if f.down {
		return LedgerPiece{}, errors.New("connection refused")
	}
	p, ok := f.pieces[id]
	if !ok {
		return LedgerPiece{}, ErrNotFound
	}
	return p, nil
}

func (f *fakeLedger) History(ctx context.Context, id string) (json.RawMessage, error) {
	p, err := f.Piece(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(fmt.Sprintf(`{"piece":{"id":%q},"events":[{"type":"registered"}]}`, p.ID)), nil
}

func newTestServer() (*Server, *fakeStore, *fakeLedger) {
	st := &fakeStore{byPiece: map[string]Record{}}
	led := &fakeLedger{pieces: map[string]LedgerPiece{"p1": {ID: "p1", OwnerID: "w1", EventCount: 1}}}
	return &Server{store: st, ledger: led, baseURL: "http://gw.test"}, st, led
}

func do(t *testing.T, s *Server, method, path, userID string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return rec.Code, env
}

func TestGenerateByOwner(t *testing.T) {
	s, _, _ := newTestServer()
	code, env := do(t, s, "POST", "/qr/p1/generate", "w1")
	if code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %v", code, env)
	}
	data := env["data"].(map[string]any)
	qr := data["code"].(string)
	if len(qr) != 22 || data["resolveUrl"] != "http://gw.test/api/v1/qr/"+qr+"/resolve" {
		t.Fatalf("unexpected code payload: %v", data)
	}
	png, err := base64.StdEncoding.DecodeString(data["pngBase64"].(string))
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("pngBase64 is not a PNG: %v", err)
	}

	// Asking again returns the same printed code.
	code, env = do(t, s, "POST", "/qr/p1/generate", "w1")
	if code != http.StatusOK || env["data"].(map[string]any)["code"] != qr {
		t.Fatalf("second generate: want 200 with the same code, got %d: %v", code, env)
	}
}

func TestGenerateRules(t *testing.T) {
	s, st, led := newTestServer()
	if code, _ := do(t, s, "POST", "/qr/p1/generate", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous: want 401, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/qr/p1/generate", "someone"); code != http.StatusForbidden {
		t.Errorf("not owner: want 403, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/qr/nope/generate", "w1"); code != http.StatusNotFound {
		t.Errorf("unknown piece: want 404, got %d", code)
	}
	led.down = true
	if code, _ := do(t, s, "POST", "/qr/p1/generate", "w1"); code != http.StatusBadGateway {
		t.Errorf("ledger down: want 502, got %d", code)
	}
	if len(st.byPiece) != 0 {
		t.Fatal("no code may be stored when generate is refused")
	}
}

func TestResolveIsPublic(t *testing.T) {
	s, _, led := newTestServer()
	_, env := do(t, s, "POST", "/qr/p1/generate", "w1")
	qr := env["data"].(map[string]any)["code"].(string)

	code, env := do(t, s, "GET", "/qr/"+qr+"/resolve", "")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d: %v", code, env)
	}
	data := env["data"].(map[string]any)
	if data["pieceId"] != "p1" || data["verifyUrl"] != "/api/v1/pieces/p1/verify" {
		t.Fatalf("unexpected resolve payload: %v", data)
	}
	if events := data["history"].(map[string]any)["events"].([]any); len(events) != 1 {
		t.Fatalf("history not passed through: %v", data["history"])
	}
	if code, _ := do(t, s, "GET", "/qr/forged/resolve", ""); code != http.StatusNotFound {
		t.Errorf("unknown code: want 404, got %d", code)
	}
	led.down = true
	if code, _ := do(t, s, "GET", "/qr/"+qr+"/resolve", ""); code != http.StatusBadGateway {
		t.Errorf("ledger down: want 502, got %d", code)
	}
}

func TestScanLog(t *testing.T) {
	s, st, _ := newTestServer()
	_, env := do(t, s, "POST", "/qr/p1/generate", "w1")
	qr := env["data"].(map[string]any)["code"].(string)
	if code, _ := do(t, s, "POST", "/qr/"+qr+"/scan-log", ""); code != http.StatusCreated {
		t.Fatalf("want 201, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/qr/forged/scan-log", ""); code != http.StatusNotFound {
		t.Fatalf("unknown code: want 404, got %d", code)
	}
	if len(st.scans) != 1 {
		t.Fatalf("want 1 scan, got %v", st.scans)
	}
}

func handoffMsg(index int, to string) []byte {
	return []byte(fmt.Sprintf(`{"eventType":"PieceHandoffSigned","payload":{"pieceId":"p1","index":%d,"toUserId":%q,"dataHash":"0xh%d"}}`, index, to, index))
}

func TestHandleHandoffMovesOwnerForwardOnly(t *testing.T) {
	st := &fakeStore{byPiece: map[string]Record{"p1": {PieceID: "p1", Code: "c1", OwnerID: "w1", EventCount: 1}}}
	ctx := context.Background()
	for _, m := range [][]byte{handoffMsg(1, "d1"), handoffMsg(2, "h1"), handoffMsg(1, "d1") /* replay */} {
		if err := handleHandoff(ctx, st, m); err != nil {
			t.Fatal(err)
		}
	}
	if r := st.byPiece["p1"]; r.OwnerID != "h1" || r.EventCount != 3 || r.LedgerRef != "0xh2" {
		t.Fatalf("owner cache must follow the latest event only: %+v", r)
	}
	if err := handleHandoff(ctx, st, []byte(`not json`)); err != nil {
		t.Fatalf("malformed messages are skipped, got %v", err)
	}
	st.down = true
	if err := handleHandoff(ctx, st, handoffMsg(3, "r1")); err == nil {
		t.Fatal("a store failure must be returned so the offset is not committed")
	}
}

// testStore connects to TEST_DB_URL inside a throwaway schema. Skipped when TEST_DB_URL is unset.
func testStore(t *testing.T) *pgStore {
	t.Helper()
	url := os.Getenv("TEST_DB_URL")
	if url == "" {
		t.Skip("TEST_DB_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("qr_test_%d", time.Now().UnixNano())

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate must be a no-op: %v", err)
	}
	return &pgStore{pool: pool}
}

func TestPGStore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	rec, err := s.Create(ctx, Record{PieceID: "p1", Code: "c1", OwnerID: "w1", EventCount: 1})
	if err != nil || rec.Code != "c1" || rec.ProductID != 0 {
		t.Fatalf("create: %+v %v", rec, err)
	}
	// A racing second create keeps the first code.
	if rec, err := s.Create(ctx, Record{PieceID: "p1", Code: "c2", OwnerID: "w1", EventCount: 1}); err != nil || rec.Code != "c1" {
		t.Fatalf("second create: %+v %v", rec, err)
	}
	if rec, err := s.GetByCode(ctx, "c1"); err != nil || rec.PieceID != "p1" {
		t.Fatalf("by code: %+v %v", rec, err)
	}
	if _, err := s.GetByCode(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code: want ErrNotFound, got %v", err)
	}
	if err := s.LogScan(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.LogScan(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scan of unknown code: want ErrNotFound, got %v", err)
	}
	var owner string
	if err := s.pool.QueryRow(ctx, `SELECT owner_id FROM scans WHERE qr_code = 'c1'`).Scan(&owner); err != nil || owner != "w1" {
		t.Fatalf("scan not attributed to the owner: %q %v", owner, err)
	}

	if err := s.ApplyHandoff(ctx, "p1", "d1", 2, "0xh1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyHandoff(ctx, "p1", "stale", 2, "0xold"); err != nil { // replayed event
		t.Fatal(err)
	}
	if err := s.ApplyHandoff(ctx, "unknown", "d1", 2, ""); err != nil {
		t.Fatalf("pieces without a code are ignored: %v", err)
	}
	if rec, _ := s.GetByPiece(ctx, "p1"); rec.OwnerID != "d1" || rec.EventCount != 2 || rec.LedgerRef != "0xh1" {
		t.Fatalf("apply handoff: %+v", rec)
	}
}
