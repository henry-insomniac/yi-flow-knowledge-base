# .claude 文档索引

`.claude` 目录保存 `yi-flow-knowledge-base` 的长期上下文，供人类维护者和 Agent 在开发、排障、评审时快速理解项目约定。

## 文档列表

- `project-architecture.md`：项目定位、目录职责、架构约束和扩展原则。
- `skill-authoring.md`：项目级 skills 的编写、安装和维护规范。
- `bug-fix-log.md`：bug 修复记录、复盘模板和已知问题。
- `git-collaboration.md`：分支命名、提交信息、PR、评审和发布约定。
- `tech-stack.md`：当前技术栈、推荐工具链、脚本和文档规范。

## 维护规则

- 修改项目结构时，同步更新 `project-architecture.md`。
- 新增、删除或重命名项目级 skill 时，同步更新 `skill-authoring.md`。
- 修复 bug 后，同步更新 `bug-fix-log.md`。
- 调整协作流程时，同步更新 `git-collaboration.md`。
- 引入新语言、运行时、包管理器、测试框架或格式化工具时，同步更新 `tech-stack.md`。

## 当前状态

本目录由脚手架在 2026-06-18 初始化。请根据项目真实情况补充架构、技术栈和验证命令。
