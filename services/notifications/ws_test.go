package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dial(t *testing.T, srv *httptest.Server, userID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if userID != "" {
		h.Set("X-User-Id", userID)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/notifications", h)
}

func TestLiveStreamDeliversOnlyOwnNotifications(t *testing.T) {
	st := newFakeStore()
	srv := httptest.NewServer((&Server{store: st}).routes())
	defer srv.Close()

	if _, resp, err := dial(t, srv, ""); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no identity: want 401, got err=%v resp=%v", err, resp)
	}

	conn, _, err := dial(t, srv, "w1")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; st.subscribers("w1") == 0; i++ {
		if i > 100 {
			t.Fatal("handler never subscribed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	st.Add(context.Background(), Notification{ID: "for-r1", UserID: "r1", RefID: "o1"}, "e1")
	st.Add(context.Background(), Notification{ID: "for-w1", UserID: "w1", RefID: "o1"}, "e2")

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var n Notification
	json.Unmarshal(msg, &n)
	if n.ID != "for-w1" {
		t.Fatalf("w1 received %s, want only its own notification", msg)
	}
}

// TestRedisPublishesOnAdd runs against TEST_REDIS_URL (flushed). Skipped when unset.
func TestRedisPublishesOnAdd(t *testing.T) {
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

	msgs, stop, err := s.Subscribe(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	s.Add(ctx, Notification{ID: "a", UserID: "r1", RefID: "o1"}, "e1")
	s.Add(ctx, Notification{ID: "b", UserID: "w1", RefID: "o1"}, "e2")
	s.Add(ctx, Notification{ID: "b2", UserID: "w1", RefID: "o1"}, "e2") // redelivery: no second push

	select {
	case m := <-msgs:
		var n Notification
		json.Unmarshal([]byte(m), &n)
		if n.ID != "b" || n.UserID != "w1" {
			t.Fatalf("got %s, want notification b for w1", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no live notification received")
	}
	select {
	case m := <-msgs:
		t.Fatalf("unexpected extra push: %s", m)
	case <-time.After(200 * time.Millisecond):
	}
}
