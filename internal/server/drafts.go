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

func (h *Handler) buildDraftDocument(kbID string, version string, payload draftSaveRequest) (draftDocument, error) {
	if len(payload.Chunks) == 0 {
		return draftDocument{}, fmt.Errorf("chunks must not be empty")
	}

	chunks := make([]knowledgePackBuildChunk, len(payload.Chunks))
	for index, chunk := range payload.Chunks {
		normalized, err := normalizeBuildChunk(index, chunk)
		if err != nil {
			return draftDocument{}, err
		}
		chunks[index] = normalized
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
