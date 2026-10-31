package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A database created before versioned migrations already has media_files; migrating it must not fail,
// and a second run must be a no-op. Skipped when TEST_DB_URL is unset.
func TestMigrationsAdoptExistingDB(t *testing.T) {
	url := os.Getenv("TEST_DB_URL")
	if url == "" {
		t.Skip("TEST_DB_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("media_test_%d", time.Now().UnixNano())

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

	if _, err := pool.Exec(ctx, `CREATE TABLE media_files (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), owner_id text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := migrate(ctx, pool); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	var versions []string
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		rows.Scan(&v)
		versions = append(versions, v)
	}
	if fmt.Sprint(versions) != "[0001_media_files.sql]" {
		t.Fatalf("recorded versions: %v", versions)
	}
}
