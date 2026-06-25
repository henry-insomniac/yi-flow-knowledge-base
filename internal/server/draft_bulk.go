package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

type draftReviewQueueItem struct {
	ChunkID string                  `json:"chunk_id"`
	Reasons []string                `json:"reasons"`
	Chunk   knowledgePackBuildChunk `json:"chunk"`
}

type draftReviewReport struct {
	KBID                 string                    `json:"kb_id"`
	Version              string                    `json:"version"`
	ChunkCount           int                       `json:"chunk_count"`
	SampleCount          int                       `json:"sample_count"`
	MissingCitationCount int                       `json:"missing_citation_count"`
	DuplicateCount       int                       `json:"duplicate_count"`
	ContaminationCount   int                       `json:"contamination_count"`
	GoldenPromptCount    int                       `json:"golden_prompt_count"`
	SourceCounts         map[string]int            `json:"source_counts"`
	LicenseCounts        map[string]int            `json:"license_counts"`
	SampledChunks        []knowledgePackBuildChunk `json:"sampled_chunks"`
}

type draftFieldError struct {
	Field       string `json:"field"`
	Remediation string `json:"remediation"`
}

func (h *Handler) handleDraftBulkImport(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/import")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	var payload draftSaveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	draft, err := h.buildDraftDocument(kbID, version, payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":        err.Error(),
			"field_errors": draftValidationFieldErrors(err),
		})
		return
	}
	qualityReport := evaluateDraftQuality(draft)
	dryRun := r.URL.Query().Get("dry_run") == "1" || strings.EqualFold(r.URL.Query().Get("dry_run"), "true")
	if dryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"kb_id":          draft.KBID,
			"version":        draft.Version,
			"dry_run":        true,
			"would_save":     false,
			"chunk_count":    len(draft.Chunks),
			"prompt_count":   len(draft.Prompts),
			"quality_status": qualityReport.Status,
			"quality_report": qualityReport,
		})
		return
	}
	if err := h.writeDraft(draft); err != nil {
		http.Error(w, "write draft failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":          draft.KBID,
		"version":        draft.Version,
		"status":         draft.Status,
		"dry_run":        false,
		"would_save":     true,
		"chunk_count":    len(draft.Chunks),
		"prompt_count":   len(draft.Prompts),
		"quality_status": qualityReport.Status,
		"quality_report": qualityReport,
	})
}

func draftValidationFieldErrors(err error) []draftFieldError {
	if err == nil {
		return nil
	}
	message := err.Error()
	field := validationFieldFromMessage(message)
	if field == "" {
		field = "draft"
	}
	return []draftFieldError{{
		Field:       field,
		Remediation: validationRemediation(field, message),
	}}
}

func validationFieldFromMessage(message string) string {
	if strings.HasPrefix(message, "chunks must not be empty") {
		return "chunks"
	}
	if strings.HasPrefix(message, "duplicate chunk_id") {
		return "chunks[].chunk_id"
	}
	if strings.HasPrefix(message, "duplicate prompt id") {
		return "prompts[].id"
	}
	token := strings.Fields(message)
	if len(token) == 0 {
		return ""
	}
	field := token[0]
	if strings.Contains(field, "[") && strings.Contains(field, "].") {
		return strings.TrimSpace(field)
	}
	return ""
}

func validationRemediation(field string, message string) string {
	switch {
	case field == "chunks":
		return "至少添加 1 条 chunk 后重试。"
	case strings.HasSuffix(field, ".chunk_id"):
		return "补齐唯一 chunk_id，避免和已有 chunk 重复。"
	case strings.HasSuffix(field, ".title"):
		return "补齐 title 后重试。"
	case strings.HasSuffix(field, ".path"):
		return "补齐稳定 path，便于后续检索和溯源。"
	case strings.HasSuffix(field, ".source"):
		return "补齐 source，用于区分 manual、moegirl、yi-flow-core 等来源。"
	case strings.HasSuffix(field, ".content"):
		return "补齐 content，内容不能为空。"
	case strings.HasSuffix(field, ".id"):
		return "补齐唯一 prompt id，避免和已有 prompt 重复。"
	case strings.HasSuffix(field, ".question"):
		return "补齐 prompt question，质量门禁需要可执行问题。"
	case strings.HasSuffix(field, ".expected_chunk_ids"):
		return "确认 expected_chunk_ids 引用的 chunk_id 已存在。"
	case strings.Contains(message, "moegirl source metadata"):
		return "按 Moegirl source metadata 要求补齐 citation、license、source_policy 和页面元数据。"
	default:
		return "修正该字段后重新执行 dry-run。"
	}
}

func (h *Handler) handleDraftExport(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/export")
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
	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":     draft.KBID,
		"version":   draft.Version,
		"chunks":    draft.Chunks,
		"prompts":   draft.Prompts,
		"citations": json.RawMessage(draft.Citations),
	})
}

func (h *Handler) handleDraftReviewQueue(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/review-queue")
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
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	sourceFamily := strings.TrimSpace(r.URL.Query().Get("source_family"))
	items := draftReviewQueue(draft, filter, sourceFamily)
	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":   draft.KBID,
		"version": draft.Version,
		"filter":  filter,
		"total":   len(draft.Chunks),
		"matched": len(items),
		"items":   items,
	})
}

