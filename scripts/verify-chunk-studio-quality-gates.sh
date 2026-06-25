#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftQualityGatesReportFailuresAndMetrics|TestAdminDraftQualityGatesLocalLatencySmoke|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "quality-gates"
  "block_publish"
  "required_fields"
  "duplicate_chunk_ids"
  "near_duplicate_content"
  "invalid_lengths"
  "missing_citations"
  "contamination"
  "prompt_references"
  "golden_eval"
  "top5_hit_rate"
  "citation_rate"
  "duplicate_answer_rate"
  "refusal_pass_rate"
  "missing_citation_count"
  "unsupported_entity_count"
  "运行 quality gates"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_quality_gates_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_quality_gates_ok"
