# RAGFlow Replacement Runbook

This document defines the RAGFlow-backed Knowledge Pack production flow. RAGFlow is the chunk authoring system; this service is the signed export and publish adapter for the yi-flow app.

## Responsibilities

| Layer | Responsibility |
| --- | --- |
| RAGFlow | Source upload, document parsing, chunk editing, retrieval testing, human review |
| auth-service | Admin authentication for `https://rag.yi-flow.com` |
| yi-flow-knowledge-base | Export reviewed RAGFlow chunks, enforce metadata gates, build signed Knowledge Packs, publish latest and rollback |
| yi-flow app | Download `manifest.json` and `knowledge-pack.zip`; no RAGFlow dependency at runtime |

## Required Datasets

| Dataset ID | Purpose | Source boundary |
| --- | --- | --- |
| `yi-flow-core` | Project state, agent runtime, Knowledge Pack update path, core FAQ | Internal yi-flow knowledge only |
| `moegirl-acgn-faq` | Reviewed Moegirl summary FAQ pack | Summary-only third-party content with attribution |

Dataset names may be more descriptive in the RAGFlow UI, but exported dataset IDs must stay stable because the admin API references them directly.

## Seed Migration

Prepare reviewed `yi-flow-core` source material for RAGFlow:

```bash
RAGFLOW_SEED_OUTPUT_DIR=/tmp/ragflow-seed-yi-flow-core \
scripts/prepare-ragflow-seed.sh
```

The script reads `knowledge-packs/yi-flow-core/chunks.json`, `citations.json`, and `prompts.json`, then writes:

- `manifest.json`: migration summary and required metadata fields
- `yi-flow-core.jsonl`: one row per reviewed chunk with metadata and questions
- `documents/*.md`: Markdown files with frontmatter suitable for RAGFlow upload/review

The generated metadata aligns with the exporter gates: `source_family`, `source_url`, `license`, `review_status`, `document_path`, `chunk_id`, and `title`.

## Parser and Chunk Settings

Initial settings:

| Dataset | Parser mode | Target chunk size | Notes |
| --- | --- | --- | --- |
| `yi-flow-core` | General / Markdown-friendly | 256-512 tokens | Preserve headings and short operational answers. |
| `moegirl-acgn-faq` | Q&A or General summary chunks | 128-384 tokens | Do not mirror full pages, infoboxes, media, or full article structure. |

RAGFlow retrieval tests must pass before export. Export is not a substitute for chunk review.

## Required Metadata

Every exported document or chunk must resolve these fields from RAGFlow `meta_fields` or `metadata`. Chunk-level metadata overrides document-level metadata.

| Field | Required | Example | Gate |
| --- | --- | --- | --- |
| `source_family` | yes | `yi-flow-core`, `moegirl` | Prevents cross-pack contamination. |
| `source_url` | yes | `https://yi-flow.com/docs/runtime` | Required for citations. |
| `license` | yes | `reviewed internal knowledge`, `CC BY-NC-SA 3.0 CN` | Required for third-party attribution. |
| `review_status` | yes | `reviewed` | Only reviewed chunks can be exported. |
| `title` | recommended | `Agent Runtime` | Falls back to document name. |
| `document_path` | recommended | `yi-flow/core/runtime.md` | Falls back to RAGFlow location/name. |

The exporter rejects missing required metadata instead of publishing partial or ambiguous chunks.

## Export API

Dry run:

```bash
curl -X POST "$BASE/admin/api/kb/yi-flow-core/ragflow/export-dry-run" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "version": "2026.06.25.001",
    "dataset_id": "yi-flow-core",
    "llm_recommended": ["Qwen3-4B-Q4_K_M"]
  }'
```

Publish:

```bash
curl -X POST "$BASE/admin/api/kb/yi-flow-core/ragflow/export-publish" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "version": "2026.06.25.001",
    "dataset_id": "yi-flow-core",
    "llm_recommended": ["Qwen3-4B-Q4_K_M"]
  }'
```

