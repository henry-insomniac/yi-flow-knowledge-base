#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$ROOT_DIR"
go test ./...
go build -o /tmp/yi-flow-knowledge-base-verify ./cmd/server
if docker compose version >/dev/null 2>&1; then
  RAGFLOW_IMAGE=infiniflow/ragflow:v0.26.1 docker compose -f deploy/ragflow/docker-compose.auth-gate.example.yml config >/tmp/ragflow-auth-gate-compose.yml
  echo "ragflow_auth_gate_compose_config_ok"
else
  echo "ragflow_auth_gate_compose_config_skipped reason=missing_docker_compose"
fi
scripts/verify-ragflow-deploy-templates.sh
scripts/verify-yi-flow-core-coverage.sh
scripts/verify-moegirl-golden-eval.sh
scripts/prepare-moegirl-hitl-review.sh
RAGFLOW_SEED_OUTPUT_DIR="${TMPDIR:-/tmp}/ragflow-seed-verify" scripts/prepare-ragflow-seed.sh
scripts/verify-ragflow-replacement-smoke.sh

echo "knowledge_base_server_ok"
