# 技术栈与技术规范

## 当前状态

`yi-flow-knowledge-base` 当前是 Go HTTP 服务，用于发布和托管 yi-flow iOS App 可远程更新的 Knowledge Pack。

## 技术栈

- 语言：Go `1.25.0`
- HTTP：标准库 `net/http`
- SQLite：`modernc.org/sqlite`，用于服务端只读预览已发布 `knowledge-pack.zip` 内的 `chunks.sqlite`
- 测试：标准库 `testing` + `httptest`，以公开 HTTP 接口做集成式行为测试
- 存储：本地文件系统，生产环境通过 Docker volume 持久化
- 管理界面：内置 `/admin/` 静态 HTML 页面，不拆独立前端项目
- 部署：Docker / Docker Compose，宿主机本地端口 `127.0.0.1:18085`，由反代提供 HTTPS

## 文档规范

- 主要文档使用 Markdown。
- 中文为主，命令、文件名、API、包名保留英文原文。
- 文件名使用小写短横线，顶层约定文件除外，例如 `AGENTS.md`。
- 文档标题层级从一个一级标题开始，不跳级。
- 命令、路径、环境变量使用反引号标记。

## 代码规范

- Go 代码使用 `gofmt`。
- 测试通过公开 HTTP 行为验证，不 mock 项目内部模块。
- Admin 写接口必须通过 `Authorization: Bearer <ADMIN_TOKEN>` 鉴权。
- 公开接口只提供 manifest/package/版本列表/内容预览，不暴露管理写操作。
- Knowledge Pack version 发布后不可覆盖；回滚通过切换 latest 完成。

## 脚本规范

- 脚本必须支持从仓库根目录运行，或在文档中明确工作目录。
- 脚本失败时应返回非零退出码，并输出可定位问题的信息。
- 不在脚本中硬编码个人绝对路径。
- 涉及外部服务的脚本必须说明鉴权方式、权限边界和失败处理。

## 本地验证

```bash
scripts/verify-knowledge-base-server.sh
```

等价核心命令：

```bash
gofmt -w cmd internal
go test ./...
go build ./cmd/server
```

## 本地运行

```bash
ALLOW_EMPTY_ADMIN_TOKEN=1 STORAGE_DIR="$(pwd)/.data" go run ./cmd/server
```

带管理 token：

```bash
ADMIN_TOKEN="replace-with-random-token" STORAGE_DIR="$(pwd)/.data" go run ./cmd/server
```

访问：

- `http://127.0.0.1:8080/admin/`
- `http://127.0.0.1:8080/healthz`
- `http://127.0.0.1:8080/kb/yi-flow-core/latest/manifest.json`
- `http://127.0.0.1:8080/kb/yi-flow-core/latest/preview`
- 生产入口：`https://yi-flow.com/knowledge-base/`

## 部署

生产部署使用：

```bash
cp .env.example .env
# 编辑 .env，设置 ADMIN_TOKEN 为高强度随机值
docker compose up -d --build
```

compose 默认只暴露 `127.0.0.1:18085`，应由 Caddy 或 Nginx 反代到公网域名并启用 HTTPS。

当前服务器 nginx 反代：

```text
https://yi-flow.com/knowledge-base/ -> http://127.0.0.1:18085/
```

## 依赖规范

新增依赖前需要说明：

- 依赖解决什么问题。
- 是否已有项目内工具、系统工具或标准库可替代。
- 是否会增加安装、运行或维护成本。
- 是否需要网络、账号或密钥。

## 安全规范

- 不提交 `.env`、密钥、令牌、Cookie、账号凭据。
- 示例配置使用 `.env.example` 或文档片段，不使用真实值。
- 涉及外部 API 的流程必须说明鉴权方式和权限边界。
- 涉及文件删除、发布、推送、远程写入的操作必须有明确前置检查。
- 服务器 root 密码不得写入仓库、脚本或命令历史。部署后应改为 SSH key 登录，并轮换初始密码。
- 签名私钥不得部署到服务器；服务器只保存公开 manifest、zip 包和 latest 指针。

## 验证规范

```bash
scripts/verify-knowledge-base-server.sh
```
