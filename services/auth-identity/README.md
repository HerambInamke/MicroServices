# auth-identity

Go · port **8081** · Postgres `auth_db`.

Accounts, roles, JWTs and wallet linking. Roles: `weaver`, `dye-house`, `wholesaler`, `retailer`, `admin`.
Passwords are bcrypt-hashed. Access tokens are short-lived HS256 JWTs. Refresh tokens are tracked by id in
Postgres and revoked when used (rotation) or on logout. An admin is seeded from `ADMIN_EMAIL` / `ADMIN_PASSWORD` on start.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /auth/signup` `{email, password, role, phone?}` | public (role cannot be admin) |
| `POST /auth/login` → `{accessToken, refreshToken}` | public |
| `POST /auth/refresh`, `POST /auth/logout` | public / caller |
| `GET /auth/me` | caller |
| `POST /auth/wallet/link` `{address}`, `DELETE /auth/wallet/unlink` | caller (the ledger checks handoff signatures against this wallet) |
| `GET /users/:id` → `{id, role, walletAddress}` | any logged-in user; used by other services, no email or phone |
| `PATCH /users/:id/role` | admin |

## Configuration

`DB_URL` (Postgres), `JWT_SECRET` (must equal the gateway's), `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `PORT`.
SQL migrations in `migrations/` run on start.

## Tests

```bash
go test ./...                                  # handlers with an in-memory store
TEST_DB_URL=postgres://... go test ./...       # plus the Postgres store and migrations
```
