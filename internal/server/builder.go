package server

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultEmbeddingModel     = "Qwen3-Embedding-0.6B"
	defaultEmbeddingDimension = 1024
)

type buildPublishRequest struct {
	Version        string                     `json:"version"`
	Chunks         []knowledgePackBuildChunk  `json:"chunks"`
	Prompts        []knowledgePackBuildPrompt `json:"prompts"`
	Citations      json.RawMessage            `json:"citations"`
	LLMRecommended []string                   `json:"llm_recommended"`
}

type knowledgePackBuildChunk struct {
	ChunkID          string   `json:"chunk_id"`
	Title            string   `json:"title"`
	Path             string   `json:"path"`
	Source           string   `json:"source"`
	Content          string   `json:"content"`
	Tags             []string `json:"tags,omitempty"`
	ReviewStatus     string   `json:"review_status,omitempty"`
	CitationURL      string   `json:"citation_url,omitempty"`
	CitationTitle    string   `json:"citation_title,omitempty"`
	SourceName       string   `json:"source_name,omitempty"`
	License          string   `json:"license,omitempty"`
	SourcePolicy     string   `json:"source_policy,omitempty"`
	SourceRevisionID string   `json:"source_revision_id,omitempty"`
	SourcePageID     string   `json:"source_page_id,omitempty"`
}

type knowledgePackBuildPrompt struct {
	ID       string `json:"id,omitempty"`
	Title    string `json:"title,omitempty"`
	Question string `json:"question,omitempty"`
	Text     string `json:"text,omitempty"`
}

type knowledgePackBuildFile struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	ByteSize int64  `json:"byte_size"`
}

type knowledgePackBuildManifest struct {
	SchemaVersion      string   `json:"schema_version"`
	KBID               string   `json:"kb_id"`
	Version            string   `json:"version"`
	ContentHash        string   `json:"content_hash"`
	Signature          string   `json:"signature"`
	ChunkSchemaVersion string   `json:"chunk_schema_version"`
	EmbeddingModel     string   `json:"embedding_model"`
	EmbeddingDimension int      `json:"embedding_dim"`
	CreatedAt          string   `json:"created_at"`
	LLMRecommended     []string `json:"llm_recommended"`
	Files              struct {
		Chunks    []knowledgePackBuildFile `json:"chunks"`
		FTS       []knowledgePackBuildFile `json:"fts"`
		Vector    []knowledgePackBuildFile `json:"vector"`
		Assets    []knowledgePackBuildFile `json:"assets"`
		Citations []knowledgePackBuildFile `json:"citations"`
		Prompts   []knowledgePackBuildFile `json:"prompts"`
	} `json:"files"`
	Security struct {
		ExecutableCodeAllowed bool   `json:"executable_code_allowed"`
		RemoteCodePolicy      string `json:"remote_code_policy"`
	} `json:"security"`
}

