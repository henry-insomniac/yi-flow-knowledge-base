package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const draftStatus = "draft"

var (
	errDraftChunkDuplicate = errors.New("duplicate chunk_id")
	errDraftChunkNotFound  = errors.New("draft chunk not found")
)

type draftDocument struct {
	KBID      string                     `json:"kb_id"`
	Version   string                     `json:"version"`
	Status    string                     `json:"status"`
	Chunks    []knowledgePackBuildChunk  `json:"chunks"`
	Prompts   []knowledgePackBuildPrompt `json:"prompts"`
	Citations json.RawMessage            `json:"citations"`
	CreatedAt string                     `json:"created_at"`
	UpdatedAt string                     `json:"updated_at"`
}

type draftSaveRequest struct {
	Chunks    []knowledgePackBuildChunk  `json:"chunks"`
	Prompts   []knowledgePackBuildPrompt `json:"prompts"`
	Citations json.RawMessage            `json:"citations"`
}

func (h *Handler) handleSaveDraft(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	var payload draftSaveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	draft, err := h.buildDraftDocument(kbID, version, payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.writeDraft(draft); err != nil {
		http.Error(w, "write draft failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":       draft.KBID,
		"version":     draft.Version,
		"status":      draft.Status,
		"chunk_count": len(draft.Chunks),
		"created_at":  draft.CreatedAt,
		"updated_at":  draft.UpdatedAt,
	})
}

func (h *Handler) handleGetDraft(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "")
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
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "read draft failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (h *Handler) handleDraftPreview(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/preview")
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
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "read draft failed", http.StatusInternalServerError)
		return
	}

	limit := previewLimit(r)
	chunks := make([]knowledgePackPreviewChunk, 0, min(limit, len(draft.Chunks)))
	questions := draftSuggestedQuestions(draft.Prompts)
	for index, chunk := range draft.Chunks {
		if index >= limit {
			break
		}
		chunks = append(chunks, knowledgePackPreviewChunk{
			ChunkID:            chunk.ChunkID,
			Title:              chunk.Title,
			Path:               chunk.Path,
			Source:             chunk.Source,
			Content:            truncateRunes(strings.TrimSpace(chunk.Content), maxPreviewContentRunes),
			SuggestedQuestions: questions,
		})
	}

	writeJSON(w, http.StatusOK, struct {
		KBID     string                      `json:"kb_id"`
		Version  string                      `json:"version"`
		Status   string                      `json:"status"`
		Latest   bool                        `json:"latest"`
		Chunks   []knowledgePackPreviewChunk `json:"chunks"`
		Warnings []string                    `json:"warnings,omitempty"`
	}{
		KBID:    draft.KBID,
		Version: draft.Version,
		Status:  draft.Status,
		Latest:  false,
		Chunks:  chunks,
	})
}

func (h *Handler) handleListDraftChunks(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, ok, err := parseAdminDraftChunkCollectionPath(r.URL.Path)
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

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	reviewStatus := strings.TrimSpace(r.URL.Query().Get("review_status"))
	chunks := filterDraftChunks(draft.Chunks, query, reviewStatus)
	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":    draft.KBID,
		"version":  draft.Version,
		"status":   draft.Status,
		"total":    len(draft.Chunks),
		"matched":  len(chunks),
		"query":    query,
		"chunks":   chunks,
		"filtered": query != "" || reviewStatus != "",
	})
}

