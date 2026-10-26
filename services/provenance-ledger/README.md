# provenance-ledger

Go · port **8086** · immudb + EVM chain (`ProvenanceRegistry` in `contracts/`).

Chain of custody for individual pieces. A handoff takes two steps:
1. The current owner offers the piece to the next user.
2. That user accepts by signing (`personal_sign`) the returned `pendingHandoff.dataHash` with the wallet linked
   to their account.

The service checks the signature, appends the event to immudb (tamper-evident, fast to read), and records the
hash on-chain through a relayer account. Records are idempotent, so a retry never double-records.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /pieces` `{productId, description}` | weaver |
| `GET /pieces/:id`, `GET /pieces/:id/history` | public |
| `GET /pieces/:id/verify` | public: compares immudb history with the chain |
| `POST /pieces/:id/handoffs` `{toUserId}` | current owner |
| `POST /pieces/:id/handoffs/accept` `{signature}` | the offered recipient |

## Events

Publishes `piece.registered` and `piece.handoff.signed` (`pieceId, fromUserId, toUserId, role, ...`).

## Configuration

`IMMUDB_HOST`, `IMMUDB_USER`, `IMMUDB_PASSWORD`, `IMMUDB_DB`, `CHAIN_RPC_URL`, `RELAYER_KEY`, `CONTRACT_ADDRESS`,
`AUTH_URL` (looks up linked wallets), `KAFKA_BROKERS`, `PORT`. The contract ABI is embedded as `registry.abi.json`.

## Tests

```bash
go test ./...                                                                  # fakes only
TEST_IMMUDB_HOST=localhost TEST_CHAIN_RPC_URL=http://localhost:8545 go test ./...  # plus real immudb and Anvil with the registry deployed
```
