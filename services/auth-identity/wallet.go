package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

const challengeTTL = 5 * time.Minute

var errBadSignature = errors.New("bad signature")

// challengeMessage is the text the wallet signs. It is stateless: the tag binds user, address and expiry to our
// secret, so a link request only has to echo it back. A replay before expiry re-links the same wallet to the
// same user, which is harmless, so no single-use store is needed.
func challengeMessage(secret []byte, userID, address string, expires int64) string {
	body := fmt.Sprintf("Provenance wallet link\nUser: %s\nAddress: %s\nExpires: %d", userID, address, expires)
	mac := hmac.New(sha256.New, append([]byte("wallet-link:"), secret...)) // separate key from JWT signing
	mac.Write([]byte(body))
	return body + "\nTag: " + hex.EncodeToString(mac.Sum(nil))
}

// checkChallenge reports whether msg is a challenge we issued to userID for address that has not expired.
func checkChallenge(secret []byte, msg, userID, address string, now time.Time) bool {
	var expires int64 = -1
	for _, line := range strings.Split(msg, "\n") {
		if v, ok := strings.CutPrefix(line, "Expires: "); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				expires = n
			}
		}
	}
	if expires < now.Unix() {
		return false
	}
	return hmac.Equal([]byte(msg), []byte(challengeMessage(secret, userID, address, expires)))
}

func keccak(b ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, x := range b {
		h.Write(x)
	}
	return h.Sum(nil)
}

// recoverAddress returns the lowercase 0x address behind an EIP-191 personal_sign of msg (MetaMask's format).
func recoverAddress(msg, sigHex string) (string, error) {
	sig, err := hex.DecodeString(strings.TrimPrefix(sigHex, "0x"))
	if err != nil || len(sig) != 65 {
		return "", errBadSignature
	}
	v := sig[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return "", errBadSignature
	}
	// Same rule as the ledger and the contract: reject malleable high-s signatures.
	var s secp256k1.ModNScalar
	if overflow := s.SetByteSlice(sig[32:64]); overflow || s.IsOverHalfOrder() {
		return "", errBadSignature
	}
	// decred wants [27+recid | r | s] for an uncompressed key.
	compact := append([]byte{27 + v}, sig[:64]...)
	hash := keccak([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(msg), msg)))
	pub, _, err := ecdsa.RecoverCompact(compact, hash)
	if err != nil {
		return "", errBadSignature
	}
	return "0x" + hex.EncodeToString(keccak(pub.SerializeUncompressed()[1:])[12:]), nil
}
