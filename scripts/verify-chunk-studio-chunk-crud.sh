#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftChunkCRUDRoundTripsThroughPublicAPIs|TestAdminDraftChunkSearchLocalLatencySmoke|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "/chunks"
  "创建 chunk"
  "更新 chunk"
  "复制 chunk"
  "删除 chunk"
  "Chunk search"
  "Review status"
  "unsaved changes"
  "duplicate chunk_id"
  "review_status"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_chunk_crud_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_chunk_crud_ok"
