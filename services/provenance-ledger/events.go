package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

// Outgoing is one Kafka message in the outbox. Value is the full envelope, fixed when the event is written, so a
// resend keeps its eventId.
type Outgoing struct {
	Topic string `json:"topic"`
	Key   string `json:"key"`
	Value []byte `json:"value"`
}

// newOutgoing builds the shared envelope from schemas/events.json.
func newOutgoing(topic, eventType, key string, payload any) Outgoing {
	value, err := json.Marshal(map[string]any{
		"eventId":       uuid.NewString(),
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"sourceService": "provenance-ledger",
		"eventType":     eventType,
		"payload":       payload,
	})
	if err != nil {
		panic(err) // payloads are plain maps of strings, numbers and times: a failure is a programming error
	}
	return Outgoing{Topic: topic, Key: key, Value: value}
}

type kafkaSender struct{ w *kafka.Writer }

func newKafkaSender(brokers string) *kafkaSender {
	return &kafkaSender{w: &kafka.Writer{
		Addr:                   kafka.TCP(strings.Split(brokers, ",")...),
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
	}}
}

// Send returns nil once Kafka acknowledged the message.
func (k *kafkaSender) Send(ctx context.Context, o Outgoing) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	msg := kafka.Message{Topic: o.Topic, Key: []byte(o.Key), Value: o.Value}
	err := k.w.WriteMessages(ctx, msg)
	// The first write to an auto-created topic fails while its leader is elected; kafka-go does not retry that.
	for i := 0; i < 5 && errors.Is(err, kafka.LeaderNotAvailable); i++ {
		time.Sleep(500 * time.Millisecond)
		err = k.w.WriteMessages(ctx, msg)
	}
	return err
}

// relayOnce sends pending outbox entries oldest first per piece and deletes each one Kafka acknowledged. It stops at
// the first failure so no later event overtakes it. Delivery is at least once: a crash between the send and the
// delete resends the same envelope, which consumers de-duplicate on eventId. No cross-replica lock: the ledger
// already runs as a single replica (see lockPiece).
func relayOnce(ctx context.Context, store Store, send func(context.Context, Outgoing) error) (int, error) {
	pending, err := store.Pending(ctx, 100)
	if err != nil {
		return 0, err
	}
	for i, p := range pending {
		if err := send(ctx, p.Outgoing); err != nil {
			log.Printf("kafka did not take %s for %s, will retry: %v", p.Topic, p.Key, err)
			return i, nil
		}
		if err := store.Sent(ctx, p.StoreKey); err != nil {
			return i, err
		}
	}
	return len(pending), nil
}

// runRelay is the blocking relay loop; main runs it in a goroutine.
func runRelay(store Store, send func(context.Context, Outgoing) error) {
	for {
		n, err := relayOnce(context.Background(), store, send)
		if err != nil {
			log.Printf("outbox relay: %v", err)
		}
		if n == 0 || err != nil {
			time.Sleep(time.Second)
		}
	}
}
