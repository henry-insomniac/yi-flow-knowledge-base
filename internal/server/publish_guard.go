package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"yi-flow/knowledge-base/internal/sourcepolicy"
)

func validateBuildPublishBoundary(kbID string, payload buildPublishRequest) error {
	switch {
	case kbID == "yi-flow-core":
		if family, field, ok := forbiddenCoreSourceFamily(payload); ok {
			return fmt.Errorf("kb_id %q rejects %s source family in %s", kbID, family, field)
		}
	case isMoegirlKB(kbID):
		if family, field, ok := forbiddenMoegirlSourceFamily(payload); ok {
			return fmt.Errorf("kb_id %q rejects %s source family in %s", kbID, family, field)
		}
	}
	return validateBuildPublishSourceMetadata(kbID, payload)
}

func isMoegirlKB(kbID string) bool {
	kbID = strings.ToLower(strings.TrimSpace(kbID))
	return strings.HasPrefix(kbID, "moegirl-") || strings.Contains(kbID, "-moegirl-") || strings.Contains(kbID, "moegirl")
}

func forbiddenCoreSourceFamily(payload buildPublishRequest) (string, string, bool) {
	for index, chunk := range payload.Chunks {
		for _, candidate := range []struct {
			field string
			value string
		}{
			{field: "chunk_id", value: chunk.ChunkID},
			{field: "title", value: chunk.Title},
			{field: "path", value: chunk.Path},
			{field: "source", value: chunk.Source},
			{field: "citation_url", value: chunk.CitationURL},
			{field: "citation_title", value: chunk.CitationTitle},
			{field: "source_name", value: chunk.SourceName},
			{field: "license", value: chunk.License},
			{field: "source_policy", value: chunk.SourcePolicy},
		} {
			if family, ok := classifyExternalSourceFamily(candidate.value); ok {
				return family, fmt.Sprintf("chunks[%d].%s", index, candidate.field), true
			}
		}
	}

	if family, ok := classifyExternalSourceFamily(string(payload.Citations)); ok {
		return family, "citations", true
	}
	return "", "", false
}

func classifyExternalSourceFamily(value string) (string, bool) {
	return sourcepolicy.ClassifyExternalSourceFamily(value)
}

func forbiddenMoegirlSourceFamily(payload buildPublishRequest) (string, string, bool) {
	for index, chunk := range payload.Chunks {
		for _, candidate := range []struct {
			field string
			value string
		}{
			{field: "chunk_id", value: chunk.ChunkID},
			{field: "title", value: chunk.Title},
			{field: "path", value: chunk.Path},
			{field: "source", value: chunk.Source},
			{field: "citation_url", value: chunk.CitationURL},
			{field: "citation_title", value: chunk.CitationTitle},
			{field: "source_name", value: chunk.SourceName},
			{field: "license", value: chunk.License},
			{field: "source_policy", value: chunk.SourcePolicy},
		} {
			if family, ok := classifyInternalSourceFamily(candidate.value); ok {
				return family, fmt.Sprintf("chunks[%d].%s", index, candidate.field), true
			}
		}
	}

	if family, ok := classifyInternalSourceFamily(string(payload.Citations)); ok {
		return family, "citations", true
	}
	return "", "", false
}

func classifyInternalSourceFamily(value string) (string, bool) {
	return sourcepolicy.ClassifyInternalYiFlowSourceFamily(value)
}

