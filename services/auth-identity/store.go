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
	ErrDuplicate = errors.New("already exists")
	ErrNotFound  = errors.New("not found")
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
	GetUser(ctx context.Context, id string) (User, error)
	// SetWallet links address to the user; an empty address unlinks.
	SetWallet(ctx context.Context, id, address string) error
	SetRole(ctx context.Context, id, role string) error
	SaveRefresh(ctx context.Context, jti, userID string, expiresAt time.Time) error
	// UseRefresh atomically revokes a live refresh token and returns its owner.
	UseRefresh(ctx context.Context, jti string) (string, error)
	RevokeRefresh(ctx context.Context, jti, userID string) error
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
);
CREATE TABLE IF NOT EXISTS refresh_tokens (
	jti        text PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at timestamptz NOT NULL,
	revoked    boolean NOT NULL DEFAULT false
);`

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
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isInvalidUUID reports malformed ids, which are simply "not found" to callers.
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

func (s *pgStore) CreateUser(ctx context.Context, u User) (User, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, phone, password_hash, role) VALUES ($1, NULLIF($2, ''), $3, $4) RETURNING `+userCols,
		u.Email, u.Phone, u.PasswordHash, u.Role)
	out, err := scanUser(row)
	if isUniqueViolation(err) {
		return User{}, ErrDuplicate
	}
	return out, err
}

func (s *pgStore) FindUser(ctx context.Context, email, phone string) (User, error) {
	if email != "" {
		return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email))
	}
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE phone = $1`, phone))
}

func (s *pgStore) GetUser(ctx context.Context, id string) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
	if isInvalidUUID(err) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *pgStore) exec(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	switch {
	case isUniqueViolation(err):
		return ErrDuplicate
	case isInvalidUUID(err):
		return ErrNotFound
	case err != nil:
		return err
	case tag.RowsAffected() == 0:
		return ErrNotFound
	}
	return nil
}

func (s *pgStore) SetWallet(ctx context.Context, id, address string) error {
	return s.exec(ctx, `UPDATE users SET wallet_address = NULLIF($2, '') WHERE id = $1`, id, address)
}

func (s *pgStore) SetRole(ctx context.Context, id, role string) error {
	return s.exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, id, role)
}

func (s *pgStore) SaveRefresh(ctx context.Context, jti, userID string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO refresh_tokens (jti, user_id, expires_at) VALUES ($1, $2, $3)`, jti, userID, expiresAt)
	return err
}

func (s *pgStore) UseRefresh(ctx context.Context, jti string) (string, error) {
	var userID string
	err := s.pool.QueryRow(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE jti = $1 AND NOT revoked AND expires_at > now() RETURNING user_id`,
		jti).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

func (s *pgStore) RevokeRefresh(ctx context.Context, jti, userID string) error {
	return s.exec(ctx, `UPDATE refresh_tokens SET revoked = true WHERE jti = $1 AND user_id = $2`, jti, userID)
}

// seedAdmin creates the admin account once; admins can't self-register.
func (s *pgStore) seedAdmin(ctx context.Context, email, passwordHash string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1, $2, 'admin') ON CONFLICT (email) DO NOTHING`,
		email, passwordHash)
	return err
}
