#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminCanSaveAndPreviewDraftChunkWithoutPublishingLatest|TestAdminDraftSaveLocalLatencySmoke|TestAdminWriteEndpointsRequireBearerToken|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "/drafts/"
  "保存草稿"
  "预览草稿 chunk"
  "Draft workspace status"
  "draft.json"
  "status"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_draft_workspace_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_draft_workspace_ok"