func validateChunkSourceMetadata(kbID string, chunks []knowledgePackBuildChunk) error {
	switch {
	case kbID == "yi-flow-core":
		for index, chunk := range chunks {
			if family, field, ok := forbiddenCoreChunkSourceFamily(chunk); ok {
				return fmt.Errorf("kb_id %q rejects %s source family in chunks[%d].%s", kbID, family, index, field)
			}
		}
	case isMoegirlKB(kbID):
		for index, chunk := range chunks {
			if err := validateMoegirlChunkSourceMetadata(index, chunk); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBuildPublishSourceMetadata(kbID string, payload buildPublishRequest) error {
	if isMoegirlKB(kbID) && hasCompleteMoegirlCitations(payload.Citations) {
		return nil
	}
	return validateChunkSourceMetadata(kbID, payload.Chunks)
}

func hasCompleteMoegirlCitations(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var decoded struct {
		CrawlManifest []moegirlCrawlManifestEntry `json:"crawl_manifest"`
	}
	if err := json.Unmarshal(raw, &decoded); err == nil && completeMoegirlCrawlManifest(decoded.CrawlManifest) {
		return true
	}
	citationCount, missing := moegirlCitationMetrics(raw)
	return citationCount > 0 && missing == 0
}

func completeMoegirlCrawlManifest(rows []moegirlCrawlManifestEntry) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if strings.TrimSpace(row.KBID) == "" ||
			strings.TrimSpace(row.SourceName) == "" ||
			strings.TrimSpace(row.SourceURL) == "" ||
			strings.TrimSpace(row.CanonicalURL) == "" ||
			row.PageID == 0 ||
			strings.TrimSpace(row.RevisionID) == "" ||
			strings.TrimSpace(row.Touched) == "" ||
			strings.TrimSpace(row.License) == "" ||
			strings.TrimSpace(row.SourcePolicy) == "" ||
			len(row.Categories) == 0 ||
			strings.TrimSpace(row.FetchedAt) == "" {
			return false
		}
	}
	return true
}

func forbiddenCoreChunkSourceFamily(chunk knowledgePackBuildChunk) (string, string, bool) {
	for _, candidate := range chunkSourceMetadataCandidates(chunk) {
		if family, ok := classifyExternalSourceFamily(candidate.value); ok {
			return family, candidate.field, true
		}
	}
	return "", "", false
}

func chunkSourceMetadataCandidates(chunk knowledgePackBuildChunk) []struct {
	field string
	value string
} {
	return []struct {
		field string
		value string
	}{
		{field: "chunk_id", value: chunk.ChunkID},
		{field: "title", value: chunk.Title},
		{field: "path", value: chunk.Path},
		{field: "source", value: chunk.Source},
		{field: "citation_url", value: chunk.CitationURL},
		{field: "citation_title", value: chunk.CitationTitle},
		{field: "source_name", value: chunk.SourceName},
		{field: "license", value: chunk.License},
		{field: "source_policy", value: chunk.SourcePolicy},
	}
}

func validateMoegirlChunkSourceMetadata(index int, chunk knowledgePackBuildChunk) error {
	required := []struct {
		field string
		value string
	}{
		{field: "citation_url", value: chunk.CitationURL},
		{field: "license", value: chunk.License},
		{field: "source_policy", value: chunk.SourcePolicy},
		{field: "citation_title", value: chunk.CitationTitle},
		{field: "source_name", value: chunk.SourceName},
		{field: "source_revision_id", value: chunk.SourceRevisionID},
		{field: "source_page_id", value: chunk.SourcePageID},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("chunks[%d].%s is required for moegirl source metadata", index, item.field)
		}
	}
	if !isMoegirlSourceURL(chunk.CitationURL) {
		return fmt.Errorf("chunks[%d].citation_url must use zh.moegirl.org.cn for moegirl source metadata", index)
	}
	if !strings.Contains(chunk.License, "CC BY-NC-SA 3.0 CN") {
		return fmt.Errorf("chunks[%d].license must be CC BY-NC-SA 3.0 CN for moegirl source metadata", index)
	}
	policy := strings.ToLower(chunk.SourcePolicy)
	if !(strings.Contains(policy, "summary") || strings.Contains(policy, "faq")) || !strings.Contains(policy, "no full") {
		return fmt.Errorf("chunks[%d].source_policy must be summary/FAQ only with no full article mirror", index)
	}
	if _, err := parseMoegirlPageID(chunk.SourcePageID); err != nil {
		return fmt.Errorf("chunks[%d].source_page_id must be numeric for moegirl source metadata", index)
	}
	return nil
}
