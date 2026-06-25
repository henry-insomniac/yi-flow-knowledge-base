package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultRAGFlowTimeout      = 15 * time.Second
	defaultRAGFlowPageSize     = 100
	maxRAGFlowPageSize         = 500
	defaultRAGFlowExportSource = "RAGFlow"
)

type RAGFlowOptions struct {
	BaseURL  string
	APIKey   string
	Timeout  time.Duration
	PageSize int
}

type ragFlowClient struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
	pageSize   int
}

type ragFlowExportPublishRequest struct {
	Version        string                     `json:"version"`
	DatasetID      string                     `json:"dataset_id"`
	Prompts        []knowledgePackBuildPrompt `json:"prompts"`
	LLMRecommended []string                   `json:"llm_recommended"`
}

type ragFlowAPIResponse[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type ragFlowDocumentsData struct {
	Docs      []ragFlowDocument `json:"docs"`
	Documents []ragFlowDocument `json:"documents"`
	Total     int               `json:"total"`
}

type ragFlowChunksData struct {
	Chunks []ragFlowChunk   `json:"chunks"`
	Doc    *ragFlowDocument `json:"doc"`
	Total  int              `json:"total"`
}

type ragFlowDocument struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Location   string         `json:"location"`
	SourceType string         `json:"source_type"`
	MetaFields map[string]any `json:"meta_fields"`
	Metadata   map[string]any `json:"metadata"`
}

type ragFlowChunk struct {
	ID                string         `json:"id"`
	Content           string         `json:"content"`
	ContentWithWeight string         `json:"content_with_weight"`
	DocumentID        string         `json:"document_id"`
	DocID             string         `json:"doc_id"`
	DocName           string         `json:"docnm_kwd"`
	Available         *bool          `json:"available"`
	AvailableInt      *int           `json:"available_int"`
	ImportantKeywords []string       `json:"important_keywords"`
	ImportantKwd      []string       `json:"important_kwd"`
	Questions         []string       `json:"questions"`
	QuestionKwd       []string       `json:"question_kwd"`
	Tags              []string       `json:"tag_kwd"`
	MetaFields        map[string]any `json:"meta_fields"`
	Metadata          map[string]any `json:"metadata"`
}

type ragFlowExportCitationFile struct {
	Source      string                  `json:"source"`
	DatasetID   string                  `json:"dataset_id"`
	GeneratedAt string                  `json:"generated_at"`
	Citations   []ragFlowExportCitation `json:"citations"`
}

type ragFlowExportCitation struct {
	ChunkID           string   `json:"chunk_id"`
	Title             string   `json:"title"`
	URL               string   `json:"url"`
	Source            string   `json:"source"`
	SourceFamily      string   `json:"source_family"`
	License           string   `json:"license"`
	ReviewStatus      string   `json:"review_status"`
	RAGFlowDatasetID  string   `json:"ragflow_dataset_id"`
	RAGFlowDocumentID string   `json:"ragflow_document_id"`
	RAGFlowChunkID    string   `json:"ragflow_chunk_id"`
	ImportantKeywords []string `json:"important_keywords,omitempty"`
	Questions         []string `json:"questions,omitempty"`
	Tags              []string `json:"tags,omitempty"`
}

type preparedRAGFlowExport struct {
	KBID          string
	Version       string
	DatasetID     string
	BuildPayload  buildPublishRequest
	PackageBytes  []byte
	Manifest      []byte
	CitationCount int
}

type ragFlowQualityReport struct {
	Status  string                    `json:"status"`
	Checks  []ragFlowQualityCheck     `json:"checks"`
	Metrics map[string]int            `json:"metrics"`
	Sources map[string]map[string]int `json:"sources,omitempty"`
}

type ragFlowQualityCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func newRAGFlowClient(options RAGFlowOptions) (*ragFlowClient, error) {
	baseURL := strings.TrimSpace(options.BaseURL)
	apiKey := strings.TrimSpace(options.APIKey)
	if baseURL == "" && apiKey == "" {
		return nil, nil
	}
	if baseURL == "" {
		return nil, fmt.Errorf("ragflow base url is required when RAGFlow API key is configured")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ragflow api key is required when RAGFlow base url is configured")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid ragflow base url")
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultRAGFlowTimeout
	}
	pageSize := options.PageSize
	if pageSize <= 0 {
		pageSize = defaultRAGFlowPageSize
	}
	if pageSize > maxRAGFlowPageSize {
		pageSize = maxRAGFlowPageSize
	}

	return &ragFlowClient{
		baseURL:    parsed,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
		pageSize:   pageSize,
	}, nil
}

