package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

// retry runs fn until it succeeds, because Postgres and MinIO may still be starting.
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
		port = "8085"
	}

	var meta *pgStore
	dbURL := requireEnv("DB_URL")
	retry("database", func() (err error) {
		meta, err = newPGStore(context.Background(), dbURL)
		return err
	})

	endpoint, err := url.Parse(requireEnv("MINIO_URL"))
	if err != nil {
		log.Fatalf("invalid MINIO_URL: %v", err)
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(requireEnv("MINIO_ACCESS_KEY"), requireEnv("MINIO_SECRET_KEY"), ""),
		Secure: endpoint.Scheme == "https",
	})
	if err != nil {
		log.Fatalf("minio client: %v", err)
	}
	objects := &minioStore{client: client, bucket: requireEnv("MINIO_BUCKET")}
	retry("object storage", func() error { return objects.ensureBucket(context.Background()) })

	srv := &Server{meta: meta, objects: objects}

	log.Printf("Media/Upload Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.routes()); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
