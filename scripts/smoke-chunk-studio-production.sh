#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

tmpdir="$(mktemp -d)"
server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmpdir"
}
trap cleanup EXIT

port="${CHUNK_STUDIO_SMOKE_PORT:-}"
if [[ -z "$port" ]]; then
  if command -v python3 >/dev/null 2>&1; then
    port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
  else
    port="18083"
  fi
fi

base_url="${CHUNK_STUDIO_SMOKE_BASE_URL:-http://127.0.0.1:${port}}"
admin_token="chunk-studio-smoke-token-$RANDOM-$$"
auth_header="Authorization: Bearer ${admin_token}"
signing_key_base64="MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
kb_id="chunk-studio-smoke"
version_one="2026.06.26.smoke.001"
version_two="2026.06.26.smoke.002"

if [[ -z "${CHUNK_STUDIO_SMOKE_BASE_URL:-}" ]]; then
  go build -o "$tmpdir/yi-flow-knowledge-base-server" ./cmd/server
  STORAGE_DIR="$tmpdir/storage" \
    ADDR="127.0.0.1:${port}" \
    ADMIN_TOKEN="$admin_token" \
    KNOWLEDGE_PACK_SIGNING_KEY_BASE64="$signing_key_base64" \
    "$tmpdir/yi-flow-knowledge-base-server" >"$tmpdir/server.log" 2>&1 &
  server_pid="$!"
fi

wait_for_health() {
  for _ in $(seq 1 80); do
    if curl -fsS "$base_url/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  echo "chunk_studio_smoke_failed health_timeout" >&2
  if [[ -f "$tmpdir/server.log" ]]; then
    sed -n '1,80p' "$tmpdir/server.log" >&2
  fi
  exit 1
}

curl_admin() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -fsS -X "$method" "$base_url$path" -H "$auth_header" -H "Content-Type: application/json" --data-binary "@$body"
  else
    curl -fsS -X "$method" "$base_url$path" -H "$auth_header"
  fi
}

assert_file_contains() {
  local file="$1"
  local expected="$2"
  if ! grep -F "$expected" "$file" >/dev/null; then
    echo "chunk_studio_smoke_failed missing=${expected}" >&2
    sed -n '1,120p' "$file" >&2
    exit 1
  fi
}

write_draft_body() {
  local version="$1"
  local title_suffix="$2"
  local file="$3"
  cat >"$file" <<JSON
{
  "chunks": [
    {
      "chunk_id": "smoke-routing-${version}",
      "title": "Chunk Studio Smoke Routing ${title_suffix}",
      "path": "smoke/routing/${version}",
      "source": "manual",
      "content": "Chunk Studio smoke routing knowledge verifies draft creation preview quality gates dry run publish rollback and admin pagination using stable searchable terms.",
      "tags": ["smoke", "chunk-studio"],
      "review_status": "approved",
      "citation_url": "https://yi-flow.com/docs/chunk-studio-smoke",
      "citation_title": "Chunk Studio Smoke Source",
      "source_name": "yi-flow smoke",
      "license": "reviewed internal knowledge",
      "source_policy": "reviewed smoke chunks only"
    }
  ],
  "prompts": [
    {
      "id": "smoke-answerable-${version}",
      "title": "Smoke answerable",
      "question": "Chunk Studio smoke routing knowledge",
      "expected_chunk_ids": ["smoke-routing-${version}"],
      "answerability": "answerable",
      "answerable": true
    },
    {
      "id": "smoke-refusal-${version}",
      "title": "Smoke refusal",
      "question": "unrelated tokyo weather forecast zqx",
      "expected_chunk_ids": [],
      "answerability": "refusal",
      "answerable": false
    }
  ],
  "citations": {
    "citations": [
      {
        "chunk_id": "smoke-routing-${version}",
        "url": "https://yi-flow.com/docs/chunk-studio-smoke",
        "title": "Chunk Studio Smoke Source",
        "license": "reviewed internal knowledge",
        "source_policy": "reviewed smoke chunks only"
      }
    ]
  }
}
JSON
}

wait_for_health
echo "chunk_studio_smoke_health_ok"

admin_html="$tmpdir/admin.html"
curl -fsS "$base_url/admin/" -o "$admin_html"
assert_file_contains "$admin_html" "Chunk Studio"
if grep -E 'chunk-studio-smoke-token|ADMIN_TOKEN|KNOWLEDGE_PACK_SIGNING_KEY_BASE64' "$admin_html" >/dev/null; then
  echo "chunk_studio_smoke_failed secret_in_admin_html" >&2
  exit 1
fi
echo "chunk_studio_smoke_admin_ok"

