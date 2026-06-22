package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yi-flow/knowledge-base/internal/server"
)

func TestRAGGatewayQueriesWeKnoraAndNormalizesChunks(t *testing.T) {
	var upstreamHeader string
	var upstreamRequest struct {
		QueryText             string `json:"query_text"`
		MatchCount            int    `json:"match_count"`
		DisableVectorMatch    bool   `json:"disable_vector_match"`
		DisableKeywordsMatch  bool   `json:"disable_keywords_match"`
		SkipContextEnrichment bool   `json:"skip_context_enrichment"`
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeader = r.Header.Get("X-API-Key")
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/knowledge-bases/kb-upstream/hybrid-search" {
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{
			"success": true,
			"data": []map[string]any{
				{
					"id":                 "chunk-001",
					"content":            "知识包更新路径通过 manifest.json 和 knowledge-pack.zip 发布。",
					"knowledge_id":       "doc-001",
					"knowledge_title":    "知识包更新路径",
					"score":              0.91,
					"knowledge_filename": "runtime/update.md",
					"knowledge_source":   "manual",
				},
				{
					"id":                 "chunk-002",
					"content":            "第二条结果应被 top_k 截断。",
					"knowledge_id":       "doc-002",
					"knowledge_title":    "截断验证",
					"score":              0.2,
					"knowledge_filename": "runtime/other.md",
					"knowledge_source":   "manual",
				},
			},
		})
	}))
	defer upstream.Close()

	handler := newRAGGatewayTestHandler(t, upstream.URL, "yi-flow-core=kb-upstream", 5*time.Second)
	requestBody := bytes.NewBufferString(`{"kb_id":"yi-flow-core","query":"知识包更新路径","top_k":1}`)
	request := httptest.NewRequest(http.MethodPost, "/rag/api/query", requestBody)
	request.Header.Set("Authorization", "Bearer app-rag-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("gateway status=%d body=%s", response.Code, response.Body.String())
	}
	if upstreamHeader != "sk-weknora" {
		t.Fatalf("upstream api key header = %q", upstreamHeader)
	}
	if upstreamRequest.QueryText != "知识包更新路径" || upstreamRequest.MatchCount != 1 || !upstreamRequest.DisableVectorMatch || upstreamRequest.DisableKeywordsMatch || !upstreamRequest.SkipContextEnrichment {
		t.Fatalf("upstream request = %+v", upstreamRequest)
	}

	var decoded struct {
		Provider         string `json:"provider"`
		Status           string `json:"status"`
		KnowledgeVersion string `json:"knowledge_version"`
		Query            string `json:"query"`
		Chunks           []struct {
			ChunkID string  `json:"chunk_id"`
			Title   string  `json:"title"`
			Path    string  `json:"path"`
			Source  string  `json:"source"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"chunks"`
		LatencyMS int64 `json:"latency_ms"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode gateway response: %v", err)
	}
	if decoded.Provider != "weknora" || decoded.Status != "ok" || decoded.KnowledgeVersion != "remote:weknora:kb-upstream" || decoded.Query != "知识包更新路径" {
		t.Fatalf("gateway header = %+v", decoded)
	}
	if len(decoded.Chunks) != 1 {
		t.Fatalf("chunks len=%d body=%s", len(decoded.Chunks), response.Body.String())
	}
	chunk := decoded.Chunks[0]
	if chunk.ChunkID != "weknora:chunk-001" || chunk.Title != "知识包更新路径" || chunk.Path != "runtime/update.md" || chunk.Source != "weknora:manual" || chunk.Score != 0.91 {
		t.Fatalf("normalized chunk = %+v", chunk)
	}
	if !strings.Contains(chunk.Content, "manifest.json") {
		t.Fatalf("chunk content = %q", chunk.Content)
	}
	if decoded.LatencyMS < 0 {
		t.Fatalf("latency_ms should be non-negative: %d", decoded.LatencyMS)
	}
}

func TestRAGGatewayAuditLogDoesNotExposeSecretsOrRawQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{
			"success": true,
			"data": []map[string]any{
				{
					"id":                 "chunk-001",
					"content":            "知识包更新路径通过 manifest.json 发布。",
					"knowledge_title":    "知识包更新路径",
					"score":              0.91,
					"knowledge_filename": "runtime/update.md",
					"knowledge_source":   "manual",
				},
			},
		})
	}))
	defer upstream.Close()

	var audit bytes.Buffer
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
		RAGGateway: server.RAGGatewayOptions{
			Token:          "app-rag-token",
			WeKnoraBaseURL: upstream.URL,
			WeKnoraAPIKey:  "sk-weknora",
			WeKnoraKBMap:   "yi-flow-core=kb-upstream",
			Timeout:        5 * time.Second,
			TopKMax:        8,
			AuditLog:       &audit,
		},
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	rawQuery := "知识包更新路径"
	request := httptest.NewRequest(http.MethodPost, "/rag/api/query", bytes.NewBufferString(`{"kb_id":"yi-flow-core","query":"`+rawQuery+`","top_k":1}`))
	request.Header.Set("Authorization", "Bearer app-rag-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("gateway status=%d body=%s", response.Code, response.Body.String())
	}

	logLine := audit.String()
	for _, expected := range []string{"event=rag_gateway_query", "kb_id=yi-flow-core", "provider=weknora", "status=ok", "query_hash=", "latency_ms=", "chunks=1"} {
		if !strings.Contains(logLine, expected) {
			t.Fatalf("audit log missing %q in %q", expected, logLine)
		}
	}
	for _, forbidden := range []string{rawQuery, "app-rag-token", "sk-weknora"} {
		if strings.Contains(logLine, forbidden) {
			t.Fatalf("audit log exposed %q in %q", forbidden, logLine)
		}
	}
}

func TestRAGGatewayRequiresAppToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unauthorized request should not reach upstream")
	}))
	defer upstream.Close()

	handler := newRAGGatewayTestHandler(t, upstream.URL, "yi-flow-core=kb-upstream", 5*time.Second)
	request := httptest.NewRequest(http.MethodPost, "/rag/api/query", bytes.NewBufferString(`{"kb_id":"yi-flow-core","query":"hello"}`))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("gateway status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "rag_unauthorized") {
		t.Fatalf("body missing stable error code: %s", response.Body.String())
	}
}

func TestRAGGatewayDisabledWithoutConfiguration(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/rag/api/query", bytes.NewBufferString(`{"kb_id":"yi-flow-core","query":"hello"}`))
	request.Header.Set("Authorization", "Bearer app-rag-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("gateway status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "rag_gateway_disabled") {
		t.Fatalf("body missing stable error code: %s", response.Body.String())
	}
}

func TestRAGGatewayReturnsStableEmptyResult(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{
			"success": true,
			"data":    []map[string]any{},
		})
	}))
	defer upstream.Close()

	handler := newRAGGatewayTestHandler(t, upstream.URL, "yi-flow-core=kb-upstream", 5*time.Second)
	request := httptest.NewRequest(http.MethodPost, "/rag/api/query", bytes.NewBufferString(`{"kb_id":"yi-flow-core","query":"unknown"}`))
	request.Header.Set("Authorization", "Bearer app-rag-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("gateway status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"status":"empty_result"`) || !strings.Contains(response.Body.String(), `"chunks":[]`) {
		t.Fatalf("body missing stable empty result: %s", response.Body.String())
	}
}

