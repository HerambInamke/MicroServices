package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

// handleHandoff applies one piece.handoff.signed envelope to the owner cache. Malformed
// messages are logged and skipped (nil) so they cannot block the partition.
func handleHandoff(ctx context.Context, store Store, value []byte) error {
	var env struct {
		Payload struct {
			PieceID  string `json:"pieceId"`
			Index    int    `json:"index"`
			ToUserID string `json:"toUserId"`
			DataHash string `json:"dataHash"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(value, &env); err != nil || env.Payload.PieceID == "" || env.Payload.ToUserID == "" {
		log.Printf("skipping malformed piece.handoff.signed message: %.200s", value)
		return nil
	}
	p := env.Payload
	// Event at history index i means the piece now has i+1 events.
	return store.ApplyHandoff(ctx, p.PieceID, p.ToUserID, p.Index+1, p.DataHash)
}

func newHandoffReader(brokers string) *kafka.Reader {
	return kafka.NewReader(kafka.ReaderConfig{
		Brokers:     strings.Split(brokers, ","),
		GroupID:     "qr-verification",
		Topic:       "piece.handoff.signed",
		StartOffset: kafka.FirstOffset,
	})
}

// consume runs forever. The offset is committed only after the update is stored, so a
// crash or DB outage replays the message instead of losing it (updates are idempotent).
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
		for err := handleHandoff(ctx, store, msg.Value); err != nil; err = handleHandoff(ctx, store, msg.Value) {
			log.Printf("could not apply handoff at offset %d, retrying: %v", msg.Offset, err)
			time.Sleep(2 * time.Second)
		}
		if err := r.CommitMessages(ctx, msg); err != nil {
			log.Printf("kafka commit failed at offset %d: %v", msg.Offset, err)
		}
	}
}
