#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${RAG_GATEWAY_BASE_URL:-http://127.0.0.1:18085}"
TOKEN="${RAG_GATEWAY_TOKEN:-}"
KB_ID="${RAG_GATEWAY_KB_ID:-yi-flow-core}"
QUERY="${RAG_GATEWAY_QUERY:-知识包更新路径是什么}"
TOP_K="${RAG_GATEWAY_TOP_K:-5}"
TIMEOUT_SECONDS="${RAG_GATEWAY_TIMEOUT_SECONDS:-10}"

if [[ -z "$TOKEN" ]]; then
  echo "weknora_gateway_smoke_skipped reason=missing_RAG_GATEWAY_TOKEN"
  exit 0
fi

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

request_json="$tmp_dir/request.json"
response_json="$tmp_dir/response.json"

python3 - "$KB_ID" "$QUERY" "$TOP_K" > "$request_json" <<'PY'
import json
import sys

kb_id = sys.argv[1]
query = sys.argv[2]
top_k = int(sys.argv[3])
print(json.dumps({
    "kb_id": kb_id,
    "query": query,
    "top_k": top_k,
    "mode": "hybrid",
}, ensure_ascii=False))
PY

status="$(
  curl -sS \
    --max-time "$TIMEOUT_SECONDS" \
    -o "$response_json" \
    -w "%{http_code}" \
    -X POST "${BASE_URL%/}/rag/api/query" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    --data-binary "@$request_json" || true
)"

if [[ "$status" != "200" ]]; then
  echo "weknora_gateway_smoke_failed status=$status body=$(tr '\n' ' ' < "$response_json" | cut -c 1-360)" >&2
  exit 1
fi

python3 - "$response_json" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as handle:
    payload = json.load(handle)

if payload.get("provider") != "weknora":
    raise SystemExit("weknora_gateway_smoke_failed provider_not_weknora")

status = payload.get("status")
chunks = payload.get("chunks")
if status not in {"ok", "empty_result"}:
    raise SystemExit(f"weknora_gateway_smoke_failed unexpected_status={status}")
if not isinstance(chunks, list):
    raise SystemExit("weknora_gateway_smoke_failed chunks_not_array")

if status == "ok" and not chunks:
    raise SystemExit("weknora_gateway_smoke_failed ok_without_chunks")

first = chunks[0].get("chunk_id") if chunks else "none"
print(f"weknora_gateway_smoke_ok status={status} chunks={len(chunks)} first_chunk={first}")
PY
