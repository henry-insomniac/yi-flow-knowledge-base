package server

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	defaultRAGCompareTopK = 5
	maxRAGCompareTopK     = 12
)

type ragCompareRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

type ragCompareResponse struct {
	KBID   string               `json:"kb_id"`
	Query  string               `json:"query"`
	TopK   int                  `json:"top_k"`
	Local  ragCompareLocalSide  `json:"local"`
	Remote ragCompareRemoteSide `json:"remote"`
}

type ragCompareLocalSide struct {
	Status  string                      `json:"status"`
	Version string                      `json:"version,omitempty"`
	Chunks  []knowledgePackPreviewChunk `json:"chunks"`
	Error   *ragCompareError            `json:"error,omitempty"`
}

type ragCompareRemoteSide struct {
	Status           string            `json:"status"`
	Provider         string            `json:"provider,omitempty"`
	KnowledgeVersion string            `json:"knowledge_version,omitempty"`
	LatencyMS        int64             `json:"latency_ms"`
	Chunks           []ragGatewayChunk `json:"chunks"`
	Error            *ragCompareError  `json:"error,omitempty"`
}

type ragCompareError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) handleAdminRAGCompare(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, ok := strings.CutPrefix(r.URL.Path, "/admin/api/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/rag/compare")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	defer r.Body.Close()
	var request ragCompareRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": ragCompareError{Code: "rag_compare_invalid_request", Message: "request body must be valid JSON"},
		})
		return
	}

	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": ragCompareError{Code: "rag_compare_invalid_request", Message: "query is required"},
		})
		return
	}
	topK := normalizedRAGCompareTopK(request.TopK)

	writeJSON(w, http.StatusOK, ragCompareResponse{
		KBID:   kbID,
		Query:  request.Query,
		TopK:   topK,
		Local:  h.localRAGCompare(r, kbID, request.Query, topK),
		Remote: h.remoteRAGCompare(r, kbID, request.Query, topK),
	})
}

func normalizedRAGCompareTopK(value int) int {
	if value <= 0 {
		return defaultRAGCompareTopK
	}
	if value > maxRAGCompareTopK {
		return maxRAGCompareTopK
	}
	return value
}

func (h *Handler) localRAGCompare(_ *http.Request, kbID string, query string, topK int) ragCompareLocalSide {
	version, err := h.latestVersion(kbID)
	if err != nil {
		return ragCompareLocalSide{
			Status: "no_pack",
			Chunks: []knowledgePackPreviewChunk{},
			Error:  &ragCompareError{Code: "local_pack_missing", Message: "latest local Knowledge Pack is not available"},
		}
	}

	chunks, err := h.searchKnowledgePack(kbID, version, query, topK)
	if err != nil {
		return ragCompareLocalSide{
			Status:  "error",
			Version: version,
			Chunks:  []knowledgePackPreviewChunk{},
			Error:   &ragCompareError{Code: "local_search_failed", Message: err.Error()},
		}
	}

	status := "ok"
	if len(chunks) == 0 {
		status = "empty_result"
	}
	return ragCompareLocalSide{
		Status:  status,
		Version: version,
		Chunks:  chunks,
	}
}

func (h *Handler) remoteRAGCompare(r *http.Request, kbID string, query string, topK int) ragCompareRemoteSide {
	if !h.ragGateway.enabled() {
		return ragCompareRemoteSide{
			Status: "disabled",
			Chunks: []ragGatewayChunk{},
			Error:  &ragCompareError{Code: "rag_gateway_disabled", Message: "remote RAG gateway is not configured"},
		}
	}

	weknoraKBID, ok := h.ragGateway.kbMap[kbID]
	if !ok {
		return ragCompareRemoteSide{
			Status: "error",
			Chunks: []ragGatewayChunk{},
			Error:  &ragCompareError{Code: "rag_kb_not_mapped", Message: "kb_id is not mapped to a WeKnora knowledge base"},
		}
	}

	remoteTopK := topK
	if remoteTopK > h.ragGateway.topKMax {
		remoteTopK = h.ragGateway.topKMax
	}
	started := time.Now()
	chunks, err := h.ragGateway.queryWeKnora(r.Context(), query, weknoraKBID, remoteTopK)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		code, message := ragCompareRemoteError(err)
		return ragCompareRemoteSide{
			Status:    "error",
			LatencyMS: latency,
			Chunks:    []ragGatewayChunk{},
			Error:     &ragCompareError{Code: code, Message: message},
		}
	}

	status := "ok"
	if len(chunks) == 0 {
		status = "empty_result"
	}
	return ragCompareRemoteSide{
		Status:           status,
		Provider:         "weknora",
		KnowledgeVersion: "remote:weknora:" + weknoraKBID,
		LatencyMS:        latency,
		Chunks:           chunks,
	}
}

func ragCompareRemoteError(err error) (string, string) {
	switch {
	case errors.Is(err, errWeKnoraTimeout):
		return "weknora_timeout", "remote RAG provider timed out"
	case errors.Is(err, errWeKnoraUnauthorized):
		return "weknora_unauthorized", "remote RAG provider rejected server credentials"
	case errors.Is(err, errWeKnoraUpstreamStatus):
		return "weknora_upstream_status", "remote RAG provider returned an error status"
	case errors.Is(err, errWeKnoraInvalidResponse):
		return "weknora_invalid_response", "remote RAG provider returned an invalid response"
	default:
		return "weknora_unavailable", "remote RAG provider is unavailable"
	}
}

