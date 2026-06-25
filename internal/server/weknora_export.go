package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultWeKnoraExportSource       = "Tencent WeKnora"
	defaultWeKnoraExportLicense      = "reviewed internal knowledge"
	defaultWeKnoraExportSourcePolicy = "reviewed chunks only; preserve source URL and license; no unreviewed full-article mirror"
)

type weknoraExportPublishRequest struct {
	Version        string                     `json:"version"`
	Chunks         []weknoraExportChunk       `json:"chunks"`
	Prompts        []knowledgePackBuildPrompt `json:"prompts"`
	LLMRecommended []string                   `json:"llm_recommended"`
	Source         string                     `json:"source"`
	License        string                     `json:"license"`
	SourcePolicy   string                     `json:"source_policy"`
}

type weknoraExportChunk struct {
	ID                string                 `json:"id"`
	Content           string                 `json:"content"`
	KnowledgeID       string                 `json:"knowledge_id"`
	KnowledgeTitle    string                 `json:"knowledge_title"`
	KnowledgeFilename string                 `json:"knowledge_filename"`
	KnowledgeSource   string                 `json:"knowledge_source"`
	URL               string                 `json:"url"`
	Score             float64                `json:"score"`
	Metadata          map[string]interface{} `json:"metadata"`
	Reviewed          bool                   `json:"reviewed"`
	License           string                 `json:"license"`
	SourcePolicy      string                 `json:"source_policy"`
}

type weknoraExportCitationFile struct {
	Source       string                  `json:"source"`
	License      string                  `json:"license"`
	SourcePolicy string                  `json:"source_policy"`
	GeneratedAt  string                  `json:"generated_at"`
	Citations    []weknoraExportCitation `json:"citations"`
}

type weknoraExportCitation struct {
	ChunkID      string  `json:"chunk_id"`
	Title        string  `json:"title"`
	URL          string  `json:"url,omitempty"`
	Source       string  `json:"source"`
	License      string  `json:"license"`
	SourcePolicy string  `json:"source_policy"`
	WeKnoraID    string  `json:"weknora_id"`
	KnowledgeID  string  `json:"knowledge_id,omitempty"`
	Score        float64 `json:"score,omitempty"`
	ReviewStatus string  `json:"review_status"`
}

type preparedWeKnoraExport struct {
	KBID          string
	Version       string
	BuildPayload  buildPublishRequest
	PackageBytes  []byte
	Manifest      []byte
	CitationCount int
}

type weknoraQualityReport struct {
	Status  string                    `json:"status"`
	Checks  []weknoraQualityCheck     `json:"checks"`
	Metrics map[string]int            `json:"metrics"`
	Sources map[string]map[string]int `json:"sources,omitempty"`
}

type weknoraQualityCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (h *Handler) handleWeKnoraExportDryRun(w http.ResponseWriter, r *http.Request) {
	prepared, status, err := h.prepareWeKnoraExport(r, "/weknora/export-dry-run")
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	digest := sha256.Sum256(prepared.PackageBytes)
	qualityReport := weknoraQualityReportFor(prepared)
	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":          prepared.KBID,
		"version":        prepared.Version,
		"latest":         false,
		"chunk_count":    len(prepared.BuildPayload.Chunks),
		"citation_count": prepared.CitationCount,
		"package_hash":   "sha256:" + hex.EncodeToString(digest[:]),
		"quality_status": qualityReport.Status,
		"quality_report": qualityReport,
	})
}

func (h *Handler) handleWeKnoraExportPublish(w http.ResponseWriter, r *http.Request) {
	prepared, status, err := h.prepareWeKnoraExport(r, "/weknora/export-publish")
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if err := h.storePublishedVersion(prepared.KBID, prepared.Version, prepared.Manifest, bytes.NewReader(prepared.PackageBytes)); err != nil {
		writePublishError(w, err)
		return
	}
	qualityReport := weknoraQualityReportFor(prepared)
	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":          prepared.KBID,
		"version":        prepared.Version,
		"latest":         true,
		"chunk_count":    len(prepared.BuildPayload.Chunks),
		"citation_count": prepared.CitationCount,
		"source":         defaultWeKnoraExportSource,
		"quality_status": qualityReport.Status,
		"quality_report": qualityReport,
	})
}

