#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminCan(DryRunWeKnoraExportWithoutPublishingLatest|ExportReviewedWeKnoraChunksAndPublishKnowledgePack)|TestAdminPageIsServedByTheKnowledgeBaseService|TestAdminWeKnoraExportRejectsUnreviewedChunks' -count=1

if rg -n "RAGFlow 知识包发布|https://rag\\.yi-flow\\.com|/ragflow/export-(dry-run|publish)" internal/server/server.go README.md .env.example >/tmp/weknora-lightweight-ragflow-hits 2>/dev/null; then
  echo "weknora_lightweight_replacement_failed active_ragflow_reference=$(tr '\n' ';' < /tmp/weknora-lightweight-ragflow-hits | cut -c 1-360)" >&2
  exit 1
fi

if [[ -n "${WEKNORA_BASE_URL:-}" ]]; then
  scripts/verify-weknora-poc.sh
else
  echo "weknora_remote_smoke_skipped set_WEKNORA_BASE_URL"
fi

echo "weknora_lightweight_replacement_ok"