func (h *Handler) handleRAGFlowExportDryRun(w http.ResponseWriter, r *http.Request) {
	prepared, status, err := h.prepareRAGFlowExport(r, "/ragflow/export-dry-run")
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	digest := sha256.Sum256(prepared.PackageBytes)
	qualityReport := ragFlowQualityReportFor(prepared)
	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":          prepared.KBID,
		"version":        prepared.Version,
		"latest":         false,
		"dataset_id":     prepared.DatasetID,
		"chunk_count":    len(prepared.BuildPayload.Chunks),
		"citation_count": prepared.CitationCount,
		"package_hash":   "sha256:" + hex.EncodeToString(digest[:]),
		"quality_status": qualityReport.Status,
		"quality_report": qualityReport,
	})
}

func (h *Handler) handleRAGFlowExportPublish(w http.ResponseWriter, r *http.Request) {
	prepared, status, err := h.prepareRAGFlowExport(r, "/ragflow/export-publish")
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if err := h.storePublishedVersion(prepared.KBID, prepared.Version, prepared.Manifest, bytes.NewReader(prepared.PackageBytes)); err != nil {
		writePublishError(w, err)
		return
	}
	qualityReport := ragFlowQualityReportFor(prepared)

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":          prepared.KBID,
		"version":        prepared.Version,
		"latest":         true,
		"dataset_id":     prepared.DatasetID,
		"chunk_count":    len(prepared.BuildPayload.Chunks),
		"citation_count": prepared.CitationCount,
		"source":         defaultRAGFlowExportSource,
		"quality_status": qualityReport.Status,
		"quality_report": qualityReport,
	})
}

func (h *Handler) prepareRAGFlowExport(r *http.Request, suffix string) (preparedRAGFlowExport, int, error) {
	if !h.authorized(r) {
		return preparedRAGFlowExport{}, http.StatusUnauthorized, fmt.Errorf("unauthorized")
	}
	if len(h.knowledgePackSigningSeed) == 0 {
		return preparedRAGFlowExport{}, http.StatusServiceUnavailable, fmt.Errorf("knowledge pack signing key is not configured")
	}
	if h.ragFlow == nil {
		return preparedRAGFlowExport{}, http.StatusServiceUnavailable, fmt.Errorf("ragflow client is not configured")
	}

	kbID, ok := strings.CutPrefix(r.URL.Path, "/admin/api/kb/")
	if !ok {
		return preparedRAGFlowExport{}, http.StatusNotFound, fmt.Errorf("not found")
	}
	kbID, ok = strings.CutSuffix(kbID, suffix)
	if !ok {
		return preparedRAGFlowExport{}, http.StatusNotFound, fmt.Errorf("not found")
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, err
	}

	var payload ragFlowExportPublishRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&payload); err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, fmt.Errorf("invalid json body")
	}
	version, err := safeComponent(payload.Version)
	if err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, fmt.Errorf("invalid version")
	}
	datasetID, err := safeComponent(payload.DatasetID)
	if err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, fmt.Errorf("invalid dataset_id")
	}

	dataset, err := h.ragFlow.exportDataset(r.Context(), datasetID)
	if err != nil {
		return preparedRAGFlowExport{}, http.StatusBadGateway, err
	}
	buildPayload, citationCount, err := ragFlowDatasetBuildPayload(datasetID, dataset, payload)
	if err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, err
	}
	buildPayload.Version = version
	buildPayload.LLMRecommended = payload.LLMRecommended

	if err := validateBuildPublishBoundary(kbID, buildPayload); err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, err
	}

	packageBytes, manifest, err := buildKnowledgePack(kbID, version, buildPayload, h.knowledgePackSigningSeed)
	if err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, err
	}
	if err := auditKnowledgePackBeforePublish(manifest, packageBytes); err != nil {
		return preparedRAGFlowExport{}, http.StatusBadRequest, err
	}
	return preparedRAGFlowExport{
		KBID:          kbID,
		Version:       version,
		DatasetID:     datasetID,
		BuildPayload:  buildPayload,
		PackageBytes:  packageBytes,
		Manifest:      manifest,
		CitationCount: citationCount,
	}, http.StatusOK, nil
}

