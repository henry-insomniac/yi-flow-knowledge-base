package server

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	defaultTop5Threshold        = 0.85
	defaultCitationThreshold    = 0.95
	defaultDuplicateThreshold   = 0.05
	defaultRefusalPassThreshold = 0.90
)

type draftQualityReport struct {
	KBID         string                 `json:"kb_id"`
	Version      string                 `json:"version"`
	Status       string                 `json:"status"`
	BlockPublish bool                   `json:"block_publish"`
	LatencyMS    int64                  `json:"latency_ms"`
	Metrics      draftQualityMetrics    `json:"metrics"`
	Thresholds   draftQualityThresholds `json:"thresholds"`
	Checks       []draftQualityCheck    `json:"checks"`
}

type draftQualityCheck struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Severity    string   `json:"severity"`
	Count       int      `json:"count"`
	ChunkIDs    []string `json:"chunk_ids,omitempty"`
	PromptIDs   []string `json:"prompt_ids,omitempty"`
	Remediation string   `json:"remediation"`
}

type draftQualityMetrics struct {
	Top1HitRate            float64 `json:"top1_hit_rate"`
	Top5HitRate            float64 `json:"top5_hit_rate"`
	CitationRate           float64 `json:"citation_rate"`
	DuplicateAnswerRate    float64 `json:"duplicate_answer_rate"`
	RefusalPassRate        float64 `json:"refusal_pass_rate"`
	MissingCitationCount   int     `json:"missing_citation_count"`
	UnsupportedEntityCount int     `json:"unsupported_entity_count"`
}

type draftQualityThresholds struct {
	Top5HitRate         float64 `json:"top5_hit_rate"`
	CitationRate        float64 `json:"citation_rate"`
	DuplicateAnswerRate float64 `json:"duplicate_answer_rate"`
	RefusalPassRate     float64 `json:"refusal_pass_rate"`
}

func (h *Handler) handleDraftQualityGates(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/quality-gates")
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
	writeJSON(w, http.StatusOK, evaluateDraftQuality(draft))
}

func evaluateDraftQuality(draft draftDocument) draftQualityReport {
	started := time.Now()
	checks := []draftQualityCheck{
		requiredFieldsQualityCheck(draft),
		duplicateChunkIDsQualityCheck(draft),
		nearDuplicateContentQualityCheck(draft),
		invalidLengthsQualityCheck(draft),
		missingCitationsQualityCheck(draft),
		contaminationQualityCheck(draft),
		promptReferencesQualityCheck(draft),
	}
	metrics := goldenEvalMetrics(draft)
	checks = append(checks, goldenEvalQualityCheck(metrics))

	blockPublish := false
	for _, check := range checks {
		if check.Severity == "P0" && check.Status == "failed" {
			blockPublish = true
			break
		}
	}
	status := "passed"
	if blockPublish {
		status = "failed"
	}
	return draftQualityReport{
		KBID:         draft.KBID,
		Version:      draft.Version,
		Status:       status,
		BlockPublish: blockPublish,
		LatencyMS:    time.Since(started).Milliseconds(),
		Metrics:      metrics,
		Thresholds: draftQualityThresholds{
			Top5HitRate:         defaultTop5Threshold,
			CitationRate:        defaultCitationThreshold,
			DuplicateAnswerRate: defaultDuplicateThreshold,
			RefusalPassRate:     defaultRefusalPassThreshold,
		},
		Checks: checks,
	}
}

func requiredFieldsQualityCheck(draft draftDocument) draftQualityCheck {
	failing := []string{}
	for _, chunk := range draft.Chunks {
		if strings.TrimSpace(chunk.ChunkID) == "" ||
			strings.TrimSpace(chunk.Title) == "" ||
			strings.TrimSpace(chunk.Path) == "" ||
			strings.TrimSpace(chunk.Source) == "" ||
			strings.TrimSpace(chunk.Content) == "" {
			failing = append(failing, chunk.ChunkID)
		}
	}
	return qualityCheck("required_fields", "P0", failing, nil, "补齐 chunk_id/title/path/source/content。")
}

