#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftChunkListPaginatesThousandChunks|TestAdminDraftBulkImportValidationReturnsFieldErrors|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1
scripts/smoke-chunk-studio-production.sh

required_terms=(
  "limit"
  "offset"
  "next_offset"
  "field_errors"
  "@media (max-width: 720px)"
  "preserveUnsavedDraftOnError"
  "smoke-chunk-studio-production"
  "chunk_studio_smoke_ok"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server scripts >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_admin_ux_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_admin_ux_ok"
