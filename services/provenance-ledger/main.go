package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

type HealthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(HealthResponse{
		Status:  "ok",
		Service: "provenance-ledger",
	})
}

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

// retry runs fn until it succeeds, because immudb and the chain may still be starting.
func retry(what string, fn func() error) {
	var err error
	for i := 0; i < 30; i++ {
		if err = fn(); err == nil {
			return
		}
		log.Printf("waiting for %s: %v", what, err)
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("%s unavailable: %v", what, err)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8086"
	}

	var store *immuStore
	host, user, pass, db := requireEnv("IMMUDB_HOST"), requireEnv("IMMUDB_USER"), requireEnv("IMMUDB_PASSWORD"), requireEnv("IMMUDB_DB")
	retry("immudb", func() (err error) {
		store, err = newImmuStore(context.Background(), host, user, pass, db)
		return err
	})

	var chain *ethChain
	rpcURL, relayerKey, contract := requireEnv("CHAIN_RPC_URL"), requireEnv("RELAYER_KEY"), requireEnv("CONTRACT_ADDRESS")
	retry("chain", func() (err error) {
		chain, err = newEthChain(context.Background(), rpcURL, relayerKey, contract)
		return err
	})

	go runRelay(store, newKafkaSender(requireEnv("KAFKA_BROKERS")).Send)
	srv := &Server{
		store: store,
		users: newAuthClient(requireEnv("AUTH_URL")),
		chain: chain,
	}

	log.Printf("Provenance Ledger Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.routes()); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
