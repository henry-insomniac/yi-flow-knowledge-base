#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftPromptCRUDAndPromptPreview|TestBuildPublishExportsPromptMetadata|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "expected_chunk_ids"
  "answerability"
  "answerable"
  "/prompts"
  "Prompts / golden questions"
  "运行 prompt 预览"
  "prompts.json"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_prompts_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_prompts_ok"
