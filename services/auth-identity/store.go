package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDuplicate = errors.New("user already exists")
	ErrNotFound  = errors.New("user not found")
)

type User struct {
	ID            string
	Email         string
	Phone         string
	PasswordHash  string
	Role          string
	WalletAddress string
	CreatedAt     time.Time
}

type Store interface {
	CreateUser(ctx context.Context, u User) (User, error)
	// FindUser looks a user up by email, or by phone when email is empty.
	FindUser(ctx context.Context, email, phone string) (User, error)
}

// shortcut: schema is applied at startup with IF NOT EXISTS, not versioned migrations; adopt a migration tool when the first schema change lands.
const schema = `
CREATE TABLE IF NOT EXISTS users (
	id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	email          text NOT NULL UNIQUE,
	phone          text UNIQUE,
	password_hash  text NOT NULL,
	role           text NOT NULL,
	wallet_address text UNIQUE,
	created_at     timestamptz NOT NULL DEFAULT now()
)`

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
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, err
	}
	return &pgStore{pool: pool}, nil
}

const userCols = `id, email, COALESCE(phone, ''), password_hash, role, COALESCE(wallet_address, ''), created_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Phone, &u.PasswordHash, &u.Role, &u.WalletAddress, &u.CreatedAt)
	return u, err
}

func (s *pgStore) CreateUser(ctx context.Context, u User) (User, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, phone, password_hash, role) VALUES ($1, NULLIF($2, ''), $3, $4) RETURNING `+userCols,
		u.Email, u.Phone, u.PasswordHash, u.Role)
	out, err := scanUser(row)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return User{}, ErrDuplicate
	}
	return out, err
}

func (s *pgStore) FindUser(ctx context.Context, email, phone string) (User, error) {
	var row pgx.Row
	if email != "" {
		row = s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email)
	} else {
		row = s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE phone = $1`, phone)
	}
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}
