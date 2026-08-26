package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
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

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	r := mux.NewRouter()

	// Standard health check required by CONTRIBUTING.md
	r.HandleFunc("/health", healthHandler).Methods("GET")

	// Future: Auth and Wallet linking endpoints here
	// POST /auth/register
	// POST /auth/login
	// POST /auth/wallet/link

	log.Printf("Auth & Identity Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}