func (h *Handler) prepareWeKnoraExport(r *http.Request, suffix string) (preparedWeKnoraExport, int, error) {
	if !h.authorized(r) {
		return preparedWeKnoraExport{}, http.StatusUnauthorized, fmt.Errorf("unauthorized")
	}
	if len(h.knowledgePackSigningSeed) == 0 {
		return preparedWeKnoraExport{}, http.StatusServiceUnavailable, fmt.Errorf("knowledge pack signing key is not configured")
	}

	kbID, ok := strings.CutPrefix(r.URL.Path, "/admin/api/kb/")
	if !ok {
		return preparedWeKnoraExport{}, http.StatusNotFound, fmt.Errorf("not found")
	}
	kbID, ok = strings.CutSuffix(kbID, suffix)
	if !ok {
		return preparedWeKnoraExport{}, http.StatusNotFound, fmt.Errorf("not found")
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		return preparedWeKnoraExport{}, http.StatusBadRequest, err
	}

	var payload weknoraExportPublishRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&payload); err != nil {
		return preparedWeKnoraExport{}, http.StatusBadRequest, fmt.Errorf("invalid json body")
	}

	version, err := safeComponent(payload.Version)
	if err != nil {
		return preparedWeKnoraExport{}, http.StatusBadRequest, fmt.Errorf("invalid version")
	}

	buildPayload, citationCount, err := weknoraExportBuildPayload(payload)
	if err != nil {
		return preparedWeKnoraExport{}, http.StatusBadRequest, err
	}
	buildPayload.Version = version
	buildPayload.LLMRecommended = payload.LLMRecommended

	if err := validateBuildPublishBoundary(kbID, buildPayload); err != nil {
		return preparedWeKnoraExport{}, http.StatusBadRequest, err
	}

	packageBytes, manifest, err := buildKnowledgePack(kbID, version, buildPayload, h.knowledgePackSigningSeed)
	if err != nil {
		return preparedWeKnoraExport{}, http.StatusBadRequest, err
	}
	if err := auditKnowledgePackBeforePublish(manifest, packageBytes); err != nil {
		return preparedWeKnoraExport{}, http.StatusBadRequest, err
	}

	return preparedWeKnoraExport{
		KBID:          kbID,
		Version:       version,
		BuildPayload:  buildPayload,
		PackageBytes:  packageBytes,
		Manifest:      manifest,
		CitationCount: citationCount,
	}, http.StatusOK, nil
}

func weknoraQualityReportFor(prepared preparedWeKnoraExport) weknoraQualityReport {
	return weknoraQualityReport{
		Status: "passed",
		Checks: []weknoraQualityCheck{
			{Name: "review_status", Status: "passed"},
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
			"chunks": weknoraChunkSources(prepared.BuildPayload.Chunks),
		},
	}
}

