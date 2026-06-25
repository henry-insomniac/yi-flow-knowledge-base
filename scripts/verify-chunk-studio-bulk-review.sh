#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftBulkImportExportReviewQueueAndGateBoundary|TestAdminDraftReviewReportSamplesThirtyChunksAndCounts|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "/import"
  "/export"
  "/review-queue"
  "/review-report"
  "missing_citation"
  "failed_gate"
  "changed_since_last_publish"
  "sample_count"
  "missing_citation_count"
  "duplicate_count"
  "contamination_count"
  "golden_prompt_count"
  "验证批量导入"
  "导出 draft JSON"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_bulk_review_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_bulk_review_ok"
