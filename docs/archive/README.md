# 已归档文档

> 归档时间：2026-08-28
> 这里存放**已退役的历史文档**：或被现行文档取代，或只剩追溯价值。移出主索引以免干扰后续设计与开发决策；内容仅作历史参考，**不再维护**，每份文件顶部有归档横幅标注。

## 旧版前端交付文档（frontend-legacy/）

描述工作区架构重构之前的旧信息架构（`/projects`、`/instances`、Prism 联动、DevView 等）。旧前端的**代码**目前仍在 `frontend/src` 运行（迁移尚未开始），实现层信息需要时可直接读代码。

| 文件 | 原位置 | 说明 |
| --- | --- | --- |
| [frontend-legacy/01-overview.md](./frontend-legacy/01-overview.md) | `docs/frontend/` | 旧前端总览与目录结构 |
| [frontend-legacy/02-routes.md](./frontend-legacy/02-routes.md) | `docs/frontend/` | 旧路由表（`/`、`/projects`、`/instances`…） |
| [frontend-legacy/03-components.md](./frontend-legacy/03-components.md) | `docs/frontend/` | 旧组件清单（common/projects/prism） |
| [frontend-legacy/04-stores-and-utils.md](./frontend-legacy/04-stores-and-utils.md) | `docs/frontend/` | 旧 stores、工具函数与系统对话框 |
| [frontend-legacy/UI_UX_REVIEW.md](./frontend-legacy/UI_UX_REVIEW.md) | `docs/` | 旧视图 UI/UX 检视报告（v1.0 检视 + v2.0 原型落地对照） |

## 其他归档（2026-08-28 清理）

| 文件 | 原位置 | 说明 |
| --- | --- | --- |
| [docs.md](./docs.md) | `docs/` | 「重新设计」讨论完整实录（Snapshot/SyncCommit/对象存储概念的起源），核心结论已沉淀进 [architecture/](../architecture/) |
| [CODE_REVIEW_BUGS.md](./CODE_REVIEW_BUGS.md) | `docs/` | 2026-08-15 代码审查报告，27 个 bug 已全部修复并附回归测试（对象为现已冻结的 legacy `internal/service`） |
| [AGENT History.md](./AGENT%20History.md) | 项目根 | 旧 AI 工作日志；新记录在项目根 `AGENT.md` 持续追加 |

## 现行设计依据（以此为准）

- [docs/frontend/05-workspace-ux-prototype.md](../frontend/05-workspace-ux-prototype.md) —— 工作区 UX 交互原型设计（新信息架构规格）
- [docs/frontend/workspace-ux-prototype.html](../frontend/workspace-ux-prototype.html) —— 可交互原型（Round 1 / P1 主闭环，双击即开）
- [docs/frontend/06-shadcn-migration.md](../frontend/06-shadcn-migration.md) —— shadcn-vue 迁移指南（UI 重构执行依据）
- [docs/architecture/](../architecture/) —— 目标底层架构与实施路线
