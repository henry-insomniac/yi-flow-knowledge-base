#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
YIFLOW_CORE_SEED="${YIFLOW_CORE_WEKNORA_SEED:-$ROOT_DIR/knowledge-packs/yi-flow-core/weknora-export.seed.json}"
MOEGIRL_SAMPLE="${MOEGIRL_WEKNORA_SAMPLE:-$ROOT_DIR/knowledge-packs/moegirl-acgn-faq/weknora-export.sample.json}"
BUILD_DIR="${TMPDIR:-/tmp}/weknora-pilot-migration"
PORT="${YIFLOW_WEKNORA_PILOT_PORT:-$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)}"
BASE_URL="http://127.0.0.1:$PORT"
STORAGE_DIR="$BUILD_DIR/storage"
SERVER_LOG="$BUILD_DIR/server.log"

if [[ ! -f "$YIFLOW_CORE_SEED" ]]; then
  echo "weknora_pilot_migration_failed missing_yi_flow_core_seed=$YIFLOW_CORE_SEED" >&2
  exit 1
fi
if [[ ! -f "$MOEGIRL_SAMPLE" ]]; then
  echo "weknora_pilot_migration_failed missing_moegirl_sample=$MOEGIRL_SAMPLE" >&2
  exit 1
fi

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR" "$STORAGE_DIR"

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

python3 - "$BUILD_DIR" "$YIFLOW_CORE_SEED" "$MOEGIRL_SAMPLE" <<'PY'
import json
import os
import sys

build_dir, yi_flow_seed_path, moegirl_sample_path = sys.argv[1:4]

def load_json(path):
    with open(path, "r", encoding="utf-8") as handle:
        return json.load(handle)

def dump_json(path, value):
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(value, handle, ensure_ascii=False, indent=2)
        handle.write("\n")

baseline = {
    "version": "2026.06.25.weknora-baseline",
    "chunks": [{
        "chunk_id": "yi-flow-core-weknora-baseline-001",
        "title": "WeKnora pilot baseline",
        "path": "tests/weknora/baseline",
        "source": "yi-flow-core",
        "content": "baseline active_version before WeKnora pilot migration"
    }],
    "prompts": [{
        "id": "weknora-baseline-check",
        "title": "Verify baseline",
        "question": "What version was active before WeKnora pilot migration?"
    }]
}
dump_json(os.path.join(build_dir, "baseline.json"), baseline)

core = load_json(yi_flow_seed_path)
core["version"] = "2026.06.25.weknora-core-pilot"
dump_json(os.path.join(build_dir, "yi-flow-core-weknora-pilot.json"), core)

moegirl = load_json(moegirl_sample_path)
moegirl["version"] = "2026.06.25.weknora-moegirl-pilot"
dump_json(os.path.join(build_dir, "moegirl-weknora-pilot.json"), moegirl)
PY

cd "$ROOT_DIR"
ADDR="127.0.0.1:$PORT" \
STORAGE_DIR="$STORAGE_DIR" \
ALLOW_EMPTY_ADMIN_TOKEN=1 \
KNOWLEDGE_PACK_SIGNING_KEY_BASE64="$SIGNING_KEY_BASE64" \
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
  echo "weknora_pilot_migration_failed server_not_ready log=$(tr '\n' ' ' < "$SERVER_LOG" | cut -c 1-360)" >&2
  exit 1
fi

post_json() {
  local path="$1"
  local body="$2"
  local out="$3"
  curl -sS -o "$out" -w "%{http_code}" \
    -X POST "$BASE_URL$path" \
    -H "Content-Type: application/json" \
    --data-binary "@$body"
}

expect_status() {
  local actual="$1"
  local expected="$2"
  local label="$3"
  local body="$4"
  if [[ "$actual" != "$expected" ]]; then
    echo "weknora_pilot_migration_failed ${label}_status=$actual body=$(tr '\n' ' ' < "$body" | cut -c 1-360)" >&2
    exit 1
  fi
}

baseline_body="$BUILD_DIR/baseline-response.json"
baseline_status="$(post_json "/admin/api/kb/yi-flow-core/build-publish" "$BUILD_DIR/baseline.json" "$baseline_body")"
expect_status "$baseline_status" "201" "baseline_publish" "$baseline_body"

core_dry_body="$BUILD_DIR/core-dry-run.json"
core_dry_status="$(post_json "/admin/api/kb/yi-flow-core/weknora/export-dry-run" "$BUILD_DIR/yi-flow-core-weknora-pilot.json" "$core_dry_body")"
expect_status "$core_dry_status" "200" "core_dry_run" "$core_dry_body"
python3 - "$core_dry_body" <<'PY'
import json
import sys
payload = json.load(open(sys.argv[1], encoding="utf-8"))
if payload.get("latest") is not False or payload.get("quality_status") != "passed":
    raise SystemExit("weknora_pilot_migration_failed core_dry_run_gate=" + json.dumps(payload, ensure_ascii=False))
