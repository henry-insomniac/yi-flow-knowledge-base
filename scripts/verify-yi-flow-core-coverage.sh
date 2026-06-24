#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="${YIFLOW_KNOWLEDGE_APP_DIR:-$(cd "$ROOT_DIR/../yi-flow-knowledge-app" && pwd)}"
SOURCE_DIR="${YIFLOW_CORE_SOURCE_DIR:-$ROOT_DIR/knowledge-packs/yi-flow-core}"
BUILD_DIR="${TMPDIR:-/tmp}/yi-flow-core-coverage"
VERSION="${YIFLOW_CORE_COVERAGE_VERSION:-2026.06.24.coverage}"
PORT="${YIFLOW_KB_COVERAGE_PORT:-$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)}"
BASE_URL="http://127.0.0.1:$PORT"
PAYLOAD="$BUILD_DIR/build-publish.json"
REPORT="$BUILD_DIR/coverage-report.json"
SERVER_LOG="$BUILD_DIR/server.log"
STORAGE_DIR="$BUILD_DIR/storage"

mkdir -p "$BUILD_DIR"
rm -rf "$STORAGE_DIR"
mkdir -p "$STORAGE_DIR"

python3 - "$SOURCE_DIR" "$VERSION" "$PAYLOAD" "$REPORT" <<'PY'
import json
import os
import sys

source_dir, version, payload_path, report_path = sys.argv[1:5]
required_files = {
    "chunks": os.path.join(source_dir, "chunks.json"),
    "prompts": os.path.join(source_dir, "prompts.json"),
    "citations": os.path.join(source_dir, "citations.json"),
    "coverage": os.path.join(source_dir, "coverage-matrix.json"),
}
missing_files = [path for path in required_files.values() if not os.path.exists(path)]
if missing_files:
    raise SystemExit("yi_flow_core_coverage_failed missing_manual_sources=" + ",".join(missing_files))

generated_names = {
    "active_version",
    "chunks.sqlite",
    "knowledge-pack.zip",
    "manifest.json",
    "package.sha256.digest",
    "signature.txt",
    "vector.index",
}
generated_in_source = sorted(name for name in os.listdir(source_dir) if name in generated_names)
if generated_in_source:
    raise SystemExit("yi_flow_core_coverage_failed generated_files_in_manual_source=" + ",".join(generated_in_source))

def load_json(path):
    with open(path, "r", encoding="utf-8") as handle:
        return json.load(handle)

def unwrap_array(value, key):
    if isinstance(value, list):
        return value
    if isinstance(value, dict) and isinstance(value.get(key), list):
        return value[key]
    raise SystemExit(f"yi_flow_core_coverage_failed {key}_must_be_array")

chunks = unwrap_array(load_json(required_files["chunks"]), "chunks")
prompts = unwrap_array(load_json(required_files["prompts"]), "prompts")
citations = load_json(required_files["citations"])
coverage_root = load_json(required_files["coverage"])
coverage = unwrap_array(coverage_root, "questions")

if len(coverage) < 20:
    raise SystemExit(f"yi_flow_core_coverage_failed coverage_questions={len(coverage)} required=20")
if len(prompts) < 20:
    raise SystemExit(f"yi_flow_core_coverage_failed prompts={len(prompts)} required=20")

allowed_statuses = {"present", "missing", "stale", "ambiguous"}
chunk_ids = {chunk.get("chunk_id") for chunk in chunks}
source_linked_chunks = [
    chunk for chunk in chunks
    if chunk.get("chunk_id")
    and chunk.get("title")
    and chunk.get("path")
    and chunk.get("source")
    and chunk.get("content")
]
if len(source_linked_chunks) < 5:
    raise SystemExit(f"yi_flow_core_coverage_failed source_linked_chunks={len(source_linked_chunks)} required=5")

required_update_keys = {
    "update_path": "知识包更新路径",
    "publish_flow": "发布流程",
    "latest_manifest": "latest manifest",
    "package_zip": "knowledge_pack.zip",
    "active_version": "active_version",
}
update_key_hits = {key: 0 for key in required_update_keys}
status_counts = {status: 0 for status in allowed_statuses}

for index, row in enumerate(coverage):
    question = str(row.get("question", "")).strip()
    status = row.get("status")
    row_chunk_ids = row.get("chunk_ids")
    if not question:
        raise SystemExit(f"yi_flow_core_coverage_failed coverage[{index}].question_required")
    if status not in allowed_statuses:
        raise SystemExit(f"yi_flow_core_coverage_failed coverage[{index}].status={status}")
    status_counts[status] += 1
    if status == "present":
        if not isinstance(row_chunk_ids, list) or not row_chunk_ids:
            raise SystemExit(f"yi_flow_core_coverage_failed coverage[{index}].present_without_chunk_ids")
        missing_chunk_ids = [chunk_id for chunk_id in row_chunk_ids if chunk_id not in chunk_ids]
        if missing_chunk_ids:
            raise SystemExit(
                f"yi_flow_core_coverage_failed coverage[{index}].unknown_chunk_ids="
                + ",".join(missing_chunk_ids)
            )
    coverage_key = row.get("coverage_key")
    if coverage_key in update_key_hits and status == "present":
        update_key_hits[coverage_key] += 1

missing_update_keys = [
    f"{key}:{label}"
    for key, label in required_update_keys.items()
    if update_key_hits[key] == 0
]
if missing_update_keys:
    raise SystemExit("yi_flow_core_coverage_failed missing_update_coverage=" + ",".join(missing_update_keys))

