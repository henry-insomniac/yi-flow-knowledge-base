package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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
	signingSeed, err := knowledgePackSigningSeedFromEnv()
	if err != nil {
		log.Fatalf("load knowledge pack signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 storageDir,
		AdminToken:                 adminToken,
		KnowledgePackSigningSeed:   signingSeed,
		MoegirlAPIURL:              os.Getenv("MOEGIRL_API_URL"),
		MoegirlSitemapIndexURL:     os.Getenv("MOEGIRL_SITEMAP_INDEX_URL"),
		MoegirlPublicArticleOrigin: os.Getenv("MOEGIRL_PUBLIC_ARTICLE_ORIGIN"),
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

func knowledgePackSigningSeedFromEnv() ([]byte, error) {
	encoded := strings.TrimSpace(os.Getenv("KNOWLEDGE_PACK_SIGNING_KEY_BASE64"))
	if encoded == "" {
		keyFile := strings.TrimSpace(os.Getenv("KNOWLEDGE_PACK_SIGNING_KEY_FILE"))
		if keyFile == "" {
			return nil, nil
		}
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read KNOWLEDGE_PACK_SIGNING_KEY_FILE: %w", err)
		}
		encoded = strings.TrimSpace(string(data))
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode signing key base64: %w", err)
	}
	return key, nil
}
