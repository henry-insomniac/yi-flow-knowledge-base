#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC_PATH="$ROOT_DIR/docs/rag/weknora-export.md"
YIFLOW_CORE_SEED="$ROOT_DIR/knowledge-packs/yi-flow-core/weknora-export.seed.json"

python3 - "$DOC_PATH" "$YIFLOW_CORE_SEED" <<'PY'
import json
import os
import sys
from urllib.parse import urlparse

doc_path, yi_flow_core_seed_path = sys.argv[1:3]

with open(doc_path, "r", encoding="utf-8") as handle:
    doc = handle.read()

required_terms = [
    "yi-flow-core",
    "moegirl-acgn-faq",
    "review_status",
    "source_url",
    "license",
    "source_policy",
    "summary-only",
    "no full-article mirror",
]
missing_terms = [term for term in required_terms if term not in doc]
if missing_terms:
    raise SystemExit("weknora_dataset_contract_failed doc_missing_terms=" + ",".join(missing_terms))

if not os.path.exists(yi_flow_core_seed_path):
    raise SystemExit("weknora_dataset_contract_failed missing_yi_flow_core_seed=" + yi_flow_core_seed_path)

with open(yi_flow_core_seed_path, "r", encoding="utf-8") as handle:
    seed = json.load(handle)

if seed.get("kb_id") != "yi-flow-core":
    raise SystemExit("weknora_dataset_contract_failed yi_flow_core_seed_kb_id=" + str(seed.get("kb_id")))
if seed.get("weknora_kb_id") not in {"yi-flow-core", "yi-flow-core-reviewed"}:
    raise SystemExit("weknora_dataset_contract_failed yi_flow_core_seed_weknora_kb_id=" + str(seed.get("weknora_kb_id")))

chunks = seed.get("chunks")
if not isinstance(chunks, list) or len(chunks) < 3:
    raise SystemExit("weknora_dataset_contract_failed yi_flow_core_seed_chunks_lt_3")

source = str(seed.get("source", ""))
license_value = str(seed.get("license", ""))
source_policy = str(seed.get("source_policy", ""))
if "yi-flow" not in source.lower():
    raise SystemExit("weknora_dataset_contract_failed yi_flow_core_seed_source=" + source)
if not license_value.strip():
    raise SystemExit("weknora_dataset_contract_failed yi_flow_core_seed_license_required")
if "reviewed" not in source_policy.lower():
    raise SystemExit("weknora_dataset_contract_failed yi_flow_core_seed_source_policy=" + source_policy)

for index, chunk in enumerate(chunks):
    for field in ["id", "content", "knowledge_title", "knowledge_filename", "knowledge_source"]:
        if not str(chunk.get(field, "")).strip():
            raise SystemExit(f"weknora_dataset_contract_failed chunks[{index}].{field}_required")
    status = str(chunk.get("review_status", "")).strip().lower()
    if status != "reviewed" and chunk.get("reviewed") is not True:
        raise SystemExit(f"weknora_dataset_contract_failed chunks[{index}].review_status_required")
    metadata = chunk.get("metadata") if isinstance(chunk.get("metadata"), dict) else {}
    source_url = str(chunk.get("url") or metadata.get("url") or metadata.get("source_url") or "").strip()
    if not source_url:
        raise SystemExit(f"weknora_dataset_contract_failed chunks[{index}].source_url_required")
    parsed = urlparse(source_url)
    if parsed.scheme not in {"https", "http"} or not parsed.netloc:
        raise SystemExit(f"weknora_dataset_contract_failed chunks[{index}].source_url_invalid")
    lower_identity = " ".join(str(chunk.get(field, "")) for field in ["id", "knowledge_title", "knowledge_filename", "knowledge_source", "content"]).lower()
    if any(marker in lower_identity for marker in ["moegirl", "萌娘百科", "zh.moegirl.org.cn", "genshin", "原神", "acgn"]):
        raise SystemExit(f"weknora_dataset_contract_failed chunks[{index}].external_identity_in_yi_flow_core")

print(f"weknora_dataset_contract_ok yi_flow_core_chunks={len(chunks)}")
PY