func weknoraChunkSources(chunks []knowledgePackBuildChunk) map[string]int {
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

func weknoraExportBuildPayload(payload weknoraExportPublishRequest) (buildPublishRequest, int, error) {
	if len(payload.Chunks) == 0 {
		return buildPublishRequest{}, 0, fmt.Errorf("chunks must not be empty")
	}

	source := resolvedWeKnoraExportSource(payload)
	license := resolvedWeKnoraExportLicense(payload)
	sourcePolicy := resolvedWeKnoraExportSourcePolicy(payload)
	buildChunks := make([]knowledgePackBuildChunk, 0, len(payload.Chunks))
	citations := make([]weknoraExportCitation, 0, len(payload.Chunks))
	seen := map[string]bool{}

	for index, chunk := range payload.Chunks {
		if !chunk.Reviewed {
			return buildPublishRequest{}, 0, fmt.Errorf("chunks[%d].reviewed must be true before export", index)
		}
		buildChunk, citation, err := weknoraExportBuildChunk(index, chunk, source, license, sourcePolicy)
		if err != nil {
			return buildPublishRequest{}, 0, err
		}
		if seen[buildChunk.ChunkID] {
			return buildPublishRequest{}, 0, fmt.Errorf("chunks[%d].id duplicates exported chunk_id %s", index, buildChunk.ChunkID)
		}
		seen[buildChunk.ChunkID] = true
		buildChunks = append(buildChunks, buildChunk)
		citations = append(citations, citation)
	}

	citationFile := weknoraExportCitationFile{
		Source:       source,
		License:      license,
		SourcePolicy: sourcePolicy,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Citations:    citations,
	}
	citationsData, err := json.Marshal(citationFile)
	if err != nil {
		return buildPublishRequest{}, 0, fmt.Errorf("encode citations: %w", err)
	}

	return buildPublishRequest{
		Chunks:    buildChunks,
		Prompts:   payload.Prompts,
		Citations: citationsData,
	}, len(citations), nil
}

func weknoraExportBuildChunk(
	index int,
	chunk weknoraExportChunk,
	defaultSource string,
	defaultLicense string,
	defaultSourcePolicy string,
) (knowledgePackBuildChunk, weknoraExportCitation, error) {
	id := strings.TrimSpace(chunk.ID)
	content := strings.TrimSpace(chunk.Content)
	if id == "" {
		return knowledgePackBuildChunk{}, weknoraExportCitation{}, fmt.Errorf("chunks[%d].id is required", index)
	}
	if content == "" {
		return knowledgePackBuildChunk{}, weknoraExportCitation{}, fmt.Errorf("chunks[%d].content is required", index)
	}

	title := firstNonEmpty(chunk.KnowledgeTitle, chunk.KnowledgeFilename, chunk.KnowledgeID, id)
	path := firstNonEmpty(chunk.KnowledgeFilename, chunk.KnowledgeID, title, id)
	source := firstNonEmpty(chunk.KnowledgeSource, defaultSource)
	license := firstNonEmpty(chunk.License, defaultLicense)
	sourcePolicy := firstNonEmpty(chunk.SourcePolicy, defaultSourcePolicy)
	sourceURL := firstNonEmpty(chunk.URL, stringMetadata(chunk.Metadata, "url"), stringMetadata(chunk.Metadata, "source_url"))
	chunkID := weknoraExportChunkID(id)

	contentParts := []string{content}
	if sourceURL != "" {
		contentParts = append(contentParts, "【来源】"+sourceURL)
	}
	contentParts = append(
		contentParts,
		"【许可】"+license,
		"【导出策略】"+sourcePolicy,
	)

	buildChunk := knowledgePackBuildChunk{
		ChunkID: chunkID,
		Title:   title,
		Path:    "weknora/" + slugComponent(path),
		Source:  "weknora:" + source,
		Content: strings.Join(contentParts, "\n"),
	}
	citation := weknoraExportCitation{
		ChunkID:      chunkID,
		Title:        title,
		URL:          sourceURL,
		Source:       source,
		License:      license,
		SourcePolicy: sourcePolicy,
		WeKnoraID:    id,
		KnowledgeID:  strings.TrimSpace(chunk.KnowledgeID),
		Score:        chunk.Score,
		ReviewStatus: "reviewed",
	}
	return buildChunk, citation, nil
}

func weknoraExportChunkID(id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "weknora:") {
		return id
	}
	return "weknora:" + id
}

func resolvedWeKnoraExportSource(payload weknoraExportPublishRequest) string {
	return firstNonEmpty(payload.Source, defaultWeKnoraExportSource)
}

func resolvedWeKnoraExportLicense(payload weknoraExportPublishRequest) string {
	return firstNonEmpty(payload.License, defaultWeKnoraExportLicense)
}

func resolvedWeKnoraExportSourcePolicy(payload weknoraExportPublishRequest) string {
	return firstNonEmpty(payload.SourcePolicy, defaultWeKnoraExportSourcePolicy)
}
