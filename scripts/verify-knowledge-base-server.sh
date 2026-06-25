#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$ROOT_DIR"
go test ./...
go build -o /tmp/yi-flow-knowledge-base-verify ./cmd/server
scripts/verify-chunk-studio-mainline.sh
scripts/verify-chunk-studio-draft-workspace.sh
scripts/verify-chunk-studio-chunk-crud.sh
scripts/verify-chunk-studio-source-policy.sh
scripts/verify-chunk-studio-prompts.sh
scripts/verify-weknora-lightweight-replacement.sh
scripts/verify-weknora-dataset-contract.sh
scripts/verify-weknora-pilot-migration.sh
scripts/verify-yi-flow-core-coverage.sh
scripts/verify-moegirl-golden-eval.sh
scripts/prepare-moegirl-hitl-review.sh

echo "knowledge_base_server_ok"
