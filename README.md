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