func (h *Handler) handleBuildAndPublishVersion(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if len(h.knowledgePackSigningSeed) == 0 {
		http.Error(w, "knowledge pack signing key is not configured", http.StatusServiceUnavailable)
		return
	}

	kbID, ok := strings.CutPrefix(r.URL.Path, "/admin/api/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/build-publish")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var payload buildPublishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	version, err := safeComponent(payload.Version)
	if err != nil {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}
	if err := validateBuildPublishBoundary(kbID, payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	packageBytes, manifest, err := buildKnowledgePack(kbID, version, payload, h.knowledgePackSigningSeed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := auditKnowledgePackBeforePublish(manifest, packageBytes); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.storePublishedVersion(kbID, version, manifest, bytes.NewReader(packageBytes)); err != nil {
		writePublishError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":       kbID,
		"version":     version,
		"latest":      true,
		"chunk_count": len(payload.Chunks),
	})
}

func buildKnowledgePack(
	kbID string,
	version string,
	payload buildPublishRequest,
	signingSeed []byte,
) ([]byte, []byte, error) {
	if len(payload.Chunks) == 0 {
		return nil, nil, fmt.Errorf("chunks must not be empty")
	}

	chunks := make([]knowledgePackBuildChunk, len(payload.Chunks))
	for index, chunk := range payload.Chunks {
		normalized, err := normalizeBuildChunk(index, chunk)
		if err != nil {
			return nil, nil, err
		}
		chunks[index] = normalized
	}

	tempDir, err := os.MkdirTemp("", "yi-flow-knowledge-pack-build-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create build workspace: %w", err)
	}
	defer os.RemoveAll(tempDir)

	chunksPath := filepath.Join(tempDir, "chunks.sqlite")
	if err := writeChunksSQLite(chunksPath, chunks); err != nil {
		return nil, nil, err
	}
	chunksData, err := os.ReadFile(chunksPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read chunks sqlite: %w", err)
	}

	promptsData, err := json.MarshalIndent(map[string]any{"prompts": payload.Prompts}, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode prompts: %w", err)
	}
	promptsData = append(promptsData, '\n')

	citationsData, err := normalizeBuildCitations(kbID, chunks, payload.Citations)
	if err != nil {
		return nil, nil, err
	}

	vectorData := emptyVectorIndex(defaultEmbeddingDimension)
	files := map[string][]byte{
		"chunks.sqlite":  chunksData,
		"citations.json": citationsData,
		"prompts.json":   promptsData,
		"vector.index":   vectorData,
	}

	packageBytes, err := storedZip(files)
	if err != nil {
		return nil, nil, err
	}

	manifest, err := buildManifest(kbID, version, payload, files, packageBytes, signingSeed)
	if err != nil {
		return nil, nil, err
	}
	return packageBytes, manifest, nil
}

func normalizeBuildChunk(index int, chunk knowledgePackBuildChunk) (knowledgePackBuildChunk, error) {
	chunk.ChunkID = strings.TrimSpace(chunk.ChunkID)
	chunk.Title = strings.TrimSpace(chunk.Title)
	chunk.Path = strings.TrimSpace(chunk.Path)
	chunk.Source = strings.TrimSpace(chunk.Source)
	chunk.Content = strings.TrimSpace(chunk.Content)
	chunk.Tags = normalizeStringList(chunk.Tags)
	chunk.ReviewStatus = strings.TrimSpace(chunk.ReviewStatus)
	if chunk.ReviewStatus == "" {
		chunk.ReviewStatus = draftStatus
	}
	chunk.CitationURL = strings.TrimSpace(chunk.CitationURL)
	chunk.CitationTitle = strings.TrimSpace(chunk.CitationTitle)
	chunk.SourceName = strings.TrimSpace(chunk.SourceName)
	chunk.License = strings.TrimSpace(chunk.License)
	chunk.SourcePolicy = strings.TrimSpace(chunk.SourcePolicy)
	chunk.SourceRevisionID = strings.TrimSpace(chunk.SourceRevisionID)
	chunk.SourcePageID = strings.TrimSpace(chunk.SourcePageID)

	if chunk.ChunkID == "" {
		return chunk, fmt.Errorf("chunks[%d].chunk_id is required", index)
	}
	if chunk.Title == "" {
		return chunk, fmt.Errorf("chunks[%d].title is required", index)
	}
	if chunk.Path == "" {
		return chunk, fmt.Errorf("chunks[%d].path is required", index)
	}
	if chunk.Source == "" {
		return chunk, fmt.Errorf("chunks[%d].source is required", index)
	}
	if chunk.Content == "" {
		return chunk, fmt.Errorf("chunks[%d].content is required", index)
	}
	return chunk, nil
}

func normalizeStringList(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		normalized = append(normalized, value)
	}
	return normalized
}

func writeChunksSQLite(path string, chunks []knowledgePackBuildChunk) error {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open chunks sqlite: %w", err)
	}
	defer database.Close()

	if _, err := database.Exec(`
		CREATE VIRTUAL TABLE chunks USING fts5(
			chunk_id UNINDEXED,
			title,
			path UNINDEXED,
			source UNINDEXED,
			content,
			tokenize = 'trigram'
		);
	`); err != nil {
		return fmt.Errorf("create chunks table: %w", err)
	}

	statement, err := database.Prepare("INSERT INTO chunks(chunk_id, title, path, source, content) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare chunk insert: %w", err)
	}
	defer statement.Close()

	for _, chunk := range chunks {
		if _, err := statement.Exec(chunk.ChunkID, chunk.Title, chunk.Path, chunk.Source, chunk.Content); err != nil {
			return fmt.Errorf("insert chunk %s: %w", chunk.ChunkID, err)
		}
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close chunks sqlite: %w", err)
	}
	return nil
}

