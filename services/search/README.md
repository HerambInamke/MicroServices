# search

Python/FastAPI · port **8089** · Elasticsearch.

Full-text search over products, wholesalers and designs. Products and designs stay in sync through events;
wholesalers appear on the next admin reindex, which rebuilds everything from the source services.

## Endpoints

| Method & path | Who |
|---|---|
| `GET /search?q=&type=product\|wholesaler\|design` | public |
| `GET /search/suggest?q=` | public |
| `POST /search/reindex` | admin |

## Events

Consumes `product.created`, `product.updated`, `product.deleted` (catalog) and `design.registered`, `design.deleted`
(counterfeit-detection), group `search`.

## Configuration

`ELASTICSEARCH_URL`, `KAFKA_BROKERS`, `CATALOG_URL`, `PROFILE_URL`, `COUNTERFEIT_URL`, `PORT`.

## Tests

```bash
pip install -r requirements-dev.txt && pytest -q
TEST_ES_URL=http://localhost:9200 pytest -q   # plus real Elasticsearch
```
