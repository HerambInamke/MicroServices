#!/bin/sh
# Idempotent deploy: on a fresh chain, the deployer's first tx lands at CONTRACT_ADDRESS.
# On a chain that already has the contract (anvil restarted with saved state), do nothing.
set -e
if [ "$(cast code "$CONTRACT_ADDRESS" --rpc-url "$CHAIN_RPC_URL")" = "0x" ]; then
  forge create src/ProvenanceRegistry.sol:ProvenanceRegistry \
    --rpc-url "$CHAIN_RPC_URL" --private-key "$RELAYER_KEY" --broadcast
fi
if [ "$(cast code "$CONTRACT_ADDRESS" --rpc-url "$CHAIN_RPC_URL")" = "0x" ]; then
  echo "no contract at $CONTRACT_ADDRESS: the deployer nonce was not 0, set CONTRACT_ADDRESS to the address printed above" >&2
  exit 1
fi
echo "ProvenanceRegistry at $CONTRACT_ADDRESS"