func normalizeCitations(raw json.RawMessage) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return []byte("{\"citations\":[]}\n"), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("citations must be valid json")
	}
	if raw[0] == '[' {
		raw = append([]byte("{\"citations\":"), raw...)
		raw = append(raw, '}')
	}
	raw = append(raw, '\n')
	return raw, nil
}

type buildCitationFile struct {
	Source        string                      `json:"source,omitempty"`
	License       string                      `json:"license,omitempty"`
	SourcePolicy  string                      `json:"source_policy,omitempty"`
	Citations     []buildChunkCitation        `json:"citations"`
	CrawlManifest []moegirlCrawlManifestEntry `json:"crawl_manifest,omitempty"`
}

type buildChunkCitation struct {
	ChunkID      string `json:"chunk_id"`
	Source       string `json:"source,omitempty"`
	Title        string `json:"title,omitempty"`
	URL          string `json:"url,omitempty"`
	License      string `json:"license,omitempty"`
	SourcePolicy string `json:"source_policy,omitempty"`
	RevisionID   string `json:"revision_id,omitempty"`
	PageID       string `json:"page_id,omitempty"`
}

func normalizeBuildCitations(kbID string, chunks []knowledgePackBuildChunk, raw json.RawMessage) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		return normalizeCitations(raw)
	}

	citationFile := buildCitationFile{
		Citations: make([]buildChunkCitation, 0, len(chunks)),
	}
	sourceCounts := map[string]int{}
	licenseCounts := map[string]int{}
	policyCounts := map[string]int{}

	for index, chunk := range chunks {
		if !chunkHasCitationMetadata(chunk) {
			continue
		}
		citation := buildChunkCitation{
			ChunkID:      chunk.ChunkID,
			Source:       firstNonEmpty(chunk.SourceName, chunk.Source),
			Title:        firstNonEmpty(chunk.CitationTitle, chunk.Title),
			URL:          chunk.CitationURL,
			License:      chunk.License,
			SourcePolicy: chunk.SourcePolicy,
			RevisionID:   chunk.SourceRevisionID,
			PageID:       chunk.SourcePageID,
		}
		citationFile.Citations = append(citationFile.Citations, citation)
		if citation.Source != "" {
			sourceCounts[citation.Source]++
		}
		if citation.License != "" {
			licenseCounts[citation.License]++
		}
		if citation.SourcePolicy != "" {
			policyCounts[citation.SourcePolicy]++
		}
		if isMoegirlKB(kbID) {
			pageID, err := parseMoegirlPageID(chunk.SourcePageID)
			if err != nil {
				return nil, fmt.Errorf("chunks[%d].source_page_id must be numeric for moegirl citation metadata", index)
			}
			citationFile.CrawlManifest = append(citationFile.CrawlManifest, moegirlCrawlManifestEntry{
				KBID:         kbID,
				SourceName:   firstNonEmpty(chunk.SourceName, chunk.Source),
				SourceURL:    chunk.CitationURL,
				CanonicalURL: chunk.CitationURL,
				PageID:       pageID,
				RevisionID:   chunk.SourceRevisionID,
				Touched:      time.Now().UTC().Format(time.RFC3339),
				FetchedAt:    time.Now().UTC().Format(time.RFC3339),
				License:      chunk.License,
				SourcePolicy: chunk.SourcePolicy,
				Categories:   nonEmptyStringsWithFallback(chunk.Tags, "faq"),
			})
		}
	}

	citationFile.Source = mostFrequentString(sourceCounts)
	citationFile.License = mostFrequentString(licenseCounts)
	citationFile.SourcePolicy = mostFrequentString(policyCounts)
	data, err := json.MarshalIndent(citationFile, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode citations: %w", err)
	}
	data = append(data, '\n')
	return data, nil
}

