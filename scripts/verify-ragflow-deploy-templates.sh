#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY_DIR="$ROOT_DIR/deploy/ragflow"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.auth-gate.example.yml"
CADDY_FILE="$DEPLOY_DIR/Caddyfile.example"
OAUTH_FILE="$DEPLOY_DIR/oauth2-proxy.env.example"
RUNBOOK_FILE="$ROOT_DIR/docs/rag/ragflow-replacement.md"

python3 - "$COMPOSE_FILE" "$CADDY_FILE" "$OAUTH_FILE" "$RUNBOOK_FILE" <<'PY'
import re
import sys
from pathlib import Path

compose_path, caddy_path, oauth_path, runbook_path = [Path(path) for path in sys.argv[1:]]
for path in (compose_path, caddy_path, oauth_path, runbook_path):
    if not path.exists():
        raise SystemExit(f"ragflow_deploy_templates_failed missing={path}")

compose = compose_path.read_text(encoding="utf-8")
caddy = caddy_path.read_text(encoding="utf-8")
oauth = oauth_path.read_text(encoding="utf-8")
runbook = runbook_path.read_text(encoding="utf-8")

if '"80:80"' not in compose or '"443:443"' not in compose:
    raise SystemExit("ragflow_deploy_templates_failed caddy_public_ports_missing")

ragflow_block_match = re.search(r"(?ms)^  ragflow:\n(?P<body>.*?)(?:\n\S|\Z)", compose)
if not ragflow_block_match:
    raise SystemExit("ragflow_deploy_templates_failed ragflow_service_missing")
ragflow_block = ragflow_block_match.group("body")
if re.search(r"(?m)^    ports:", ragflow_block):
    raise SystemExit("ragflow_deploy_templates_failed ragflow_ports_must_not_be_public")
if "expose:" not in ragflow_block:
    raise SystemExit("ragflow_deploy_templates_failed ragflow_expose_missing")
if "${RAGFLOW_IMAGE:?set RAGFLOW_IMAGE}" not in ragflow_block:
    raise SystemExit("ragflow_deploy_templates_failed ragflow_image_pin_env_missing")

required_caddy = [
    "Strict-Transport-Security",
    "X-Content-Type-Options",
    "X-Frame-Options",
    "max_size",
    "read_timeout",
    "write_timeout",
    "dial_timeout",
    "reverse_proxy oauth2-proxy:4180",
]
for needle in required_caddy:
    if needle not in caddy:
        raise SystemExit(f"ragflow_deploy_templates_failed caddy_missing={needle}")

required_oauth = [
    "OAUTH2_PROXY_PROVIDER=oidc",
    "OAUTH2_PROXY_CLIENT_ID=ragflow-admin",
    "OAUTH2_PROXY_REDIRECT_URL=https://rag.yi-flow.com/oauth2/callback",
    "OAUTH2_PROXY_OIDC_ISSUER_URL=https://auth.yi-flow.com",
    "OAUTH2_PROXY_UPSTREAMS=http://ragflow:80",
]
for needle in required_oauth:
    if needle not in oauth:
        raise SystemExit(f"ragflow_deploy_templates_failed oauth_missing={needle}")

combined = "\n".join((compose, caddy, oauth, runbook))
secret_patterns = {
    "root_ssh_target": r"root@\d+\.\d+\.\d+\.\d+",
    "openai_key": r"sk-[A-Za-z0-9_-]{12,}",
    "github_token": r"ghp_[A-Za-z0-9_]{12,}",
    "inline_password_assignment": r"(?i)(password|passwd|pwd)\s*[:=]\s*['\"]?[^\s'\"<>]{8,}",
}
for name, pattern in secret_patterns.items():
    if re.search(pattern, combined):
        raise SystemExit(f"ragflow_deploy_templates_failed secret_like_value={name}")

required_runbook_terms = [
    "docker compose up -d",
    "docker compose down",
    "docker compose logs",
    "backup",
    "restore",
    "monitoring",
    "secrets",
    "rollback",
]
lower_runbook = runbook.lower()
for term in required_runbook_terms:
    if term not in lower_runbook:
        raise SystemExit(f"ragflow_deploy_templates_failed runbook_missing={term}")

print("ragflow_deploy_templates_ok")
PY
