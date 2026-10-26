# inventory-sync

Rust (axum) · port **8090** · Redis db 0.

Stock per product and warehouse. Order reservations are atomic Lua scripts: an order either reserves every
line or nothing, so stock can never be oversold.

## Endpoints

| Method & path | Who |
|---|---|
| `GET /inventory/:productId` → `{total, warehouses}` | public |
| `PATCH /inventory/:productId` `{warehouseId, delta}` | the product's wholesaler (checked against the catalog) |
| `POST /inventory/reserve`, `POST /inventory/release` `{orderId, items}` | **internal**, order-payment only; the gateway never routes these, and requests carrying `X-User-*` identity get 403 |

## Configuration

`REDIS_URL`, `CATALOG_URL`, `PORT`.

## Tests

```bash
TEST_REDIS_URL=redis://localhost:6379/15 cargo test
```