type ragFlowExportDataset struct {
	Documents []ragFlowDocument
	Chunks    map[string][]ragFlowChunk
}

func (c *ragFlowClient) exportDataset(ctx context.Context, datasetID string) (ragFlowExportDataset, error) {
	documents, err := c.listDocuments(ctx, datasetID)
	if err != nil {
		return ragFlowExportDataset{}, err
	}
	if len(documents) == 0 {
		return ragFlowExportDataset{}, fmt.Errorf("ragflow dataset %s has no documents", datasetID)
	}
	chunksByDocument := make(map[string][]ragFlowChunk, len(documents))
	for _, document := range documents {
		documentID := strings.TrimSpace(document.ID)
		if documentID == "" {
			return ragFlowExportDataset{}, fmt.Errorf("ragflow document id is required")
		}
		chunks, err := c.listChunks(ctx, datasetID, documentID)
		if err != nil {
			return ragFlowExportDataset{}, err
		}
		chunksByDocument[documentID] = chunks
	}
	return ragFlowExportDataset{Documents: documents, Chunks: chunksByDocument}, nil
}

func (c *ragFlowClient) listDocuments(ctx context.Context, datasetID string) ([]ragFlowDocument, error) {
	result := []ragFlowDocument{}
	for page := 1; page <= 100; page++ {
		var response ragFlowAPIResponse[ragFlowDocumentsData]
		if err := c.get(ctx, "/api/v1/datasets/"+url.PathEscape(datasetID)+"/documents", pageQuery(page, c.pageSize), &response); err != nil {
			return nil, err
		}
		if response.Code != 0 {
			return nil, fmt.Errorf("ragflow list documents failed: %s", response.Message)
		}
		documents := response.Data.Docs
		if len(documents) == 0 {
			documents = response.Data.Documents
		}
		result = append(result, documents...)
		if pageDone(len(documents), len(result), response.Data.Total, c.pageSize) {
			return result, nil
		}
	}
	return nil, fmt.Errorf("ragflow list documents exceeded pagination limit")
}

func (c *ragFlowClient) listChunks(ctx context.Context, datasetID string, documentID string) ([]ragFlowChunk, error) {
	result := []ragFlowChunk{}
	for page := 1; page <= 100; page++ {
		var response ragFlowAPIResponse[ragFlowChunksData]
		path := "/api/v1/datasets/" + url.PathEscape(datasetID) + "/documents/" + url.PathEscape(documentID) + "/chunks"
		if err := c.get(ctx, path, pageQuery(page, c.pageSize), &response); err != nil {
			return nil, err
		}
		if response.Code != 0 {
			return nil, fmt.Errorf("ragflow list chunks failed for document %s: %s", documentID, response.Message)
		}
		result = append(result, response.Data.Chunks...)
		if pageDone(len(response.Data.Chunks), len(result), response.Data.Total, c.pageSize) {
			return result, nil
		}
	}
	return nil, fmt.Errorf("ragflow list chunks exceeded pagination limit for document %s", documentID)
}

func (c *ragFlowClient) get(ctx context.Context, path string, query url.Values, target any) error {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("ragflow request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("ragflow status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode ragflow response: %w", err)
	}
	return nil
}

