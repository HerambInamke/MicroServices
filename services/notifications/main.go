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
		Service: "notifications",
	})
}

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

// retry runs fn until it succeeds, because Redis may still be starting.
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
		port = "8092"
	}

	var store *redisStore
	redisURL := requireEnv("REDIS_URL")
	retry("redis", func() (err error) {
		store, err = newRedisStore(context.Background(), redisURL)
		return err
	})

	go consume(context.Background(), newEventReader(requireEnv("KAFKA_BROKERS")), store)

	srv := &Server{store: store}

	log.Printf("Notifications Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.routes()); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
