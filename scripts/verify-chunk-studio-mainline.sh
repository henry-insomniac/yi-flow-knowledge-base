#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run TestAdminPageIsServedByTheKnowledgeBaseService -count=1

required_terms=(
  "Chunk Studio"
  "自研 chunk 内容创建和管理后台"
  "manifest.json"
  "knowledge-pack.zip"
  "chunks.sqlite"
  "citations.json"
  "prompts.json"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server/server.go README.md >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_mainline_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

primary_banned_patterns=(
  "RAGFlow 知识包发布"
  "WeKnora 知识包发布"
  "Reviewed WeKnora export JSON"
  "MaxKB 替代"
  "MaxKB is the Primary candidate"
  "管理页主流程已经切换为 WeKnora"
  "https://rag\\.yi-flow\\.com"
  "/ragflow/export-(dry-run|publish)"
)

for pattern in "${primary_banned_patterns[@]}"; do
  if rg -n "$pattern" internal/server/server.go README.md .env.example docs/rag >/tmp/chunk-studio-mainline-hits 2>/dev/null; then
    echo "chunk_studio_mainline_failed external_primary_reference=$(tr '\n' ';' < /tmp/chunk-studio-mainline-hits | cut -c 1-500)" >&2
    exit 1
  fi
done

echo "chunk_studio_mainline_ok"