func TestAdminRAGCompareShowsRemoteWeKnoraResults(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{
			"success": true,
			"data": []map[string]any{
				{
					"id":                 "remote-001",
					"content":            "WeKnora 命中的远程知识片段。",
					"knowledge_id":       "doc-remote",
					"knowledge_title":    "远程 RAG",
					"score":              0.88,
					"knowledge_filename": "remote/rag.md",
					"knowledge_source":   "manual",
				},
			},
		})
	}))
	defer upstream.Close()

	handler := newRAGGatewayTestHandler(t, upstream.URL, "yi-flow-core=kb-upstream", 5*time.Second)
	request := httptest.NewRequest(http.MethodPost, "/admin/api/kb/yi-flow-core/rag/compare", bytes.NewBufferString(`{"query":"远程 RAG","top_k":2}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("rag compare status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"status":"no_pack"`) {
		t.Fatalf("local no-pack state missing: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"provider":"weknora"`) ||
		!strings.Contains(response.Body.String(), `"knowledge_version":"remote:weknora:kb-upstream"`) ||
		!strings.Contains(response.Body.String(), `"chunk_id":"weknora:remote-001"`) {
		t.Fatalf("remote WeKnora result missing: %s", response.Body.String())
	}
}

func TestRAGGatewayClassifiesUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"error": map[string]any{
				"message": "boom",
			},
		})
	}))
	defer upstream.Close()

	handler := newRAGGatewayTestHandler(t, upstream.URL, "yi-flow-core=kb-upstream", 5*time.Second)
	request := httptest.NewRequest(http.MethodPost, "/rag/api/query", bytes.NewBufferString(`{"kb_id":"yi-flow-core","query":"hello"}`))
	request.Header.Set("Authorization", "Bearer app-rag-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("gateway status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "weknora_upstream_status") {
		t.Fatalf("body missing stable error code: %s", response.Body.String())
	}
}

func TestRAGGatewayClassifiesTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		writeTestJSON(t, w, http.StatusOK, map[string]any{"success": true, "data": []map[string]any{}})
	}))
	defer upstream.Close()

	handler := newRAGGatewayTestHandler(t, upstream.URL, "yi-flow-core=kb-upstream", time.Millisecond)
	request := httptest.NewRequest(http.MethodPost, "/rag/api/query", bytes.NewBufferString(`{"kb_id":"yi-flow-core","query":"hello"}`))
	request.Header.Set("Authorization", "Bearer app-rag-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("gateway status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "weknora_timeout") {
		t.Fatalf("body missing stable timeout code: %s", response.Body.String())
	}
}

func newRAGGatewayTestHandler(t *testing.T, upstreamURL string, kbMap string, timeout time.Duration) http.Handler {
	t.Helper()
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
		RAGGateway: server.RAGGatewayOptions{
			Token:          "app-rag-token",
			WeKnoraBaseURL: upstreamURL,
			WeKnoraAPIKey:  "sk-weknora",
			WeKnoraKBMap:   kbMap,
			Timeout:        timeout,
			TopKMax:        8,
		},
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	return handler
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}
