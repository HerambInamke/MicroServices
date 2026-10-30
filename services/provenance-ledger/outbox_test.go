package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// fakeKafka fails while down and records what it acknowledged.
type fakeKafka struct {
	down bool
	sent []Outgoing
}

func (k *fakeKafka) send(_ context.Context, o Outgoing) error {
	if k.down {
		return errors.New("kafka unavailable")
	}
	k.sent = append(k.sent, o)
	return nil
}

func eventID(t *testing.T, o Outgoing) string {
	t.Helper()
	var env struct{ EventID string }
	if err := json.Unmarshal(o.Value, &env); err != nil || env.EventID == "" {
		t.Fatalf("not an envelope: %s", o.Value)
	}
	return env.EventID
}

func TestRelaySurvivesKafkaOutageInOrder(t *testing.T) {
	f := newHandoffFixture(t)
	hash := f.offer(t, "d1")
	if code, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", `{"signature":"`+personalSign(t, f.dyeKey, hash)+`"}`); code != http.StatusOK {
		t.Fatalf("accept: %d %v", code, env)
	}
	written, _ := f.st.Pending(context.Background(), 100)
	if len(written) != 2 {
		t.Fatalf("want registration and handoff in the outbox, got %d", len(written))
	}

	kafka := &fakeKafka{down: true}
	if n, err := relayOnce(context.Background(), f.st, kafka.send); n != 0 || err != nil {
		t.Fatalf("kafka down: sent %d, err %v", n, err)
	}
	if still, _ := f.st.Pending(context.Background(), 100); len(still) != 2 {
		t.Fatalf("events lost while kafka was down: %d left", len(still))
	}

	kafka.down = false
	if n, err := relayOnce(context.Background(), f.st, kafka.send); n != 2 || err != nil {
		t.Fatalf("kafka up: sent %d, err %v", n, err)
	}
	for i, o := range kafka.sent {
		if o.Key != f.pieceID || eventID(t, o) != eventID(t, written[i].Outgoing) {
			t.Fatalf("event %d sent out of order or with a new eventId: %+v", i, o)
		}
	}
	if kafka.sent[0].Topic != "piece.registered" || kafka.sent[1].Topic != "piece.handoff.signed" {
		t.Fatalf("history order lost: %s, %s", kafka.sent[0].Topic, kafka.sent[1].Topic)
	}
	if left, _ := f.st.Pending(context.Background(), 100); len(left) != 0 {
		t.Fatalf("sent events still pending: %+v", left)
	}
}

func TestRelayStopsAtFirstFailure(t *testing.T) {
	s, st := newTestServer()
	for i := 0; i < 2; i++ {
		if code, env := do(t, s, "POST", "/pieces", "w1", "weaver", `{}`); code != http.StatusCreated {
			t.Fatalf("register: %d %v", code, env)
		}
	}
	first, _ := st.Pending(context.Background(), 100)
	var tried []string
	failFirst := func(_ context.Context, o Outgoing) error {
		tried = append(tried, o.Key)
		if len(tried) == 1 {
			return errors.New("timeout")
		}
		return nil
	}
	if n, err := relayOnce(context.Background(), st, failFirst); n != 0 || err != nil {
		t.Fatalf("sent %d, err %v", n, err)
	}
	if len(tried) != 1 || tried[0] != first[0].Key {
		t.Fatalf("a later event was tried ahead of the failed one: %v", tried)
	}
	if left, _ := st.Pending(context.Background(), 100); len(left) != 2 {
		t.Fatalf("want both events kept, got %d", len(left))
	}
}

func TestFailedHandoffWritesNoEvent(t *testing.T) {
	f := newHandoffFixture(t)
	hash := f.offer(t, "d1")
	f.chain.fail = true
	if code, _ := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", `{"signature":"`+personalSign(t, f.dyeKey, hash)+`"}`); code != http.StatusBadGateway {
		t.Fatalf("chain down: want 502, got %d", code)
	}
	if sent := f.st.published(); len(sent) != 1 || sent[0].topic != "piece.registered" {
		t.Fatalf("only the registration may be in the outbox: %v", sent)
	}
}