func (h *Handler) searchKnowledgePack(kbID string, version string, query string, topK int) ([]knowledgePackPreviewChunk, error) {
	versionDir := h.versionDir(kbID, version)
	manifestPath := filepath.Join(versionDir, "manifest.json")
	packagePath := filepath.Join(versionDir, "knowledge-pack.zip")

	manifest, err := readPreviewManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	archive, err := zip.OpenReader(packagePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()

	chunksFile := findChunkSQLiteFile(archive.File, manifest)
	if chunksFile == nil {
		return nil, fmt.Errorf("%w: chunks sqlite file not found", errPreviewUnavailable)
	}

	return searchChunksFromSQLite(chunksFile, query, topK)
}

func searchChunksFromSQLite(file *zip.File, query string, topK int) ([]knowledgePackPreviewChunk, error) {
	database, tempPath, err := openSQLiteZipEntry(file)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = database.Close()
		_ = os.Remove(tempPath)
	}()

	rows, err := database.Query(`
		SELECT
			COALESCE(chunk_id, ''),
			COALESCE(title, ''),
			COALESCE(path, ''),
			COALESCE(source, ''),
			COALESCE(content, '')
		FROM chunks
		WHERE chunks MATCH ?
		ORDER BY bm25(chunks) ASC
		LIMIT ?;
	`, ftsMatchQuery(query), topK)
	if err != nil {
		return nil, fmt.Errorf("%w: search chunks table: %v", errPreviewUnavailable, err)
	}
	defer rows.Close()

	return scanPreviewChunks(rows)
}

func ftsMatchQuery(query string) string {
	phraseQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	grams := cjkTrigrams(query)
	if len(grams) == 0 {
		return phraseQuery
	}
	return strings.Join(append([]string{phraseQuery}, grams...), " OR ")
}

func cjkTrigrams(query string) []string {
	searchable := make([]rune, 0, len(query))
	hasCJK := false
	for _, value := range query {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			searchable = append(searchable, value)
			if isCJKRune(value) {
				hasCJK = true
			}
		}
	}
	if !hasCJK || len(searchable) < 3 {
		return nil
	}

	seen := map[string]bool{}
	grams := []string{}
	for index := 0; index <= len(searchable)-3; index++ {
		gramRunes := searchable[index : index+3]
		containsCJK := false
		for _, value := range gramRunes {
			if isCJKRune(value) {
				containsCJK = true
				break
			}
		}
		if !containsCJK {
			continue
		}
		gram := string(gramRunes)
		if seen[gram] {
			continue
		}
		seen[gram] = true
		grams = append(grams, gram)
		if len(grams) >= 12 {
			break
		}
	}
	return grams
}

func isCJKRune(value rune) bool {
	return (value >= 0x4E00 && value <= 0x9FFF) ||
		(value >= 0x3400 && value <= 0x4DBF) ||
		(value >= 0xF900 && value <= 0xFAFF) ||
		(value >= 0x3040 && value <= 0x30FF) ||
		(value >= 0xAC00 && value <= 0xD7AF)
}

func openSQLiteZipEntry(file *zip.File) (*sql.DB, string, error) {
	if file.UncompressedSize64 > maxPreviewSQLiteBytes {
		return nil, "", fmt.Errorf("%w: chunks sqlite is too large for admin preview", errPreviewUnavailable)
	}

	tempFile, err := os.CreateTemp("", "yi-flow-knowledge-sqlite-*.sqlite")
	if err != nil {
		return nil, "", fmt.Errorf("create sqlite temp file: %w", err)
	}
	tempPath := tempFile.Name()

	reader, err := file.Open()
	if err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return nil, "", fmt.Errorf("open chunks sqlite from package: %w", err)
	}
	_, copyErr := io.Copy(tempFile, io.LimitReader(reader, maxPreviewSQLiteBytes+1))
	closeErr := reader.Close()
	tempCloseErr := tempFile.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return nil, "", fmt.Errorf("copy chunks sqlite: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return nil, "", fmt.Errorf("close chunks sqlite entry: %w", closeErr)
	}
	if tempCloseErr != nil {
		_ = os.Remove(tempPath)
		return nil, "", fmt.Errorf("close sqlite temp file: %w", tempCloseErr)
	}

	database, err := sql.Open("sqlite", tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return nil, "", fmt.Errorf("open sqlite: %w", err)
	}
	return database, tempPath, nil
}

func scanPreviewChunks(rows *sql.Rows) ([]knowledgePackPreviewChunk, error) {
	chunks := []knowledgePackPreviewChunk{}
	for rows.Next() {
		var chunk knowledgePackPreviewChunk
		if err := rows.Scan(&chunk.ChunkID, &chunk.Title, &chunk.Path, &chunk.Source, &chunk.Content); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		chunk.Content = truncateRunes(strings.TrimSpace(chunk.Content), maxPreviewContentRunes)
		chunk.SuggestedQuestions = suggestedQuestions(chunk)
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}
	return chunks, nil
}