func ragFlowDatasetBuildPayload(datasetID string, dataset ragFlowExportDataset, request ragFlowExportPublishRequest) (buildPublishRequest, int, error) {
	buildChunks := []knowledgePackBuildChunk{}
	prompts := append([]knowledgePackBuildPrompt{}, request.Prompts...)
	citations := []ragFlowExportCitation{}
	seen := map[string]bool{}

	for _, document := range dataset.Documents {
		documentID := strings.TrimSpace(document.ID)
		for _, chunk := range dataset.Chunks[documentID] {
			if !ragFlowChunkAvailable(chunk) {
				continue
			}
			buildChunk, citation, chunkPrompts, err := ragFlowBuildChunk(datasetID, document, chunk)
			if err != nil {
				return buildPublishRequest{}, 0, err
			}
			if seen[buildChunk.ChunkID] {
				return buildPublishRequest{}, 0, fmt.Errorf("ragflow chunk id duplicates exported chunk_id %s", buildChunk.ChunkID)
			}
			seen[buildChunk.ChunkID] = true
			buildChunks = append(buildChunks, buildChunk)
			citations = append(citations, citation)
			prompts = append(prompts, chunkPrompts...)
		}
	}
	if len(buildChunks) == 0 {
		return buildPublishRequest{}, 0, fmt.Errorf("ragflow dataset %s has no exportable reviewed chunks", datasetID)
	}

	citationFile := ragFlowExportCitationFile{
		Source:      defaultRAGFlowExportSource,
		DatasetID:   datasetID,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Citations:   citations,
	}
	citationsData, err := json.Marshal(citationFile)
	if err != nil {
		return buildPublishRequest{}, 0, fmt.Errorf("encode ragflow citations: %w", err)
	}

	return buildPublishRequest{
		Chunks:    buildChunks,
		Prompts:   prompts,
		Citations: citationsData,
	}, len(citations), nil
}

func ragFlowQualityReportFor(prepared preparedRAGFlowExport) ragFlowQualityReport {
	return ragFlowQualityReport{
		Status: "passed",
		Checks: []ragFlowQualityCheck{
			{Name: "required_chunk_fields", Status: "passed"},
			{Name: "citation_metadata", Status: "passed"},
			{Name: "duplicate_chunk_ids", Status: "passed"},
			{Name: "source_boundary", Status: "passed"},
			{Name: "package_audit", Status: "passed"},
		},
		Metrics: map[string]int{
			"chunk_count":    len(prepared.BuildPayload.Chunks),
			"citation_count": prepared.CitationCount,
			"prompt_count":   len(prepared.BuildPayload.Prompts),
		},
		Sources: map[string]map[string]int{
			"chunks": ragFlowChunkSources(prepared.BuildPayload.Chunks),
		},
	}
}

func ragFlowChunkSources(chunks []knowledgePackBuildChunk) map[string]int {
	result := map[string]int{}
	for _, chunk := range chunks {
		source := strings.TrimSpace(chunk.Source)
		if source == "" {
			source = "unknown"
		}
		result[source]++
	}
	return result
}

