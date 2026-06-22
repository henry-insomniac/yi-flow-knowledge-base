#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${WEKNORA_BASE_URL:-http://127.0.0.1:8080}"
API_KEY="${WEKNORA_API_KEY:-}"
KB_ID="${WEKNORA_KB_ID:-}"
QUERY="${WEKNORA_QUERY:-知识包更新路径是什么}"
TIMEOUT_SECONDS="${WEKNORA_TIMEOUT_SECONDS:-10}"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

health_url="${BASE_URL%/}/health"
health_body="$tmp_dir/health.body"
: > "$health_body"
health_status="$(
  curl -sS \
    --max-time "$TIMEOUT_SECONDS" \
    -o "$health_body" \
    -w "%{http_code}" \
    "$health_url" || true
)"

if [[ "$health_status" != "200" ]]; then
  echo "weknora_poc_health_failed url=$health_url status=$health_status body=$(tr '\n' ' ' < "$health_body" | cut -c 1-240)" >&2
  exit 1
fi

if [[ -z "$API_KEY" || -z "$KB_ID" ]]; then
  echo "weknora_poc_health_ok search=skipped reason=missing_WEKNORA_API_KEY_or_WEKNORA_KB_ID"
  exit 0
fi

request_json="$tmp_dir/request.json"
response_json="$tmp_dir/response.json"
: > "$response_json"

python3 - "$QUERY" "$KB_ID" > "$request_json" <<'PY'
import json
import sys

query = sys.argv[1]
kb_id = sys.argv[2]
print(json.dumps({
    "query_text": query,
    "match_count": 5,
    "disable_vector_match": True,
    "disable_keywords_match": False,
    "skip_context_enrichment": True,
}, ensure_ascii=False))
PY

search_url="${BASE_URL%/}/api/v1/knowledge-bases/${KB_ID}/hybrid-search"
search_status="$(
  curl -sS \
    --max-time "$TIMEOUT_SECONDS" \
    -o "$response_json" \
    -w "%{http_code}" \
    -X POST "$search_url" \
    -H "X-API-Key: $API_KEY" \
    -H "Content-Type: application/json" \
    --data-binary "@$request_json" || true
)"

if [[ "$search_status" != "200" ]]; then
  echo "weknora_poc_search_failed url=$search_url status=$search_status body=$(tr '\n' ' ' < "$response_json" | cut -c 1-360)" >&2
  exit 1
fi

python3 - "$response_json" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as handle:
    payload = json.load(handle)

if payload.get("success") is False:
    raise SystemExit(f"weknora_poc_search_failed success=false body={json.dumps(payload, ensure_ascii=False)[:360]}")

data = payload.get("data")
if not isinstance(data, list):
    raise SystemExit("weknora_poc_search_failed data_not_array")

if not data:
    raise SystemExit("weknora_poc_search_empty")

first = data[0]
missing = [key for key in ("id", "content") if not first.get(key)]
if missing:
    raise SystemExit("weknora_poc_search_failed missing_first_result_fields=" + ",".join(missing))

title = first.get("knowledge_title") or first.get("knowledge_filename") or first.get("knowledge_id") or "untitled"
score = first.get("score", "unknown")
print(f"weknora_poc_search_ok count={len(data)} first_id={first.get('id')} first_title={title} first_score={score}")
PY
