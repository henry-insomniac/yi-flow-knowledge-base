package server

import (
	"bytes"
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

func (h *Handler) handleWeKnoraExportPublish(w http.ResponseWriter, r *http.Request) {
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
	kbID, ok = strings.CutSuffix(kbID, "/weknora/export-publish")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var payload weknoraExportPublishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	version, err := safeComponent(payload.Version)
	if err != nil {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}

	buildPayload, citationCount, err := weknoraExportBuildPayload(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	buildPayload.Version = version
	buildPayload.LLMRecommended = payload.LLMRecommended

	packageBytes, manifest, err := buildKnowledgePack(kbID, version, buildPayload, h.knowledgePackSigningSeed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.storePublishedVersion(kbID, version, manifest, bytes.NewReader(packageBytes)); err != nil {
		writePublishError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":          kbID,
		"version":        version,
		"latest":         true,
		"chunk_count":    len(buildPayload.Chunks),
		"citation_count": citationCount,
		"source":         resolvedWeKnoraExportSource(payload),
	})
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