func ragFlowBuildChunk(datasetID string, document ragFlowDocument, chunk ragFlowChunk) (knowledgePackBuildChunk, ragFlowExportCitation, []knowledgePackBuildPrompt, error) {
	metadata := mergedRAGFlowMetadata(document, chunk)
	chunkID := strings.TrimSpace(chunk.ID)
	content := firstNonEmpty(chunk.Content, chunk.ContentWithWeight)
	if chunkID == "" {
		return knowledgePackBuildChunk{}, ragFlowExportCitation{}, nil, fmt.Errorf("ragflow chunk id is required")
	}
	if strings.TrimSpace(content) == "" {
		return knowledgePackBuildChunk{}, ragFlowExportCitation{}, nil, fmt.Errorf("ragflow chunk %s content is required", chunkID)
	}

	reviewStatus := strings.ToLower(firstNonEmpty(metadata["review_status"], metadata["reviewed"], metadata["status"]))
	if reviewStatus != "reviewed" && reviewStatus != "true" {
		return knowledgePackBuildChunk{}, ragFlowExportCitation{}, nil, fmt.Errorf("ragflow chunk %s review_status must be reviewed", chunkID)
	}
	sourceFamily := firstNonEmpty(metadata["source_family"], metadata["source"], document.SourceType)
	sourceURL := firstNonEmpty(metadata["source_url"], metadata["url"])
	license := firstNonEmpty(metadata["license"])
	if sourceFamily == "" {
		return knowledgePackBuildChunk{}, ragFlowExportCitation{}, nil, fmt.Errorf("ragflow chunk %s source_family is required", chunkID)
	}
	if sourceURL == "" {
		return knowledgePackBuildChunk{}, ragFlowExportCitation{}, nil, fmt.Errorf("ragflow chunk %s source_url is required", chunkID)
	}
	if license == "" {
		return knowledgePackBuildChunk{}, ragFlowExportCitation{}, nil, fmt.Errorf("ragflow chunk %s license is required", chunkID)
	}

	title := firstNonEmpty(metadata["title"], document.Name, chunk.DocName, document.Location, chunkID)
	documentPath := firstNonEmpty(metadata["document_path"], metadata["path"], document.Location, document.Name, document.ID)
	importantKeywords := uniqueStrings(append(chunk.ImportantKeywords, chunk.ImportantKwd...), 12)
	questions := uniqueStrings(append(chunk.Questions, chunk.QuestionKwd...), 12)
	tags := uniqueStrings(chunk.Tags, 12)

	contentParts := []string{strings.TrimSpace(content)}
	if len(importantKeywords) > 0 {
		contentParts = append(contentParts, "【关键词】"+strings.Join(importantKeywords, "、"))
	}
	if len(questions) > 0 {
		contentParts = append(contentParts, "【问题】"+strings.Join(questions, "；"))
	}
	contentParts = append(contentParts, "【来源】"+sourceURL, "【许可】"+license)

	exportedChunkID := ragFlowExportChunkID(datasetID, chunkID)
	buildChunk := knowledgePackBuildChunk{
		ChunkID: exportedChunkID,
		Title:   title,
		Path:    documentPath,
		Source:  "ragflow:" + sourceFamily,
		Content: strings.Join(contentParts, "\n"),
	}
	citation := ragFlowExportCitation{
		ChunkID:           exportedChunkID,
		Title:             title,
		URL:               sourceURL,
		Source:            "ragflow:" + sourceFamily,
		SourceFamily:      sourceFamily,
		License:           license,
		ReviewStatus:      "reviewed",
		RAGFlowDatasetID:  datasetID,
		RAGFlowDocumentID: firstNonEmpty(chunk.DocumentID, chunk.DocID, document.ID),
		RAGFlowChunkID:    chunkID,
		ImportantKeywords: importantKeywords,
		Questions:         questions,
		Tags:              tags,
	}
	prompts := make([]knowledgePackBuildPrompt, 0, len(questions))
	for index, question := range questions {
		prompts = append(prompts, knowledgePackBuildPrompt{
			ID:       slugComponent(exportedChunkID) + "-q-" + fmt.Sprintf("%02d", index+1),
			Title:    title,
			Question: question,
		})
	}
	return buildChunk, citation, prompts, nil
}

func ragFlowChunkAvailable(chunk ragFlowChunk) bool {
	if chunk.Available != nil {
		return *chunk.Available
	}
	if chunk.AvailableInt != nil {
		return *chunk.AvailableInt != 0
	}
	return true
}

func mergedRAGFlowMetadata(document ragFlowDocument, chunk ragFlowChunk) map[string]string {
	result := map[string]string{}
	copyRAGFlowMetadata(result, document.MetaFields)
	copyRAGFlowMetadata(result, document.Metadata)
	copyRAGFlowMetadata(result, chunk.MetaFields)
	copyRAGFlowMetadata(result, chunk.Metadata)
	return result
}

func copyRAGFlowMetadata(target map[string]string, source map[string]any) {
	for key, value := range source {
		key = strings.TrimSpace(strings.ToLower(key))
		if key == "" {
			continue
		}
		if text := metadataText(value); text != "" {
			target[key] = text
		}
	}
}

func metadataText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func ragFlowExportChunkID(datasetID string, chunkID string) string {
	return "ragflow:" + datasetID + ":" + strings.TrimSpace(chunkID)
}

func pageQuery(page int, pageSize int) url.Values {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", page))
	query.Set("page_size", fmt.Sprintf("%d", pageSize))
	return query
}

func pageDone(pageItems int, totalItems int, remoteTotal int, pageSize int) bool {
	if pageItems == 0 {
		return true
	}
	if remoteTotal > 0 && totalItems >= remoteTotal {
		return true
	}
	return pageItems < pageSize
}
