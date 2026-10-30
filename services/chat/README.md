# chat

Node/Express · port **8093** · Mongo `chat` + Redis db 2 (pub/sub).

Conversations between one wholesaler and one retailer, with text messages and structured price offers.
Sending is always REST. The WebSocket only receives: every saved message, and every offer status change, is
published to `chat:{threadId}` and fanned out to the thread's open sockets.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /chat/threads` `{participantId}` | wholesaler or retailer; the other side must have the opposite role (checked with auth). Idempotent per pair: 201 when new, 200 when existing |
| `GET /chat/threads?limit=` | own threads, latest activity first |
| `GET /chat/threads/:id/messages?before=<messageId>&limit=` | participants, newest first, `meta.hasMore` |
| `POST /chat/threads/:id/messages` `{text}` (1–2000 chars) | participants |
| `POST /chat/threads/:id/offers` `{productId, qty, unitPrice}` | participants |
| `POST /chat/threads/:id/offers/:msgId/respond` `{action: accept\|decline\|counter, qty?, unitPrice?}` | the participant who did not make the offer, while it is open (409 `OFFER_NOT_OPEN` otherwise) |
| `GET /ws/chat/:threadId` (WebSocket) | participants |

Accepting an offer only records the agreement. The order is still placed through `POST /orders` at catalog prices.

## Events

Publishes `chat.message.sent` `{threadId, messageId, senderId, recipientId, kind}` for every new message or offer, and
`chat.offer.updated` `{threadId, messageId, status, responderId, recipientId}` when an offer is accepted or declined.

Events go through a transactional outbox: the message (or offer change), the thread bump and an `outbox` document
are written in one MongoDB transaction, and a relay sends them to Kafka oldest first, deleting each once
acknowledged. Only the instance holding the `relay_leases` lease relays, so order holds with several chat instances.
Transactions need a replica set: compose runs MongoDB as the single-node replica set `rs0`.

## Configuration

`MONGO_URL`, `REDIS_URL`, `KAFKA_BROKERS`, `AUTH_URL`, `PORT`.

## Tests

```bash
TEST_MONGO_URL="mongodb://root:rootpassword@localhost:27017/chat_test?authSource=admin&directConnection=true" \
TEST_REDIS_URL=redis://localhost:6379/5 npm test
```
