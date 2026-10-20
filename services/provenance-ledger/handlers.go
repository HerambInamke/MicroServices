package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// A piece only moves forward along this chain; stages may be skipped.
var roleRank = map[string]int{"weaver": 0, "dye-house": 1, "wholesaler": 2, "retailer": 3}

type Server struct {
	store  Store
	events Publisher
	users  Users
	chain  Chain
	locks  sync.Map // piece id -> *sync.Mutex
}

// lockPiece serialises state changes on one piece.
// shortcut: in-process lock, correct only with a single ledger replica; use an immudb precondition on the read tx before scaling out.
func (s *Server) lockPiece(id string) func() {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Server) routes() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/health", healthHandler).Methods("GET")
	r.HandleFunc("/pieces", s.authed(s.register)).Methods("POST")
	r.HandleFunc("/pieces/{id}", s.get).Methods("GET")
	r.HandleFunc("/pieces/{id}/history", s.history).Methods("GET")
	r.HandleFunc("/pieces/{id}/verify", s.verify).Methods("GET")
	r.HandleFunc("/pieces/{id}/handoffs", s.authed(s.offer)).Methods("POST")
	r.HandleFunc("/pieces/{id}/handoffs/accept", s.authed(s.accept)).Methods("POST")
	return r
}

// respond writes the {data, error, meta} envelope from api-architecture.md.
func respond(w http.ResponseWriter, status int, data any, code, msg string) {
	var apiErr any
	if code != "" {
		apiErr = map[string]string{"code": code, "message": msg}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"data": data, "error": apiErr, "meta": map[string]any{}})
}

func fail(w http.ResponseWriter, status int, code, msg string) { respond(w, status, nil, code, msg) }

func internal(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	fail(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
}

// authed requires the identity headers the gateway sets after verifying the JWT.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, string, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-Id")
		if userID == "" {
			fail(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
			return
		}
		h(w, r, userID, r.Header.Get("X-User-Role"))
	}
}