Dry-run returns `quality_report`, `quality_status`, `chunk_count`, `citation_count`, and `package_hash`. Publish returns the same quality status and writes the signed version as latest only after the same gates pass.

Smoke verification:

```bash
KNOWLEDGE_BASE_BASE_URL=https://yi-flow.com/knowledge-base \
ADMIN_TOKEN=<secret> \
RAGFLOW_SMOKE_DATASET_ID=yi-flow-core \
scripts/verify-ragflow-replacement-smoke.sh
```

Full publish smoke is opt-in:

```bash
RAGFLOW_SMOKE_PUBLISH=1 \
RAGFLOW_ROLLBACK_VERSION=<previous-version> \
KNOWLEDGE_BASE_BASE_URL=https://yi-flow.com/knowledge-base \
ADMIN_TOKEN=<secret> \
scripts/verify-ragflow-replacement-smoke.sh
```

The script intentionally skips remote actions when required environment variables are missing, so it is safe in local CI. It does not print bearer tokens.

## Runtime Configuration

Configure secrets through the deployment environment, not git:

```bash
RAGFLOW_BASE_URL=https://rag.yi-flow.com
RAGFLOW_API_KEY=<secret>
RAGFLOW_TIMEOUT=15s
RAGFLOW_PAGE_SIZE=100
```

Do not commit RAGFlow API keys, auth-service client secrets, PostgreSQL passwords, SSH passwords, or signing keys.

## Quality Gates

The RAGFlow export path must pass:

- required chunk fields: `chunk_id`, `title`, `path`, `source`, `content`
- required citation metadata: `source_url`, `license`, `review_status`
- duplicate exported chunk ID rejection
- source-family boundary validation
- existing Knowledge Pack audit before publish
- Moegirl golden eval before pilot latest switch

Pilot thresholds:

- Top5 hit rate: `>= 0.90`
- Citation rate: `>= 0.90`
- Duplicate retrieval rate: `< 0.05`
- Refusal pass rate: `>= 0.90`

## Deployment Boundary

`https://rag.yi-flow.com` must be protected by auth-service SSO/OIDC or an auth-gated reverse proxy. Public exposure is limited to 80/443. RAGFlow internal ports, PostgreSQL, Redis, object storage, and search/index services must remain private.

Deployment templates live under `deploy/ragflow/`:

- `oauth2-proxy.env.example`: OIDC auth gate pointed at auth-service
- `Caddyfile.example`: TLS edge and security headers
- `docker-compose.auth-gate.example.yml`: Caddy + oauth2-proxy + private RAGFlow upstream topology

Before deployment, run the host readiness script on the target host:

```bash
scripts/verify-ragflow-host-readiness.sh
```

To verify PostgreSQL reachability without printing credentials:

```bash
RAGFLOW_POSTGRES_DSN=<secret-dsn> scripts/verify-ragflow-host-readiness.sh
```

or use standard `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, and `PGPASSWORD` environment variables.

Minimums:

- CPU: `>= 4`
- RAM: `>= 16 GB`
- free disk on `/`: `>= 50 GB`
- Docker installed
- Docker Compose v2 installed
- `vm.max_map_count >= 262144`
- `rag.yi-flow.com` DNS exists

Operational commands for the production deployment directory:

```bash
docker compose pull
docker compose up -d
docker compose ps
docker compose logs --tail=200 caddy oauth2-proxy ragflow
docker compose restart ragflow
docker compose down
```

Rollback uses the previous pinned compose/env bundle and persistent volumes:

```bash
git -C /opt/yi-flow-ragflow-deploy checkout <previous-deploy-tag>
docker compose up -d
```

Backup commands must be run from the server without printing secrets:

```bash
pg_dump --format=custom --file=/var/backups/ragflow/ragflow-metadata.dump "$RAGFLOW_POSTGRES_DSN"
tar -C /opt/yi-flow-ragflow -czf /var/backups/ragflow/ragflow-data.tgz uploads object-store search-index redis logs config
tar -C /opt/yi-flow-knowledge-base -czf /var/backups/ragflow/knowledge-packs.tgz storage
```

Restore dry-run path:

```bash
createdb ragflow_restore_drill
pg_restore --dbname=ragflow_restore_drill /var/backups/ragflow/ragflow-metadata.dump
mkdir -p /tmp/ragflow-restore-drill
tar -C /tmp/ragflow-restore-drill -xzf /var/backups/ragflow/ragflow-data.tgz
tar -C /tmp/ragflow-restore-drill -xzf /var/backups/ragflow/knowledge-packs.tgz
```

Monitoring checks:

- HTTPS availability for `https://rag.yi-flow.com`
- unauthenticated requests redirect or deny before RAGFlow
- authenticated admin requests reach the RAGFlow UI
- RAGFlow local health endpoint responds from the server
- container health and restart count
- disk free space for uploads, object storage, search/index, and published packs
- memory pressure and OOM kills
- failed RAGFlow export count
- latest Knowledge Pack manifest/preview status

