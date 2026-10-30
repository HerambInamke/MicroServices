package main

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"net/http"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

// personalSign mimics MetaMask: EIP-191 prefix, v in {27, 28}.
func personalSign(t *testing.T, key *ecdsa.PrivateKey, dataHashHex string) string {
	t.Helper()
	h := common.HexToHash(dataHashHex)
	sig, err := crypto.Sign(accounts.TextHash(h[:]), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return hexutil.Encode(sig)
}

type handoffFixture struct {
	s       *Server
	st      *fakeStore
	chain   *fakeChain
	pieceID string
	dyeKey  *ecdsa.PrivateKey
}

// newHandoffFixture registers a piece by weaver w1; d1 is a dye-house with a linked wallet.
func newHandoffFixture(t *testing.T) *handoffFixture {
	t.Helper()
	s, st := newTestServer()
	key, _ := crypto.GenerateKey()
	s.users = fakeUsers{
		"w1": {ID: "w1", Role: "weaver"},
		"d1": {ID: "d1", Role: "dye-house", WalletAddress: crypto.PubkeyToAddress(key.PublicKey).Hex()},
		"h1": {ID: "h1", Role: "wholesaler", WalletAddress: "0x0000000000000000000000000000000000000001"},
		"w2": {ID: "w2", Role: "weaver"},
		"r9": {ID: "r9", Role: "retailer"},
	}
	_, env := do(t, s, "POST", "/pieces", "w1", "weaver", `{}`)
	return &handoffFixture{s: s, st: st, chain: s.chain.(*fakeChain), pieceID: env["data"].(map[string]any)["id"].(string), dyeKey: key}
}

func (f *handoffFixture) offer(t *testing.T, to string) string {
	t.Helper()
	code, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs", "w1", "weaver", `{"toUserId":"`+to+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("offer: want 201, got %d: %v", code, env)
	}
	return env["data"].(map[string]any)["pendingHandoff"].(map[string]any)["dataHash"].(string)
}

func TestHandoffOfferThenSignedAccept(t *testing.T) {
	f := newHandoffFixture(t)
	hash := f.offer(t, "d1")

	code, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", `{"signature":"`+personalSign(t, f.dyeKey, hash)+`"}`)
	if code != http.StatusOK {
		t.Fatalf("accept: want 200, got %d: %v", code, env)
	}
	ev := env["data"].(map[string]any)
	if ev["txHash"] != "0xtx1" || ev["dataHash"] != hash || ev["index"] != float64(1) {
		t.Fatalf("unexpected event: %v", ev)
	}
	p := f.st.pieces[f.pieceID]
	if p.OwnerID != "d1" || p.OwnerRole != "dye-house" || p.Pending != nil || p.LastHash != hash || p.EventCount != 2 {
		t.Fatalf("piece not handed off: %+v", p)
	}
	if !common.IsHexAddress(f.st.events[f.pieceID][1].ActorAddress) {
		t.Fatalf("actor address missing: %+v", f.st.events[f.pieceID][1])
	}
	if sent := f.st.published(); len(sent) != 2 || sent[1] != (published{"piece.handoff.signed", "PieceHandoffSigned", f.pieceID}) {
		t.Fatalf("unexpected events: %v", sent)
	}

	// The offer is consumed: the same signature cannot be replayed.
	if code, _ := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", `{"signature":"`+personalSign(t, f.dyeKey, hash)+`"}`); code != http.StatusConflict {
		t.Fatalf("replay: want 409, got %d", code)
	}
}

func TestHandoffChainsHashes(t *testing.T) {
	f := newHandoffFixture(t)
	first := f.offer(t, "d1")
	pid := chainPieceID(f.pieceID)
	if first != common.Hash(handoffHash(pid, common.Hash{}, "w1", "d1", "dye-house")).Hex() {
		t.Fatal("first handoff must chain from the zero hash")
	}
	do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", `{"signature":"`+personalSign(t, f.dyeKey, first)+`"}`)
	_, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs", "d1", "dye-house", `{"toUserId":"h1"}`)
	second := env["data"].(map[string]any)["pendingHandoff"].(map[string]any)["dataHash"].(string)
	if second != common.Hash(handoffHash(pid, common.HexToHash(first), "d1", "h1", "wholesaler")).Hex() {
		t.Fatal("second handoff must chain from the first")
	}
}

func TestHandoffOfferRules(t *testing.T) {
	f := newHandoffFixture(t)
	path := "/pieces/" + f.pieceID + "/handoffs"
	cases := []struct {
		name, user, role, body string
		want                   int
	}{
		{"not owner", "d1", "dye-house", `{"toUserId":"h1"}`, http.StatusForbidden},
		{"to self", "w1", "weaver", `{"toUserId":"w1"}`, http.StatusBadRequest},
		{"unknown recipient", "w1", "weaver", `{"toUserId":"ghost"}`, http.StatusUnprocessableEntity},
		{"backwards role", "w1", "weaver", `{"toUserId":"w2"}`, http.StatusConflict},
		{"skipping stages is fine", "w1", "weaver", `{"toUserId":"r9"}`, http.StatusCreated},
	}
	for _, c := range cases {
		if code, env := do(t, f.s, "POST", path, c.user, c.role, c.body); code != c.want {
			t.Errorf("%s: want %d, got %d: %v", c.name, c.want, code, env)
		}
	}
	if code, _ := do(t, f.s, "POST", "/pieces/nope/handoffs", "w1", "weaver", `{"toUserId":"d1"}`); code != http.StatusNotFound {
		t.Errorf("unknown piece: want 404, got %d", code)
	}
}

func TestHandoffAcceptRejections(t *testing.T) {
	f := newHandoffFixture(t)
	accept := "/pieces/" + f.pieceID + "/handoffs/accept"

	if code, _ := do(t, f.s, "POST", accept, "d1", "dye-house", `{"signature":"0x00"}`); code != http.StatusConflict {
		t.Errorf("no offer: want 409, got %d", code)
	}
	hash := f.offer(t, "d1")
	other, _ := crypto.GenerateKey()
	cases := []struct {
		name, user, role, sig string
		want                  int
	}{
		{"not the recipient", "h1", "wholesaler", personalSign(t, f.dyeKey, hash), http.StatusForbidden},
		{"role changed", "d1", "wholesaler", personalSign(t, f.dyeKey, hash), http.StatusConflict},
		{"malformed signature", "d1", "dye-house", "0x1234", http.StatusBadRequest},
		{"other wallet", "d1", "dye-house", personalSign(t, other, hash), http.StatusForbidden},
		{"signed a different hash", "d1", "dye-house", personalSign(t, f.dyeKey, common.Hash{1}.Hex()), http.StatusForbidden},
	}
	for _, c := range cases {
		if code, env := do(t, f.s, "POST", accept, c.user, c.role, `{"signature":"`+c.sig+`"}`); code != c.want {
			t.Errorf("%s: want %d, got %d: %v", c.name, c.want, code, env)
		}
	}
	if len(f.chain.recorded) != 0 || f.st.pieces[f.pieceID].OwnerID != "w1" {
		t.Fatal("a rejected accept must not touch the chain or the owner")
	}
}

func TestHandoffAcceptNeedsLinkedWallet(t *testing.T) {
	f := newHandoffFixture(t)
	key, _ := crypto.GenerateKey()
	f.s.users.(fakeUsers)["d2"] = User{ID: "d2", Role: "dye-house"}
	_, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs", "w1", "weaver", `{"toUserId":"d2"}`)
	hash := env["data"].(map[string]any)["pendingHandoff"].(map[string]any)["dataHash"].(string)
	if code, _ := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d2", "dye-house", `{"signature":"`+personalSign(t, key, hash)+`"}`); code != http.StatusConflict {
		t.Fatalf("no wallet: want 409, got %d", code)
	}
}

func TestHandoffChainFailureKeepsOffer(t *testing.T) {
	f := newHandoffFixture(t)
	hash := f.offer(t, "d1")
	f.chain.fail = true
	body := `{"signature":"` + personalSign(t, f.dyeKey, hash) + `"}`
	if code, _ := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", body); code != http.StatusBadGateway {
		t.Fatalf("chain down: want 502, got %d", code)
	}
	if p := f.st.pieces[f.pieceID]; p.OwnerID != "w1" || p.Pending == nil {
		t.Fatalf("owner must not change when the chain write fails: %+v", p)
	}
	f.chain.fail = false
	if code, _ := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", body); code != http.StatusOK {
		t.Fatalf("retry: want 200, got %d", code)
	}
}

// handedOff runs one full w1 -> d1 handoff on the fixture's piece.
func (f *handoffFixture) handedOff(t *testing.T) {
	t.Helper()
	hash := f.offer(t, "d1")
	if code, env := do(t, f.s, "POST", "/pieces/"+f.pieceID+"/handoffs/accept", "d1", "dye-house", `{"signature":"`+personalSign(t, f.dyeKey, hash)+`"}`); code != http.StatusOK {
		t.Fatalf("accept: want 200, got %d: %v", code, env)
	}
}

func verifyResult(t *testing.T, f *handoffFixture) map[string]any {
	t.Helper()
	code, env := do(t, f.s, "GET", "/pieces/"+f.pieceID+"/verify", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("verify: want 200, got %d: %v", code, env)
	}
	return env["data"].(map[string]any)
}

func TestHistoryIsPublicAndOrdered(t *testing.T) {
	f := newHandoffFixture(t)
	f.handedOff(t)
	code, env := do(t, f.s, "GET", "/pieces/"+f.pieceID+"/history", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	events := env["data"].(map[string]any)["events"].([]any)
	if len(events) != 2 || events[0].(map[string]any)["type"] != "registered" || events[1].(map[string]any)["type"] != "handoff" {
		t.Fatalf("unexpected history: %v", events)
	}
	if code, _ := do(t, f.s, "GET", "/pieces/nope/history", "", "", ""); code != http.StatusNotFound {
		t.Fatalf("unknown piece: want 404, got %d", code)
	}
}

func TestVerifyMatchesChain(t *testing.T) {
	f := newHandoffFixture(t)
	if v := verifyResult(t, f); v["verified"] != true || v["onChainCount"] != float64(0) {
		t.Fatalf("fresh piece: %v", v)
	}
	f.handedOff(t)
	if v := verifyResult(t, f); v["verified"] != true || v["onChainCount"] != float64(1) || v["mirroredCount"] != float64(1) {
		t.Fatalf("after handoff: %v", v)
	}
}

func TestVerifyDetectsTamperedMirror(t *testing.T) {
	f := newHandoffFixture(t)
	f.handedOff(t)
	f.st.events[f.pieceID][1].ActorAddress = "0x0000000000000000000000000000000000000bad"
	v := verifyResult(t, f)
	if v["verified"] != false {
		t.Fatalf("tampered actor must fail verification: %v", v)
	}
	if m := v["mismatch"].(map[string]any); m["index"] != float64(1) {
		t.Fatalf("mismatch should point at history index 1: %v", m)
	}
}

func TestVerifyDetectsMissingMirror(t *testing.T) {
	f := newHandoffFixture(t)
	f.handedOff(t)
	f.st.events[f.pieceID] = f.st.events[f.pieceID][:1] // the mirror lost the handoff
	if v := verifyResult(t, f); v["verified"] != false || v["mismatch"].(map[string]any)["index"] != float64(-1) {
		t.Fatalf("count mismatch must fail verification: %v", v)
	}
}

func TestVerifyChainDown(t *testing.T) {
	f := newHandoffFixture(t)
	f.chain.fail = true
	if code, _ := do(t, f.s, "GET", "/pieces/"+f.pieceID+"/verify", "", "", ""); code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", code)
	}
}

func TestRecoverSignerRejectsHighS(t *testing.T) {
	key, _ := crypto.GenerateKey()
	h := common.Hash{7}
	sig, _ := crypto.Sign(accounts.TextHash(h[:]), key)
	// Flip s to n - s (and v) to get the malleable twin of a valid signature.
	n := crypto.S256().Params().N
	s := new(big.Int).Sub(n, new(big.Int).SetBytes(sig[32:64]))
	copy(sig[32:64], common.LeftPadBytes(s.Bytes(), 32))
	sig[64] ^= 1
	if _, _, err := recoverSigner(h, hexutil.Encode(sig)); err == nil {
		t.Fatal("high-s signature must be rejected")
	}
}

// Runs against the compose anvil + deployed contract when TEST_CHAIN_RPC_URL is set.
func TestEthChainRecordIsIdempotent(t *testing.T) {
	rpc := os.Getenv("TEST_CHAIN_RPC_URL")
	if rpc == "" {
		t.Skip("TEST_CHAIN_RPC_URL not set")
	}
	ctx := context.Background()
	c, err := newEthChain(ctx, rpc, "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80", "0x5FbDB2315678afecb367f032d93F642f64180aa3")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.GenerateKey()
	pid := chainPieceID(uuid.NewString())
	h := handoffHash(pid, common.Hash{}, "w1", "d1", "dye-house")
	sig, _ := hexutil.Decode(personalSign(t, key, common.Hash(h).Hex()))

	tx1, err := c.Record(ctx, pid, "dye-house", h, sig)
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := c.Record(ctx, pid, "dye-house", h, sig)
	if err != nil || tx2 != tx1 {
		t.Fatalf("second record must return the original tx %s, got %s (%v)", tx1, tx2, err)
	}
	evs, err := c.History(ctx, pid)
	if err != nil || len(evs) != 1 || evs[0].Actor != crypto.PubkeyToAddress(key.PublicKey) || evs[0].Role != "dye-house" || evs[0].DataHash != h {
		t.Fatalf("on-chain history: %+v (%v)", evs, err)
	}
}
