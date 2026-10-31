# api-gateway

Node/Express · port **3000** · the only service reachable from outside.

The single entry point. It verifies the JWT access token (HS256), removes any `X-User-*` headers sent by
the client, and forwards the caller's identity as `X-User-Id` / `X-User-Role` to the service that owns the
path. It also rate-limits per IP, proxies WebSocket upgrades, and reports aggregate health.

## Routing

`/api/v1/<prefix>/...` → owning service, with the path forwarded without `/api/v1`. The table is `ROUTES` in `app.js`:
`/auth`, `/users` → auth · `/profiles` → profile · `/products`, `/categories` → catalog · `/media` → media ·
`/orders`, `/payments` → order · `/pieces` → ledger · `/qr` → qr · `/designs` → counterfeit · `/search` → search ·
`/inventory` → inventory · `/analytics` → analytics · `/notifications` → notifications · `/chat` → chat ·
`/reviews`, `/trust-score` → reviews.

- `PUBLIC` lists the routes that work without a token. Every other route needs `Authorization: Bearer <accessToken>`.
- `INTERNAL` (`/inventory/reserve|release`, `/chat/offers/…`) is never routed.
- Paths that change when normalised (`..`, `%2e%2e`, backslashes) get 404, so they cannot slip past either list.
- WebSockets: `/api/v1/ws/notifications` and `/api/v1/ws/chat/:threadId`. The token comes from the header or
  `?access_token=`, and is stripped before the request is forwarded.
- `GET /api/v1/health` checks every service's `/health`: 200 when all are up, 503 with a per-service map otherwise.

## Configuration

| Variable | Notes |
|---|---|
| `JWT_SECRET` | Must equal auth-identity's |
| `RATE_LIMIT_PER_MINUTE` | Per IP per minute, default 300 |
| `TRUST_PROXY` | Express `trust proxy` (e.g. `1` = one proxy hop). Set only behind a proxy (prod compose): the limit then keys on the client IP from `X-Forwarded-For`. Unset: the socket address, since a directly exposed gateway would trust spoofed headers |
| `REDIS_URL` | Optional. Set: one limit shared by every gateway instance. Unset: in memory, per instance. If Redis is unreachable, requests are let through (and logged) rather than refused |
| `AUTH_URL`, `PROFILE_URL`, `CATALOG_URL`, `MEDIA_URL`, `ORDER_URL`, `LEDGER_URL`, `QR_URL`, `COUNTERFEIT_URL`, `SEARCH_URL`, `INVENTORY_URL`, `ANALYTICS_URL`, `NOTIFICATIONS_URL`, `CHAT_URL`, `REVIEWS_URL` | Base URLs of the services, all required |

## Tests

```bash
npm ci && npm test                                    # stub upstream; no other services needed
TEST_REDIS_URL=redis://localhost:6379/5 npm test      # plus the shared Redis limiter
```
