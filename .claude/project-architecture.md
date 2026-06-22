# 项目架构

## 项目定位

`yi-flow-knowledge-base`：yi-flow Knowledge Pack 远程发布服务。

本项目负责生成、托管和管理 iOS App 可远程更新的 Knowledge Pack。iOS App 只消费公开 manifest/package URL，并在本地完成 SHA256、Ed25519 签名校验、安装和 `active_version` 切换；服务端不参与 App 端可信判断。

本文件用于记录项目边界、目录职责、关键架构决策和后续扩展原则。内容应以项目真实结构为准，避免只保留通用模板。

## 当前目录结构

```text
.
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   └── server/
│       ├── server.go
│       └── server_test.go
├── scripts/
│   └── verify-knowledge-base-server.sh
├── Dockerfile
├── docker-compose.yml
├── go.mod
├── AGENTS.md
├── LICENSE
└── .claude/
    ├── README.md
    ├── project-architecture.md
    ├── skill-authoring.md
    ├── bug-fix-log.md
    ├── git-collaboration.md
    └── tech-stack.md
```

## 目录职责

### `AGENTS.md`

Agent 入口文件。用于说明项目目标、协作原则和关键文档索引。任何 Agent 开始工作前都应先阅读该文件。

### `.claude/`

项目长期上下文目录。这里保存架构、规范、协作流程和故障记录，避免重要信息散落在对话或临时笔记中。

### `cmd/server/`

HTTP 服务入口。读取 `ADDR`、`STORAGE_DIR` 和 `ADMIN_TOKEN`，创建服务 handler 并监听端口。生产环境必须设置 `ADMIN_TOKEN`；只有本地开发可以显式设置 `ALLOW_EMPTY_ADMIN_TOKEN=1`。

### `internal/server/`

Knowledge Pack 发布服务核心。当前提供：

- `GET /healthz`：容器和反代健康检查。
- `GET /admin/`：内置轻量管理页，不单独拆前端项目。
- `POST /admin/api/kb/:kb_id/versions`：上传 `manifest.json` 和 `knowledge-pack.zip`，发布不可变版本并设为 latest。
- `POST /admin/api/kb/:kb_id/build-publish`：从管理页 JSON 构建 `chunks.sqlite`、`vector.index`、`citations.json`、`prompts.json`、签名 manifest 和包，并发布为 latest。
- `POST /admin/api/kb/:kb_id/moegirl/build-publish`：从萌娘百科公开 sitemap/API 构建摘要型知识包；只保存高层摘要、分类和原文引用，不保存完整条目、图片或 infobox 等价数据集。
- `POST /admin/api/kb/:kb_id/latest`：把 latest 回滚/切换到已存在版本。
- `GET /kb/:kb_id/latest/manifest.json`：App 端拉取 latest manifest。
- `GET /kb/:kb_id/latest/preview`：管理页读取 latest 知识包 chunks，展示内容摘要和可复制提问样例。
- `GET /kb/:kb_id/versions`：列出版本和 latest。
- `GET /kb/:kb_id/versions/:version/knowledge-pack.zip`：App 端下载指定版本完整包。
- `GET /kb/:kb_id/versions/:version/preview`：按版本读取知识包 chunks，便于发布后人工核验内容。

存储布局：

```text
<STORAGE_DIR>/kb/<kb_id>/
├── latest
└── versions/
    └── <version>/
        ├── manifest.json
        └── knowledge-pack.zip
```

### `scripts/`

验证脚本目录。`verify-knowledge-base-server.sh` 会运行 Go 测试并确认服务可构建。

### `Dockerfile` / `docker-compose.yml`

单服务部署入口。容器内部监听 `:8080`，compose 默认只绑定宿主机 `127.0.0.1:18085`，由 Caddy/Nginx 负责 HTTPS 反代。

### `.agents/skills/`

可选的项目级 Agent Skills 目录。只有在项目明确需要可复用 Agent 工作流时才创建。新增 skill 时，应同步说明触发条件、输入输出、验证方式和安全边界。

## 架构原则

- 让目录结构表达职责边界。
- 优先遵循项目已有模式，不为了新功能随意引入新风格。
- 共享逻辑需要有清晰调用边界和验证方式。
- 外部服务、账号、密钥、网络访问和数据写入必须明确安全边界。
- 项目级 skills 应保持触发条件明确，避免把泛用提示词或个人偏好写成长期能力。
- 架构变更必须同步更新本文件。
- Knowledge Pack 版本发布后不可覆盖；需要修正时发布新版本或回滚 latest。
- 私钥不应放在服务器上。推荐在本地或 CI 中签名后上传已签名的 manifest/package，服务器只托管公开产物。
- 生产服务器不使用 root 密码长期登录；应创建部署用户、使用 SSH key，并关闭 root password login。
- 第三方内容源必须记录许可、来源 URL 和使用边界。萌娘百科入口固定为摘要与引用用途，遵守 `CC BY-NC-SA 3.0 CN`，不得扩展成全文镜像、训练数据集或商业再分发包。

## 架构变更记录

| 日期 | 变更 | 原因 | 验证 |
| --- | --- | --- | --- |
| 2026-06-18 | 初始化 Agent 项目文档 | 建立项目长期上下文和协作基线 | 已创建 `AGENTS.md` 与 `.claude` 文档 |
| 2026-06-20 | 新增 Knowledge Pack 发布服务 | 支撑 iOS App 远程更新知识包，提供公开 manifest/package 下载和 token 保护的管理接口 | `scripts/verify-knowledge-base-server.sh` 通过 |
| 2026-06-21 | 新增 Knowledge Pack 内容预览 | 管理页可查看 chunks 内容并复制样例问题到 App 验证知识包加载 | `scripts/verify-knowledge-base-server.sh` 通过 |
| 2026-06-22 | 新增萌娘百科摘要知识包构建入口 | 支持从公开 sitemap/API 生成带引用的 ACGN 摘要知识包，避免全文镜像和训练数据用途 | `go test ./...` 通过 |