draft_one="$tmpdir/draft-one.json"
write_draft_body "$version_one" "one" "$draft_one"
curl_admin PUT "/admin/api/kb/${kb_id}/drafts/${version_one}" "$draft_one" >"$tmpdir/save-one.json"
assert_file_contains "$tmpdir/save-one.json" "\"chunk_count\":1"

create_chunk="$tmpdir/create-chunk.json"
cat >"$create_chunk" <<JSON
{
  "chunk_id": "smoke-crud-extra",
  "title": "Smoke CRUD Extra",
  "path": "smoke/crud/extra",
  "source": "manual",
  "content": "Smoke CRUD extra chunk validates create update list and delete operations without changing the main publish fixture.",
  "review_status": "needs_review",
  "citation_url": "https://yi-flow.com/docs/chunk-studio-smoke-crud",
  "citation_title": "Chunk Studio CRUD Smoke",
  "source_name": "yi-flow smoke",
  "license": "reviewed internal knowledge",
  "source_policy": "reviewed smoke chunks only"
}
JSON
curl_admin POST "/admin/api/kb/${kb_id}/drafts/${version_one}/chunks" "$create_chunk" >"$tmpdir/create.json"
assert_file_contains "$tmpdir/create.json" "smoke-crud-extra"
curl_admin PUT "/admin/api/kb/${kb_id}/drafts/${version_one}/chunks/smoke-crud-extra" "$create_chunk" >"$tmpdir/update.json"
curl_admin GET "/admin/api/kb/${kb_id}/drafts/${version_one}/chunks?limit=1&offset=1" >"$tmpdir/list-page.json"
assert_file_contains "$tmpdir/list-page.json" "\"limit\":1"
assert_file_contains "$tmpdir/list-page.json" "\"offset\":1"
curl_admin DELETE "/admin/api/kb/${kb_id}/drafts/${version_one}/chunks/smoke-crud-extra" >"$tmpdir/delete.json"
echo "chunk_studio_smoke_draft_crud_ok"

curl_admin GET "/admin/api/kb/${kb_id}/drafts/${version_one}/preview?limit=3" >"$tmpdir/preview.json"
assert_file_contains "$tmpdir/preview.json" "smoke-routing-${version_one}"
curl_admin POST "/admin/api/kb/${kb_id}/drafts/${version_one}/quality-gates" >"$tmpdir/gate-one.json"
assert_file_contains "$tmpdir/gate-one.json" "\"status\":\"passed\""
curl_admin POST "/admin/api/kb/${kb_id}/drafts/${version_one}/build-dry-run?limit=3" >"$tmpdir/dry-run-one.json"
assert_file_contains "$tmpdir/dry-run-one.json" "\"package_hash\""
curl_admin POST "/admin/api/kb/${kb_id}/drafts/${version_one}/publish" >"$tmpdir/publish-one.json"
assert_file_contains "$tmpdir/publish-one.json" "\"latest\":true"
echo "chunk_studio_smoke_publish_one_ok"

draft_two="$tmpdir/draft-two.json"
write_draft_body "$version_two" "two" "$draft_two"
curl_admin PUT "/admin/api/kb/${kb_id}/drafts/${version_two}" "$draft_two" >/dev/null
curl_admin POST "/admin/api/kb/${kb_id}/drafts/${version_two}/quality-gates" >"$tmpdir/gate-two.json"
assert_file_contains "$tmpdir/gate-two.json" "\"status\":\"passed\""
curl_admin POST "/admin/api/kb/${kb_id}/drafts/${version_two}/build-dry-run?limit=3" >/dev/null
curl_admin POST "/admin/api/kb/${kb_id}/drafts/${version_two}/publish" >"$tmpdir/publish-two.json"
assert_file_contains "$tmpdir/publish-two.json" "\"latest\":true"

rollback_body="$tmpdir/rollback.json"
cat >"$rollback_body" <<JSON
{"version":"${version_one}"}
JSON
curl_admin POST "/admin/api/kb/${kb_id}/latest" "$rollback_body" >"$tmpdir/rollback-response.json"
assert_file_contains "$tmpdir/rollback-response.json" "\"version\":\"${version_one}\""
curl -fsS "$base_url/kb/${kb_id}/latest/manifest.json" -o "$tmpdir/latest-manifest.json"
assert_file_contains "$tmpdir/latest-manifest.json" "\"version\": \"${version_one}\""
echo "chunk_studio_smoke_rollback_ok"

if grep -R -E 'chunk-studio-smoke-token|ADMIN_TOKEN|KNOWLEDGE_PACK_SIGNING_KEY_BASE64' "$tmpdir" --exclude='yi-flow-knowledge-base-server' >/dev/null 2>&1; then
  echo "chunk_studio_smoke_failed secret_in_smoke_artifacts" >&2
  exit 1
fi

echo "chunk_studio_smoke_ok"