payload = {
    "version": version,
    "chunks": chunks,
    "prompts": prompts,
    "citations": citations,
    "llm_recommended": ["Qwen3-4B-Q4_K_M"],
}
with open(payload_path, "w", encoding="utf-8") as handle:
    json.dump(payload, handle, ensure_ascii=False, indent=2)
    handle.write("\n")
with open(report_path, "w", encoding="utf-8") as handle:
    json.dump({
        "coverage_questions": len(coverage),
        "chunks": len(chunks),
        "prompts": len(prompts),
        "source_linked_chunks": len(source_linked_chunks),
        "status_counts": status_counts,
        "update_key_hits": update_key_hits,
    }, handle, ensure_ascii=False, sort_keys=True)
    handle.write("\n")
PY

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
TRUSTED_PUBLIC_KEY_BASE64="$(python3 - "$KEYPAIR_JSON" <<'PY'
import json
import sys
print(json.loads(sys.argv[1])["public"])
PY
)"

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
  echo "yi_flow_core_coverage_failed server_not_ready log=$(tr '\n' ' ' < "$SERVER_LOG" | cut -c 1-360)" >&2
  exit 1
fi

publish_body="$BUILD_DIR/publish-response.json"
publish_status="$(
  curl -sS \
    -o "$publish_body" \
    -w "%{http_code}" \
    -X POST "$BASE_URL/admin/api/kb/yi-flow-core/build-publish" \
    -H "Content-Type: application/json" \
    --data-binary "@$PAYLOAD"
)"
if [[ "$publish_status" != "201" ]]; then
  echo "yi_flow_core_coverage_failed publish_status=$publish_status body=$(tr '\n' ' ' < "$publish_body" | cut -c 1-360)" >&2
  exit 1
fi

manifest_json="$BUILD_DIR/latest-manifest.json"
preview_json="$BUILD_DIR/latest-preview.json"
package_zip="$BUILD_DIR/knowledge-pack.zip"
curl -fsS "$BASE_URL/kb/yi-flow-core/latest/manifest.json" -o "$manifest_json"
curl -fsS "$BASE_URL/kb/yi-flow-core/latest/preview?limit=50" -o "$preview_json"
curl -fsS "$BASE_URL/kb/yi-flow-core/versions/$VERSION/knowledge-pack.zip" -o "$package_zip"

python3 - "$VERSION" "$manifest_json" "$preview_json" "$package_zip" <<'PY'
import json
import sys
import zipfile

version, manifest_path, preview_path, package_path = sys.argv[1:5]
with open(manifest_path, "r", encoding="utf-8") as handle:
    manifest = json.load(handle)
with open(preview_path, "r", encoding="utf-8") as handle:
    preview = json.load(handle)

if manifest.get("kb_id") != "yi-flow-core" or manifest.get("version") != version:
    raise SystemExit("yi_flow_core_coverage_failed manifest_header")
if not str(manifest.get("content_hash", "")).startswith("sha256:"):
    raise SystemExit("yi_flow_core_coverage_failed manifest_content_hash")
if not str(manifest.get("signature", "")).startswith("ed25519:"):
    raise SystemExit("yi_flow_core_coverage_failed manifest_signature")
if not preview.get("chunks") or len(preview["chunks"]) < 5:
    raise SystemExit("yi_flow_core_coverage_failed preview_chunks")

with zipfile.ZipFile(package_path) as archive:
    names = set(archive.namelist())
expected_names = {"chunks.sqlite", "citations.json", "prompts.json", "vector.index"}
if not expected_names.issubset(names):
    raise SystemExit("yi_flow_core_coverage_failed package_missing=" + ",".join(sorted(expected_names - names)))
for generated_source in ("chunks.json", "coverage-matrix.json"):
    if generated_source in names:
        raise SystemExit("yi_flow_core_coverage_failed manual_source_inside_package=" + generated_source)
PY

app_install_log="$BUILD_DIR/app-install.log"
if [[ ! -x "$APP_DIR/scripts/verify-yi-flow-core-local-install.sh" ]]; then
  echo "yi_flow_core_coverage_failed app_install_smoke_missing=$APP_DIR/scripts/verify-yi-flow-core-local-install.sh" >&2
  exit 1
fi
YIFLOW_CORE_MANIFEST_URL="$BASE_URL/kb/yi-flow-core/latest/manifest.json" \
YIFLOW_CORE_TRUSTED_PUBLIC_KEY_BASE64="$TRUSTED_PUBLIC_KEY_BASE64" \
YIFLOW_CORE_EXPECTED_VERSION="$VERSION" \
"$APP_DIR/scripts/verify-yi-flow-core-local-install.sh" > "$app_install_log"
if ! grep -q "yi_flow_core_local_install_ok" "$app_install_log"; then
  echo "yi_flow_core_coverage_failed app_install_smoke_output=$(tr '\n' ' ' < "$app_install_log" | cut -c 1-360)" >&2
  exit 1
fi

python3 - "$REPORT" "$VERSION" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    report = json.load(handle)
print(
    "yi_flow_core_coverage_ok "
    f"version={sys.argv[2]} "
    f"questions={report['coverage_questions']} "
    f"chunks={report['chunks']} "
    f"source_linked_chunks={report['source_linked_chunks']} "
    f"update_keys={sum(report['update_key_hits'].values())}"
)
PY
cat "$app_install_log"
