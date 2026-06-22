package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type RAGGatewayOptions struct {
	Token              string
	WeKnoraBaseURL     string
	WeKnoraAPIKey      string
	WeKnoraKBMap       string
	DefaultWeKnoraKBID string
	Timeout            time.Duration
	TopKMax            int
	HTTPClient         *http.Client
	AuditLog           io.Writer
}

type ragGateway struct {
	token          string
	weknoraBaseURL *url.URL
	weknoraAPIKey  string
	kbMap          map[string]string
	timeout        time.Duration
	topKMax        int
	httpClient     *http.Client
	auditLog       io.Writer
}

type ragGatewayQueryRequest struct {
	KBID  string `json:"kb_id"`
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
	Mode  string `json:"mode"`
}

type ragGatewayQueryResponse struct {
	Provider         string            `json:"provider"`
	Status           string            `json:"status"`
	KnowledgeVersion string            `json:"knowledge_version"`
	Query            string            `json:"query"`
	Chunks           []ragGatewayChunk `json:"chunks"`
	LatencyMS        int64             `json:"latency_ms"`
}

type ragGatewayChunk struct {
	ChunkID string  `json:"chunk_id"`
	Title   string  `json:"title"`
	Path    string  `json:"path"`
	Source  string  `json:"source"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	URL     string  `json:"url,omitempty"`
}

type weknoraSearchRequest struct {
	Query           string `json:"query"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
}

type weknoraSearchResponse struct {
	Success *bool                  `json:"success"`
	Data    []weknoraSearchResult  `json:"data"`
	Error   map[string]interface{} `json:"error"`
}

type weknoraSearchResult struct {
	ID                string                 `json:"id"`
	Content           string                 `json:"content"`
	KnowledgeID       string                 `json:"knowledge_id"`
	KnowledgeTitle    string                 `json:"knowledge_title"`
	KnowledgeFilename string                 `json:"knowledge_filename"`
	KnowledgeSource   string                 `json:"knowledge_source"`
	Score             float64                `json:"score"`
	Metadata          map[string]interface{} `json:"metadata"`
}

func newRAGGateway(options RAGGatewayOptions) (*ragGateway, error) {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	topKMax := options.TopKMax
	if topKMax <= 0 {
		topKMax = 8
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	auditLog := options.AuditLog
	if auditLog == nil {
		auditLog = io.Discard
	}
	gateway := &ragGateway{
		token:         strings.TrimSpace(options.Token),
		weknoraAPIKey: strings.TrimSpace(options.WeKnoraAPIKey),
		kbMap:         map[string]string{},
		timeout:       timeout,
		topKMax:       topKMax,
		httpClient:    client,
		auditLog:      auditLog,
	}
	baseURL := strings.TrimSpace(options.WeKnoraBaseURL)
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("invalid WeKnora base URL")
		}
		gateway.weknoraBaseURL = parsed
	}
	for kbID, weknoraKBID := range parseWeKnoraKBMap(options.WeKnoraKBMap) {
		gateway.kbMap[kbID] = weknoraKBID
	}
	defaultKBID := strings.TrimSpace(options.DefaultWeKnoraKBID)
	if defaultKBID != "" {
		if _, exists := gateway.kbMap["yi-flow-core"]; !exists {
			gateway.kbMap["yi-flow-core"] = defaultKBID
		}
	}
	return gateway, nil
}

func parseWeKnoraKBMap(raw string) map[string]string {
	mapping := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		kbID := strings.TrimSpace(parts[0])
		weknoraKBID := strings.TrimSpace(parts[1])
		if kbID != "" && weknoraKBID != "" {
			mapping[kbID] = weknoraKBID
		}
	}
	return mapping
}

func (g *ragGateway) enabled() bool {
	return g != nil && g.token != "" && g.weknoraBaseURL != nil && g.weknoraAPIKey != "" && len(g.kbMap) > 0
}

func (h *Handler) handleRAGQuery(w http.ResponseWriter, r *http.Request) {
	if !h.ragGateway.enabled() {
		writeRAGGatewayError(w, http.StatusServiceUnavailable, "rag_gateway_disabled", "remote RAG gateway is not configured")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+h.ragGateway.token {
		writeRAGGatewayError(w, http.StatusUnauthorized, "rag_unauthorized", "remote RAG gateway token is invalid")
		return
	}
	if r.Body == nil {
		writeRAGGatewayError(w, http.StatusBadRequest, "rag_invalid_request", "request body is required")
		return
	}
	defer r.Body.Close()

	var request ragGatewayQueryRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeRAGGatewayError(w, http.StatusBadRequest, "rag_invalid_request", "request body must be valid JSON")
		return
	}
	request.KBID = strings.TrimSpace(request.KBID)
	request.Query = strings.TrimSpace(request.Query)
	if request.KBID == "" || request.Query == "" {
		writeRAGGatewayError(w, http.StatusBadRequest, "rag_invalid_request", "kb_id and query are required")
		return
	}
	weknoraKBID, ok := h.ragGateway.kbMap[request.KBID]
	if !ok {
		h.ragGateway.auditQuery(request.KBID, "weknora", "rag_kb_not_mapped", request.Query, 0, 0)
		writeRAGGatewayError(w, http.StatusBadRequest, "rag_kb_not_mapped", "kb_id is not mapped to a WeKnora knowledge base")
		return
	}
	topK := request.TopK
	if topK <= 0 {
		topK = 5
	}
	if topK > h.ragGateway.topKMax {
		topK = h.ragGateway.topKMax
	}

	started := time.Now()
	chunks, err := h.ragGateway.queryWeKnora(r.Context(), request.Query, weknoraKBID, topK)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		h.ragGateway.auditQuery(request.KBID, "weknora", ragGatewayAuditStatusForError(err), request.Query, latency, 0)
		writeRAGGatewayUpstreamError(w, err)
		return
	}
	status := "ok"
	if len(chunks) == 0 {
		status = "empty_result"
	}
	h.ragGateway.auditQuery(request.KBID, "weknora", status, request.Query, latency, len(chunks))
	writeJSON(w, http.StatusOK, ragGatewayQueryResponse{
		Provider:         "weknora",
		Status:           status,
		KnowledgeVersion: "remote:weknora:" + weknoraKBID,
		Query:            request.Query,
		Chunks:           chunks,
		LatencyMS:        latency,
	})
}

