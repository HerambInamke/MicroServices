package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	accessTTL  = 15 * time.Minute
	refreshTTL = 7 * 24 * time.Hour

	tokenAccess  = "access"
	tokenRefresh = "refresh"
)

type claims struct {
	Role string `json:"role"`
	Type string `json:"typ"`
	jwt.RegisteredClaims
}

func newJTI() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func issueToken(secret []byte, u User, typ string, ttl time.Duration) (string, error) {
	now := time.Now()
	c := claims{
		Role: u.Role,
		Type: typ,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			ID:        newJTI(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(secret)
}

func parseToken(secret []byte, tok, typ string) (*claims, error) {
	c := &claims{}
	_, err := jwt.ParseWithClaims(tok, c, func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	if c.Type != typ {
		return nil, fmt.Errorf("wrong token type %q", c.Type)
	}
	return c, nil
}
