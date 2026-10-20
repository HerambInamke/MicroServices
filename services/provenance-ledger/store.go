package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	immu "github.com/codenotary/immudb/pkg/client"
)

var ErrNotFound = errors.New("not found")

type Piece struct {
	ID          string    `json:"id"`
	ProductID   int64     `json:"productId,omitempty"` // catalog product id; 0 when not linked
	Description string    `json:"description,omitempty"`
	WeaverID    string    `json:"weaverId"`
	OwnerID     string    `json:"currentOwnerId"`
	OwnerRole   string    `json:"currentOwnerRole"`
	EventCount  int       `json:"eventCount"`
	LastHash    string    `json:"lastDataHash,omitempty"`
	Pending     *Offer    `json:"pendingHandoff,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Offer is a handoff the current owner has proposed and the receiver has not yet signed.
type Offer struct {
	FromUserID string    `json:"fromUserId"`
	ToUserID   string    `json:"toUserId"`
	Role       string    `json:"role"`
	DataHash   string    `json:"dataHash"` // what the receiver must personal_sign
	OfferedAt  time.Time `json:"offeredAt"`
}

type Event struct {
	Index        int       `json:"index"`
	Type         string    `json:"type"`
	Role         string    `json:"role"`
	UserID       string    `json:"userId"`
	ActorAddress string    `json:"actorAddress,omitempty"`
	DataHash     string    `json:"dataHash,omitempty"`
	TxHash       string    `json:"txHash,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

type Store interface {
	Create(ctx context.Context, p Piece, first Event) error
	Get(ctx context.Context, id string) (Piece, error)
	// Update replaces the piece state and, when e is set, appends that history entry in the same transaction.
	Update(ctx context.Context, p Piece, e *Event) error
	History(ctx context.Context, id string) ([]Event, error)
}

// immudb keys: piece:{id} holds the current piece state, piece:{id}:event:{n} each history entry.
func pieceKey(id string) []byte { return []byte("piece:" + id) }

func eventKey(id string, n int) []byte { return []byte(fmt.Sprintf("piece:%s:event:%06d", id, n)) }

type immuStore struct{ c immu.ImmuClient }

func newImmuStore(ctx context.Context, host, user, pass, db string) (*immuStore, error) {
	// The client keeps its verified-state file in Dir; it is what lets VerifiedGet detect tampering.
	c := immu.NewClient().WithOptions(immu.DefaultOptions().WithAddress(host).WithPort(3322).WithDir(os.TempDir()))
	if err := c.OpenSession(ctx, []byte(user), []byte(pass), db); err != nil {
		return nil, err
	}
	return &immuStore{c: c}, nil
}

func (s *immuStore) Create(ctx context.Context, p Piece, first Event) error {
	pb, err := json.Marshal(p)
	if err != nil {
		return err
	}
	eb, err := json.Marshal(first)
	if err != nil {
		return err
	}
	// One immudb transaction, so a piece never exists without its registration entry.
	_, err = s.c.SetAll(ctx, &schema.SetRequest{
		KVs:           []*schema.KeyValue{{Key: pieceKey(p.ID), Value: pb}, {Key: eventKey(p.ID, first.Index), Value: eb}},
		Preconditions: []*schema.Precondition{schema.PreconditionKeyMustNotExist(pieceKey(p.ID))},
	})
	return err
}

func (s *immuStore) Update(ctx context.Context, p Piece, e *Event) error {
	pb, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req := &schema.SetRequest{
		KVs:           []*schema.KeyValue{{Key: pieceKey(p.ID), Value: pb}},
		Preconditions: []*schema.Precondition{schema.PreconditionKeyMustExist(pieceKey(p.ID))},
	}
	if e != nil {
		eb, err := json.Marshal(e)
		if err != nil {
			return err
		}
		// History is append-only: an entry index is written once and never overwritten.
		req.KVs = append(req.KVs, &schema.KeyValue{Key: eventKey(p.ID, e.Index), Value: eb})
		req.Preconditions = append(req.Preconditions, schema.PreconditionKeyMustNotExist(eventKey(p.ID, e.Index)))
	}
	_, err = s.c.SetAll(ctx, req)
	return err
}

// maxHistory caps one scan. shortcut: a piece passes through a handful of actors; page the scan if that ever changes.
const maxHistory = 1000

func (s *immuStore) History(ctx context.Context, id string) ([]Event, error) {
	// Event keys are zero-padded, so the key-ordered scan is the history order.
	list, err := s.c.Scan(ctx, &schema.ScanRequest{Prefix: []byte("piece:" + id + ":event:"), Limit: maxHistory})
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(list.Entries))
	for _, entry := range list.Entries {
		var e Event
		if err := json.Unmarshal(entry.Value, &e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

func (s *immuStore) Get(ctx context.Context, id string) (Piece, error) {
	entry, err := s.c.VerifiedGet(ctx, pieceKey(id))
	if err != nil {
		if strings.Contains(err.Error(), "key not found") {
			return Piece{}, ErrNotFound
		}
		return Piece{}, err
	}
	var p Piece
	return p, json.Unmarshal(entry.Value, &p)
}
