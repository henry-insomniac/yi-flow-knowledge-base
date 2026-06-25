package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
)

const weakDraftRetrievalScore = 0.5

type draftRetrievalPreviewRequest struct {
	Query    string `json:"query"`
	PromptID string `json:"prompt_id"`
	TopK     int    `json:"top_k"`
}

type draftRetrievalPreviewResponse struct {
	KBID      string                        `json:"kb_id"`
	Version   string                        `json:"version"`
	Status    string                        `json:"status"`
	Query     string                        `json:"query"`
	PromptID  string                        `json:"prompt_id,omitempty"`
	TopK      int                           `json:"top_k"`
	LatencyMS int64                         `json:"latency_ms"`
	Reasons   []string                      `json:"reasons"`
	Results   []draftRetrievalPreviewResult `json:"results"`
	Prompt    *knowledgePackBuildPrompt     `json:"prompt,omitempty"`
}

type draftRetrievalPreviewResult struct {
	ChunkID      string                 `json:"chunk_id"`
	Title        string                 `json:"title"`
	Path         string                 `json:"path"`
	Source       string                 `json:"source"`
	Score        float64                `json:"score"`
	MatchedTerms []string               `json:"matched_terms"`
	Snippet      string                 `json:"snippet"`
	Reasons      []string               `json:"reasons"`
	Citation     draftRetrievalCitation `json:"citation"`
}

type draftRetrievalCitation struct {
	URL          string `json:"url,omitempty"`
	Title        string `json:"title,omitempty"`
	SourceName   string `json:"source_name,omitempty"`
	License      string `json:"license,omitempty"`
	SourcePolicy string `json:"source_policy,omitempty"`
	RevisionID   string `json:"revision_id,omitempty"`
	PageID       string `json:"page_id,omitempty"`
}

func (h *Handler) handleDraftRetrievalPreview(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/retrieval-preview")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	var request draftRetrievalPreviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	request.Query = strings.TrimSpace(request.Query)
	request.PromptID = strings.TrimSpace(request.PromptID)
	topK := normalizedRAGCompareTopK(request.TopK)

	draft, err := h.readDraft(kbID, version)
	if err != nil {
		writeDraftReadError(w, r, err)
		return
	}

	var prompt *knowledgePackBuildPrompt
	if request.PromptID != "" {
		index, exists := findDraftPromptIndex(draft.Prompts, request.PromptID)
		if !exists {
			http.Error(w, "draft prompt not found", http.StatusNotFound)
			return
		}
		promptValue := draft.Prompts[index]
		prompt = &promptValue
		if request.Query == "" {
			request.Query = strings.TrimSpace(promptValue.Question)
		}
	}
	if request.Query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}

	started := time.Now()
	results, reasons := searchDraftChunksForPreview(kbID, draft.Chunks, request.Query, topK)
	status := "ok"
	if len(results) == 0 {
		status = "no_answer"
		reasons = appendReason(reasons, "empty_retrieval")
	} else if results[0].Score < weakDraftRetrievalScore {
		status = "weak_score"
		reasons = appendReason(reasons, "weak_score")
		results[0].Reasons = appendReason(results[0].Reasons, "weak_score")
	}

	writeJSON(w, http.StatusOK, draftRetrievalPreviewResponse{
		KBID:      draft.KBID,
		Version:   draft.Version,
		Status:    status,
		Query:     request.Query,
		PromptID:  request.PromptID,
		TopK:      topK,
		LatencyMS: time.Since(started).Milliseconds(),
		Reasons:   reasons,
		Results:   results,
		Prompt:    prompt,
	})
}

func searchDraftChunksForPreview(kbID string, chunks []knowledgePackBuildChunk, query string, topK int) ([]draftRetrievalPreviewResult, []string) {
	terms := draftRetrievalTerms(query)
	if len(terms) == 0 {
		return []draftRetrievalPreviewResult{}, []string{"empty_query_terms"}
	}

	results := make([]draftRetrievalPreviewResult, 0, min(topK, len(chunks)))
	reasons := []string{}
	for _, chunk := range chunks {
		matched := matchedDraftTerms(chunk, terms)
		if len(matched) == 0 {
			continue
		}
		score := float64(len(matched)) / float64(len(terms))
		result := draftRetrievalPreviewResult{
			ChunkID:      chunk.ChunkID,
			Title:        chunk.Title,
			Path:         chunk.Path,
			Source:       chunk.Source,
			Score:        score,
			MatchedTerms: matched,
			Snippet:      draftSnippet(chunk.Content, matched),
			Citation:     draftRetrievalCitationForChunk(chunk),
			Reasons:      draftRetrievalReasons(kbID, chunk, score),
		}
		for _, reason := range result.Reasons {
			reasons = appendReason(reasons, reason)
		}
		results = append(results, result)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ChunkID < results[j].ChunkID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > topK {
		results = results[:topK]
	}
	return results, reasons
}

func draftRetrievalTerms(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	terms := []string{}
	var current []rune
	flush := func() {
		if len(current) == 0 {
			return
		}
		terms = append(terms, string(current))
		current = nil
	}
	for _, value := range query {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			current = append(current, value)
			continue
		}
		flush()
	}
	flush()
	terms = append(terms, cjkTrigrams(query)...)
	return uniqueStrings(terms, 24)
}

func matchedDraftTerms(chunk knowledgePackBuildChunk, terms []string) []string {
	searchText := strings.ToLower(strings.Join([]string{
		chunk.ChunkID,
		chunk.Title,
		chunk.Path,
		chunk.Source,
		chunk.Content,
		strings.Join(chunk.Tags, " "),
		chunk.CitationTitle,
		chunk.SourceName,
	}, " "))
	matched := []string{}
	for _, term := range terms {
		if strings.Contains(searchText, term) {
			matched = append(matched, term)
		}
	}
	return matched
}

func draftSnippet(content string, matchedTerms []string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lower := strings.ToLower(content)
	start := 0
	for _, term := range matchedTerms {
		if index := strings.Index(lower, strings.ToLower(term)); index >= 0 {
			start = index
			break
		}
	}
	if start > 48 {
		start -= 48
	} else {
		start = 0
	}
	end := start + 180
	if end > len(content) {
		end = len(content)
	}
	snippet := strings.TrimSpace(content[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(content) {
		snippet += "..."
	}
	return snippet
}

func draftRetrievalCitationForChunk(chunk knowledgePackBuildChunk) draftRetrievalCitation {
	return draftRetrievalCitation{
		URL:          chunk.CitationURL,
		Title:        chunk.CitationTitle,
		SourceName:   chunk.SourceName,
		License:      chunk.License,
		SourcePolicy: chunk.SourcePolicy,
		RevisionID:   chunk.SourceRevisionID,
		PageID:       chunk.SourcePageID,
	}
}

func draftRetrievalReasons(kbID string, chunk knowledgePackBuildChunk, score float64) []string {
	reasons := []string{}
	if chunk.CitationURL == "" || chunk.License == "" || chunk.SourcePolicy == "" {
		reasons = appendReason(reasons, "missing_citation")
	}
	if score < weakDraftRetrievalScore {
		reasons = appendReason(reasons, "weak_score")
	}
	if err := validateChunkSourceMetadata(kbID, []knowledgePackBuildChunk{chunk}); err != nil {
		reasons = appendReason(reasons, "contamination")
	}
	return reasons
}

func appendReason(reasons []string, reason string) []string {
	if reason == "" {
		return reasons
	}
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}