func (g *ragGateway) queryWeKnora(parent context.Context, query string, weknoraKBID string, topK int) ([]ragGatewayChunk, error) {
	requestBody, err := json.Marshal(weknoraSearchRequest{
		Query:           query,
		KnowledgeBaseID: weknoraKBID,
	})
	if err != nil {
		return nil, err
	}
	endpoint := g.weknoraBaseURL.ResolveReference(&url.URL{Path: "/api/v1/knowledge-search"})
	ctx, cancel := context.WithTimeout(parent, g.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-API-Key", g.weknoraAPIKey)

	response, err := g.httpClient.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errWeKnoraTimeout
		}
		return nil, fmt.Errorf("%w: %v", errWeKnoraUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read upstream body", errWeKnoraInvalidResponse)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, errWeKnoraUnauthorized
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %d", errWeKnoraUpstreamStatus, response.StatusCode)
	}
	var decoded weknoraSearchResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("%w: decode", errWeKnoraInvalidResponse)
	}
	if decoded.Success != nil && !*decoded.Success {
		return nil, errWeKnoraInvalidResponse
	}
	return normalizeWeKnoraChunks(decoded.Data, topK), nil
}

func (g *ragGateway) auditQuery(kbID string, provider string, status string, query string, latencyMS int64, chunks int) {
	if g == nil || g.auditLog == nil {
		return
	}
	queryHash := sha256.Sum256([]byte(query))
	_, _ = fmt.Fprintf(
		g.auditLog,
		"event=rag_gateway_query kb_id=%s provider=%s status=%s query_hash=%x latency_ms=%d chunks=%d\n",
		strings.TrimSpace(kbID),
		strings.TrimSpace(provider),
		strings.TrimSpace(status),
		queryHash,
		latencyMS,
		chunks,
	)
}

func ragGatewayAuditStatusForError(err error) string {
	switch {
	case errors.Is(err, errWeKnoraTimeout):
		return "weknora_timeout"
	case errors.Is(err, errWeKnoraUnauthorized):
		return "weknora_unauthorized"
	case errors.Is(err, errWeKnoraUpstreamStatus):
		return "weknora_upstream_status"
	case errors.Is(err, errWeKnoraInvalidResponse):
		return "weknora_invalid_response"
	default:
		return "weknora_unavailable"
	}
}

func normalizeWeKnoraChunks(results []weknoraSearchResult, topK int) []ragGatewayChunk {
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	chunks := make([]ragGatewayChunk, 0, len(results))
	for _, result := range results {
		content := strings.TrimSpace(result.Content)
		if strings.TrimSpace(result.ID) == "" || content == "" {
			continue
		}
		title := firstNonEmpty(result.KnowledgeTitle, result.KnowledgeFilename, result.KnowledgeID, result.ID)
		path := firstNonEmpty(result.KnowledgeFilename, result.KnowledgeID, result.ID)
		source := firstNonEmpty(result.KnowledgeSource, "knowledge")
		chunk := ragGatewayChunk{
			ChunkID: "weknora:" + strings.TrimSpace(result.ID),
			Title:   title,
			Path:    path,
			Source:  "weknora:" + source,
			Content: content,
			Score:   result.Score,
		}
		if sourceURL := stringMetadata(result.Metadata, "url"); sourceURL != "" {
			chunk.URL = sourceURL
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func stringMetadata(metadata map[string]interface{}, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	stringValue, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringValue)
}

var (
	errWeKnoraTimeout         = errors.New("weknora timeout")
	errWeKnoraUnavailable     = errors.New("weknora unavailable")
	errWeKnoraUnauthorized    = errors.New("weknora unauthorized")
	errWeKnoraUpstreamStatus  = errors.New("weknora upstream status")
	errWeKnoraInvalidResponse = errors.New("weknora invalid response")
)

func writeRAGGatewayUpstreamError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWeKnoraTimeout):
		writeRAGGatewayError(w, http.StatusGatewayTimeout, "weknora_timeout", "remote RAG provider timed out")
	case errors.Is(err, errWeKnoraUnauthorized):
		writeRAGGatewayError(w, http.StatusBadGateway, "weknora_unauthorized", "remote RAG provider rejected server credentials")
	case errors.Is(err, errWeKnoraUpstreamStatus):
		writeRAGGatewayError(w, http.StatusBadGateway, "weknora_upstream_status", "remote RAG provider returned an error status")
	case errors.Is(err, errWeKnoraInvalidResponse):
		writeRAGGatewayError(w, http.StatusBadGateway, "weknora_invalid_response", "remote RAG provider returned an invalid response")
	default:
		writeRAGGatewayError(w, http.StatusBadGateway, "weknora_unavailable", "remote RAG provider is unavailable")
	}
}

func writeRAGGatewayError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
