#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go test ./internal/server -run 'TestAdminDraftPublishRequiresSuccessfulDryRunForSameContentHash|TestAdminDraftPublishLatestRollbackAndAuditLog|TestAdminPageIsServedByTheKnowledgeBaseService|TestAdminWriteEndpointsRequireBearerToken' -count=1

required_terms=(
  "/publish"
  "successful dry-run build required before publish"
  "dry-run content hash mismatch"
  "draft_publish"
  "rollback_latest"
  "audit.log"
  "content_hash"
  "gate_status"
  "published_at"
  "rollback_at"
  "发布 draft 为 latest"
)

missing=()
for term in "${required_terms[@]}"; do
  if ! rg -F "$term" internal/server >/dev/null; then
    missing+=("$term")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "chunk_studio_publish_rollback_failed missing_terms=$(IFS=,; echo "${missing[*]}")" >&2
  exit 1
fi

echo "chunk_studio_publish_rollback_ok"
