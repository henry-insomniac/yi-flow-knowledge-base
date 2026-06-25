#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KB_ID="${RAGFLOW_SEED_KB_ID:-yi-flow-core}"
PACK_DIR="${RAGFLOW_SEED_PACK_DIR:-$ROOT_DIR/knowledge-packs/$KB_ID}"
OUTPUT_DIR="${RAGFLOW_SEED_OUTPUT_DIR:-${TMPDIR:-/tmp}/ragflow-seed-$KB_ID}"

mkdir -p "$OUTPUT_DIR"

python3 - "$KB_ID" "$PACK_DIR" "$OUTPUT_DIR" <<'PY'
import json
import re
import sys
from pathlib import Path

kb_id = sys.argv[1]
pack_dir = Path(sys.argv[2])
output_dir = Path(sys.argv[3])
documents_dir = output_dir / "documents"
documents_dir.mkdir(parents=True, exist_ok=True)

chunks_path = pack_dir / "chunks.json"
citations_path = pack_dir / "citations.json"
prompts_path = pack_dir / "prompts.json"

with chunks_path.open("r", encoding="utf-8") as handle:
    chunks = json.load(handle)
with citations_path.open("r", encoding="utf-8") as handle:
    citations_payload = json.load(handle)
with prompts_path.open("r", encoding="utf-8") as handle:
    prompts = json.load(handle)

citations = {
    row.get("chunk_id"): row
    for row in citations_payload.get("citations", [])
    if row.get("chunk_id")
}
prompts_by_title = {}
for prompt in prompts:
    title = str(prompt.get("title", "")).strip()
    if title:
        prompts_by_title.setdefault(title, []).append(prompt.get("question", ""))


def slug(value: str) -> str:
    value = re.sub(r"[^A-Za-z0-9._-]+", "-", value.strip())
    value = re.sub(r"-+", "-", value).strip("-")
    return value or "chunk"


def yaml_quote(value: str) -> str:
    return json.dumps(str(value), ensure_ascii=False)


rows = []
for index, chunk in enumerate(chunks, start=1):
    chunk_id = str(chunk.get("chunk_id", "")).strip()
    title = str(chunk.get("title", "")).strip()
    source = str(chunk.get("source", "")).strip()
    path = str(chunk.get("path", "")).strip()
    content = str(chunk.get("content", "")).strip()
    if not all([chunk_id, title, source, path, content]):
        raise SystemExit(f"ragflow_seed_failed missing_required_chunk_fields index={index} chunk_id={chunk_id!r}")

    citation = citations.get(chunk_id, {})
    source_url = str(citation.get("url") or citation.get("source_url") or f"https://yi-flow.com/knowledge-base/source/{source}/{path}").strip()
    license_name = str(citation.get("license") or "reviewed internal knowledge").strip()
    questions = [q for q in prompts_by_title.get(title, []) if q]

    filename = f"{index:03d}-{slug(chunk_id)}.md"
    document_path = documents_dir / filename
    frontmatter = {
        "kb_id": kb_id,
        "chunk_id": chunk_id,
        "title": title,
        "source_family": kb_id,
        "source": source,
        "source_url": source_url,
        "license": license_name,
        "review_status": "reviewed",
        "document_path": path,
    }
    document = ["---"]
    for key, value in frontmatter.items():
        document.append(f"{key}: {yaml_quote(value)}")
    if questions:
        document.append("questions:")
        for question in questions:
            document.append(f"  - {yaml_quote(question)}")
    document.extend(["---", "", f"# {title}", "", content, ""])
    document_path.write_text("\n".join(document), encoding="utf-8")

    rows.append({
        "document_file": f"documents/{filename}",
        "dataset_id": kb_id,
        "chunk_id": chunk_id,
        "title": title,
        "content": content,
        "metadata": frontmatter,
        "questions": questions,
    })

with (output_dir / f"{kb_id}.jsonl").open("w", encoding="utf-8") as handle:
    for row in rows:
        handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True) + "\n")

manifest = {
    "kb_id": kb_id,
    "source_pack": str(pack_dir),
    "document_count": len(rows),
    "chunk_count": len(rows),
    "jsonl": f"{kb_id}.jsonl",
    "documents_dir": "documents",
    "required_metadata": [
        "source_family",
        "source_url",
        "license",
        "review_status",
        "document_path",
        "chunk_id",
        "title",
    ],
}
(output_dir / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

print(f"ragflow_seed_ok kb_id={kb_id} chunks={len(rows)} output={output_dir}")
PY
