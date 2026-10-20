package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// LedgerPiece is the part of the ledger's piece we need; the ledger is the source of truth for ownership.
type LedgerPiece struct {
	ID         string `json:"id"`
	ProductID  int64  `json:"productId"`
	OwnerID    string `json:"currentOwnerId"`
	EventCount int    `json:"eventCount"`
	LastHash   string `json:"lastDataHash"`
}

type Ledger interface {
	Piece(ctx context.Context, id string) (LedgerPiece, error)
	// History returns the ledger's {piece, events} payload unchanged, to pass through to scanners.
	History(ctx context.Context, id string) (json.RawMessage, error)
}

type ledgerClient struct {
	base string
	http *http.Client
}

func newLedgerClient(base string) *ledgerClient {
	return &ledgerClient{base: base, http: &http.Client{Timeout: 5 * time.Second}}
}

func (l *ledgerClient) get(ctx context.Context, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", l.base+path, nil)
	if err != nil {
		return nil, err
	}
	res, err := l.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ledger returned %d for %s", res.StatusCode, path)
	}
	var env struct{ Data json.RawMessage }
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

func (l *ledgerClient) Piece(ctx context.Context, id string) (LedgerPiece, error) {
	data, err := l.get(ctx, "/pieces/"+url.PathEscape(id))
	if err != nil {
		return LedgerPiece{}, err
	}
	var p LedgerPiece
	return p, json.Unmarshal(data, &p)
}

func (l *ledgerClient) History(ctx context.Context, id string) (json.RawMessage, error) {
	return l.get(ctx, "/pieces/"+url.PathEscape(id)+"/history")
}