func (h *Handler) handleDraftReviewReport(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	kbID, version, ok, err := parseAdminDraftPath(r.URL.Path, "/review-report")
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
	writeJSON(w, http.StatusOK, draftReviewReportFor(draft))
}

func draftReviewQueue(draft draftDocument, filter string, sourceFamily string) []draftReviewQueueItem {
	filter = strings.ToLower(strings.TrimSpace(filter))
	sourceFamily = strings.ToLower(strings.TrimSpace(sourceFamily))
	failedChunkIDs := draftFailedGateChunkIDs(draft)
	items := []draftReviewQueueItem{}
	for _, chunk := range draft.Chunks {
		reasons := draftReviewReasons(draft.KBID, chunk, failedChunkIDs)
		if !draftReviewQueueMatches(filter, sourceFamily, chunk, reasons) {
			continue
		}
		items = append(items, draftReviewQueueItem{ChunkID: chunk.ChunkID, Reasons: reasons, Chunk: chunk})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ChunkID < items[j].ChunkID })
	return items
}

func draftReviewReasons(kbID string, chunk knowledgePackBuildChunk, failedChunkIDs map[string]bool) []string {
	reasons := []string{}
	status := strings.ToLower(strings.TrimSpace(chunk.ReviewStatus))
	if status == "" || status == draftStatus || status == "needs_review" {
		reasons = append(reasons, "unreviewed")
	}
	if chunk.CitationURL == "" || chunk.License == "" || chunk.SourcePolicy == "" {
		reasons = append(reasons, "missing_citation")
	}
	if failedChunkIDs[chunk.ChunkID] {
		reasons = append(reasons, "failed_gate")
	}
	if err := validateChunkSourceMetadata(kbID, []knowledgePackBuildChunk{chunk}); err != nil {
		reasons = append(reasons, "contamination")
	}
	if family, ok := draftChunkSourceFamily(chunk); ok {
		reasons = append(reasons, "source_family:"+family)
	}
	reasons = uniqueStrings(reasons, 20)
	sort.Strings(reasons)
	return reasons
}

func draftReviewQueueMatches(filter string, sourceFamily string, chunk knowledgePackBuildChunk, reasons []string) bool {
	if sourceFamily != "" {
		return containsStringValue(reasons, "source_family:"+sourceFamily)
	}
	switch filter {
	case "", "all":
		return true
	case "unreviewed", "missing_citation", "failed_gate", "contamination", "changed_since_last_publish":
		if filter == "changed_since_last_publish" {
			return true
		}
		return containsStringValue(reasons, filter)
	default:
		if strings.HasPrefix(filter, "source_family:") {
			return containsStringValue(reasons, filter)
		}
		return strings.Contains(strings.ToLower(chunk.ChunkID), filter) ||
			strings.Contains(strings.ToLower(chunk.Title), filter) ||
			strings.Contains(strings.ToLower(chunk.Content), filter)
	}
}

func draftFailedGateChunkIDs(draft draftDocument) map[string]bool {
	result := map[string]bool{}
	for _, check := range evaluateDraftQuality(draft).Checks {
		if check.Status != "failed" {
			continue
		}
		for _, chunkID := range check.ChunkIDs {
			result[chunkID] = true
		}
	}
	return result
}

func draftChunkSourceFamily(chunk knowledgePackBuildChunk) (string, bool) {
	for _, candidate := range chunkSourceMetadataCandidates(chunk) {
		if family, ok := classifyExternalSourceFamily(candidate.value); ok {
			return family, true
		}
		if family, ok := classifyInternalSourceFamily(candidate.value); ok {
			return family, true
		}
	}
	return "", false
}

func draftReviewReportFor(draft draftDocument) draftReviewReport {
	sampled := append([]knowledgePackBuildChunk(nil), draft.Chunks...)
	sort.Slice(sampled, func(i, j int) bool { return sampled[i].ChunkID < sampled[j].ChunkID })
	if len(sampled) > 30 {
		sampled = sampled[:30]
	}
	report := draftReviewReport{
		KBID:          draft.KBID,
		Version:       draft.Version,
		ChunkCount:    len(draft.Chunks),
		SampleCount:   len(sampled),
		SourceCounts:  map[string]int{},
		LicenseCounts: map[string]int{},
		SampledChunks: sampled,
	}
	seenContent := map[string]bool{}
	for _, chunk := range draft.Chunks {
		source := strings.TrimSpace(chunk.Source)
		if source == "" {
			source = "unknown"
		}
		report.SourceCounts[source]++
		license := strings.TrimSpace(chunk.License)
		if license == "" {
			license = "missing"
		}
		report.LicenseCounts[license]++
		if chunk.CitationURL == "" || chunk.License == "" || chunk.SourcePolicy == "" {
			report.MissingCitationCount++
		}
		if err := validateChunkSourceMetadata(draft.KBID, []knowledgePackBuildChunk{chunk}); err != nil {
			report.ContaminationCount++
		}
		fingerprint := normalizedContentFingerprint(chunk.Content)
		if fingerprint != "" {
			if seenContent[fingerprint] {
				report.DuplicateCount++
			}
			seenContent[fingerprint] = true
		}
	}
	for _, prompt := range draft.Prompts {
		if len(prompt.ExpectedChunkIDs) > 0 {
			report.GoldenPromptCount++
		}
	}
	return report
}

func containsStringValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
