#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

DOC_PATH="docs/rag/maxkb-replacement.md"

if [[ ! -f "$DOC_PATH" ]]; then
  echo "maxkb_validation_failed missing_doc=$DOC_PATH" >&2
  exit 1
fi

required_terms=(
  "MaxKB"
  "Primary candidate"
  "RAGFlow abandoned"
  "WeKnora is not the target"
  "document upload"
  "online document crawling"
  "automatic text splitting"
  "PostgreSQL"
  "pgvector"
  "signed Knowledge Pack"
  "chunk_id"
  "citations.json"
  "prompts.json"
  "fallback candidates"
  "Dify"
  "AnythingLLM"
  "Open WebUI"
  "verification slice"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" "$DOC_PATH" >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "maxkb_validation_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

if rg -n "(PASSWORD|password|API[_ -]?KEY|api[_ -]?key|OAUTH|oauth|SIGNING|signing)[[:space:]]*[:=][[:space:]]*[^[:space:]<]" "$DOC_PATH" >/tmp/maxkb-validation-secret-hits 2>/dev/null; then
  echo "maxkb_validation_failed secret_like_text=$(tr '\n' ';' < /tmp/maxkb-validation-secret-hits | cut -c 1-360)" >&2
  exit 1
fi

echo "maxkb_validation_ok doc=$DOC_PATH"
