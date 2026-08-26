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
		Service: "media-upload",
	})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}

	r := mux.NewRouter()

	r.HandleFunc("/health", healthHandler).Methods("GET")

	// Future: File upload logic wrapping MinIO
	// POST /media/upload

	log.Printf("Media/Upload Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}