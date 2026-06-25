#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftChunkCitationMetadataAndSourceAudit|TestBuildPublishUsesChunkCitationMetadataForPreview|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "citation_url"
  "citation_title"
  "source_name"
  "license"
  "source_policy"
  "source-audit"
  "source_family_counts"
  "CC BY-NC-SA 3.0 CN"
  "rejects"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_source_policy_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_source_policy_ok"
