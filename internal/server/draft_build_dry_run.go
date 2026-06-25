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
	"os"
	"path/filepath"
	"time"
)

type preparedDraftPackage struct {
	BuildPayload  buildPublishRequest
	PackageBytes  []byte
	ManifestBytes []byte
	Manifest      knowledgePackBuildManifest
	Files         []knowledgePackPreviewFile
	CitationCount int
	PackageHash   string
}

type draftDryRunRecord struct {
	KBID           string `json:"kb_id"`
	Version        string `json:"version"`
	ContentHash    string `json:"content_hash"`
	QualityStatus  string `json:"quality_status"`
	ChunkCount     int    `json:"chunk_count"`
	CitationCount  int    `json:"citation_count"`
	PromptCount    int    `json:"prompt_count"`
	DraftUpdatedAt string `json:"draft_updated_at,omitempty"`
	CreatedAt      string `json:"created_at"`
}

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

	prepared, err := h.prepareDraftPackage(draft)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.writeDraftDryRunRecord(draftDryRunRecord{
		KBID:           draft.KBID,
		Version:        draft.Version,
		ContentHash:    prepared.PackageHash,
		QualityStatus:  qualityReport.Status,
		ChunkCount:     len(draft.Chunks),
		CitationCount:  prepared.CitationCount,
		PromptCount:    len(draft.Prompts),
		DraftUpdatedAt: draft.UpdatedAt,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		http.Error(w, "write dry-run record failed", http.StatusInternalServerError)
		return
	}

	limit := previewLimit(r)
	preview := draftDryRunPreview(draft, limit)
	preview.Files = prepared.Files

	writeJSON(w, http.StatusOK, draftBuildDryRunResponse{
		KBID:          draft.KBID,
		Version:       draft.Version,
		Latest:        false,
		ChunkCount:    len(draft.Chunks),
		CitationCount: prepared.CitationCount,
		PromptCount:   len(draft.Prompts),
		PackageHash:   prepared.PackageHash,
		QualityStatus: qualityReport.Status,
		QualityReport: qualityReport,
		Manifest:      prepared.Manifest,
		Files:         prepared.Files,
		PreviewURL:    fmt.Sprintf("/admin/api/kb/%s/drafts/%s/build-dry-run?limit=%d", draft.KBID, draft.Version, limit),
		Preview:       preview,
	})
}

func (h *Handler) prepareDraftPackage(draft draftDocument) (preparedDraftPackage, error) {
	buildPayload := buildPublishRequest{
		Version:   draft.Version,
		Chunks:    draft.Chunks,
		Prompts:   draft.Prompts,
		Citations: draftCitationsForBuild(draft.Citations),
	}
	if err := validateBuildPublishBoundary(draft.KBID, buildPayload); err != nil {
		return preparedDraftPackage{}, err
	}

	packageBytes, manifestBytes, err := buildKnowledgePack(draft.KBID, draft.Version, buildPayload, h.knowledgePackSigningSeed)
	if err != nil {
		return preparedDraftPackage{}, err
	}
	if err := auditKnowledgePackBeforePublish(manifestBytes, packageBytes); err != nil {
		return preparedDraftPackage{}, err
	}

	var manifest knowledgePackBuildManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return preparedDraftPackage{}, fmt.Errorf("decode dry-run manifest failed: %w", err)
	}
	files, citationCount, err := draftDryRunPackageDetails(manifestBytes, packageBytes)
	if err != nil {
		return preparedDraftPackage{}, err
	}
	digest := sha256.Sum256(packageBytes)
	return preparedDraftPackage{
		BuildPayload:  buildPayload,
		PackageBytes:  packageBytes,
		ManifestBytes: manifestBytes,
		Manifest:      manifest,
		Files:         files,
		CitationCount: citationCount,
		PackageHash:   "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
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

func (h *Handler) handleDraftPublish(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if len(h.knowledgePackSigningSeed) == 0 {
		http.Error(w, "knowledge pack signing key is not configured", http.StatusServiceUnavailable)
		return
	}

	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/publish")
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
			"error":          "quality gates failed: fix P0 checks before publish",
			"quality_status": qualityReport.Status,
			"quality_report": qualityReport,
		})
		return
	}

	prepared, err := h.prepareDraftPackage(draft)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record, err := h.readDraftDryRunRecord(draft.KBID, draft.Version)
	if err != nil {
		http.Error(w, "successful dry-run build required before publish", http.StatusConflict)
		return
	}
	if record.QualityStatus != "passed" || record.ContentHash == "" {
		http.Error(w, "successful dry-run build required before publish", http.StatusConflict)
		return
	}
	if record.ContentHash != prepared.PackageHash {
		http.Error(w, "dry-run content hash mismatch: rerun dry-run build before publish", http.StatusConflict)
		return
	}

	if err := h.storePublishedVersion(draft.KBID, draft.Version, prepared.ManifestBytes, bytes.NewReader(prepared.PackageBytes)); err != nil {
		writePublishError(w, err)
		return
	}
	publishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.appendAuditLog(draft.KBID, map[string]any{
		"event":        "draft_publish",
		"version":      draft.Version,
		"content_hash": prepared.PackageHash,
		"gate_status":  qualityReport.Status,
		"chunk_count":  len(draft.Chunks),
		"prompt_count": len(draft.Prompts),
		"published_at": publishedAt,
		"actor":        "admin",
	}); err != nil {
		http.Error(w, "write audit log failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":          draft.KBID,
		"version":        draft.Version,
		"latest":         true,
		"chunk_count":    len(draft.Chunks),
		"citation_count": prepared.CitationCount,
		"prompt_count":   len(draft.Prompts),
		"content_hash":   prepared.PackageHash,
		"gate_status":    qualityReport.Status,
		"published_at":   publishedAt,
	})
}

func (h *Handler) writeDraftDryRunRecord(record draftDryRunRecord) error {
	draftDir := h.draftDir(record.KBID, record.Version)
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(draftDir, "dry-run.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (h *Handler) readDraftDryRunRecord(kbID string, version string) (draftDryRunRecord, error) {
	data, err := os.ReadFile(filepath.Join(h.draftDir(kbID, version), "dry-run.json"))
	if err != nil {
		return draftDryRunRecord{}, err
	}
	var record draftDryRunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return draftDryRunRecord{}, fmt.Errorf("decode dry-run record: %w", err)
	}
	return record, nil
}

func (h *Handler) appendAuditLog(kbID string, event map[string]any) error {
	kbDir := h.kbDir(kbID)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		return err
	}
	event["kb_id"] = kbID
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(filepath.Join(kbDir, "audit.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func (h *Handler) publishedVersionContentHash(kbID string, version string) string {
	data, err := os.ReadFile(filepath.Join(h.versionDir(kbID, version), "manifest.json"))
	if err != nil {
		return ""
	}
	var manifest struct {
		ContentHash string `json:"content_hash"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ""
	}
	return manifest.ContentHash
}
