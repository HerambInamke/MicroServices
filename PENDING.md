# Pending work: Provenance (MicroServices)

Snapshot: 2026-10-30. Done on 2026-10-30: D1 (#28), D3 (#29), D6 (#30), D2 outbox everywhere (#31 order-payment + catalog, #33 ledger,
#34 chat, which runs MongoDB as a single-node replica set), and B1. Phases 1–5 are built and merged.
Phase 6 (OSS polish) is mostly done. Smoke test Phases 1–5 and CI (17 jobs) are green.

To resume: open this file and pick a section. Items are grouped by **what blocks them**.

---

## A. Blocked on you (inputs only you can provide)

### A1. Calibrate counterfeit matching on real saree photos
- **Why:** `DEFAULT_MATCH_THRESHOLD = 0.9` in `services/counterfeit-detection/app.py` is a placeholder. On synthetic
  patterns, neither CLIP nor DINOv2 could separate "same design" from "different design" (numbers in `tasks/phase3-todo.md`).
- **Needs from you:** about 10 real saree designs, 2–3 photos each, in folders `<dir>/<design-name>/<photo>.jpg`.
- **Then:**
  1. Run `test_threshold_separates_real_photos` (command in `docs/CONTRIBUTING.md`).
  2. Set the threshold between the lowest same-design score and the highest different-design score.
  3. Decide whole-image vs multi-crop (3x3 tiles + chamfer, see `tasks/phase3-multicrop_experiment.py`).
  4. Switch the Phase 3 smoke from the identical image to a transformed copy (marked `shortcut:` in `scripts/smoke.mjs`).
  5. Add the real-CLIP end-to-end test (`TEST_CLIP=1` + `TEST_WEAVIATE_URL`).

### A2. Razorpay test keys: test the full payment → delivery → review path
- **Why:** an order can only become `paid` through a verified Razorpay webhook, so `shipped → delivered → review →
  trust score` has never run end to end. It is covered by unit tests only, and Checkpoint N's e2e box is still open.
- **Needs from you:** `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`, `RAZORPAY_WEBHOOK_SECRET` (test mode) in `.env`.
- **Then:** extend `scripts/smoke.mjs` Phase 5 with pay (signed webhook) → ship → deliver → review → trust score
  moves. This also closes the Phase 1 items "real Razorpay checkout" and "`payment.completed` on real Kafka".

### A3. Real chain (Polygon Amoy), optional
- **Needs from you:** a funded relayer private key.
- **Then:** deploy `ProvenanceRegistry` with `contracts/deploy.sh`, and set `CHAIN_RPC_URL`, `RELAYER_KEY`,
  `CONTRACT_ADDRESS`. **Before going live, fix first:** `chain.go` scans logs from genesis, which public Amoy
  RPCs reject. Store the deploy block and query from it (marked `shortcut:` in `services/provenance-ledger/chain.go`).

---

## B. Waiting for your go-ahead (outward-facing)

### B1. "Good first issue" issues: done
Filed #26 (D7, analytics backfill) and #27 (D9, ledger history paging) with the `good first issue` label.

### B2. Human-review checkpoints
Never-ticked "Human review" boxes, for you to look over and tick:
- `tasks/todo.md` (Phase 1: before 1B, before 1C, end)
- `tasks/phase2-todo.md` (before 2B, end)
- `tasks/phase3-todo.md` (F, G)
- `tasks/phase4-todo.md` (4 boxes)
- `tasks/phase5-todo.md` (L, M, N, O)

---

## C. Before a real deployment (checklist, also in README section 4)

- [ ] Real values in `.env`: `JWT_SECRET`, `ADMIN_EMAIL` / `ADMIN_PASSWORD`, `POSTGRES_PASSWORD`, `MONGO_PASSWORD`,
      `MINIO_PASSWORD`, `CLICKHOUSE_PASSWORD`, `PUBLIC_BASE_URL`, plus Razorpay.
      `docker-compose.prod.yml` refuses to start until these (plus `IMMUDB_PASSWORD`, `DOMAIN`) are set.
- [x] Close the infrastructure ports that compose publishes (5432, 27017, 9000/9001, 9092/29092, 2181, 8545, 3322).
      Only the gateway may be reachable; otherwise anyone can inject fake Kafka events. (`docker-compose.prod.yml`)
- [x] TLS reverse proxy in front of `:3000` that passes WebSocket upgrades for `/api/v1/ws/`. (Caddy, `infra/Caddyfile`)
- [x] immudb still uses its default login `immudb`/`immudb`; change it, or make sure 3322 is closed.
      (`IMMUDB_PASSWORD` in the prod override; ledger uses the same value)
- [ ] Gateway rate limit behind the proxy: it keys on the proxy's IP, so all clients share one bucket. Trust
      `X-Forwarded-For` from Caddy (`app.set('trust proxy', ...)` plus the WebSocket path).
- [ ] Volume backups (or move to managed databases). Only Mongo has a named volume so far.

---

## D. Engineering backlog (known limits, each marked `shortcut:` in the code)

| # | Item | Where | Risk if left |
|---|---|---|---|
| D4 | **Ledger handoff lock is in process.** It is correct only with one ledger replica; use an immudb precondition before scaling out. | `services/provenance-ledger/handlers.go` | Low (single instance) |
| D5 | **No schema migrations** for order-payment (create_all) and media-upload (IF NOT EXISTS). Adopt Alembic / a migration tool at the first schema change. | `order-payment/app.py`, `media-upload/store.go` | Low now |
| D7 | **Analytics:** if the catalog is unreachable during ingest, the line is filed under category 0 for good. Add a backfill job (#26). | `services/analytics/ingest.py` | Low |
| D8 | **Weaviate paging** is offset-based, unordered, capped at 10k. Switch to the cursor API for large registries. | `counterfeit-detection/store.py` | Low |
| D9 | **Ledger history scan** caps at 1000 entries per piece. Page it if pieces get long histories (#27). | `provenance-ledger/store.go` | Low |
| D10 | **Trust-score weights** (0.7 rating / 0.3 fulfilment, prior m=3, C=5) are a first guess; tune with real data. | `services/reviews/score.js` | Product |

---

## E. Future features (agreed out of scope so far)

- Negotiated (accepted-offer) prices flowing into orders; today orders always use catalog prices.
- Push/device notifications (`POST /notifications/subscribe`); there is no push provider yet.
- A frontend (web/mobile). Ask where to put it before scaffolding.
- Wholesaler replies to reviews (decided "no" for now).

---

## F. Housekeeping

- `tasks/phase3-todo.md` T19: three boxes (Weaviate in compose, CLIP embedder, synthetic calibration) are unticked.
  The first two are built and merged; tick them. The third was replaced by A1.
- Local MicroServices branches `phase5-chat`, `phase5-reviews`, `phase5-wrapup`, `phase6-ci`, `phase6-service-docs`
  still hold commits with the old attribution trailer (local only; already deleted on GitHub). To remove them:
  `git branch -D phase5-chat phase5-reviews phase5-wrapup phase6-ci phase6-service-docs`.
- Merged local branches `inventory-internal-guard`, `design-delete`, `offer-response-notify` can be deleted (`git branch -d ...`).
- Product events (`product.created` / `product.deleted`) travel on separate topics, so a create and a quick delete
  can reach search out of order. Designs use one topic (`design.changed`) for this reason; products could follow.
- Not yet security-reviewed: auth `GET /users/:id` exposure rules, and order-payment's party check (reviews relies on it).

---

## Quick reference

- Start everything: `docker compose up --build -d` → `curl localhost:3000/api/v1/health`
- End-to-end check: `node scripts/smoke.mjs` (re-runnable)
- Full guide: `README.md` (root); per service: `services/<name>/README.md`; carried-forward detail: `tasks/open-items.md`
