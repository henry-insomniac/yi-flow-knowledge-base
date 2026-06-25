# Moegirl FAQ Knowledge Pack Source Policy

## Scope

`moegirl-acgn-faq` is a derived FAQ/summary knowledge pack for ACGN questions. It is separate from `yi-flow-core`; Moegirl, anime, game, external fan-wiki, and ACGN-derived content must not be published into `yi-flow-core`.

## Allowed Source

- Source site: `https://zh.moegirl.org.cn`
- Preferred fetch interface: MediaWiki API at `https://zh.moegirl.org.cn/api.php`
- Initial content mode: page summaries and FAQ-shaped derived chunks
- Required attribution: source name, canonical page URL, license, page id, revision id, touched timestamp, and fetched timestamp

## Disallowed Content

- Full-article mirrors
- Downloaded images, audio, video, or other media assets
- Unattributed copied page bodies
- AI-training exports
- Publishing Moegirl-derived chunks into `yi-flow-core`

## Required Crawl Manifest Fields

Each accepted page must be represented in `citations.json` under `crawl_manifest` with:

- `kb_id`
- `source_name`
- `source_url`
- `canonical_url`
- `page_id`
- `revision_id`
- `touched`
- `license`
- `source_policy`
- `categories`
- `fetched_at`

## Crawl Runtime Rules

- Default API base URL is `https://zh.moegirl.org.cn/api.php`.
- Title allowlists are deduplicated before fetching; duplicate titles are counted in the crawl report.
- API fetches are sequential and bounded to at most 50 titles per request.
- The initial MVP limit is 300 pages unless a smaller operator-provided limit is used.
- Missing pages, non-article namespaces, empty extracts, too-short extracts, build-limit skips, and duplicate page revision hits must be reported in `crawl_report.skipped_pages`.
- Duplicate page revisions are treated as cache hits within one build run and are not emitted as duplicate chunks.

## FAQ Chunking Rules

- Each accepted page emits typed FAQ chunks instead of one broad page-summary chunk.
- The MVP emits at least `faq_overview`, `faq_identity`, and `faq_facts` when the page has enough evidence.
- FAQ chunks must be self-contained and include source URL, license, FAQ type, and a citation pointer.
- FAQ chunks must not invent relationships, character lists, or facts that are absent from the page summary or categories.
- Each accepted page should emit at least three prompt questions aligned to the FAQ chunk types.

## Validation Gate

The package audit must fail when:

- `yi-flow-core` contains Moegirl/anime/game/external fan-wiki source identities.
- `moegirl-*` contains internal yi-flow product documentation.
- `moegirl-*` lacks required source, license, source policy, or crawl manifest metadata.
