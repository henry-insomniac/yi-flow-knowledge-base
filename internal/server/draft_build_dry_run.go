package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type draftBuildDryRunResponse struct {
	KBID          string                     `json:"kb_id"`
	Version       string                     `json:"version"`
	Latest        bool                       `json:"latest"`
	ChunkCount    int                        `json:"chunk_count"`
	CitationCount int                        `json:"citation_count"`
	PromptCount   int                        `json:"prompt_count"`
	PackageHash   string                     `json:"package_hash"`
	QualityStatus string                     `json:"quality_status"`
	QualityReport draftQualityReport         `json:"quality_report"`
	Manifest      knowledgePackBuildManifest `json:"manifest"`
	Files         []knowledgePackPreviewFile `json:"files"`
	PreviewURL    string                     `json:"preview_url"`
	Preview       knowledgePackPreview       `json:"preview"`
}

func (h *Handler) handleDraftBuildDryRun(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if len(h.knowledgePackSigningSeed) == 0 {
		http.Error(w, "knowledge pack signing key is not configured", http.StatusServiceUnavailable)
		return
	}

	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/build-dry-run")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	draft, err := h.readDraft(kbID, version)
	if err != nil {
		writeDraftReadError(w, r, err)
		return
	}

	qualityReport := evaluateDraftQuality(draft)
	if qualityReport.BlockPublish {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":          "quality gates failed: fix P0 checks before dry-run build",
			"quality_status": qualityReport.Status,
			"quality_report": qualityReport,
		})
		return
	}

	buildPayload := buildPublishRequest{
		Version:   draft.Version,
		Chunks:    draft.Chunks,
		Prompts:   draft.Prompts,
		Citations: draftCitationsForBuild(draft.Citations),
	}
	if err := validateBuildPublishBoundary(draft.KBID, buildPayload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	packageBytes, manifestBytes, err := buildKnowledgePack(draft.KBID, draft.Version, buildPayload, h.knowledgePackSigningSeed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := auditKnowledgePackBeforePublish(manifestBytes, packageBytes); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var manifest knowledgePackBuildManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		http.Error(w, "decode dry-run manifest failed", http.StatusInternalServerError)
		return
	}
	files, citationCount, err := draftDryRunPackageDetails(manifestBytes, packageBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	limit := previewLimit(r)
	preview := draftDryRunPreview(draft, limit)
	preview.Files = files
	digest := sha256.Sum256(packageBytes)

	writeJSON(w, http.StatusOK, draftBuildDryRunResponse{
		KBID:          draft.KBID,
		Version:       draft.Version,
		Latest:        false,
		ChunkCount:    len(draft.Chunks),
		CitationCount: citationCount,
		PromptCount:   len(draft.Prompts),
		PackageHash:   "sha256:" + hex.EncodeToString(digest[:]),
		QualityStatus: qualityReport.Status,
		QualityReport: qualityReport,
		Manifest:      manifest,
		Files:         files,
		PreviewURL:    fmt.Sprintf("/admin/api/kb/%s/drafts/%s/build-dry-run?limit=%d", draft.KBID, draft.Version, limit),
		Preview:       preview,
	})
}

func draftCitationsForBuild(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	var citationFile struct {
		Citations     []json.RawMessage `json:"citations"`
		CrawlManifest []json.RawMessage `json:"crawl_manifest"`
	}
	if err := json.Unmarshal(trimmed, &citationFile); err == nil && len(citationFile.Citations) == 0 && len(citationFile.CrawlManifest) == 0 {
		return nil
	}
	return raw
}

func draftDryRunPackageDetails(manifestBytes []byte, packageBytes []byte) ([]knowledgePackPreviewFile, int, error) {
	reader, err := zip.NewReader(bytes.NewReader(packageBytes), int64(len(packageBytes)))
	if err != nil {
		return nil, 0, fmt.Errorf("open dry-run package: %w", err)
	}

	files := []knowledgePackPreviewFile{
		{Path: "manifest.json", Size: uint64(len(manifestBytes))},
		{Path: "knowledge-pack.zip", Size: uint64(len(packageBytes))},
	}
	citationCount := 0
	for _, file := range reader.File {
		files = append(files, knowledgePackPreviewFile{
			Path: file.Name,
			Size: file.UncompressedSize64,
		})
		if file.Name != "citations.json" {
			continue
		}
		count, err := draftDryRunCitationCount(file)
		if err != nil {
			return nil, 0, err
		}
		citationCount = count
	}
	return files, citationCount, nil
}

func draftDryRunCitationCount(file *zip.File) (int, error) {
	body, err := file.Open()
	if err != nil {
		return 0, fmt.Errorf("open dry-run citations: %w", err)
	}
	defer body.Close()

	data, err := io.ReadAll(io.LimitReader(body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("read dry-run citations: %w", err)
	}
	var citationFile struct {
		Citations []json.RawMessage `json:"citations"`
	}
	if err := json.Unmarshal(data, &citationFile); err != nil {
		return 0, fmt.Errorf("decode dry-run citations: %w", err)
	}
	return len(citationFile.Citations), nil
}

func draftDryRunPreview(draft draftDocument, limit int) knowledgePackPreview {
	if limit > maxPreviewChunkLimit {
		limit = maxPreviewChunkLimit
	}
	if limit < 1 {
		limit = defaultPreviewChunkLimit
	}
	questions := draftSuggestedQuestions(draft.Prompts)
	chunks := make([]knowledgePackPreviewChunk, 0, min(limit, len(draft.Chunks)))
	for index, chunk := range draft.Chunks {
		if index >= limit {
			break
		}
		chunks = append(chunks, knowledgePackPreviewChunk{
			ChunkID:            chunk.ChunkID,
			Title:              chunk.Title,
			Path:               chunk.Path,
			Source:             chunk.Source,
			SourceURL:          chunk.CitationURL,
			CitationTitle:      chunk.CitationTitle,
			SourceName:         chunk.SourceName,
			License:            chunk.License,
			SourcePolicy:       chunk.SourcePolicy,
			RevisionID:         chunk.SourceRevisionID,
			SourcePageID:       chunk.SourcePageID,
			Content:            truncateRunes(chunk.Content, maxPreviewContentRunes),
			SuggestedQuestions: questions,
		})
	}
	return knowledgePackPreview{
		KBID:    draft.KBID,
		Version: draft.Version,
		Latest:  false,
		Chunks:  chunks,
	}
}
