# wholesale-catalog

Java 17 / Spring Boot · port **8083** · Postgres `catalog_db`.

Categories and wholesale products, with a minimum order quantity (MOQ) and bulk price tiers. It is also the
source of truth for prices: order-payment asks it for a quote, so a client can never set its own price.

## Endpoints

| Method & path | Who |
|---|---|
| `GET /categories`, `POST /categories` | public / admin |
| `GET /products?wholesalerId=&categoryId=...`, `GET /products/:id` | public |
| `POST /products`, `PATCH /products/:id`, `DELETE /products/:id` | wholesaler (owner for changes) |
| `POST /products/:id/price-tiers` | owner |
| `GET /products/:id/quote?qty=` | internal: the price for that quantity after MOQ and tiers |

## Events

Publishes `product.created`, `product.updated`, `product.deleted`, which search and analytics consume.

Events go through a transactional outbox: `EventPublisher` writes each one to the `outbox` table inside the product
change's transaction, and `OutboxRelay` sends them to Kafka every second, in order, deleting each once acknowledged.
An event is never lost to a Kafka outage; delivery is at least once, with a stable `eventId`.

## Configuration

`DB_URL` (JDBC), `DB_USER`, `DB_PASS`, `KAFKA_BROKERS`, `PORT`.

## Tests

```bash
mvn -q test   # in-memory H2, no services needed
```
