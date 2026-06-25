#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftDryRunBuildGeneratesPackagePreviewWithoutPublishingLatest|TestAdminDraftDryRunBuildRequiresPassingQualityGates|TestAdminPageIsServedByTheKnowledgeBaseService' -count=1

required_terms=(
  "build-dry-run"
  "package_hash"
  "manifest"
  "knowledge-pack.zip"
  "chunks.sqlite"
  "citations.json"
  "prompts.json"
  "vector.index"
  "preview_url"
  "quality gates failed"
  "运行 draft dry-run build"
  "draftDryRunBuildReport"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_dry_run_build_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_dry_run_build_ok"
