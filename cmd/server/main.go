package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"yi-flow/knowledge-base/internal/server"
)

func main() {
	addr := getenv("ADDR", ":8080")
	storageDir := getenv("STORAGE_DIR", "/var/lib/yi-flow-knowledge-base")
	adminToken := os.Getenv("ADMIN_TOKEN")
	if adminToken == "" && os.Getenv("ALLOW_EMPTY_ADMIN_TOKEN") != "1" {
		log.Fatal("ADMIN_TOKEN is required; set ALLOW_EMPTY_ADMIN_TOKEN=1 only for local development")
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir: storageDir,
		AdminToken: adminToken,
	})
	if err != nil {
		log.Fatalf("create handler: %v", err)
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("yi-flow knowledge base listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
}

func getenv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
