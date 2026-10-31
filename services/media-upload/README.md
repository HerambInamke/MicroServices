# media-upload

Go · port **8085** · Postgres `media_db` (metadata) + MinIO (the files).

Image upload and serving for products, profiles and designs. The content type is checked from the file's
bytes, not from the client's header.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /media/upload` (multipart `file`) | logged-in user |
| `GET /media/:id`, `GET /media/:id/content` | public |
| `DELETE /media/:id` | uploader |

## Configuration

`DB_URL`, `MINIO_URL`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `MINIO_BUCKET`, `PORT`.
SQL migrations in `migrations/` run on start, in filename order, each once; add a change as the next numbered file.

## Tests

```bash
go test ./...
TEST_DB_URL=postgres://... go test ./...       # plus the Postgres migrations
```
