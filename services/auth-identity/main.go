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
		Service: "auth-identity",
	})
}

// connectStore retries because Postgres may still be starting when the container comes up.
func connectStore(url string) *pgStore {
	var err error
	for i := 0; i < 30; i++ {
		var s *pgStore
		if s, err = newPGStore(context.Background(), url); err == nil {
			return s
		}
		log.Printf("waiting for database: %v", err)
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("database unavailable: %v", err)
	return nil
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is required")
	}
	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		log.Fatal("DB_URL is required")
	}

	srv := &Server{store: connectStore(dbURL), secret: []byte(secret)}

	log.Printf("Auth & Identity Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.routes()); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
