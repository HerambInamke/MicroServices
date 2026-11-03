#!/bin/sh
# Idempotent deploy: on a fresh chain, the deployer's first tx lands at CONTRACT_ADDRESS.
# On a chain that already has the contract (anvil restarted with saved state), do nothing.
set -e
if [ "$(cast code "$CONTRACT_ADDRESS" --rpc-url "$CHAIN_RPC_URL")" = "0x" ]; then
  out=$(forge create src/ProvenanceRegistry.sol:ProvenanceRegistry \
    --rpc-url "$CHAIN_RPC_URL" --private-key "$RELAYER_KEY" --broadcast)
  echo "$out"
  tx=$(echo "$out" | sed -n 's/^Transaction hash: //p')
  # The ledger scans Recorded logs from here; public RPCs reject scans from genesis.
  echo "set CONTRACT_DEPLOY_BLOCK=$(cast receipt "$tx" blockNumber --rpc-url "$CHAIN_RPC_URL")"
fi
if [ "$(cast code "$CONTRACT_ADDRESS" --rpc-url "$CHAIN_RPC_URL")" = "0x" ]; then
  echo "no contract at $CONTRACT_ADDRESS: the deployer nonce was not 0, set CONTRACT_ADDRESS to the address printed above" >&2
  exit 1
fi
echo "ProvenanceRegistry at $CONTRACT_ADDRESS"
