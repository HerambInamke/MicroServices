# Provenance

A B2B marketplace for Indian textile wholesale (starting with sarees), with a tamper-evident,
publicly verifiable **chain of custody** for each piece: weaver → dye-house → wholesaler → retailer.
Every handoff is signed by the receiver's own wallet and anchored in a smart contract, so the history
cannot be quietly rewritten by whoever runs the app. Around that trust core sits a full wholesale
marketplace: catalog, bulk orders and payments, search, stock, analytics, chat with price
negotiation, notifications, and reviews with a trust score.

It is also a deliberately polyglot reference architecture: 16 services in Go, Node.js, Python, Java,
Rust and Solidity, each owning its own datastore and talking over HTTP (through one gateway) and Kafka.

---

## 1. How it works

### Two tracks

**Trust (the differentiator)**
1. A weaver registers a piece (`POST /pieces`) and prints its QR code (`POST /qr/:pieceId/generate`).
2. The current owner offers the piece to the next actor (`POST /pieces/:id/handoffs {toUserId}`).
3. The receiver accepts by signing the handoff hash with their linked wallet (`POST /pieces/:id/handoffs/accept {signature}`).
4. The ledger writes the handoff to **immudb** (fast, tamper-evident reads) and anchors its hash in the
   `ProvenanceRegistry` smart contract (local Anvil chain, or Polygon Amoy).
5. Anyone scans the QR (`GET /qr/:code/resolve`, no login) and sees the timeline; `GET /pieces/:id/verify`
   re-checks it against the chain.
6. **Counterfeit detection**: a wholesaler registers a design image (CLIP embedding in Weaviate);
   anyone can upload a photo to `POST /designs/match` to find which registered design it resembles, and
   a second wholesaler cannot register the same design.

**Commerce (the marketplace)**
1. Wholesalers list products with MOQ and price tiers (catalog); images go to MinIO (media).
2. Retailers search (Elasticsearch), chat with a wholesaler, and negotiate with offers
   (offer → counter → accept).
3. A retailer places a bulk order; stock is reserved atomically (inventory-sync); the wholesaler confirms;
   the retailer pays with Razorpay; a verified Razorpay webhook marks it paid; the wholesaler ships and
   delivers.
4. Every step emits a Kafka event. Notifications turns events into a per-user inbox with a live
   WebSocket; search and analytics index them; reviews records delivered/cancelled outcomes.
5. After delivery the retailer can review the order once; the wholesaler's public **trust score** blends
   a smoothed average rating (70%) with their delivered-vs-cancelled ratio (30%).

### Architecture

```
   clients (web / mobile / curl): HTTP + WebSocket, JWT bearer
                 │
                 ▼  :3000, the only published port
        ┌─────────────────┐
        │   api-gateway   │  verifies JWT → sets X-User-Id / X-User-Role,
        │     (Node)      │  rate-limits, proxies HTTP and WebSocket
        └────────┬────────┘
                 │  /api/v1/<prefix> → owning service (table below)
                 ▼
   15 services, each with its own datastore ◄──── Kafka events ────► each other
   (Postgres, Mongo, Redis, MinIO, immudb + EVM chain, Weaviate, Elasticsearch, ClickHouse)
```

Inside the network every service trusts the `X-User-Id` / `X-User-Role` headers the gateway sets after
verifying the token, and enforces its own authorization. **That is why no service port other than the
gateway's may ever be exposed.**

### Services

