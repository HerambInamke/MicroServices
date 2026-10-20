// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {ProvenanceRegistry} from "../src/ProvenanceRegistry.sol";

contract ProvenanceRegistryTest is Test {
    ProvenanceRegistry reg;
    bytes32 constant PIECE = keccak256("piece-1");
    uint256 constant ALICE_KEY = 0xA11CE;
    uint256 constant BOB_KEY = 0xB0B;

    function setUp() public {
        reg = new ProvenanceRegistry();
    }

    function sign(uint256 key, bytes32 dataHash) internal pure returns (bytes memory) {
        bytes32 digest = keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", dataHash));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, digest);
        return abi.encodePacked(r, s, v);
    }

    function test_recordsSignerAsActor() public {
        bytes32 h = keccak256("h1");
        address actor = reg.record(PIECE, "dye-house", h, sign(ALICE_KEY, h));
        assertEq(actor, vm.addr(ALICE_KEY));
        ProvenanceRegistry.ProvenanceEvent[] memory evs = reg.history(PIECE);
        assertEq(evs.length, 1);
        assertEq(evs[0].actor, vm.addr(ALICE_KEY));
        assertEq(evs[0].role, "dye-house");
        assertEq(evs[0].dataHash, h);
    }

    function test_historyKeepsOrder() public {
        bytes32 h1 = keccak256("h1");
        bytes32 h2 = keccak256("h2");
        reg.record(PIECE, "dye-house", h1, sign(ALICE_KEY, h1));
        reg.record(PIECE, "wholesaler", h2, sign(BOB_KEY, h2));
        ProvenanceRegistry.ProvenanceEvent[] memory evs = reg.history(PIECE);
        assertEq(reg.count(PIECE), 2);
        assertEq(evs[0].actor, vm.addr(ALICE_KEY));
        assertEq(evs[1].actor, vm.addr(BOB_KEY));
    }

    function test_signatureOverOtherHashYieldsOtherActor() public {
        bytes32 h = keccak256("h1");
        address actor = reg.record(PIECE, "dye-house", h, sign(ALICE_KEY, keccak256("other")));
        assertTrue(actor != vm.addr(ALICE_KEY));
    }

    function test_rejectsMalformedSignature() public {
        vm.expectRevert(ProvenanceRegistry.BadSignature.selector);
        reg.record(PIECE, "dye-house", keccak256("h1"), hex"1234");
    }

    function test_rejectsReplay() public {
        bytes32 h = keccak256("h1");
        bytes memory sig = sign(ALICE_KEY, h);
        reg.record(PIECE, "dye-house", h, sig);
        vm.expectRevert(ProvenanceRegistry.AlreadyRecorded.selector);
        reg.record(keccak256("piece-2"), "dye-house", h, sig);
    }

    function test_onlyRelayerCanRecord() public {
        bytes32 h = keccak256("h1");
        bytes memory sig = sign(ALICE_KEY, h);
        vm.prank(vm.addr(ALICE_KEY));
        vm.expectRevert(ProvenanceRegistry.NotRelayer.selector);
        reg.record(PIECE, "dye-house", h, sig);
    }
}
