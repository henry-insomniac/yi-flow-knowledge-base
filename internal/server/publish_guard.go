package server

import (
	"fmt"

	"yi-flow/knowledge-base/internal/sourcepolicy"
)

func validateBuildPublishBoundary(kbID string, payload buildPublishRequest) error {
	if kbID != "yi-flow-core" {
		return nil
	}

	if family, field, ok := forbiddenCoreSourceFamily(payload); ok {
		return fmt.Errorf("kb_id %q rejects %s source family in %s", kbID, family, field)
	}
	return nil
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
