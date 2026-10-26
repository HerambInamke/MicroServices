# qr-verification

Go · port **8087** · Postgres `qr_db`.

One QR code per piece. Scanning it shows the piece's verified history without logging in. Only the piece's
current owner can generate or fetch the code, and the owner is kept up to date from `piece.handoff.signed`.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /qr/:pieceId/generate` → `{pieceId, code, resolveUrl, pngBase64}` | current owner (the same code every time; others get 403 `NOT_OWNER`) |
| `GET /qr/:code/resolve` | public: piece and history from the ledger |
| `POST /qr/:code/scan-log` | public: records a scan |

## Configuration

`DB_URL`, `LEDGER_URL`, `KAFKA_BROKERS`, `PORT`, `PUBLIC_BASE_URL` (the origin printed into the QR; set it to your public domain).

## Tests

```bash
go test ./...
TEST_DB_URL=postgres://... go test ./...   # plus the Postgres store
```