func chunkHasCitationMetadata(chunk knowledgePackBuildChunk) bool {
	return chunk.CitationURL != "" ||
		chunk.CitationTitle != "" ||
		chunk.SourceName != "" ||
		chunk.License != "" ||
		chunk.SourcePolicy != "" ||
		chunk.SourceRevisionID != "" ||
		chunk.SourcePageID != ""
}

func parseMoegirlPageID(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("page id is required")
	}
	pageID := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("page id must be numeric")
		}
		pageID = pageID*10 + int(r-'0')
	}
	if pageID == 0 {
		return 0, fmt.Errorf("page id must be positive")
	}
	return pageID, nil
}

func nonEmptyStringsWithFallback(values []string, fallback string) []string {
	values = normalizeStringList(values)
	if len(values) == 0 && fallback != "" {
		return []string{fallback}
	}
	return values
}

func mostFrequentString(counts map[string]int) string {
	best := ""
	bestCount := 0
	for value, count := range counts {
		if count > bestCount || (count == bestCount && (best == "" || value < best)) {
			best = value
			bestCount = count
		}
	}
	return best
}

func emptyVectorIndex(dimension int) []byte {
	var buffer bytes.Buffer
	buffer.Write([]byte{0x59, 0x46, 0x4B, 0x56, 0x45, 0x43, 0x31, 0x00})
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(dimension))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(0))
	return buffer.Bytes()
}

func storedZip(files map[string][]byte) ([]byte, error) {
	var body bytes.Buffer
	writer := zip.NewWriter(&body)

	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		header := &zip.FileHeader{
			Name:   path,
			Method: zip.Store,
		}
		header.SetModTime(time.Unix(0, 0).UTC())
		file, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("create zip entry %s: %w", path, err)
		}
		if _, err := io.Copy(file, bytes.NewReader(files[path])); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("write zip entry %s: %w", path, err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close knowledge pack zip: %w", err)
	}
	return body.Bytes(), nil
}

func buildManifest(
	kbID string,
	version string,
	payload buildPublishRequest,
	files map[string][]byte,
	packageBytes []byte,
	signingSeed []byte,
) ([]byte, error) {
	packageDigest := sha256.Sum256(packageBytes)
	privateKey := ed25519.NewKeyFromSeed(signingSeed)
	signature := ed25519.Sign(privateKey, packageDigest[:])

	recommended := payload.LLMRecommended
	if len(recommended) == 0 {
		recommended = []string{"Qwen3-4B-Q4_K_M"}
	}

	file := func(path string) knowledgePackBuildFile {
		digest := sha256.Sum256(files[path])
		return knowledgePackBuildFile{
			Path:     path,
			SHA256:   "sha256:" + hex.EncodeToString(digest[:]),
			ByteSize: int64(len(files[path])),
		}
	}

	manifest := knowledgePackBuildManifest{
		SchemaVersion:      "knowledge-pack-manifest/v1",
		KBID:               kbID,
		Version:            version,
		ContentHash:        "sha256:" + hex.EncodeToString(packageDigest[:]),
		Signature:          "ed25519:" + base64.StdEncoding.EncodeToString(signature),
		ChunkSchemaVersion: "chunk-v1",
		EmbeddingModel:     defaultEmbeddingModel,
		EmbeddingDimension: defaultEmbeddingDimension,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339),
		LLMRecommended:     recommended,
	}
	manifest.Files.Chunks = []knowledgePackBuildFile{file("chunks.sqlite")}
	manifest.Files.FTS = []knowledgePackBuildFile{file("chunks.sqlite")}
	manifest.Files.Vector = []knowledgePackBuildFile{file("vector.index")}
	manifest.Files.Assets = []knowledgePackBuildFile{}
	manifest.Files.Citations = []knowledgePackBuildFile{file("citations.json")}
	manifest.Files.Prompts = []knowledgePackBuildFile{file("prompts.json")}
	manifest.Security.ExecutableCodeAllowed = false
	manifest.Security.RemoteCodePolicy = "forbidden"

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')
	return data, nil
}
