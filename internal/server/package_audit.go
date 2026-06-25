package server

import (
	"fmt"

	"yi-flow/knowledge-base/internal/packaudit"
)

func auditKnowledgePackBeforePublish(manifest []byte, packageBytes []byte) error {
	if _, err := packaudit.Audit(manifest, packageBytes); err != nil {
		return fmt.Errorf("knowledge pack audit failed: %w", err)
	}
	return nil
}
