#!/usr/bin/env bash
set -euo pipefail

PUBLIC_RAGFLOW_URL="${RAGFLOW_PUBLIC_URL:-https://rag.yi-flow.com}"
LOCAL_RAGFLOW_HEALTH_URL="${RAGFLOW_LOCAL_HEALTH_URL:-}"
PUBLIC_RAGFLOW_API_HEALTH_URL="${RAGFLOW_PUBLIC_API_HEALTH_URL:-}"
AUTH_HEADER="${RAGFLOW_AUTH_HEADER:-}"
AUTH_COOKIE_HEADER="${RAGFLOW_AUTH_COOKIE_HEADER:-}"
BASE_URL="${KNOWLEDGE_BASE_BASE_URL:-}"
ADMIN_TOKEN="${ADMIN_TOKEN:-}"
KB_ID="${RAGFLOW_SMOKE_KB_ID:-yi-flow-core}"
DATASET_ID="${RAGFLOW_SMOKE_DATASET_ID:-yi-flow-core}"
VERSION="${RAGFLOW_SMOKE_VERSION:-$(date -u +%Y.%m.%d.ragflow-smoke)}"

curl_tls_flags=()
if [[ "${RAGFLOW_ALLOW_INSECURE_TLS:-0}" == "1" ]]; then
  curl_tls_flags=(-k)
fi

auth_curl_headers=()
if [[ -n "$AUTH_HEADER" ]]; then
  auth_curl_headers=(-H "$AUTH_HEADER")
elif [[ -n "$AUTH_COOKIE_HEADER" ]]; then
  auth_curl_headers=(-H "Cookie: $AUTH_COOKIE_HEADER")
fi

if [[ "${RAGFLOW_CHECK_PUBLIC_INGRESS:-0}" == "1" ]]; then
  status="$(curl "${curl_tls_flags[@]}" -sS -o /tmp/ragflow-public-smoke-body -w "%{http_code}" --max-time 15 "$PUBLIC_RAGFLOW_URL" || true)"
  case "$status" in
    200)
      echo "ragflow_public_ingress_failed unauthenticated_status=200 url=$PUBLIC_RAGFLOW_URL" >&2
      exit 1
      ;;
    301|302|303|307|308|401|403)
      echo "ragflow_public_ingress_ok unauthenticated_status=$status url=$PUBLIC_RAGFLOW_URL"
      ;;
    *)
      echo "ragflow_public_ingress_failed unauthenticated_status=$status url=$PUBLIC_RAGFLOW_URL" >&2
      exit 1
      ;;
  esac
else
  echo "ragflow_public_ingress_skipped set_RAGFLOW_CHECK_PUBLIC_INGRESS=1"
fi

if [[ -n "$LOCAL_RAGFLOW_HEALTH_URL" ]]; then
  local_health_status="$(curl -sS -o /tmp/ragflow-local-health-body -w "%{http_code}" --max-time 15 "$LOCAL_RAGFLOW_HEALTH_URL" || true)"
  if [[ "$local_health_status" =~ ^2[0-9][0-9]$ ]]; then
    echo "ragflow_local_health_ok status=$local_health_status"
  else
    echo "ragflow_local_health_failed status=$local_health_status body=$(tr '\n' ' ' < /tmp/ragflow-local-health-body | cut -c 1-240)" >&2
    exit 1
  fi
else
  echo "ragflow_local_health_skipped set_RAGFLOW_LOCAL_HEALTH_URL"
fi

if [[ "${#auth_curl_headers[@]}" -gt 0 ]]; then
  auth_status="$(curl "${curl_tls_flags[@]}" -sS -o /tmp/ragflow-auth-smoke-body -w "%{http_code}" --max-time 15 "${auth_curl_headers[@]}" "$PUBLIC_RAGFLOW_URL" || true)"
  if [[ "$auth_status" =~ ^2[0-9][0-9]$ ]]; then
    echo "ragflow_authenticated_ingress_ok status=$auth_status"
  else
    echo "ragflow_authenticated_ingress_failed status=$auth_status" >&2
    exit 1
  fi
else
  echo "ragflow_authenticated_ingress_skipped set_RAGFLOW_AUTH_HEADER_or_RAGFLOW_AUTH_COOKIE_HEADER"
fi

if [[ -n "$PUBLIC_RAGFLOW_API_HEALTH_URL" ]]; then
  api_status="$(curl "${curl_tls_flags[@]}" -sS -o /tmp/ragflow-public-api-health-body -w "%{http_code}" --max-time 15 "${auth_curl_headers[@]}" "$PUBLIC_RAGFLOW_API_HEALTH_URL" || true)"
  if [[ "$api_status" =~ ^2[0-9][0-9]$ ]]; then
    echo "ragflow_public_api_health_ok status=$api_status"
  else
    echo "ragflow_public_api_health_failed status=$api_status body=$(tr '\n' ' ' < /tmp/ragflow-public-api-health-body | cut -c 1-240)" >&2
    exit 1
  fi
else
  echo "ragflow_public_api_health_skipped set_RAGFLOW_PUBLIC_API_HEALTH_URL"
