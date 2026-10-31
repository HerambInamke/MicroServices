package main

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
)

var ErrNotFound = errors.New("not found")

type MediaFile struct {
	ID          string
	OwnerID     string
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	Kind        string // "image" or "document"
	UploadedAt  time.Time
}

// MetaStore keeps file metadata (Postgres).
type MetaStore interface {
	Create(ctx context.Context, f MediaFile) (MediaFile, error)
	Get(ctx context.Context, id string) (MediaFile, error)
	Delete(ctx context.Context, id string) error
}

// ObjectStore keeps file bytes (MinIO).
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Remove(ctx context.Context, key string) error
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

const cols = `id, owner_id, object_key, content_type, size_bytes, kind, uploaded_at`

func scanFile(row pgx.Row) (MediaFile, error) {
	var f MediaFile
	err := row.Scan(&f.ID, &f.OwnerID, &f.ObjectKey, &f.ContentType, &f.SizeBytes, &f.Kind, &f.UploadedAt)
	var pgErr *pgconn.PgError
	// 22P02: malformed uuid, which callers should see as "not found".
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "22P02") {
		return MediaFile{}, ErrNotFound
	}
	return f, err
}

func (s *pgStore) Create(ctx context.Context, f MediaFile) (MediaFile, error) {
	return scanFile(s.pool.QueryRow(ctx,
		`INSERT INTO media_files (owner_id, object_key, content_type, size_bytes, kind) VALUES ($1, $2, $3, $4, $5) RETURNING `+cols,
		f.OwnerID, f.ObjectKey, f.ContentType, f.SizeBytes, f.Kind))
}

func (s *pgStore) Get(ctx context.Context, id string) (MediaFile, error) {
	return scanFile(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM media_files WHERE id = $1`, id))
}

func (s *pgStore) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, id)
	return err
}

type minioStore struct {
	client *minio.Client
	bucket string
}

func (m *minioStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := m.client.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (m *minioStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
}

func (m *minioStore) Remove(ctx context.Context, key string) error {
	return m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}

func (m *minioStore) ensureBucket(ctx context.Context) error {
	exists, err := m.client.BucketExists(ctx, m.bucket)
	if err != nil || exists {
		return err
	}
	return m.client.MakeBucket(ctx, m.bucket, minio.MakeBucketOptions{})
}
