# order-payment

Python/FastAPI · port **8084** · Postgres `order_db`.

Bulk orders and Razorpay payments. Order prices come from the catalog's quote, and stock is reserved through
inventory-sync before the order is saved. One order belongs to one wholesaler.

Status flow: `placed → confirmed → paid → shipped → delivered`, with `cancelled` possible from placed or
confirmed (cancelling releases the stock). Only a verified Razorpay webhook can set `paid`.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /orders` `{items: [{productId, qty}]}` | retailer |
| `GET /orders?status=...` | own orders (retailer or wholesaler); admin sees all |
| `GET /orders/:id` | the order's retailer or wholesaler, or an admin |
| `PATCH /orders/:id/status` `{status}` | the order's wholesaler (allowed transitions only, else 409) |
| `POST /orders/:id/payment` → Razorpay order | the order's retailer |
| `POST /payments/webhook` | Razorpay (HMAC signature in `X-Razorpay-Signature`) |

## Events

Publishes `order.placed`, `order.status.changed` (adds `previousStatus`) and `payment.completed`, with payload
`{orderId, retailerId, wholesalerId, status, total, currency}`.

## Configuration

`DB_URL`, `KAFKA_BROKERS`, `CATALOG_URL`, `INVENTORY_URL`, `PORT`. Optional `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`
and `RAZORPAY_WEBHOOK_SECRET`; without them the payment endpoints answer 503 `PAYMENTS_NOT_CONFIGURED`.

## Tests

```bash
pip install -r requirements-dev.txt
TEST_DB_URL=postgresql+psycopg://root:rootpassword@localhost:5432/order_test pytest -q
```
