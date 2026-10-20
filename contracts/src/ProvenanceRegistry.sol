// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// Append-only chain of custody per piece. The relayer (the ledger service) pays gas,
/// but the actor of every event is recovered from their own signature, so the relayer
/// cannot record a handoff on someone else's behalf.
contract ProvenanceRegistry {
    struct ProvenanceEvent {
        address actor;
        string role;
        uint64 timestamp;
        bytes32 dataHash;
    }

    address public immutable relayer;
    mapping(bytes32 => ProvenanceEvent[]) private events;
    mapping(bytes32 => bool) public used;

    event Recorded(bytes32 indexed pieceId, uint256 index, address indexed actor, string role, bytes32 dataHash);

    error NotRelayer();
    error BadSignature();
    error AlreadyRecorded();

    constructor() {
        relayer = msg.sender;
    }

    /// `signature` is an EIP-191 personal_sign over the 32-byte `dataHash` (what MetaMask produces).
    function record(bytes32 pieceId, string calldata role, bytes32 dataHash, bytes calldata signature)
        external
        returns (address actor)
    {
        if (msg.sender != relayer) revert NotRelayer();
        if (used[dataHash]) revert AlreadyRecorded();
        actor = recover(keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", dataHash)), signature);
        used[dataHash] = true;
        events[pieceId].push(ProvenanceEvent(actor, role, uint64(block.timestamp), dataHash));
        emit Recorded(pieceId, events[pieceId].length - 1, actor, role, dataHash);
    }

    function count(bytes32 pieceId) external view returns (uint256) {
        return events[pieceId].length;
    }

    function history(bytes32 pieceId) external view returns (ProvenanceEvent[] memory) {
        return events[pieceId];
    }

    function recover(bytes32 digest, bytes calldata sig) private pure returns (address signer) {
        if (sig.length != 65) revert BadSignature();
        bytes32 r = bytes32(sig[0:32]);
        bytes32 s = bytes32(sig[32:64]);
        uint8 v = uint8(sig[64]);
        if (v < 27) v += 27;
        // Reject high-s (malleable) signatures, as OpenZeppelin's ECDSA does.
        if (uint256(s) > 0x7FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF5D576E7357A4501DDFE92F46681B20A0) revert BadSignature();
        signer = ecrecover(digest, v, r, s);
        if (signer == address(0)) revert BadSignature();
    }
}