func (s *Server) register(w http.ResponseWriter, r *http.Request, userID, role string) {
	if role != "weaver" {
		fail(w, http.StatusForbidden, "FORBIDDEN", "only weavers can register pieces")
		return
	}
	var req struct {
		ProductID   int64  `json:"productId"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "INVALID_BODY", "body must be JSON with an optional numeric productId and a description")
		return
	}
	if req.ProductID < 0 || len(req.Description) > 1000 {
		fail(w, http.StatusBadRequest, "VALIDATION", "productId must be positive and description at most 1000 characters")
		return
	}

	now := time.Now().UTC()
	p := Piece{
		ID: uuid.NewString(), ProductID: req.ProductID, Description: req.Description,
		WeaverID: userID, OwnerID: userID, OwnerRole: "weaver", EventCount: 1, CreatedAt: now,
	}
	// The registration is the first history entry; it is off-chain, handoffs are anchored on-chain.
	e := Event{Index: 0, Type: "registered", Role: "weaver", UserID: userID, Timestamp: now}
	if err := s.store.Create(r.Context(), p, e); err != nil {
		internal(w, err)
		return
	}
	s.events.Publish("piece.registered", "PieceRegistered", p.ID, map[string]any{
		"pieceId": p.ID, "productId": p.ProductID, "weaverId": userID, "registeredAt": now,
	})
	respond(w, http.StatusCreated, p, "", "")
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.getPiece(w, r, mux.Vars(r)["id"]); ok {
		respond(w, http.StatusOK, p, "", "")
	}
}

// getPiece loads a piece and answers 404/500 itself; ok is false when it already responded.
func (s *Server) getPiece(w http.ResponseWriter, r *http.Request, id string) (Piece, bool) {
	p, err := s.store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusNotFound, "NOT_FOUND", "piece not found")
		return p, false
	}
	if err != nil {
		internal(w, err)
		return p, false
	}
	return p, true
}

// history is the fast off-chain timeline from immudb.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	p, ok := s.getPiece(w, r, mux.Vars(r)["id"])
	if !ok {
		return
	}
	events, err := s.store.History(r.Context(), p.ID)
	if err != nil {
		internal(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"piece": p, "events": events}, "", "")
}

// verify re-reads the contract and checks every mirrored handoff against it, in order.
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	p, ok := s.getPiece(w, r, mux.Vars(r)["id"])
	if !ok {
		return
	}
	events, err := s.store.History(r.Context(), p.ID)
	if err != nil {
		internal(w, err)
		return
	}
	onChain, err := s.chain.History(r.Context(), chainPieceID(p.ID))
	if err != nil {
		log.Printf("chain history for %s failed: %v", p.ID, err)
		fail(w, http.StatusBadGateway, "CHAIN_ERROR", "could not read the contract")
		return
	}
	var handoffs []Event // the registration entry is off-chain by design
	for _, e := range events {
		if e.Type == "handoff" {
			handoffs = append(handoffs, e)
		}
	}
	m := firstMismatch(handoffs, onChain)
	respond(w, http.StatusOK, map[string]any{
		"pieceId":       p.ID,
		"verified":      m == nil,
		"onChainCount":  len(onChain),
		"mirroredCount": len(handoffs),
		"mismatch":      m,
	}, "", "")
}

type mismatch struct {
	Index  int    `json:"index"` // history index; -1 when only the counts differ
	Reason string `json:"reason"`
}

func firstMismatch(handoffs []Event, onChain []ChainEvent) *mismatch {
	for i := 0; i < len(handoffs) && i < len(onChain); i++ {
		e, c := handoffs[i], onChain[i]
		switch {
		case common.HexToHash(e.DataHash) != common.Hash(c.DataHash):
			return &mismatch{e.Index, "dataHash differs from the contract"}
		case !strings.EqualFold(e.ActorAddress, c.Actor.Hex()):
			return &mismatch{e.Index, "actor differs from the contract"}
		case e.Role != c.Role:
			return &mismatch{e.Index, "role differs from the contract"}
		}
	}
	if len(handoffs) != len(onChain) {
		return &mismatch{-1, "the contract and the mirror hold a different number of handoffs"}
	}
	return nil
}

// offer: the current owner proposes handing the piece to the next actor.
func (s *Server) offer(w http.ResponseWriter, r *http.Request, userID, role string) {
	var req struct {
		ToUserID string `json:"toUserId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "INVALID_BODY", "body must be JSON")
		return
	}
	if req.ToUserID == "" || req.ToUserID == userID {
		fail(w, http.StatusBadRequest, "VALIDATION", "toUserId is required and must be another user")
		return
	}
	id := mux.Vars(r)["id"]
	defer s.lockPiece(id)()
	p, ok := s.getPiece(w, r, id)
	if !ok {
		return
	}
	if p.OwnerID != userID {
		fail(w, http.StatusForbidden, "NOT_OWNER", "only the current owner can hand this piece off")
		return
	}
	to, err := s.users.Get(r.Context(), userID, role, req.ToUserID)
	if errors.Is(err, ErrNotFound) {
		fail(w, http.StatusUnprocessableEntity, "UNKNOWN_RECIPIENT", "recipient does not exist")
		return
	}
	if err != nil {
		log.Printf("auth lookup failed: %v", err)
		fail(w, http.StatusBadGateway, "AUTH_UNAVAILABLE", "could not look up the recipient")
		return
	}
	if rank, ok := roleRank[to.Role]; !ok || rank <= roleRank[p.OwnerRole] {
		fail(w, http.StatusConflict, "ROLE_ORDER", "a piece only moves forward: weaver, dye-house, wholesaler, retailer")
		return
	}

	var prev common.Hash
	if p.LastHash != "" {
		prev = common.HexToHash(p.LastHash)
	}
	h := handoffHash(chainPieceID(p.ID), prev, userID, to.ID, to.Role)
	// A new offer replaces any earlier one, so the owner can redirect an unaccepted handoff.
	p.Pending = &Offer{FromUserID: userID, ToUserID: to.ID, Role: to.Role, DataHash: common.Hash(h).Hex(), OfferedAt: time.Now().UTC()}
	if err := s.store.Update(r.Context(), p, nil); err != nil {
		internal(w, err)
		return
	}
	respond(w, http.StatusCreated, p, "", "")
}

