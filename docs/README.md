# PackGradle 项目文档

> 本文档目录按主题拆分，面向后续开发者与 AI 代理接续工作使用。
> 代码与文档如有出入，以代码为准；`frontend/bindings` 为自动生成物，请勿手改。

## 文档索引

| 目录 | 内容 |
| --- | --- |
| [backend/](./backend/) | Go 后端架构、暴露给前端的服务方法与返回结构、internal 包与工具集 |
| [frontend/](./frontend/) | 工作区 UX 设计基线与可交互原型（新信息架构） |
| [contract/](./contract/) | 前后端通信契约：Wails 绑定机制、错误协议、全部 DTO 数据结构 |
| [development/](./development/) | 前后端开发约定、构建 / 调试 / 发布流程 |
| [archive/](./archive/) | 已归档的历史文档：旧前端交付文档、设计讨论实录、旧审查报告与旧工作日志（仅历史参考，不作为决策依据） |

## 后端文档

1. [后端总览与启动流程](./backend/01-overview.md)
2. [服务方法 API 参考（EnvService / PackwizService / PrismService）](./backend/02-service-api.md)
3. [internal 包与工具集](./backend/03-packages-toolsets.md)
4. [新架构后端（P0+P1 只读核心）](./backend/04-new-core-architecture.md)

## 前端文档

1. [工作区 UX 交互原型设计（设计基线）](./frontend/05-workspace-ux-prototype.md)
2. [工作区交互原型（可交互 HTML，Round 1 / P1）](./frontend/workspace-ux-prototype.html)
3. [shadcn-vue 迁移指南](./frontend/06-shadcn-migration.md)

> 旧版前端交付文档（总览/路由/组件/stores 与 UI_UX_REVIEW）、重设计讨论实录（docs.md）、旧审查报告与旧工作日志已移入 [archive/](./archive/)，仅作历史参考。

## 通信契约文档

1. [Wails 绑定机制与错误协议](./contract/01-binding-and-error-protocol.md)
2. [前后端数据结构字典（DTO）](./contract/02-data-structures.md)

## 开发文档

1. [后端开发指南](./development/01-backend.md)
2. [前端开发指南（真实绑定直连）](./development/02-frontend.md)
3. [构建、调试与发布](./development/03-build-and-release.md)

## 架构与实施

1. [目标底层架构设计](./architecture/packgradle-architecture-redesign.md)
2. [重写实施路线](./architecture/packgradle-implementation-roadmap.md)
3. [P0/P1 实现检视报告](./architecture/packgradle-p0-p1-review.md)

## 重要事实速览

- 技术栈：Go 1.25 + Wails v3（beta.7），Vue 3 + TypeScript + Vite + vue-i18n + vue-router（hash 模式）；UI 层正从 Vuetify 4 迁移到 shadcn-vue + Tailwind v4（共存规则见 [frontend/06-shadcn-migration.md](./frontend/06-shadcn-migration.md)），视觉基准为 [概念风格.pdf](./概念风格.pdf)。
- 后端向 Wails 注册 4 个服务：`EnvService`（7 个方法）、`PackwizService`（8 个方法）、`PrismService`（26 个方法）+ 新架构 `SyncService`（11 个方法），共 52 个可调用方法。
- 前后端经 `src/api` 门面按契约调用真实绑定；mock 层（`src/mocks` 内存库）保留，可经设置页开关 / 顶栏 MOCK 徽标一键切换；文件/目录选择经 `utils/dialogs.ts`（`Dialogs.OpenFile`）。
- 错误只以 `err.*` 错误码从 Go 端传递，文案统一在 `frontend/src/locales/zh-CN.json`（414 个键，其中 65 个 `err.*`）。
- 持久化位置：全局 `%AppData%\PackGradle\config.toml`；项目级 `<项目目录>\packgradle.toml`。