func (h *Handler) handleCreateDraftChunk(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, ok, err := parseAdminDraftChunkCollectionPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	chunk, err := decodeDraftChunk(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	draft, err := h.readDraft(kbID, version)
	if err != nil {
		writeDraftReadError(w, r, err)
		return
	}
	if _, exists := findDraftChunkIndex(draft.Chunks, chunk.ChunkID); exists {
		http.Error(w, errDraftChunkDuplicate.Error()+": "+chunk.ChunkID, http.StatusConflict)
		return
	}
	draft.Chunks = append(draft.Chunks, chunk)
	draft.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.writeDraft(draft); err != nil {
		http.Error(w, "write draft failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":       draft.KBID,
		"version":     draft.Version,
		"status":      draft.Status,
		"chunk_count": len(draft.Chunks),
		"chunk":       chunk,
	})
}

func (h *Handler) handleUpdateDraftChunk(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, chunkID, ok, err := parseAdminDraftChunkItemPath(r.URL.Path, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	chunk, err := decodeDraftChunk(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	draft, err := h.readDraft(kbID, version)
	if err != nil {
		writeDraftReadError(w, r, err)
		return
	}

	index, exists := findDraftChunkIndex(draft.Chunks, chunkID)
	if !exists {
		http.Error(w, errDraftChunkNotFound.Error(), http.StatusNotFound)
		return
	}
	if chunk.ChunkID != chunkID {
		if _, duplicate := findDraftChunkIndex(draft.Chunks, chunk.ChunkID); duplicate {
			http.Error(w, errDraftChunkDuplicate.Error()+": "+chunk.ChunkID, http.StatusConflict)
			return
		}
	}
	draft.Chunks[index] = chunk
	draft.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.writeDraft(draft); err != nil {
		http.Error(w, "write draft failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":       draft.KBID,
		"version":     draft.Version,
		"status":      draft.Status,
		"chunk_count": len(draft.Chunks),
		"chunk":       chunk,
	})
}

func (h *Handler) handleDuplicateDraftChunk(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, chunkID, ok, err := parseAdminDraftChunkItemPath(r.URL.Path, "/duplicate")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	var payload struct {
		ChunkID string `json:"chunk_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	payload.ChunkID = strings.TrimSpace(payload.ChunkID)

	draft, err := h.readDraft(kbID, version)
	if err != nil {
		writeDraftReadError(w, r, err)
		return
	}
	index, exists := findDraftChunkIndex(draft.Chunks, chunkID)
	if !exists {
		http.Error(w, errDraftChunkNotFound.Error(), http.StatusNotFound)
		return
	}

	duplicate := draft.Chunks[index]
	if payload.ChunkID != "" {
		duplicate.ChunkID = payload.ChunkID
	} else {
		duplicate.ChunkID = nextDraftChunkCopyID(draft.Chunks, chunkID)
	}
	normalized, err := normalizeBuildChunk(len(draft.Chunks), duplicate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, exists := findDraftChunkIndex(draft.Chunks, normalized.ChunkID); exists {
		http.Error(w, errDraftChunkDuplicate.Error()+": "+normalized.ChunkID, http.StatusConflict)
		return
	}

	draft.Chunks = append(draft.Chunks, normalized)
	draft.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.writeDraft(draft); err != nil {
		http.Error(w, "write draft failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":       draft.KBID,
		"version":     draft.Version,
		"status":      draft.Status,
		"chunk_count": len(draft.Chunks),
		"chunk":       normalized,
	})
}

func (h *Handler) handleDeleteDraftChunk(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, chunkID, ok, err := parseAdminDraftChunkItemPath(r.URL.Path, "")
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
	index, exists := findDraftChunkIndex(draft.Chunks, chunkID)
	if !exists {
		http.Error(w, errDraftChunkNotFound.Error(), http.StatusNotFound)
		return
	}

	deleted := draft.Chunks[index]
	draft.Chunks = append(draft.Chunks[:index], draft.Chunks[index+1:]...)
	draft.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.writeDraft(draft); err != nil {
		http.Error(w, "write draft failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":       draft.KBID,
		"version":     draft.Version,
		"status":      draft.Status,
		"chunk_count": len(draft.Chunks),
		"deleted":     deleted.ChunkID,
	})
}

func (h *Handler) buildDraftDocument(kbID string, version string, payload draftSaveRequest) (draftDocument, error) {
	if len(payload.Chunks) == 0 {
		return draftDocument{}, fmt.Errorf("chunks must not be empty")
	}

	chunks, err := normalizeDraftChunks(payload.Chunks)
	if err != nil {
		return draftDocument{}, err
	}

	citations, err := normalizeCitations(payload.Citations)
	if err != nil {
		return draftDocument{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt := now
	if existing, err := h.readDraft(kbID, version); err == nil && existing.CreatedAt != "" {
		createdAt = existing.CreatedAt
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return draftDocument{}, err
	}

	return draftDocument{
		KBID:      kbID,
		Version:   version,
		Status:    draftStatus,
		Chunks:    chunks,
		Prompts:   payload.Prompts,
		Citations: json.RawMessage(citations),
		CreatedAt: createdAt,
		UpdatedAt: now,
	}, nil
}

func (h *Handler) writeDraft(draft draftDocument) error {
	draftDir := h.draftDir(draft.KBID, draft.Version)
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(draft, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	draftPath := filepath.Join(draftDir, "draft.json")
	tmpPath := draftPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, draftPath)
}

func (h *Handler) readDraft(kbID string, version string) (draftDocument, error) {
	data, err := os.ReadFile(filepath.Join(h.draftDir(kbID, version), "draft.json"))
	if err != nil {
		return draftDocument{}, err
	}
	var draft draftDocument
	if err := json.Unmarshal(data, &draft); err != nil {
		return draftDocument{}, fmt.Errorf("decode draft: %w", err)
	}
	return draft, nil
}

func (h *Handler) draftDir(kbID string, version string) string {
	return filepath.Join(h.kbDir(kbID), "drafts", version)
}

func parseAdminDraftPath(urlPath string, suffix string) (string, string, bool, error) {
	rest, ok := strings.CutPrefix(urlPath, "/admin/api/kb/")
	if !ok {
		return "", "", false, nil
	}
	kbID, rest, ok := strings.Cut(rest, "/drafts/")
	if !ok {
		return "", "", false, nil
	}
	if suffix != "" {
		var hasSuffix bool
		rest, hasSuffix = strings.CutSuffix(rest, suffix)
		if !hasSuffix {
			return "", "", false, nil
		}
	}
	if rest == "" || strings.Contains(rest, "/") {
		return "", "", false, nil
	}

	safeKBID, err := safeComponent(kbID)
	if err != nil {
		return "", "", true, err
	}
	version, err := safeComponent(rest)
	if err != nil {
		return "", "", true, err
	}
	return safeKBID, version, true, nil
}

func parseAdminDraftChunkCollectionPath(urlPath string) (string, string, bool, error) {
	kbID, version, tail, ok, err := parseAdminDraftChunksPath(urlPath)
	if err != nil || !ok {
		return "", "", ok, err
	}
	if tail != "" {
		return "", "", false, nil
	}
	return kbID, version, true, nil
}

func parseAdminDraftChunkItemPath(urlPath string, suffix string) (string, string, string, bool, error) {
	kbID, version, tail, ok, err := parseAdminDraftChunksPath(urlPath)
	if err != nil || !ok {
		return "", "", "", ok, err
	}
	if suffix != "" {
		var hasSuffix bool
		tail, hasSuffix = strings.CutSuffix(tail, suffix)
		if !hasSuffix {
			return "", "", "", false, nil
		}
	}
	if !strings.HasPrefix(tail, "/") {
		return "", "", "", false, nil
	}
	chunkID := strings.TrimPrefix(tail, "/")
	if chunkID == "" || strings.Contains(chunkID, "/") || strings.Contains(chunkID, `\`) {
		return "", "", "", false, nil
	}
	return kbID, version, chunkID, true, nil
}

func parseAdminDraftChunksPath(urlPath string) (string, string, string, bool, error) {
	rest, ok := strings.CutPrefix(urlPath, "/admin/api/kb/")
	if !ok {
		return "", "", "", false, nil
	}
	kbID, rest, ok := strings.Cut(rest, "/drafts/")
	if !ok {
		return "", "", "", false, nil
	}
	version, tail, ok := strings.Cut(rest, "/chunks")
	if !ok {
		return "", "", "", false, nil
	}

	safeKBID, err := safeComponent(kbID)
	if err != nil {
		return "", "", "", true, err
	}
	safeVersion, err := safeComponent(version)
	if err != nil {
		return "", "", "", true, err
	}
	return safeKBID, safeVersion, tail, true, nil
}

func decodeDraftChunk(w http.ResponseWriter, r *http.Request) (knowledgePackBuildChunk, error) {
	var chunk knowledgePackBuildChunk
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&chunk); err != nil {
		return knowledgePackBuildChunk{}, fmt.Errorf("invalid json body")
	}
	return normalizeBuildChunk(0, chunk)
}

func normalizeDraftChunks(chunks []knowledgePackBuildChunk) ([]knowledgePackBuildChunk, error) {
	normalized := make([]knowledgePackBuildChunk, len(chunks))
	seen := map[string]bool{}
	for index, chunk := range chunks {
		chunk, err := normalizeBuildChunk(index, chunk)
		if err != nil {
			return nil, err
		}
		if seen[chunk.ChunkID] {
			return nil, fmt.Errorf("%w: %s", errDraftChunkDuplicate, chunk.ChunkID)
		}
		seen[chunk.ChunkID] = true
		normalized[index] = chunk
	}
	return normalized, nil
}

func filterDraftChunks(chunks []knowledgePackBuildChunk, query string, reviewStatus string) []knowledgePackBuildChunk {
	query = strings.ToLower(strings.TrimSpace(query))
	reviewStatus = strings.ToLower(strings.TrimSpace(reviewStatus))
	filtered := make([]knowledgePackBuildChunk, 0, len(chunks))
	for _, chunk := range chunks {
		if reviewStatus != "" && strings.ToLower(strings.TrimSpace(chunk.ReviewStatus)) != reviewStatus {
			continue
		}
		if query != "" && !draftChunkMatchesQuery(chunk, query) {
			continue
		}
		filtered = append(filtered, chunk)
	}
	return filtered
}

func draftChunkMatchesQuery(chunk knowledgePackBuildChunk, query string) bool {
	fields := []string{
		chunk.ChunkID,
		chunk.Title,
		chunk.Path,
		chunk.Source,
		chunk.Content,
		chunk.ReviewStatus,
		strings.Join(chunk.Tags, " "),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func findDraftChunkIndex(chunks []knowledgePackBuildChunk, chunkID string) (int, bool) {
	for index, chunk := range chunks {
		if chunk.ChunkID == chunkID {
			return index, true
		}
	}
	return -1, false
}

func nextDraftChunkCopyID(chunks []knowledgePackBuildChunk, chunkID string) string {
	candidate := chunkID + "-copy"
	if _, exists := findDraftChunkIndex(chunks, candidate); !exists {
		return candidate
	}
	for index := 2; ; index++ {
		candidate = fmt.Sprintf("%s-copy-%d", chunkID, index)
		if _, exists := findDraftChunkIndex(chunks, candidate); !exists {
			return candidate
		}
	}
}

func writeDraftReadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	http.Error(w, "read draft failed", http.StatusInternalServerError)
}

func draftSuggestedQuestions(prompts []knowledgePackBuildPrompt) []string {
	questions := []string{}
	seen := map[string]bool{}
	for _, prompt := range prompts {
		question := strings.TrimSpace(prompt.Question)
		if question == "" {
			question = strings.TrimSpace(prompt.Text)
		}
		if question == "" || seen[question] {
			continue
		}
		seen[question] = true
		questions = append(questions, question)
		if len(questions) >= 8 {
			break
		}
	}
	return questions
}
