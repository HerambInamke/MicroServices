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

type Publisher interface {
	Publish(topic, eventType, key string, payload any)
}

type kafkaPublisher struct{ w *kafka.Writer }

func newKafkaPublisher(brokers string) *kafkaPublisher {
	return &kafkaPublisher{w: &kafka.Writer{
		Addr:                   kafka.TCP(strings.Split(brokers, ",")...),
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
	}}
}

// Publish sends the shared envelope from schemas/events.json.
func (p *kafkaPublisher) Publish(topic, eventType, key string, payload any) {
	// shortcut: published after the immudb write without an outbox, so an event is lost if Kafka is down at that moment; add an outbox before consumers depend on it.
	value, err := json.Marshal(map[string]any{
		"eventId":       uuid.NewString(),
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"sourceService": "provenance-ledger",
		"eventType":     eventType,
		"payload":       payload,
	})
	if err != nil {
		log.Printf("could not encode %s for %s: %v", eventType, key, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg := kafka.Message{Topic: topic, Key: []byte(key), Value: value}
	err = p.w.WriteMessages(ctx, msg)
	// The first write to an auto-created topic fails while its leader is elected; kafka-go does not retry that.
	for i := 0; i < 5 && errors.Is(err, kafka.LeaderNotAvailable); i++ {
		time.Sleep(500 * time.Millisecond)
		err = p.w.WriteMessages(ctx, msg)
	}
	// The write is already committed; never fail the request over an event.
	if err != nil {
		log.Printf("could not publish %s for %s: %v", eventType, key, err)
	}
}