fi

if [[ -z "$BASE_URL" || -z "$ADMIN_TOKEN" ]]; then
  echo "ragflow_export_smoke_skipped reason=missing_KNOWLEDGE_BASE_BASE_URL_or_ADMIN_TOKEN"
  exit 0
fi

payload="$(mktemp)"
dry_run_response="$(mktemp)"
publish_response="$(mktemp)"
preview_response="$(mktemp)"
trap 'rm -f "$payload" "$dry_run_response" "$publish_response" "$preview_response"' EXIT

python3 - "$VERSION" "$DATASET_ID" > "$payload" <<'PY'
import json
import sys

version, dataset_id = sys.argv[1], sys.argv[2]
json.dump({
    "version": version,
    "dataset_id": dataset_id,
    "llm_recommended": ["Qwen3-4B-Q4_K_M"],
}, sys.stdout, ensure_ascii=False)
PY

dry_status="$(curl -sS -o "$dry_run_response" -w "%{http_code}" \
  -X POST "$BASE_URL/admin/api/kb/$KB_ID/ragflow/export-dry-run" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary "@$payload")"

if [[ "$dry_status" != "200" ]]; then
  echo "ragflow_export_dry_run_failed status=$dry_status body=$(tr '\n' ' ' < "$dry_run_response" | cut -c 1-360)" >&2
  exit 1
fi

python3 - "$dry_run_response" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    payload = json.load(handle)

if payload.get("latest") is not False:
    raise SystemExit("ragflow_export_dry_run_failed latest_not_false")
if payload.get("quality_status") != "passed":
    raise SystemExit(f"ragflow_export_dry_run_failed quality_status={payload.get('quality_status')}")
if not str(payload.get("package_hash", "")).startswith("sha256:"):
    raise SystemExit("ragflow_export_dry_run_failed missing_package_hash")
report = payload.get("quality_report")
if not isinstance(report, dict) or report.get("status") != "passed":
    raise SystemExit("ragflow_export_dry_run_failed invalid_quality_report")
checks = report.get("checks")
if not isinstance(checks, list) or len(checks) < 4:
    raise SystemExit("ragflow_export_dry_run_failed missing_quality_checks")
print(
    "ragflow_export_dry_run_ok "
    f"version={payload.get('version')} "
    f"chunks={payload.get('chunk_count')} "
    f"citations={payload.get('citation_count')}"
)
PY

if [[ "${RAGFLOW_SMOKE_PUBLISH:-0}" != "1" ]]; then
  echo "ragflow_publish_skipped set_RAGFLOW_SMOKE_PUBLISH=1"
  exit 0
fi

publish_status="$(curl -sS -o "$publish_response" -w "%{http_code}" \
  -X POST "$BASE_URL/admin/api/kb/$KB_ID/ragflow/export-publish" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary "@$payload")"

if [[ "$publish_status" != "201" ]]; then
  echo "ragflow_export_publish_failed status=$publish_status body=$(tr '\n' ' ' < "$publish_response" | cut -c 1-360)" >&2
  exit 1
fi
echo "ragflow_export_publish_ok version=$VERSION"

preview_status="$(curl -sS -o "$preview_response" -w "%{http_code}" "$BASE_URL/kb/$KB_ID/latest/preview?limit=5")"
if [[ "$preview_status" != "200" ]]; then
  echo "ragflow_latest_preview_failed status=$preview_status body=$(tr '\n' ' ' < "$preview_response" | cut -c 1-360)" >&2
  exit 1
fi
python3 - "$preview_response" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    payload = json.load(handle)
chunks = payload.get("chunks")
if not isinstance(chunks, list) or not chunks:
    raise SystemExit("ragflow_latest_preview_failed chunks_empty")
print(f"ragflow_latest_preview_ok chunks={len(chunks)}")
PY

if [[ -n "${RAGFLOW_ROLLBACK_VERSION:-}" ]]; then
  rollback_payload="$(mktemp)"
  trap 'rm -f "$payload" "$dry_run_response" "$publish_response" "$preview_response" "$rollback_payload"' EXIT
  python3 - "$RAGFLOW_ROLLBACK_VERSION" > "$rollback_payload" <<'PY'
import json
import sys
json.dump({"version": sys.argv[1]}, sys.stdout)
PY
  rollback_status="$(curl -sS -o /tmp/ragflow-rollback-smoke-body -w "%{http_code}" \
    -X POST "$BASE_URL/admin/api/kb/$KB_ID/latest" \
    -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    --data-binary "@$rollback_payload")"
  if [[ "$rollback_status" != "200" ]]; then
    echo "ragflow_rollback_failed status=$rollback_status body=$(tr '\n' ' ' < /tmp/ragflow-rollback-smoke-body | cut -c 1-360)" >&2
    exit 1
  fi
  echo "ragflow_rollback_ok version=$RAGFLOW_ROLLBACK_VERSION"
else
  echo "ragflow_rollback_skipped reason=missing_RAGFLOW_ROLLBACK_VERSION"
fi

echo "ragflow_replacement_smoke_ok"