Secrets live only in the server deployment environment, password manager, Docker secrets, or root-owned `.env` files outside git. They must not be copied into issue comments, shell commands, or repository files.

Remote execution must use SSH keys, a password manager session, or another non-logging secret channel. Do not put root passwords in shell commands, issue comments, repo files, or terminal transcripts.

Suggested rollout order:

1. Verify host readiness and DNS.
2. Pin a RAGFlow Docker image version that supports PostgreSQL metadata storage.
3. Configure RAGFlow with server PostgreSQL and private internal dependencies.
4. Add auth-service client `ragflow-admin` and configure the ingress auth gate.
5. Expose only `https://rag.yi-flow.com`.
6. Create `yi-flow-core` and `moegirl-acgn-faq` datasets.
7. Run RAGFlow export dry-run from this service.
8. Publish a pilot Knowledge Pack only after quality gates pass.

## PostgreSQL Decision Gate

The target deployment requirement is to use the server PostgreSQL instance for RAGFlow metadata. Do not silently fall back to MySQL.

Current upstream references to verify during deployment:

- RAGFlow release notes list `v0.26.1` as the current release on 2026-06-17.
- The upstream Docker `.env` defaults `RAGFLOW_IMAGE` to `infiniflow/ragflow:v0.26.1`.
- The upstream Docker Compose file still depends on a `mysql` service for the default `ragflow-cpu` and `ragflow-gpu` profiles.
- The upstream configuration documentation exposes MySQL environment variables as the documented relational database path.
- `service_conf.yaml.template` contains a commented `postgres` section, so PostgreSQL must be explicitly enabled and verified against the selected image.

Required gate before production deploy:

1. Pin the exact RAGFlow image tag.
2. Configure `DB_TYPE=postgres` only if the selected image supports it.
3. Uncomment/configure the `postgres` service configuration with server PostgreSQL host, database, user, and password from the deployment secret store.
4. Start RAGFlow against PostgreSQL on a non-public port.
5. Verify HTTP startup, login, dataset creation, document upload, parsing, chunk list, chunk edit, retrieval test, and API key usage.
6. If any step fails with MySQL-specific SQL or migration errors, stop and document the blocker in the deployment issue before considering a MySQL fallback.

References:

- RAGFlow release notes: https://ragflow.io/docs/release_notes
- RAGFlow configuration docs: https://ragflow.io/docs/configurations
- RAGFlow Docker env: https://github.com/infiniflow/ragflow/blob/main/docker/.env
- RAGFlow Docker Compose: https://github.com/infiniflow/ragflow/blob/main/docker/docker-compose.yml
- RAGFlow service config template: https://github.com/infiniflow/ragflow/blob/main/docker/service_conf.yaml.template

Backups must cover:

- RAGFlow PostgreSQL metadata
- uploaded source files/object storage
- search/index storage
- yi-flow published Knowledge Pack artifacts
- signing key storage path

Restore drills should verify that RAGFlow UI data and yi-flow latest/rollback artifacts can be recovered independently.