func duplicateChunkIDsQualityCheck(draft draftDocument) draftQualityCheck {
	seen := map[string]bool{}
	duplicates := []string{}
	for _, chunk := range draft.Chunks {
		if seen[chunk.ChunkID] {
			duplicates = append(duplicates, chunk.ChunkID)
		}
		seen[chunk.ChunkID] = true
	}
	return qualityCheck("duplicate_chunk_ids", "P0", duplicates, nil, "修改重复 chunk_id，确保每个 chunk 唯一。")
}

func nearDuplicateContentQualityCheck(draft draftDocument) draftQualityCheck {
	seen := map[string]string{}
	duplicates := []string{}
	for _, chunk := range draft.Chunks {
		key := normalizedContentFingerprint(chunk.Content)
		if key == "" {
			continue
		}
		if first, exists := seen[key]; exists {
			duplicates = append(duplicates, first, chunk.ChunkID)
			continue
		}
		seen[key] = chunk.ChunkID
	}
	return qualityCheck("near_duplicate_content", "P0", uniqueStrings(duplicates, 200), nil, "合并或重写重复内容，避免同义 chunk 反复命中。")
}

func invalidLengthsQualityCheck(draft draftDocument) draftQualityCheck {
	failing := []string{}
	for _, chunk := range draft.Chunks {
		length := len([]rune(strings.TrimSpace(chunk.Content)))
		if length < 20 || length > 4000 {
			failing = append(failing, chunk.ChunkID)
		}
	}
	return qualityCheck("invalid_lengths", "P0", failing, nil, "将 chunk content 控制在 20 到 4000 字符之间。")
}

func missingCitationsQualityCheck(draft draftDocument) draftQualityCheck {
	failing := []string{}
	for _, chunk := range draft.Chunks {
		if chunk.CitationURL == "" || chunk.License == "" || chunk.SourcePolicy == "" {
			failing = append(failing, chunk.ChunkID)
		}
	}
	return qualityCheck("missing_citations", "P0", failing, nil, "补齐 citation_url、license 和 source_policy。")
}

func contaminationQualityCheck(draft draftDocument) draftQualityCheck {
	failing := []string{}
	for _, chunk := range draft.Chunks {
		if err := validateChunkSourceMetadata(draft.KBID, []knowledgePackBuildChunk{chunk}); err != nil {
			failing = append(failing, chunk.ChunkID)
		}
	}
	return qualityCheck("contamination", "P0", failing, nil, "将跨知识包或外部来源内容移到正确 KB，并补齐来源策略。")
}

func promptReferencesQualityCheck(draft draftDocument) draftQualityCheck {
	chunkIDs := map[string]bool{}
	for _, chunk := range draft.Chunks {
		chunkIDs[chunk.ChunkID] = true
	}
	failing := []string{}
	for _, prompt := range draft.Prompts {
		for _, expectedID := range prompt.ExpectedChunkIDs {
			if !chunkIDs[expectedID] {
				failing = append(failing, prompt.ID)
				break
			}
		}
	}
	return qualityCheck("prompt_references", "P0", nil, failing, "修正 expected_chunk_ids，确保引用存在的 draft chunk。")
}

