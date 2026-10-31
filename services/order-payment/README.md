# order-payment

Python/FastAPI · port **8084** · Postgres `order_db`.

Bulk orders and Razorpay payments. Order prices come from the catalog's quote, and stock is reserved through
inventory-sync before the order is saved. One order belongs to one wholesaler.

A line may carry the `offerId` of an accepted chat offer: order-payment reads it from chat's internal
`GET /chat/offers/:id` and the line then uses the offer's unit price instead of the catalog tier. The offer must be
accepted (409 `OFFER_NOT_ACCEPTED`), made to the caller (403), and match the order's wholesaler, the line's product
and its qty (422 `OFFER_MISMATCH`). An offer prices one order line ever: `order_items.offer_id` is UNIQUE, so a
second use, even a concurrent one, gets 409 `OFFER_ALREADY_USED` (also after that order is cancelled).

Status flow: `placed → confirmed → paid → shipped → delivered`, with `cancelled` possible from placed or
confirmed (cancelling releases the stock). Only a verified Razorpay webhook can set `paid`.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /orders` `{items: [{productId, qty, offerId?}]}` | retailer |
| `GET /orders?status=...` | own orders (retailer or wholesaler); admin sees all |
| `GET /orders/:id` | the order's retailer or wholesaler, or an admin |
| `PATCH /orders/:id/status` `{status}` | the order's wholesaler (allowed transitions only, else 409) |
| `POST /orders/:id/payment` → Razorpay order | the order's retailer |
| `POST /payments/webhook` | Razorpay (HMAC signature in `X-Razorpay-Signature`) |

## Events

Publishes `order.placed`, `order.status.changed` (adds `previousStatus`) and `payment.completed`, with payload
`{orderId, retailerId, wholesalerId, status, total, currency}`.

Events go through a transactional outbox (`outbox.py`): each one is written to the `outbox` table in the same
transaction as the order change, and a relay thread sends them to Kafka in order and deletes them once acknowledged.
An event is never lost to a Kafka outage; delivery is at least once, with a stable `eventId` that consumers de-duplicate on.

## Configuration

`DB_URL`, `KAFKA_BROKERS`, `CATALOG_URL`, `INVENTORY_URL`, `CHAT_URL`, `PORT`. Optional `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`
and `RAZORPAY_WEBHOOK_SECRET`; without them the payment endpoints answer 503 `PAYMENTS_NOT_CONFIGURED`.

## Migrations

The schema is managed by Alembic (`migrations/versions/`); `alembic upgrade head` runs on start. Databases created
by the old `create_all` are adopted by the baseline `0001`. To change the schema, edit `models.py`, then from this
directory run `DB_URL=postgresql+psycopg://... alembic revision --autogenerate -m "add offer_id to order items" --rev-id 0002`,
review the generated file and commit it. `test_migrations_match_models` fails if `models.py` and the migrations drift.

## Tests

```bash
pip install -r requirements-dev.txt
TEST_DB_URL=postgresql+psycopg://root:rootpassword@localhost:5432/order_test pytest -q
```
