package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

var topics = []string{"order.placed", "order.status.changed", "payment.completed", "piece.handoff.signed", "chat.message.sent"}

// eventPayload is the union of the fields each topic's payload carries.
type eventPayload struct {
	OrderID      string `json:"orderId"`
	RetailerID   string `json:"retailerId"`
	WholesalerID string `json:"wholesalerId"`
	Status       string `json:"status"`
	Total        string `json:"total"`
	Currency     string `json:"currency"`
	PieceID      string `json:"pieceId"`
	FromUserID   string `json:"fromUserId"`
	ThreadID     string `json:"threadId"`
	RecipientID  string `json:"recipientId"`
	Kind         string `json:"kind"`
}

// notificationFor maps an event to its recipient and text; ok is false for unknown topics.
func notificationFor(topic string, p eventPayload) (n Notification, ok bool) {
	switch topic {
	case "order.placed":
		return Notification{UserID: p.WholesalerID, Title: "New order", Body: "Order " + p.OrderID + " placed for " + p.Total + " " + p.Currency, RefType: "order", RefID: p.OrderID}, true
	case "order.status.changed":
		return Notification{UserID: p.RetailerID, Title: "Order " + p.Status, Body: "Order " + p.OrderID + " is now " + p.Status, RefType: "order", RefID: p.OrderID}, true
	case "payment.completed":
		return Notification{UserID: p.WholesalerID, Title: "Payment received", Body: "Order " + p.OrderID + " was paid: " + p.Total + " " + p.Currency, RefType: "order", RefID: p.OrderID}, true
	case "piece.handoff.signed":
		return Notification{UserID: p.FromUserID, Title: "Handoff accepted", Body: "Piece " + p.PieceID + " was accepted by the next owner", RefType: "piece", RefID: p.PieceID}, true
	case "chat.message.sent":
		title := "New message"
		if p.Kind == "offer" {
			title = "New offer"
		}
		return Notification{UserID: p.RecipientID, Title: title, Body: title + " in your conversation", RefType: "thread", RefID: p.ThreadID}, true
	}
	return Notification{}, false
}

// handleEvent stores the notification for one envelope. Malformed messages are logged
// and skipped (nil) so they cannot block the partition.
func handleEvent(ctx context.Context, store Store, topic string, value []byte) error {
	var env struct {
		EventID   string       `json:"eventId"`
		Timestamp string       `json:"timestamp"`
		Payload   eventPayload `json:"payload"`
	}
	if err := json.Unmarshal(value, &env); err != nil || env.EventID == "" {
		log.Printf("skipping malformed %s message: %.200s", topic, value)
		return nil
	}
	n, ok := notificationFor(topic, env.Payload)
	if !ok || n.UserID == "" || n.RefID == "" {
		log.Printf("skipping %s message without a recipient: %.200s", topic, value)
		return nil
	}
	n.ID, n.Type = newID(), topic
	n.CreatedAt = env.Timestamp
	if n.CreatedAt == "" {
		n.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := store.Add(ctx, n, env.EventID)
	return err
}

func newEventReader(brokers string) *kafka.Reader {
	return kafka.NewReader(kafka.ReaderConfig{
		Brokers:     strings.Split(brokers, ","),
		GroupID:     "notifications",
		GroupTopics: topics,
		StartOffset: kafka.FirstOffset,
	})
}

// consume runs forever. The offset is committed only after the notification is stored, so a
// crash or Redis outage replays the message instead of losing it (Add dedups on eventId).
func consume(ctx context.Context, r *kafka.Reader, store Store) {
	for {
		msg, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("kafka fetch failed: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		for err := handleEvent(ctx, store, msg.Topic, msg.Value); err != nil; err = handleEvent(ctx, store, msg.Topic, msg.Value) {
			log.Printf("could not store notification for %s at offset %d, retrying: %v", msg.Topic, msg.Offset, err)
			time.Sleep(2 * time.Second)
		}
		if err := r.CommitMessages(ctx, msg); err != nil {
			log.Printf("kafka commit failed at offset %d: %v", msg.Offset, err)
		}
	}
}
