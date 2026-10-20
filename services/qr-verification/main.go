package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
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
		Service: "qr-verification",
	})
}

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

// retry runs fn until it succeeds, because Postgres may still be starting.
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
		port = "8087"
	}

	var store *pgStore
	dbURL := requireEnv("DB_URL")
	retry("database", func() (err error) {
		store, err = newPGStore(context.Background(), dbURL)
		return err
	})

	go consume(context.Background(), newHandoffReader(requireEnv("KAFKA_BROKERS")), store)

	srv := &Server{
		store:   store,
		ledger:  newLedgerClient(requireEnv("LEDGER_URL")),
		baseURL: strings.TrimRight(requireEnv("PUBLIC_BASE_URL"), "/"),
	}

	log.Printf("QR/Scan Verification Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.routes()); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
