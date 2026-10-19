package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/bcrypt"
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

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	secret := requireEnv("JWT_SECRET")
	store := connectStore(requireEnv("DB_URL"))

	if email, pw := os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD"); email != "" && pw != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("hashing admin password: %v", err)
		}
		if err := store.seedAdmin(context.Background(), email, string(hash)); err != nil {
			log.Fatalf("seeding admin: %v", err)
		}
	}

	srv := &Server{store: store, secret: []byte(secret)}

	log.Printf("Auth & Identity Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.routes()); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
