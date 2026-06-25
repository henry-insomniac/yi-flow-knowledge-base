#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminCanImportMoegirlFAQDraftForManualReviewWithoutPublishing|TestAdminMoegirlDraftReviewFlagsFullArticleMirrorSuspects|TestAdminMoegirlBuildHandlesThreeHundredPageMVPWithBoundedBatches|TestAdminPageIsServedByTheKnowledgeBaseService|TestAdminWriteEndpointsRequireBearerToken' -count=1

required_terms=(
  "moegirl/import-draft"
  "moegirl-review"
  "full_mirror_suspect_count"
  "full_mirror_suspect_chunk_ids"
  "accepted_pages_required"
  "faq_chunks_required"
  "golden_questions_required"
  "ready_for_hitl"
  "needs_review"
  "no full-article mirror"
  "导入 Moegirl FAQ draft"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_moegirl_faq_workflow_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_moegirl_faq_workflow_ok"