| Service | Port | Stack | Owns | What it does |
|---|---|---|---|---|
| api-gateway | 3000 | Node/Express | – | JWT check, routing, rate limit, WebSocket proxy, `/api/v1/health` |
| auth-identity | 8081 | Go | Postgres `auth_db` | signup/login/refresh, roles, wallet linking, seeded admin |
| business-profile | 8082 | Node | Mongo `business_profiles` | business profiles, GSTIN, certifications |
| wholesale-catalog | 8083 | Java/Spring | Postgres `catalog_db` | categories, products, MOQ, price tiers |
| order-payment | 8084 | Python/FastAPI | Postgres `order_db` | orders, status flow, Razorpay payments + webhook |
| media-upload | 8085 | Go | Postgres `media_db` + MinIO | image upload/serve |
| provenance-ledger | 8086 | Go | immudb + EVM chain | pieces, signed handoffs, on-chain verify |
| qr-verification | 8087 | Go | Postgres `qr_db` | QR codes, public resolve, scan log |
| counterfeit-detection | 8088 | Python + CLIP | Weaviate | design registry and image matching |
| search | 8089 | Python | Elasticsearch | product/wholesaler/design search, suggest |
| inventory-sync | 8090 | Rust | Redis db 0 | per-warehouse stock, atomic reservations |
| analytics | 8091 | Python | ClickHouse | seller summary, public price trends |
| notifications | 8092 | Go | Redis db 1 | inbox from Kafka events, live WS |
| chat | 8093 | Node | Mongo `chat` + Redis db 2 | threads, messages, offers, live WS |
| reviews | 8094 | Node | Mongo `reviews` | reviews on delivered orders, trust score |
| contracts | – | Solidity/Foundry | chain | `ProvenanceRegistry` (deployed by `contract-deploy`) |

### Kafka topics

| Topic | Producer | Consumers |
|---|---|---|
| `order.placed`, `order.status.changed`, `payment.completed` | order-payment | notifications, analytics, reviews (`status.changed`) |
| `product.created/updated/deleted` | wholesale-catalog | search |
| `piece.registered`, `piece.handoff.signed` | provenance-ledger | notifications (handoff) |
| `chat.message.sent` | chat | notifications |

Every event uses the envelope in `schemas/events.json` (`eventId, timestamp, sourceService, eventType, payload`).
Consumers are idempotent (dedup by `eventId` or upsert by key) because Kafka delivers at least once.

### Main API (all under `/api/v1`)

Public (no token): `POST /auth/signup|login|refresh`, `GET /profiles…`, `GET /products…`, `GET /categories…`,
`GET /media/:id[/content]`, `GET /pieces…`, `GET /qr/:code/resolve`, `POST /qr/:code/scan-log`, `GET /designs[/:id]`,
`GET /search[/suggest]`, `GET /inventory/:productId`, `GET /analytics/trends`, `GET /reviews?wholesalerId=`,
`GET /trust-score/:wholesalerId`, `POST /payments/webhook` (Razorpay signature).

Everything else needs `Authorization: Bearer <accessToken>`:
orders and payments, profiles/products writes, pieces and handoffs, designs register/match, inventory updates,
`GET /analytics/seller/:id/summary`, `GET /notifications`, `PATCH /notifications/:id/read`,
`/chat/threads…` (create, list, messages, offers, respond), `POST /reviews`.

WebSockets (token as `?access_token=` because browsers cannot set headers):
`ws://<host>/api/v1/ws/notifications`, `ws://<host>/api/v1/ws/chat/:threadId`.

Responses always use `{ "data": …, "error": { "code", "message" } | null, "meta": {} }`.

---

## 2. Run it locally

Requirements: **Docker Desktop** (with ~8 GB RAM for Docker) and, for the smoke test, **Node.js 22+**.
Nothing else needs installing; every service builds in its own container.

```bash
git clone <repo> && cd MicroServices
cp .env.example .env          # optional locally: every value has a dev default
docker compose up --build -d  # first build takes a while (the CLIP image is large)
curl http://localhost:3000/api/v1/health   # every service should say "ok"
```

End-to-end check (drives Phases 1–5 through the gateway exactly like a client):

```bash
(cd scripts && npm install)   # once
node scripts/smoke.mjs
```

Re-running the smoke: Phase 3 needs an empty design registry first:
`docker compose up -d --force-recreate weaviate counterfeit-detection`.

Useful commands:

```bash
docker compose ps                         # what is running
docker compose logs -f chat reviews       # follow one or more services
docker compose up -d --build chat         # rebuild one service after a change
docker compose down                       # stop (data volumes kept)
docker compose down -v                    # stop and wipe all data
```

Logging in as the seeded admin: `POST /api/v1/auth/login {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD}`.

---

