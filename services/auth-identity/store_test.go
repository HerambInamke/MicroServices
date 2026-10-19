package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testStore connects to TEST_DB_URL inside a throwaway schema. Skipped when TEST_DB_URL is unset.
func testStore(t *testing.T) *pgStore {
	t.Helper()
	url := os.Getenv("TEST_DB_URL")
	if url == "" {
		t.Skip("TEST_DB_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("auth_test_%d", time.Now().UnixNano())

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &pgStore{pool: pool}
}

func TestMigrationsApplyOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// A second run must be a no-op, not an error.
	if err := migrate(ctx, s.pool); err != nil {
		t.Fatalf("re-running migrations: %v", err)
	}
	var versions []string
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		rows.Scan(&v)
		versions = append(versions, v)
	}
	if fmt.Sprint(versions) != "[0001_users.sql 0002_refresh_tokens.sql]" {
		t.Fatalf("recorded versions: %v", versions)
	}
}

func TestPGStoreSignupLoginQueries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, User{Email: "a@b.com", Phone: "+911234567890", PasswordHash: "hash", Role: "wholesaler"})
	if err != nil || u.ID == "" || u.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", u, err)
	}
	if _, err := s.CreateUser(ctx, User{Email: "a@b.com", PasswordHash: "h", Role: "retailer"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate email: %v", err)
	}
	if _, err := s.CreateUser(ctx, User{Email: "c@d.com", Phone: "+911234567890", PasswordHash: "h", Role: "retailer"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate phone: %v", err)
	}
	// Users without a phone must not collide on the phone column.
	for _, email := range []string{"x@y.com", "z@y.com"} {
		if _, err := s.CreateUser(ctx, User{Email: email, PasswordHash: "h", Role: "retailer"}); err != nil {
			t.Fatalf("phoneless user %s: %v", email, err)
		}
	}

	byEmail, err := s.FindUser(ctx, "a@b.com", "")
	if err != nil || byEmail.ID != u.ID || byEmail.PasswordHash != "hash" {
		t.Fatalf("find by email: %+v %v", byEmail, err)
	}
	byPhone, err := s.FindUser(ctx, "", "+911234567890")
	if err != nil || byPhone.ID != u.ID {
		t.Fatalf("find by phone: %+v %v", byPhone, err)
	}
	if _, err := s.FindUser(ctx, "nobody@b.com", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	if _, err := s.GetUser(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("malformed id: %v", err)
	}

	// Refresh tokens are single-use.
	if err := s.SaveRefresh(ctx, "jti-1", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if owner, err := s.UseRefresh(ctx, "jti-1"); err != nil || owner != u.ID {
		t.Fatalf("use refresh: %q %v", owner, err)
	}
	if _, err := s.UseRefresh(ctx, "jti-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reused refresh: %v", err)
	}
	if err := s.SaveRefresh(ctx, "jti-old", u.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseRefresh(ctx, "jti-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired refresh: %v", err)
	}
}