print("weknora_core_dry_run_ok version=" + payload.get("version", ""))
PY

core_publish_body="$BUILD_DIR/core-publish.json"
core_publish_status="$(post_json "/admin/api/kb/yi-flow-core/weknora/export-publish" "$BUILD_DIR/yi-flow-core-weknora-pilot.json" "$core_publish_body")"
expect_status "$core_publish_status" "201" "core_publish" "$core_publish_body"

core_manifest="$BUILD_DIR/core-latest-manifest.json"
curl -fsS "$BASE_URL/kb/yi-flow-core/latest/manifest.json" > "$core_manifest"
python3 - "$core_manifest" <<'PY'
import json
import sys
payload = json.load(open(sys.argv[1], encoding="utf-8"))
if payload.get("version") != "2026.06.25.weknora-core-pilot":
    raise SystemExit("weknora_pilot_migration_failed core_latest_version=" + str(payload.get("version")))
print("weknora_core_latest_manifest_ok version=" + payload["version"])
PY
curl -fsS "$BASE_URL/kb/yi-flow-core/versions/2026.06.25.weknora-core-pilot/knowledge-pack.zip" >/dev/null
core_preview="$BUILD_DIR/core-preview.json"
curl -fsS "$BASE_URL/kb/yi-flow-core/latest/preview?limit=5" > "$core_preview"
python3 - "$core_preview" <<'PY'
import json
import sys
payload = json.load(open(sys.argv[1], encoding="utf-8"))
body = json.dumps(payload, ensure_ascii=False)
for term in ["active_version", "knowledge-pack.zip", "Agent 回答链路"]:
    if term not in body:
        raise SystemExit("weknora_pilot_migration_failed core_preview_missing=" + term)
print("weknora_core_preview_ok chunks=" + str(len(payload.get("chunks", []))))
PY

moegirl_dry_body="$BUILD_DIR/moegirl-dry-run.json"
moegirl_dry_status="$(post_json "/admin/api/kb/moegirl-acgn-faq/weknora/export-dry-run" "$BUILD_DIR/moegirl-weknora-pilot.json" "$moegirl_dry_body")"
expect_status "$moegirl_dry_status" "200" "moegirl_dry_run" "$moegirl_dry_body"
python3 - "$moegirl_dry_body" <<'PY'
import json
import sys
payload = json.load(open(sys.argv[1], encoding="utf-8"))
if payload.get("latest") is not False or payload.get("quality_status") != "passed":
    raise SystemExit("weknora_pilot_migration_failed moegirl_dry_run_gate=" + json.dumps(payload, ensure_ascii=False))
print("weknora_moegirl_dry_run_ok version=" + payload.get("version", ""))
PY

moegirl_publish_body="$BUILD_DIR/moegirl-publish.json"
moegirl_publish_status="$(post_json "/admin/api/kb/moegirl-acgn-faq/weknora/export-publish" "$BUILD_DIR/moegirl-weknora-pilot.json" "$moegirl_publish_body")"
expect_status "$moegirl_publish_status" "201" "moegirl_publish" "$moegirl_publish_body"
moegirl_preview="$BUILD_DIR/moegirl-preview.json"
curl -fsS "$BASE_URL/kb/moegirl-acgn-faq/latest/preview?limit=5" > "$moegirl_preview"
python3 - "$moegirl_preview" <<'PY'
import json
import sys
payload = json.load(open(sys.argv[1], encoding="utf-8"))
body = json.dumps(payload, ensure_ascii=False)
for term in ["初音未来", "https://zh.moegirl.org.cn/初音未来", "CC BY-NC-SA 3.0 CN"]:
    if term not in body:
        raise SystemExit("weknora_pilot_migration_failed moegirl_preview_missing=" + term)
print("weknora_moegirl_preview_ok chunks=" + str(len(payload.get("chunks", []))))
PY

rollback_body="$BUILD_DIR/rollback-response.json"
rollback_payload="$BUILD_DIR/rollback.json"
python3 - "$rollback_payload" <<'PY'
import json
import sys
json.dump({"version": "2026.06.25.weknora-baseline"}, open(sys.argv[1], "w", encoding="utf-8"))
PY
rollback_status="$(post_json "/admin/api/kb/yi-flow-core/latest" "$rollback_payload" "$rollback_body")"
expect_status "$rollback_status" "200" "core_rollback" "$rollback_body"
rollback_manifest="$BUILD_DIR/core-rollback-manifest.json"
curl -fsS "$BASE_URL/kb/yi-flow-core/latest/manifest.json" > "$rollback_manifest"
python3 - "$rollback_manifest" <<'PY'
import json
import sys
payload = json.load(open(sys.argv[1], encoding="utf-8"))
if payload.get("version") != "2026.06.25.weknora-baseline":
    raise SystemExit("weknora_pilot_migration_failed rollback_latest_version=" + str(payload.get("version")))
print("weknora_core_rollback_ok version=" + payload["version"])
PY

echo "weknora_pilot_migration_ok"
