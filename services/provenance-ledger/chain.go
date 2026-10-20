package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ABI of contracts/src/ProvenanceRegistry.sol, copied from forge's build output.
//
//go:embed registry.abi.json
var registryABI string

type Chain interface {
	// Record anchors a signed handoff and returns its tx hash. Recording a dataHash that is
	// already on-chain returns the original tx hash, so a retry after a failed mirror is safe.
	Record(ctx context.Context, pieceID [32]byte, role string, dataHash [32]byte, sig []byte) (string, error)
	History(ctx context.Context, pieceID [32]byte) ([]ChainEvent, error)
}

// ChainEvent mirrors ProvenanceRegistry.ProvenanceEvent; field names must match the ABI for ConvertType.
type ChainEvent struct {
	Actor     common.Address
	Role      string
	Timestamp uint64
	DataHash  [32]byte
}

var errBadSignature = errors.New("signature must be a 0x-prefixed 65-byte hex string")

func chainPieceID(id string) [32]byte { return crypto.Keccak256Hash([]byte(id)) }

// handoffHash is what the receiver signs: it binds the piece, its previous event, both parties
// and the role, so a signature fits exactly one position in one piece's chain.
func handoffHash(pieceID, prev [32]byte, fromUserID, toUserID, role string) [32]byte {
	b32, _ := abi.NewType("bytes32", "", nil)
	str, _ := abi.NewType("string", "", nil)
	packed, err := abi.Arguments{{Type: b32}, {Type: b32}, {Type: str}, {Type: str}, {Type: str}}.
		Pack(pieceID, prev, fromUserID, toUserID, role)
	if err != nil {
		panic(err) // only fails on a type mismatch, which is a programming error
	}
	return crypto.Keccak256Hash(packed)
}

// recoverSigner returns the address behind an EIP-191 personal_sign over dataHash (MetaMask's format).
func recoverSigner(dataHash [32]byte, sigHex string) (common.Address, []byte, error) {
	sig, err := hexutil.Decode(sigHex)
	if err != nil || len(sig) != 65 {
		return common.Address{}, nil, errBadSignature
	}
	norm := append([]byte(nil), sig...)
	if norm[64] >= 27 {
		norm[64] -= 27
	}
	// Same rule as the contract: reject malleable high-s signatures.
	r, s := new(big.Int).SetBytes(norm[:32]), new(big.Int).SetBytes(norm[32:64])
	if !crypto.ValidateSignatureValues(norm[64], r, s, true) {
		return common.Address{}, nil, errBadSignature
	}
	pub, err := crypto.SigToPub(accounts.TextHash(dataHash[:]), norm)
	if err != nil {
		return common.Address{}, nil, errBadSignature
	}
	return crypto.PubkeyToAddress(*pub), sig, nil
}

type ethChain struct {
	client   *ethclient.Client
	abi      abi.ABI
	addr     common.Address
	contract *bind.BoundContract
	auth     *bind.TransactOpts
	sendMu   sync.Mutex // one relayer key: serialise sends so concurrent handoffs don't reuse a nonce
}

func newEthChain(ctx context.Context, rpcURL, keyHex, addrHex string) (*ethChain, error) {
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, err
	}
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return nil, err
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(keyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid RELAYER_KEY: %w", err)
	}
	auth, err := bind.NewKeyedTransactorWithChainID(key, chainID)
	if err != nil {
		return nil, err
	}
	parsed, err := abi.JSON(strings.NewReader(registryABI))
	if err != nil {
		return nil, err
	}
	addr := common.HexToAddress(addrHex)
	if code, err := client.CodeAt(ctx, addr, nil); err != nil {
		return nil, err
	} else if len(code) == 0 {
		return nil, fmt.Errorf("no contract deployed at %s", addr)
	}
	return &ethChain{client: client, abi: parsed, addr: addr, contract: bind.NewBoundContract(addr, parsed, client, client, client), auth: auth}, nil
}

func (c *ethChain) Record(ctx context.Context, pieceID [32]byte, role string, dataHash [32]byte, sig []byte) (string, error) {
	var out []any
	if err := c.contract.Call(&bind.CallOpts{Context: ctx}, &out, "used", dataHash); err != nil {
		return "", err
	}
	if out[0].(bool) {
		return c.findRecord(ctx, pieceID, dataHash)
	}

	c.sendMu.Lock()
	opts := *c.auth
	opts.Context = ctx
	tx, err := c.contract.Transact(&opts, "record", pieceID, role, dataHash, sig)
	c.sendMu.Unlock()
	if err != nil {
		return "", err
	}
	receipt, err := bind.WaitMined(ctx, c.client, tx)
	if err != nil {
		return "", err
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return "", fmt.Errorf("record tx %s reverted", tx.Hash().Hex())
	}
	return tx.Hash().Hex(), nil
}

func (c *ethChain) History(ctx context.Context, pieceID [32]byte) ([]ChainEvent, error) {
	var out []any
	if err := c.contract.Call(&bind.CallOpts{Context: ctx}, &out, "history", pieceID); err != nil {
		return nil, err
	}
	return *abi.ConvertType(out[0], new([]ChainEvent)).(*[]ChainEvent), nil
}

func (c *ethChain) findRecord(ctx context.Context, pieceID, dataHash [32]byte) (string, error) {
	// shortcut: scans the piece's logs from genesis; public Amoy RPCs cap log ranges, so store the deploy block and query from it before going to Amoy.
	logs, err := c.client.FilterLogs(ctx, ethereum.FilterQuery{
		Addresses: []common.Address{c.addr},
		Topics:    [][]common.Hash{{c.abi.Events["Recorded"].ID}, {pieceID}},
	})
	if err != nil {
		return "", err
	}
	for _, l := range logs {
		vals, err := c.abi.Unpack("Recorded", l.Data) // non-indexed fields: index, role, dataHash
		if err != nil {
			return "", err
		}
		if vals[2].([32]byte) == dataHash {
			return l.TxHash.Hex(), nil
		}
	}
	return "", fmt.Errorf("dataHash %x is already recorded for a different piece", dataHash)
}
