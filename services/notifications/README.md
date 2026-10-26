# notifications

Go · port **8092** · Redis db 1.

Turns platform events into a per-user inbox, and pushes new items live over a WebSocket.

| Event | Who is notified |
|---|---|
| `order.placed`, `payment.completed` | wholesaler |
| `order.status.changed` | retailer |
| `piece.handoff.signed` | previous owner |
| `chat.message.sent` | the other participant |
| `chat.offer.updated` (accepted/declined) | the offer's sender |

## Endpoints

| Method & path | Who |
|---|---|
| `GET /notifications?unread=true` | own inbox, newest first (last 200, kept 30 days) |
| `PATCH /notifications/:id/read` | owner (someone else's id gives 404) |
| `GET /ws/notifications` (WebSocket) | own live stream, at most 5 open per user |

Storing a notification is one Lua script: deduplicate by `eventId`, store, publish to `notify:{userId}`.
A redelivered event therefore never creates a duplicate or pushes twice.

## Configuration

`REDIS_URL`, `KAFKA_BROKERS`, `PORT`.

## Tests

```bash
go test ./...
TEST_REDIS_URL=redis://localhost:6379/15 go test ./...   # plus real Redis (flushes that db)
```
