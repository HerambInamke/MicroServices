# web

Vite + React + TypeScript single-page app. It talks only to the API gateway (`/api/v1/...`); it is not part of
`docker-compose.yml`.

## Run

Requires Node.js 22+ and the stack running on `localhost:3000` (`docker compose up -d` at the repo root).

```bash
cd apps/web
npm ci
npm run dev        # http://localhost:5173
```

The dev server proxies `/api` (REST and WebSockets) to `http://localhost:3000`, so no CORS setup is needed.

## Pages

| Path | What | Login |
|---|---|---|
| `/login` | log in, or sign up as retailer / wholesaler / weaver / dye-house | no |
| `/` | product list; search box uses `GET /search?type=product` | no |
| `/products/:id` | details and price tiers; retailers can place an order or message the wholesaler | no |
| `/verify/:code?` | enter a tag code, see the piece's chain of custody (`GET /qr/:code/resolve`) | no |
| `/orders` | own orders and their status | yes |
| `/chat/:threadId?` | conversations and text messages; new messages arrive over `/api/v1/ws/chat/:threadId` | yes |
| `/notifications` | inbox with mark-read | yes |

The session (access + refresh token and the `/auth/me` user) is kept in `localStorage`. On a 401 the client
refreshes once (shared by parallel requests, since refresh tokens are single-use) and retries; if the refresh
fails it logs out.

## Checks

```bash
npm run typecheck
npm test           # Vitest: fetch wrapper and session logic (src/api.test.ts)
npm run build      # static files in dist/
```
