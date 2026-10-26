# analytics

Python/FastAPI · port **8091** · ClickHouse.

Turns Kafka events into seller dashboards and public price trends. Events are stored once per `eventId`
(ReplacingMergeTree), so redelivery is harmless.

## Endpoints

| Method & path | Who |
|---|---|
| `GET /analytics/seller/:wholesalerId/summary?period=7d\|30d\|90d` | that wholesaler or an admin |
| `GET /analytics/trends?category=&period=30d\|90d\|365d` | public: weekly sales per category |

## Events

Consumes order, payment, product and piece events (group `analytics`).

## Configuration

`CLICKHOUSE_URL`, `KAFKA_BROKERS`, `CATALOG_URL`, `PORT`.

## Tests

```bash
pip install -r requirements-dev.txt && pytest -q
TEST_CLICKHOUSE_URL=clickhouse://default:...@localhost:8123/default pytest -q   # plus real ClickHouse
```
