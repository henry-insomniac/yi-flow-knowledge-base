#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOLDEN_PATH="${MOEGIRL_GOLDEN_PATH:-$ROOT_DIR/docs/rag/moegirl-golden-questions.json}"
OUTPUT_PATH="${MOEGIRL_HITL_REVIEW_OUTPUT:-${TMPDIR:-/tmp}/moegirl-hitl-review.json}"

python3 - "$GOLDEN_PATH" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    questions = json.load(handle)["questions"]
if len(questions) < 50:
    raise SystemExit(f"moegirl_hitl_review_failed golden_questions={len(questions)} required=50")
regressions = sum(1 for row in questions if row.get("regression"))
if regressions < 10:
    raise SystemExit(f"moegirl_hitl_review_failed regressions={regressions} required=10")
print(f"moegirl_hitl_review_golden_ok questions={len(questions)} regressions={regressions}")
PY

if [[ -z "${MOEGIRL_REVIEW_MANIFEST:-}" && -z "${MOEGIRL_REVIEW_PACKAGE:-}" ]]; then
  echo "moegirl_hitl_review_ready mode=checklist_only output_required_env=MOEGIRL_REVIEW_MANIFEST,MOEGIRL_REVIEW_PACKAGE"
  exit 0
fi

if [[ -z "${MOEGIRL_REVIEW_MANIFEST:-}" || -z "${MOEGIRL_REVIEW_PACKAGE:-}" ]]; then
  echo "moegirl_hitl_review_failed set both MOEGIRL_REVIEW_MANIFEST and MOEGIRL_REVIEW_PACKAGE" >&2
  exit 1
fi

go run "$ROOT_DIR/cmd/knowledge-pack-review" \
  -manifest "$MOEGIRL_REVIEW_MANIFEST" \
  -package "$MOEGIRL_REVIEW_PACKAGE" \
  -golden "$GOLDEN_PATH" \
  -sample-size "${MOEGIRL_REVIEW_SAMPLE_SIZE:-30}" \
  -question-size "${MOEGIRL_REVIEW_QUESTION_SIZE:-20}" > "$OUTPUT_PATH"

python3 - "$OUTPUT_PATH" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    report = json.load(handle)
sample_count = len(report.get("sample_chunks", []))
question_count = len(report.get("golden_questions", []))
if sample_count < 30:
    raise SystemExit(f"moegirl_hitl_review_failed sample_chunks={sample_count} required=30")
if question_count < 20:
    raise SystemExit(f"moegirl_hitl_review_failed golden_questions={question_count} required=20")
if report.get("full_mirror_suspect_count", 1) != 0:
    raise SystemExit(f"moegirl_hitl_review_failed full_mirror_suspects={report.get('full_mirror_suspect_count')}")
attribution = report.get("attribution", {})
if attribution.get("missing_citation_count", 1) != 0:
    raise SystemExit(f"moegirl_hitl_review_failed missing_citation_count={attribution.get('missing_citation_count')}")
source_count = attribution.get("source_count", {})
license_count = attribution.get("license_count", {})
if "萌娘百科 (Moegirlpedia)" not in source_count:
    raise SystemExit("moegirl_hitl_review_failed missing_moegirl_source")
if "CC BY-NC-SA 3.0 CN" not in license_count:
    raise SystemExit("moegirl_hitl_review_failed missing_cc_license")
print(
    "moegirl_hitl_review_ok "
    f"kb_id={report.get('kb_id')} "
    f"version={report.get('version')} "
    f"samples={sample_count} "
    f"questions={question_count} "
    f"output={sys.argv[1]}"
)
PY
