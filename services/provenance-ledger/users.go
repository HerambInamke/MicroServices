package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type User struct {
	ID            string `json:"id"`
	Role          string `json:"role"`
	WalletAddress string `json:"walletAddress"`
}

type Users interface {
	// Get looks a user up in Auth on behalf of the caller, whose identity headers are forwarded.
	Get(ctx context.Context, callerID, callerRole, userID string) (User, error)
}

type authClient struct {
	base string
	http *http.Client
}

func newAuthClient(base string) *authClient {
	return &authClient{base: base, http: &http.Client{Timeout: 5 * time.Second}}
}

func (a *authClient) Get(ctx context.Context, callerID, callerRole, userID string) (User, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.base+"/users/"+url.PathEscape(userID), nil)
	if err != nil {
		return User{}, err
	}
	req.Header.Set("X-User-Id", callerID)
	req.Header.Set("X-User-Role", callerRole)
	res, err := a.http.Do(req)
	if err != nil {
		return User{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return User{}, ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return User{}, fmt.Errorf("auth returned %d for user %s", res.StatusCode, userID)
	}
	var env struct{ Data User }
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return User{}, err
	}
	return env.Data, nil
}
