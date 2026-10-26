package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

type fakeStore struct {
	seen  map[string]bool
	items []Notification
	mu    sync.Mutex // guards subs: WS handlers subscribe from server goroutines
	subs  map[string][]chan string
}

func newFakeStore() *fakeStore {
	return &fakeStore{seen: map[string]bool{}, subs: map[string][]chan string{}}
}

func (f *fakeStore) Add(_ context.Context, n Notification, eventID string) (bool, error) {
	if f.seen[eventID] {
		return false, nil
	}
	f.seen[eventID] = true
	f.items = append(f.items, n)
	live, _ := json.Marshal(n)
	f.mu.Lock()
	for _, c := range f.subs[n.UserID] {
		c <- string(live)
	}
	f.mu.Unlock()
	return true, nil
}

func (f *fakeStore) Subscribe(_ context.Context, userID string) (<-chan string, func(), error) {
	c := make(chan string, 10)
	f.mu.Lock()
	f.subs[userID] = append(f.subs[userID], c)
	f.mu.Unlock()
	return c, func() {}, nil
}

// subscribers reports how many live connections userID has, so tests can wait for one.
func (f *fakeStore) subscribers(userID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs[userID])
}

func (f *fakeStore) List(_ context.Context, userID string, unreadOnly bool) ([]Notification, error) {
	out := []Notification{}
	for _, n := range f.items {
		if n.UserID == userID && !(unreadOnly && n.Read) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakeStore) MarkRead(_ context.Context, userID, id string) error {
	for i, n := range f.items {
		if n.ID == id && n.UserID == userID {
			f.items[i].Read = true
			return nil
		}
	}
	return ErrNotFound
}

func envelope(eventID string, payload map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"eventId": eventID, "timestamp": "2026-10-26T10:00:00Z", "payload": payload})
	return b
}

var order = map[string]any{"orderId": "o1", "retailerId": "r1", "wholesalerId": "w1", "status": "shipped", "total": "100.00", "currency": "INR"}

func TestEventGoesToTheRightUser(t *testing.T) {
	cases := []struct {
		topic   string
		payload map[string]any
		user    string
		ref     string
	}{
		{"order.placed", order, "w1", "o1"},
		{"order.status.changed", order, "r1", "o1"},
		{"payment.completed", order, "w1", "o1"},
		{"piece.handoff.signed", map[string]any{"pieceId": "p1", "fromUserId": "weaver", "toUserId": "w1"}, "weaver", "p1"},
		{"chat.message.sent", map[string]any{"threadId": "t1", "senderId": "r1", "recipientId": "w1", "kind": "offer"}, "w1", "t1"},
		{"chat.offer.updated", map[string]any{"threadId": "t1", "responderId": "w1", "recipientId": "r1", "status": "accepted"}, "r1", "t1"},
	}
	for _, c := range cases {
		s := newFakeStore()
		if err := handleEvent(context.Background(), s, c.topic, envelope("e-"+c.topic, c.payload)); err != nil {
			t.Fatalf("%s: %v", c.topic, err)
		}
		if len(s.items) != 1 || s.items[0].UserID != c.user || s.items[0].RefID != c.ref || s.items[0].Type != c.topic {
			t.Fatalf("%s: got %+v, want one notification for %s about %s", c.topic, s.items, c.user, c.ref)
		}
	}
}

func TestMalformedAndUnaddressedEventsAreSkipped(t *testing.T) {
	s := newFakeStore()
	for _, v := range [][]byte{
		[]byte("not json"),
		envelope("", order), // no eventId
		envelope("e1", map[string]any{"orderId": "o1"}),      // no recipient
		envelope("e2", map[string]any{"wholesalerId": "w1"}), // no ref
	} {
		if err := handleEvent(context.Background(), s, "order.placed", v); err != nil {
			t.Fatalf("malformed message must be skipped, got %v", err)
		}
	}
	if len(s.items) != 0 {
		t.Fatalf("stored %d notifications from malformed messages", len(s.items))
	}
}

func do(t *testing.T, s *Server, method, path, userID string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestInboxIsOwnOnly(t *testing.T) {
	st := newFakeStore()
	st.items = []Notification{{ID: "n1", UserID: "w1"}, {ID: "n2", UserID: "r1"}, {ID: "n3", UserID: "w1", Read: true}}
	s := &Server{store: st}

	if code, _ := do(t, s, "GET", "/notifications", ""); code != http.StatusUnauthorized {
		t.Fatalf("no identity: got %d, want 401", code)
	}
	_, body := do(t, s, "GET", "/notifications?userId=r1", "w1")
	if got := len(body["data"].([]any)); got != 2 {
		t.Fatalf("w1 sees %d notifications, want 2 (userId param must be ignored)", got)
	}
	_, body = do(t, s, "GET", "/notifications?unread=true", "w1")
	if got := len(body["data"].([]any)); got != 1 {
		t.Fatalf("unread: got %d, want 1", got)
	}
	if code, _ := do(t, s, "PATCH", "/notifications/n2/read", "w1"); code != http.StatusNotFound {
		t.Fatalf("marking someone else's notification: got %d, want 404", code)
	}
	if code, _ := do(t, s, "PATCH", "/notifications/n1/read", "w1"); code != http.StatusOK {
		t.Fatalf("marking own notification: got %d, want 200", code)
	}
}

// TestRedisStore runs against TEST_REDIS_URL (use a spare db, it is flushed). Skipped when unset.
func TestRedisStore(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set; skipping Redis integration test")
	}
	ctx := context.Background()
	s, err := newRedisStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	s.rdb.FlushDB(ctx)
	t.Cleanup(func() { s.rdb.FlushDB(ctx) })

	n := Notification{ID: "n1", UserID: "w1", Type: "order.placed", Title: "New order", RefType: "order", RefID: "o1", CreatedAt: "2026-10-26T10:00:00Z"}
	if added, err := s.Add(ctx, n, "e1"); err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	n.ID = "n1-again"
	if added, err := s.Add(ctx, n, "e1"); err != nil || added {
		t.Fatalf("redelivered event must be ignored: added=%v err=%v", added, err)
	}
	list, err := s.List(ctx, "w1", false)
	if err != nil || len(list) != 1 || list[0].ID != "n1" || list[0].Title != "New order" || list[0].Read {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if err := s.MarkRead(ctx, "r1", "n1"); err != ErrNotFound {
		t.Fatalf("other user's mark-read: got %v, want ErrNotFound", err)
	}
	if err := s.MarkRead(ctx, "w1", "missing"); err != ErrNotFound {
		t.Fatalf("missing id: got %v, want ErrNotFound", err)
	}
	if err := s.MarkRead(ctx, "w1", "n1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(ctx, "w1", true); len(list) != 0 {
		t.Fatalf("unread after mark-read: %+v", list)
	}

	for i := 0; i < maxPerUser+5; i++ {
		s.Add(ctx, Notification{ID: newID(), UserID: "cap", RefID: "x"}, newID())
	}
	if list, _ := s.List(ctx, "cap", false); len(list) != maxPerUser {
		t.Fatalf("cap: got %d, want %d", len(list), maxPerUser)
	}
}
