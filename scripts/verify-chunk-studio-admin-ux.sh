#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftChunkListPaginatesThousandChunks|TestAdminDraftChunkUpdateThousandChunkLocalLatencySmoke|TestAdminDraftBulkImportValidationReturnsFieldErrors|TestAdminPageIsServedByTheKnowledgeBaseService|TestAdminPageFollowsProjectDesignSpec|TestAdminPageOrganizesDashboardCategoriesAndSimplifiesChunkCreation' -count=1
scripts/smoke-chunk-studio-production.sh

required_terms=(
  "limit"
  "offset"
  "next_offset"
  "TestAdminDraftChunkUpdateThousandChunkLocalLatencySmoke"
  "field_errors"
  "@media (max-width: 720px)"
  "preserveUnsavedDraftOnError"
  "Airbnb-design-analysis"
  "--color-primary: #ff385c"
  "--radius-full: 9999px"
  "smoke-chunk-studio-production"
  "chunk_studio_smoke_ok"
  "dashboardCategoryNav"
  "dashboard-create"
  "dashboard-review"
  "dashboard-ship"
  "dashboard-inspect"
  "dashboard-operate"
  "Basic chunk fields"
  "Advanced metadata"
  "normalizeDraftChunkForCreate"
  "draftChunkPayloadForCreate"
  "auto-filled chunk_id/path/source"
  "authStatus"
  "Admin token missing"
  "Admin token invalid or missing"
  "Authorization: Bearer <token>"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F -- "$term" internal/server scripts >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_admin_ux_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_admin_ux_ok"