// accept: the recipient confirms receipt by signing the offer's dataHash with their linked wallet.
func (s *Server) accept(w http.ResponseWriter, r *http.Request, userID, role string) {
	var req struct {
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "INVALID_BODY", "body must be JSON")
		return
	}
	id := mux.Vars(r)["id"]
	defer s.lockPiece(id)()
	p, ok := s.getPiece(w, r, id)
	if !ok {
		return
	}
	offer := p.Pending
	if offer == nil {
		fail(w, http.StatusConflict, "NO_PENDING_HANDOFF", "this piece has no pending handoff")
		return
	}
	if offer.ToUserID != userID {
		fail(w, http.StatusForbidden, "NOT_RECIPIENT", "the pending handoff is for another user")
		return
	}
	if offer.Role != role {
		fail(w, http.StatusConflict, "ROLE_CHANGED", "your role no longer matches the offer; ask the owner to offer again")
		return
	}
	me, err := s.users.Get(r.Context(), userID, role, userID)
	if err != nil {
		log.Printf("auth lookup failed: %v", err)
		fail(w, http.StatusBadGateway, "AUTH_UNAVAILABLE", "could not look up your wallet")
		return
	}
	if me.WalletAddress == "" {
		fail(w, http.StatusConflict, "WALLET_NOT_LINKED", "link a wallet before accepting a handoff")
		return
	}
	dataHash := common.HexToHash(offer.DataHash)
	signer, sig, err := recoverSigner(dataHash, req.Signature)
	if err != nil {
		fail(w, http.StatusBadRequest, "INVALID_SIGNATURE", err.Error())
		return
	}
	if !strings.EqualFold(signer.Hex(), me.WalletAddress) {
		fail(w, http.StatusForbidden, "WALLET_MISMATCH", "signature is not from your linked wallet")
		return
	}

	// Detached from the request: once a tx is sent it must be mirrored even if the client goes away.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	txHash, err := s.chain.Record(ctx, chainPieceID(p.ID), offer.Role, dataHash, sig)
	if err != nil {
		log.Printf("chain record for %s failed: %v", p.ID, err)
		fail(w, http.StatusBadGateway, "CHAIN_ERROR", "could not record the handoff on-chain")
		return
	}
	e := Event{
		Index: p.EventCount, Type: "handoff", Role: offer.Role, UserID: userID, ActorAddress: signer.Hex(),
		DataHash: offer.DataHash, TxHash: txHash, Timestamp: time.Now().UTC(),
	}
	p.OwnerID, p.OwnerRole, p.EventCount, p.LastHash, p.Pending = userID, offer.Role, p.EventCount+1, offer.DataHash, nil
	if err := s.store.Update(ctx, p, &e); err != nil {
		// The offer is still stored, so repeating the request finds the tx on-chain and mirrors it.
		log.Printf("handoff for %s is on-chain in %s but not mirrored: %v", p.ID, txHash, err)
		fail(w, http.StatusInternalServerError, "MIRROR_FAILED", "recorded on-chain in tx "+txHash+" but not saved; retry the same request")
		return
	}
	s.events.Publish("piece.handoff.signed", "PieceHandoffSigned", p.ID, map[string]any{
		"pieceId": p.ID, "index": e.Index, "fromUserId": offer.FromUserID, "toUserId": userID, "role": e.Role,
		"actorAddress": e.ActorAddress, "dataHash": e.DataHash, "txHash": txHash, "signedAt": e.Timestamp,
	})
	respond(w, http.StatusOK, e, "", "")
}
