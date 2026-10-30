# reviews

Node/Express · port **8094** · Mongo `reviews`.

Reviews of wholesalers, and a public trust score. Only the retailer of a **delivered** order can review it,
and only once. The order is read from order-payment with the caller's own identity, and the wholesaler is
taken from the order, never from the request.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /reviews` `{orderId, rating: 1-5, text?}` | the order's retailer (409 `ORDER_NOT_DELIVERED` / `ALREADY_REVIEWED`) |
| `GET /reviews?wholesalerId=&page=&limit=` | public (buyer and order ids are hidden) |
| `GET /trust-score/:wholesalerId` | public: `{score, reviewCount, avgRating, delivered, cancelled}` |

## Trust score

```
rating = (5·3 + Σratings) / (5 + reviewCount)          # Bayesian: 5 phantom 3-star reviews
fulfil = (delivered + 1) / (delivered + cancelled + 2)  # Laplace-smoothed
score  = round(100 · (0.7 · (rating − 1)/4 + 0.3 · fulfil))
```

With no data the score is 50, and a single 5-star review gives 56, not 100. Delivered and cancelled counts come
from consuming `order.status.changed` (group `reviews`), upserted by order, so redelivery is harmless.

## Configuration

`MONGO_URL`, `KAFKA_BROKERS`, `ORDER_URL`, `PORT`.

## Tests

```bash
TEST_MONGO_URL="mongodb://root:rootpassword@localhost:27017/reviews_test?authSource=admin&directConnection=true" npm test
```