## 3. Configuration: the `.env` file

There is **one** `.env` file, in the repository root next to `docker-compose.yml`. Docker Compose reads
it automatically. It is git-ignored; `.env.example` is the template. Services do not read `.env`
themselves: compose passes each one the variables it needs (see `docker-compose.yml`).

### Must change before any shared or public deployment

| Variable | Used by | What to put |
|---|---|---|
| `JWT_SECRET` | api-gateway, auth-identity | Long random string (`openssl rand -hex 32`). Both services get the same value. |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | auth-identity (seeded admin), smoke | Your admin login. |
| `POSTGRES_PASSWORD` | Postgres + auth, catalog, orders, media, qr | Strong password, URL-safe characters only (letters, digits, `-_`). |
| `MONGO_PASSWORD` | Mongo + profile, chat, reviews | Same rules. |
| `MINIO_PASSWORD` | MinIO + media-upload | Same rules, at least 8 characters. |
| `CLICKHOUSE_PASSWORD` | ClickHouse + analytics | Same rules. |

The database passwords are applied when a data volume is **first** created. Changing them later means
`docker compose down -v` (wipes data) or changing the password inside the database by hand.

### Needed for specific features

| Variable | Feature | Without it |
|---|---|---|
| `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET` | Payments (dashboard.razorpay.com → API keys, test mode first) | `POST /orders/:id/payment` answers 503 `PAYMENTS_NOT_CONFIGURED`. Orders can still be placed and confirmed, but not paid, so not shipped, delivered or reviewed. |
| `RAZORPAY_WEBHOOK_SECRET` | Razorpay webhook → order `paid`. Create the webhook in the Razorpay dashboard pointing at `https://<your-domain>/api/v1/payments/webhook`, events `payment.captured`, `payment.failed` | Webhook answers 503. |
| `PUBLIC_BASE_URL` | Origin printed into QR codes, e.g. `https://provenance.example.com` | Defaults to `http://localhost:3000`, so printed QR codes only work locally. |
| `CHAIN_RPC_URL`, `RELAYER_KEY`, `CONTRACT_ADDRESS` | Real chain (e.g. Polygon Amoy `https://rpc-amoy.polygon.technology`, a funded relayer private key, the deployed `ProvenanceRegistry` address) | Uses the local Anvil chain with its well-known dev key. **Never use the default key on a real network.** |
| `MATCH_THRESHOLD` | Counterfeit match cut-off (0–1) | Placeholder 0.9 until calibrated on real photos. |
| `RATE_LIMIT_PER_MINUTE` | Gateway requests per IP per minute | 300. |

### Example production `.env`

```env
JWT_SECRET=3f9c...64-hex-chars...
ADMIN_EMAIL=ops@yourcompany.in
ADMIN_PASSWORD=a-long-admin-password
POSTGRES_PASSWORD=pg-Strong-Password-1
MONGO_PASSWORD=mongo-Strong-Password-1
MINIO_PASSWORD=minio-Strong-Password-1
CLICKHOUSE_PASSWORD=ch-Strong-Password-1
RAZORPAY_KEY_ID=rzp_live_xxxxxxxx
RAZORPAY_KEY_SECRET=xxxxxxxxxxxxxxxx
RAZORPAY_WEBHOOK_SECRET=the-secret-you-typed-in-the-dashboard
PUBLIC_BASE_URL=https://provenance.yourcompany.in
CHAIN_RPC_URL=https://rpc-amoy.polygon.technology
RELAYER_KEY=0x<funded relayer private key>
CONTRACT_ADDRESS=0x<deployed ProvenanceRegistry>
RATE_LIMIT_PER_MINUTE=300
```

---

## 4. Deploying

The compose file runs the whole platform on one machine (a VM with 4+ vCPU and 16 GB RAM is comfortable).

1. Install Docker Engine + the compose plugin on the server and copy the repository there.
2. Create `.env` as above (never commit it).
3. **Close the infrastructure ports.** `docker-compose.yml` publishes Postgres 5432, Mongo 27017, MinIO
   9000/9001, Kafka 9092/29092, ZooKeeper 2181, Anvil 8545 and immudb 3322 for local debugging. On a
   server, firewall everything except 80/443, or delete those `ports:` entries. Only the gateway should be
   reachable, and only through the TLS proxy below.
