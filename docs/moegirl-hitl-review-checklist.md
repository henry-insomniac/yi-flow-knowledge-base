# Moegirl FAQ Pack HITL Review Checklist

This checklist supports the first `moegirl-acgn-faq` package review before expanding beyond the initial MVP.

## Inputs

- `manifest.json`
- `knowledge-pack.zip`
- `docs/rag/moegirl-golden-questions.json`
- Generated review report from `scripts/prepare-moegirl-hitl-review.sh`
- Generated golden eval report from `scripts/verify-moegirl-golden-eval.sh`

Generate a fresh 300-page candidate bundle from the live Moegirl API:

```bash
MOEGIRL_HITL_VERSION=2026.06.25.moegirl-hitl-300 \
scripts/prepare-moegirl-300-page-review-bundle.sh
```

The script writes a temporary bundle directory containing:

- `manifest.json`
- `knowledge-pack.zip`
- `moegirl-hitl-review.json`
- `moegirl-golden-eval-report.json`
- `build-response.json`

By default it reads `docs/rag/moegirl-golden-questions.json`, puts answerable `expected_titles` into `priority_titles`, discovers 360 additional sitemap candidates, and requires at least 300 accepted pages. Real public pages can still be skipped when they are missing, non-article namespace pages, empty summaries, too short, or duplicate revisions.

The bundle command fails unless the generated package passes the publish gate:

- Top-5 hit rate >= 90%
- Citation rate >= 95%
- Duplicate answer rate < 5%
- Out-of-domain refusal pass rate >= 90%

Generate review material from an already-built package:

```bash
MOEGIRL_REVIEW_MANIFEST=/path/to/manifest.json \
MOEGIRL_REVIEW_PACKAGE=/path/to/knowledge-pack.zip \
MOEGIRL_HITL_REVIEW_OUTPUT=/tmp/moegirl-hitl-review.json \
scripts/prepare-moegirl-hitl-review.sh
```

## Required Human Review

- [ ] Approve the first-package crawl scope and excluded namespaces/categories.
- [ ] Confirm source-card attribution uses `萌娘百科 (Moegirlpedia)`, page URL, revision id, and `CC BY-NC-SA 3.0 CN`.
- [ ] Review at least 30 sampled chunks from `sample_chunks`.
- [ ] Confirm sampled chunks are summary/FAQ chunks, not full article mirrors.
- [ ] Review at least 20 questions from `golden_questions` through preview or device.
- [ ] Confirm `moegirl-golden-eval-report.json` passes Top-5, citation, duplicate, and refusal gates before manual answer review.
- [ ] Confirm answers are useful, cite Moegirl clearly, and do not invent unsupported entity lists.
- [ ] Confirm `yi-flow-core` contamination audit is clean before and after publishing `moegirl-acgn-faq`.
- [ ] Decide next expansion: stay at 300 pages, expand to 1,000 pages, or pause for policy review.

## Default Decision

Until human approval is recorded, keep the package at 300 pages and do not expand to 1,000 pages.
