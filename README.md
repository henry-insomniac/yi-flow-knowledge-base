# yi-flow Knowledge Base

Knowledge Pack 发布服务。它负责托管 iOS App 可远程更新的 `manifest.json` 和 `knowledge-pack.zip`，并提供轻量管理页发布、查看和回滚 latest。

## Public Endpoints

当前部署入口：

- `https://yi-flow.com/knowledge-base/healthz`
- `https://yi-flow.com/knowledge-base/admin/`
- `https://yi-flow.com/knowledge-base/kb/yi-flow-core/latest/manifest.json`
- `https://yi-flow.com/knowledge-base/kb/yi-flow-core/latest/preview`
- `https://yi-flow.com/knowledge-base/kb/yi-flow-core/versions`
- `https://yi-flow.com/knowledge-base/kb/yi-flow-core/versions/<version>/knowledge-pack.zip`
- `https://yi-flow.com/knowledge-base/kb/yi-flow-core/versions/<version>/preview`

App 侧应配置：

```text
manifestURL = https://yi-flow.com/knowledge-base/kb/yi-flow-core/latest/manifest.json
packageURL  = https://yi-flow.com/knowledge-base/kb/yi-flow-core/versions/<version>/knowledge-pack.zip
```

## Admin API

写接口需要：

```http
Authorization: Bearer <ADMIN_TOKEN>
```

管理页内置“知识包构建器”，用于直接提交 chunks/prompts/citations JSON，由服务端生成 `chunks.sqlite`、`vector.index`、`knowledge-pack.zip`、`manifest.json`，签名后发布为 latest。

管理页还内置“萌娘百科摘要知识包”构建器。该入口从萌娘百科公开 sitemap/API 读取主条目标题和 `exintro` 摘要，生成摘要型 chunks 与 `citations.json` 引用；它不会保存完整条目、不会复刻 infobox 数据集、不会下载图片，也不用于 AI 训练。生成内容必须按 `CC BY-NC-SA 3.0 CN` 署名并保留原页面 URL。

服务端需要配置签名私钥，二选一：

```bash
KNOWLEDGE_PACK_SIGNING_KEY_BASE64=<base64-encoded-ed25519-seed-or-private-key>
KNOWLEDGE_PACK_SIGNING_KEY_FILE=/var/lib/yi-flow-knowledge-base/signing/knowledge-pack-ed25519.key
```

构建并发布新版本：

```bash
curl -X POST "https://yi-flow.com/knowledge-base/admin/api/kb/yi-flow-core/build-publish" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "version": "2026.06.22.001",
    "chunks": [
      {
        "chunk_id": "topic-001",
        "title": "知识点标题",
        "path": "topic/category/name",
        "source": "yi-flow-core",
        "content": "知识内容正文，必须包含 App 中要提问验证的关键词。"
      }
    ],
    "prompts": [
      {
        "id": "topic-check-001",
        "title": "验证知识点",
        "question": "请说明知识点标题"
      }
    ],
    "citations": {"citations":[]}
  }'
```

从指定条目构建萌娘百科摘要包：

```bash
curl -X POST "https://yi-flow.com/knowledge-base/admin/api/kb/moegirl-acgn-summary/moegirl/build-publish" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "version": "2026.06.22.101",
    "titles": ["初音未来", "东方Project"],
    "llm_recommended": ["Qwen3-4B-Q4_K_M"]
  }'
```

从萌娘百科 sitemap 自动取前 N 个主条目构建摘要包：

```bash
curl -X POST "https://yi-flow.com/knowledge-base/admin/api/kb/moegirl-acgn-summary/moegirl/build-publish" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "version": "2026.06.22.102",
    "limit": 50
  }'
```

可选 Moegirl 源配置，通常生产不需要设置：

```bash
MOEGIRL_API_URL=https://zh.moegirl.org.cn/api.php
MOEGIRL_SITEMAP_INDEX_URL=https://zh.moegirl.org.cn/sitemap/sitemap-index-zhmoegirl.xml
MOEGIRL_PUBLIC_ARTICLE_ORIGIN=https://zh.moegirl.org.cn
```

发布新版本并设为 latest：

```bash
curl -X POST "https://yi-flow.com/knowledge-base/admin/api/kb/yi-flow-core/versions" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -F "version=2026.06.20.001" \
  -F "manifest=@manifest.json;type=application/json" \
  -F "package=@knowledge-pack.zip;type=application/zip"
```

回滚 latest：

```bash
curl -X POST "https://yi-flow.com/knowledge-base/admin/api/kb/yi-flow-core/latest" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"version":"2026.06.20.001"}'
```

查看已发布知识包内容预览，用于复制样例问题到 App 验证检索是否加载正确：

```bash
curl "https://yi-flow.com/knowledge-base/kb/yi-flow-core/latest/preview?limit=12"
```

## Local Development

```bash
ALLOW_EMPTY_ADMIN_TOKEN=1 STORAGE_DIR="$(pwd)/.data" go run ./cmd/server
```

Run verification:

```bash
scripts/verify-knowledge-base-server.sh
docker build -t yi-flow-knowledge-base:local .
```

## Deployment

The server is deployed on the VPS under:

```text
/opt/yi-flow-knowledge-base
```

Docker Compose binds the app to localhost:

```text
127.0.0.1:18085 -> container :8080
```

nginx publishes it under:

```text
https://yi-flow.com/knowledge-base/
```

Do not commit `.env`, admin tokens, SSH passwords, or signing private keys. Knowledge Pack signing should happen locally or in CI; the server should only host signed artifacts.