4. Put a TLS reverse proxy (Caddy, nginx, a cloud load balancer) in front of `localhost:3000`, and make sure
   it passes WebSocket upgrades (`Upgrade` / `Connection` headers) for `/api/v1/ws/`. With Caddy this is one
   line: `provenance.example.com { reverse_proxy localhost:3000 }`.
5. `docker compose up --build -d`, then `curl https://<domain>/api/v1/health`.
6. Point the Razorpay webhook at `https://<domain>/api/v1/payments/webhook`.
7. For the real chain: deploy `contracts/src/ProvenanceRegistry.sol` with Foundry (`contracts/deploy.sh`),
   set `CHAIN_RPC_URL`, `RELAYER_KEY`, `CONTRACT_ADDRESS`, and remove the `anvil` / `contract-deploy`
   dependency from `provenance-ledger` in compose.
8. Backups: the named volumes hold Postgres, Mongo, MinIO, immudb, Redis, Elasticsearch, Weaviate and
   ClickHouse data; snapshot them (or move to managed databases by changing the URLs in compose).

Known limits worth knowing before going live:
- The immudb login is its default (`immudb`/`immudb`); it is only reachable inside the Docker network once
  port 3322 is closed.
- The gateway rate limiter is in memory, per gateway instance.
- Events are published after the database write without an outbox: if Kafka is down at that moment the
  event is lost (the main data is safe; inbox/search/analytics miss it).
- Counterfeit matching threshold is uncalibrated (see `tasks/open-items.md` locally).
- There is no frontend yet; this is the API platform.

---

## 5. Tests

GitHub Actions (`.github/workflows/ci.yml`) runs every service's tests on each pull request and push to
`main`, one check per service, against real Mongo, Redis, Postgres, immudb and Anvil.

Every service has its own tests, run in Docker (no local toolchains needed). With the stack up:

```bash
# Node services (chat and reviews use the compose Mongo/Redis)
docker run --rm -v "$PWD/services/api-gateway:/app" -w /app node:20-alpine sh -c "npm ci && npm test"
docker compose run --rm --no-deps -e MONGO_URL="mongodb://root:rootpassword@mongodb:27017/bp_test?authSource=admin" business-profile npm test
docker run --rm --network microservices_provenance_net -v "$PWD/services/chat:/app" -w /app \
  -e TEST_MONGO_URL="mongodb://root:rootpassword@mongodb:27017/chat_test?authSource=admin" -e TEST_REDIS_URL=redis://redis:6379/5 \
  node:20-alpine sh -c "npm ci && node --test"
docker run --rm --network microservices_provenance_net -v "$PWD/services/reviews:/app" -w /app \
  -e TEST_MONGO_URL="mongodb://root:rootpassword@mongodb:27017/reviews_test?authSource=admin" \
  node:20-alpine sh -c "npm ci && node --test"
# Go services
docker run --rm --network microservices_provenance_net -e TEST_REDIS_URL=redis://redis:6379/15 \
  -v "$PWD/services/notifications:/app" -w /app golang:1.23-alpine go test ./...
# Solidity
docker build -t provenance-contracts contracts && docker run --rm provenance-contracts -c "forge test"
```

(Use your own passwords in the URLs if you changed them in `.env`.) The other services follow the same
pattern; their exact commands are in each service's tests and in `docs/CONTRIBUTING.md` locally.
Windows Git Bash: prefix `docker run` with `MSYS_NO_PATHCONV=1` and use `$(pwd -W)` instead of `$PWD`.

---

## 6. Repository layout

```
services/<name>/     one folder per service: code, tests, Dockerfile
contracts/           ProvenanceRegistry smart contract (Foundry) + deploy script
schemas/events.json  Kafka event envelope
infra/               Postgres init (one database per service)
scripts/smoke.mjs    end-to-end test through the gateway
docker-compose.yml   the whole platform
.env.example         configuration template
```