func goldenEvalQualityCheck(metrics draftQualityMetrics) draftQualityCheck {
	failed := []string{}
	if metrics.Top5HitRate < defaultTop5Threshold {
		failed = append(failed, "top5_hit_rate")
	}
	if metrics.CitationRate < defaultCitationThreshold {
		failed = append(failed, "citation_rate")
	}
	if metrics.DuplicateAnswerRate >= defaultDuplicateThreshold {
		failed = append(failed, "duplicate_answer_rate")
	}
	if metrics.RefusalPassRate < defaultRefusalPassThreshold {
		failed = append(failed, "refusal_pass_rate")
	}
	if metrics.MissingCitationCount > 0 {
		failed = append(failed, "missing_citation_count")
	}
	if metrics.UnsupportedEntityCount > 0 {
		failed = append(failed, "unsupported_entity_count")
	}
	return qualityCheck("golden_eval", "P0", nil, failed, "修复 golden prompts 的 expected chunks、citation 覆盖、拒答样例和重复命中。")
}

func qualityCheck(name string, severity string, chunkIDs []string, promptIDs []string, remediation string) draftQualityCheck {
	sort.Strings(chunkIDs)
	sort.Strings(promptIDs)
	count := len(chunkIDs) + len(promptIDs)
	status := "passed"
	if count > 0 {
		status = "failed"
	}
	return draftQualityCheck{
		Name:        name,
		Status:      status,
		Severity:    severity,
		Count:       count,
		ChunkIDs:    chunkIDs,
		PromptIDs:   promptIDs,
		Remediation: remediation,
	}
}

func goldenEvalMetrics(draft draftDocument) draftQualityMetrics {
	answerableTotal := 0
	top1Hits := 0
	top5Hits := 0
	citationHits := 0
	refusalTotal := 0
	refusalHits := 0
	unsupported := 0
	topAnswers := []string{}

	for _, prompt := range draft.Prompts {
		results, _ := searchDraftChunksForPreview(draft.KBID, draft.Chunks, prompt.Question, 5)
		if prompt.Answerability == "answerable" {
			answerableTotal++
			if len(prompt.ExpectedChunkIDs) == 0 {
				unsupported++
			}
			if len(results) == 0 {
				unsupported++
				continue
			}
			topAnswers = append(topAnswers, results[0].ChunkID)
			if containsExpectedChunk(results[:min(1, len(results))], prompt.ExpectedChunkIDs) {
				top1Hits++
			}
			if containsExpectedChunk(results, prompt.ExpectedChunkIDs) {
				top5Hits++
			}
			if results[0].Citation.URL != "" && results[0].Citation.License != "" {
				citationHits++
			}
			continue
		}
		refusalTotal++
		if len(results) == 0 || results[0].Score < weakDraftRetrievalScore {
			refusalHits++
		}
	}

	metrics := draftQualityMetrics{
		MissingCitationCount: missingCitationsQualityCheck(draft).Count,
		DuplicateAnswerRate:  duplicateStringRate(topAnswers),
	}
	if answerableTotal > 0 {
		metrics.Top1HitRate = float64(top1Hits) / float64(answerableTotal)
		metrics.Top5HitRate = float64(top5Hits) / float64(answerableTotal)
		metrics.CitationRate = float64(citationHits) / float64(answerableTotal)
	} else {
		metrics.Top1HitRate = 1
		metrics.Top5HitRate = 1
		metrics.CitationRate = 1
	}
	if refusalTotal > 0 {
		metrics.RefusalPassRate = float64(refusalHits) / float64(refusalTotal)
	} else {
		metrics.RefusalPassRate = 1
	}
	metrics.UnsupportedEntityCount = unsupported
	return metrics
}

func containsExpectedChunk(results []draftRetrievalPreviewResult, expectedIDs []string) bool {
	expected := map[string]bool{}
	for _, id := range expectedIDs {
		expected[id] = true
	}
	for _, result := range results {
		if expected[result.ChunkID] {
			return true
		}
	}
	return false
}

func duplicateStringRate(values []string) float64 {
	if len(values) == 0 {
		return 0
	}
	seen := map[string]bool{}
	duplicates := 0
	for _, value := range values {
		if seen[value] {
			duplicates++
		}
		seen[value] = true
	}
	return float64(duplicates) / float64(len(values))
}

func normalizedContentFingerprint(content string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(content))), " ")
}
