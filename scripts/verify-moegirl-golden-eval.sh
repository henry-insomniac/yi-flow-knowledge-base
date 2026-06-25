#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOLDEN_PATH="${MOEGIRL_GOLDEN_PATH:-$ROOT_DIR/docs/rag/moegirl-golden-questions.json}"
REPORT_PATH="${MOEGIRL_EVAL_REPORT_PATH:-${TMPDIR:-/tmp}/moegirl-golden-eval-report.json}"

python3 - "$GOLDEN_PATH" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as handle:
    questions = json.load(handle)["questions"]

expected = {
    "entity_overview": 20,
    "alias_redirect": 10,
    "relation_list": 10,
    "ambiguity": 5,
    "out_of_domain": 5,
}
counts = {key: 0 for key in expected}
regressions = 0
ids = set()
for row in questions:
    row_id = row.get("id")
    if not row_id or row_id in ids:
        raise SystemExit(f"moegirl_golden_eval_failed duplicate_or_missing_id={row_id}")
    ids.add(row_id)
    category = row.get("category")
    if category not in counts:
        raise SystemExit(f"moegirl_golden_eval_failed unknown_category={category}")
    counts[category] += 1
    if not str(row.get("question", "")).strip():
        raise SystemExit(f"moegirl_golden_eval_failed empty_question={row_id}")
    if row.get("answerable") and not row.get("expected_chunk_ids") and not row.get("expected_titles"):
        raise SystemExit(f"moegirl_golden_eval_failed missing_expected_source={row_id}")
    if row.get("regression"):
        regressions += 1

if len(questions) != 50:
    raise SystemExit(f"moegirl_golden_eval_failed questions={len(questions)} required=50")
if counts != expected:
    raise SystemExit(f"moegirl_golden_eval_failed counts={counts} expected={expected}")
if regressions < 10:
    raise SystemExit(f"moegirl_golden_eval_failed regressions={regressions} required=10")
print("moegirl_golden_questions_ok questions=50 regressions=" + str(regressions))
PY

if [[ -n "${MOEGIRL_EVAL_MANIFEST:-}" || -n "${MOEGIRL_EVAL_PACKAGE:-}" ]]; then
  if [[ -z "${MOEGIRL_EVAL_MANIFEST:-}" || -z "${MOEGIRL_EVAL_PACKAGE:-}" ]]; then
    echo "moegirl_golden_eval_failed set both MOEGIRL_EVAL_MANIFEST and MOEGIRL_EVAL_PACKAGE" >&2
    exit 1
  fi
  go run "$ROOT_DIR/cmd/knowledge-pack-eval" \
    -manifest "$MOEGIRL_EVAL_MANIFEST" \
    -package "$MOEGIRL_EVAL_PACKAGE" \
    -golden "$GOLDEN_PATH" \
    -top-k "${MOEGIRL_EVAL_TOP_K:-5}" > "$REPORT_PATH"
  python3 - "$REPORT_PATH" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    report = json.load(handle)

checks = {
    "top5_hit_rate": 0.85,
    "citation_rate": 0.95,
    "refusal_pass_rate": 0.90,
}
for key, minimum in checks.items():
    value = float(report.get(key, 0))
    if value < minimum:
        raise SystemExit(f"moegirl_golden_eval_failed {key}={value} required={minimum}")
duplicate = float(report.get("duplicate_answer_rate", 1))
if duplicate >= 0.05:
    raise SystemExit(f"moegirl_golden_eval_failed duplicate_answer_rate={duplicate} required<0.05")
print(
    "moegirl_golden_eval_ok "
    f"questions={report.get('total_questions')} "
    f"top5={report.get('top5_hit_rate')} "
    f"citation={report.get('citation_rate')} "
    f"refusal={report.get('refusal_pass_rate')}"
)
PY
fi
