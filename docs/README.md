# PackGradle 项目文档

> 本文档目录按主题拆分，面向后续开发者与 AI 代理接续工作使用。
> 代码与文档如有出入，以代码为准；`frontend/bindings` 为自动生成物，请勿手改。

## 文档索引

| 目录 | 内容 |
| --- | --- |
| [backend/](./backend/) | Go 后端架构、暴露给前端的服务方法与返回结构、internal 包与工具集 |
| [frontend/](./frontend/) | 工作区 UX 设计基线与可交互原型（新信息架构） |
| [contract/](./contract/) | 前后端通信契约：Wails 绑定机制、错误协议、全部 DTO 数据结构、P1 契约补全与事件协议执行规格 |
| [adr/](./adr/) | 架构决策记录（ADR）：已决议的关键决策及其理由 |
| [acceptance/](./acceptance/) | 验收口径与 E2E 方式：L0 自动化命令集 + L1 手工清单 |
| [development/](./development/) | 前后端开发约定、构建 / 调试 / 发布流程 |
| [agents/](./agents/) | AI 代理协作约定：领域速览、issue 协作流程、triage 标签 |
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
3. [P1 契约补全执行规格（features/availability、changes、mapping、rebind、endpoint）](./contract/03-p1-contract.md)
4. [P1 前端事件协议（订阅点、seq 跳号、受控重查）](./contract/04-p1-event-protocol.md)

## 架构决策记录（ADR）

1. [0001 · P1 前端切换与旧入口退场](./adr/0001-p1-frontend-cutover-and-legacy-retirement.md)
2. [0002 · Relation 初始 revision 语义](./adr/0002-relation-initial-revision-semantics.md)
3. [0003 · 多步元数据写入的单事务 doctrine（CreateRelation / ApplyRebind）](./adr/0003-metadata-multistep-single-transaction.md)

## 验收

1. [P1 验收口径与 E2E 方式（L0 命令集 / L1 手工清单 / A/B 口径）](./acceptance/p1-acceptance-spec.md)

> 术语表统一在仓库根 [CONTEXT.md](../CONTEXT.md)（全项目唯一）。

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
- 后端向 Wails 注册 6 个服务：`EnvService`（7 个方法）、`PackwizService`（8 个方法）、`PrismService`（26 个方法）+ 新架构 `SyncService`（11 个方法）、`ProjectService`（4 个方法）、`RuntimeService`（4 个方法），共 60 个可调用方法。
- 前后端经 `src/api` 门面按契约调用真实绑定；mock 层（`src/mocks` 内存库）保留，可经设置页开关 / 顶栏 MOCK 徽标一键切换；文件/目录选择经 `utils/dialogs.ts`（`Dialogs.OpenFile`）。
- 错误只以 `err.*` 错误码从 Go 端传递，文案统一在 `frontend/src/locales/zh-CN.json`（509 个键，其中 71 个 `err.*`）。
- 新栈前端页：`/sources`（项目源）与 `/runtimes`（运行实例）已落 shadcn-vue（发现·登记·健康走查）；其余工作区页随切换发布票施工。
- 新架构（store/sqlite + transport + pgheadless）已入主干：headless 入口 `cmd/pgheadless`（PrepareRelation → CreateRelation → StartScan → PrepareSync → GetPlan 可重复执行）；验收 L0 命令已挂 Taskfile：`task test` / `task test:vet` / `task test:race`（`-race` 需 mingw-w64 gcc 提供 CGO 工具链）。
- 持久化位置：全局 `%AppData%\PackGradle\config.toml`；项目级 `<项目目录>\packgradle.toml`。
