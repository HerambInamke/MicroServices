package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Record struct {
	PieceID    string
	ProductID  int64
	Code       string
	OwnerID    string
	LedgerRef  string
	EventCount int
	CreatedAt  time.Time
}

type Store interface {
	GetByPiece(ctx context.Context, pieceID string) (Record, error)
	GetByCode(ctx context.Context, code string) (Record, error)
	// Create stores rec unless the piece already has a code; either way it returns the stored record.
	Create(ctx context.Context, rec Record) (Record, error)
	// LogScan records a scan attributed to the cached owner; ErrNotFound for an unknown code.
	LogScan(ctx context.Context, code string) error
	// ApplyHandoff moves the cached owner forward; events at or behind eventCount are no-ops.
	ApplyHandoff(ctx context.Context, pieceID, ownerID string, eventCount int, ledgerRef string) error
}

type pgStore struct{ pool *pgxpool.Pool }

func newPGStore(ctx context.Context, url string) (*pgStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &pgStore{pool: pool}, nil
}

const recordCols = `id, COALESCE(product_id, 0), qr_code, current_owner_id, ledger_ref, event_count, created_at`

func scanRecord(row pgx.Row) (Record, error) {
	var r Record
	err := row.Scan(&r.PieceID, &r.ProductID, &r.Code, &r.OwnerID, &r.LedgerRef, &r.EventCount, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return r, err
}

func (s *pgStore) GetByPiece(ctx context.Context, pieceID string) (Record, error) {
	return scanRecord(s.pool.QueryRow(ctx, `SELECT `+recordCols+` FROM pieces WHERE id = $1`, pieceID))
}

func (s *pgStore) GetByCode(ctx context.Context, code string) (Record, error) {
	return scanRecord(s.pool.QueryRow(ctx, `SELECT `+recordCols+` FROM pieces WHERE qr_code = $1`, code))
}

func (s *pgStore) Create(ctx context.Context, rec Record) (Record, error) {
	// ON CONFLICT keeps the first code when two owners' requests race.
	_, err := s.pool.Exec(ctx, `INSERT INTO pieces (id, product_id, qr_code, current_owner_id, ledger_ref, event_count)
		VALUES ($1, NULLIF($2, 0), $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		rec.PieceID, rec.ProductID, rec.Code, rec.OwnerID, rec.LedgerRef, rec.EventCount)
	if err != nil {
		return Record{}, err
	}
	return s.GetByPiece(ctx, rec.PieceID)
}

func (s *pgStore) ApplyHandoff(ctx context.Context, pieceID, ownerID string, eventCount int, ledgerRef string) error {
	// Pieces without a QR code yet are not cached here; generate seeds them from the ledger.
	_, err := s.pool.Exec(ctx, `UPDATE pieces SET current_owner_id = $2, event_count = $3, ledger_ref = $4
		WHERE id = $1 AND event_count < $3`, pieceID, ownerID, eventCount, ledgerRef)
	return err
}

func (s *pgStore) LogScan(ctx context.Context, code string) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO scans (qr_code, owner_id)
		SELECT qr_code, current_owner_id FROM pieces WHERE qr_code = $1`, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
