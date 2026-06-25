#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KB_ID="${MOEGIRL_HITL_KB_ID:-moegirl-acgn-faq}"
DISCOVERY_LIMIT="${MOEGIRL_HITL_DISCOVERY_LIMIT:-360}"
MIN_ACCEPTED="${MOEGIRL_HITL_MIN_ACCEPTED_PAGES:-300}"
VERSION="${MOEGIRL_HITL_VERSION:-$(date -u +%Y.%m.%d.moegirl-hitl-300)}"
BUILD_ROOT="${MOEGIRL_HITL_BUILD_ROOT:-${TMPDIR:-/tmp}/moegirl-hitl-review-bundle}"
BUILD_DIR="$BUILD_ROOT/$VERSION"
STORAGE_DIR="$BUILD_DIR/storage"
SERVER_LOG="$BUILD_DIR/server.log"
MANIFEST_PATH="$BUILD_DIR/manifest.json"
PACKAGE_PATH="$BUILD_DIR/knowledge-pack.zip"
REVIEW_PATH="${MOEGIRL_HITL_REVIEW_OUTPUT:-$BUILD_DIR/moegirl-hitl-review.json}"
BUILD_RESPONSE="$BUILD_DIR/build-response.json"
PORT="${MOEGIRL_HITL_PORT:-$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)}"
BASE_URL="http://127.0.0.1:$PORT"

mkdir -p "$BUILD_DIR"
rm -rf "$STORAGE_DIR"
mkdir -p "$STORAGE_DIR"

KEYGEN_GO="$BUILD_DIR/keygen.go"
cat > "$KEYGEN_GO" <<'GO'
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
)

func main() {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
		"seed":   base64.StdEncoding.EncodeToString(privateKey.Seed()),
		"public": base64.StdEncoding.EncodeToString(publicKey),
	})
}
GO
KEYPAIR_JSON="$(go run "$KEYGEN_GO")"
SIGNING_KEY_BASE64="$(python3 - "$KEYPAIR_JSON" <<'PY'
import json
import sys
print(json.loads(sys.argv[1])["seed"])
PY
)"

cd "$ROOT_DIR"
ADDR="127.0.0.1:$PORT" \
STORAGE_DIR="$STORAGE_DIR" \
ALLOW_EMPTY_ADMIN_TOKEN=1 \
KNOWLEDGE_PACK_SIGNING_KEY_BASE64="$SIGNING_KEY_BASE64" \
MOEGIRL_API_URL="${MOEGIRL_API_URL:-}" \
MOEGIRL_SITEMAP_INDEX_URL="${MOEGIRL_SITEMAP_INDEX_URL:-}" \
MOEGIRL_PUBLIC_ARTICLE_ORIGIN="${MOEGIRL_PUBLIC_ARTICLE_ORIGIN:-}" \
go run ./cmd/server > "$SERVER_LOG" 2>&1 &
SERVER_PID=$!
cleanup() {
  kill "$SERVER_PID" >/dev/null 2>&1 || true
  wait "$SERVER_PID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for _ in $(seq 1 80); do
  if curl -fsS "$BASE_URL/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
if ! curl -fsS "$BASE_URL/healthz" >/dev/null 2>&1; then
  echo "moegirl_hitl_bundle_failed server_not_ready log=$(tr '\n' ' ' < "$SERVER_LOG" | cut -c 1-360)" >&2
  exit 1
fi

REQUEST_PATH="$BUILD_DIR/build-request.json"
python3 - "$VERSION" "$DISCOVERY_LIMIT" "$REQUEST_PATH" <<'PY'
import json
import sys

version, discovery_limit, path = sys.argv[1:4]
payload = {
    "version": version,
    "limit": int(discovery_limit),
    "llm_recommended": ["Qwen3-4B-Q4_K_M"],
}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(payload, handle, ensure_ascii=False, indent=2)
    handle.write("\n")
PY

status="$(
  curl -sS \
    -o "$BUILD_RESPONSE" \
    -w "%{http_code}" \
    -X POST "$BASE_URL/admin/api/kb/$KB_ID/moegirl/build-publish" \
    -H "Content-Type: application/json" \
    --data-binary "@$REQUEST_PATH"
)"
if [[ "$status" != "201" ]]; then
  echo "moegirl_hitl_bundle_failed build_status=$status body=$(tr '\n' ' ' < "$BUILD_RESPONSE" | cut -c 1-500)" >&2
  exit 1
fi

python3 - "$BUILD_RESPONSE" "$DISCOVERY_LIMIT" "$MIN_ACCEPTED" <<'PY'
import json
import sys

path, discovery_limit, min_accepted = sys.argv[1:4]
with open(path, "r", encoding="utf-8") as handle:
    payload = json.load(handle)
accepted = int(payload.get("crawl_report", {}).get("accepted_pages", 0))
chunks = int(payload.get("chunk_count", 0))
if accepted < int(min_accepted):
    raise SystemExit(f"moegirl_hitl_bundle_failed accepted_pages={accepted} required={min_accepted}")
if chunks < accepted * 3:
    raise SystemExit(f"moegirl_hitl_bundle_failed chunks={chunks} accepted_pages={accepted}")
print(f"moegirl_hitl_bundle_build_ok discovery_limit={discovery_limit} accepted_pages={accepted} chunks={chunks}")
PY

curl -fsS "$BASE_URL/kb/$KB_ID/latest/manifest.json" > "$MANIFEST_PATH"
curl -fsS "$BASE_URL/kb/$KB_ID/versions/$VERSION/knowledge-pack.zip" > "$PACKAGE_PATH"

MOEGIRL_REVIEW_MANIFEST="$MANIFEST_PATH" \
MOEGIRL_REVIEW_PACKAGE="$PACKAGE_PATH" \
MOEGIRL_HITL_REVIEW_OUTPUT="$REVIEW_PATH" \
scripts/prepare-moegirl-hitl-review.sh

python3 - "$BUILD_DIR" "$MANIFEST_PATH" "$PACKAGE_PATH" "$REVIEW_PATH" <<'PY'
import json
import os
import sys

build_dir, manifest_path, package_path, review_path = sys.argv[1:5]
with open(review_path, "r", encoding="utf-8") as handle:
    review = json.load(handle)
print(
    "moegirl_hitl_bundle_ready "
    f"dir={build_dir} "
    f"manifest={manifest_path} "
    f"package={package_path} "
    f"review={review_path} "
    f"total_chunks={review.get('total_chunks')} "
    f"samples={len(review.get('sample_chunks', []))} "
    f"questions={len(review.get('golden_questions', []))}"
)
PY
