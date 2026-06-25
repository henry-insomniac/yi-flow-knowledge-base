#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftRetrievalPreviewFindsTopKWithoutPublishingLatest|TestAdminDraftRetrievalPreviewLocalLatencySmoke|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "retrieval-preview"
  "matched_terms"
  "missing_citation"
  "weak_score"
  "empty_retrieval"
  "Draft retrieval preview"
  "运行 draft retrieval preview"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_draft_retrieval_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_draft_retrieval_ok"
