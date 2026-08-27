# PackGradle 项目文档

> 本文档目录按主题拆分，面向后续开发者与 AI 代理接续工作使用。
> 代码与文档如有出入，以代码为准；`frontend/bindings` 为自动生成物，请勿手改。

## 文档索引

| 目录 | 内容 |
| --- | --- |
| [backend/](./backend/) | Go 后端架构、暴露给前端的服务方法与返回结构、internal 包与工具集 |
| [frontend/](./frontend/) | Vue 前端架构、路由、组件、stores/utils 工具集（含系统对话框封装） |
| [contract/](./contract/) | 前后端通信契约：Wails 绑定机制、错误协议、全部 DTO 数据结构 |
| [development/](./development/) | 前后端开发约定、构建 / 调试 / 发布流程 |

## 后端文档

1. [后端总览与启动流程](./backend/01-overview.md)
2. [服务方法 API 参考（EnvService / PackwizService / PrismService）](./backend/02-service-api.md)
3. [internal 包与工具集](./backend/03-packages-toolsets.md)
4. [新架构后端（P0+P1 只读核心）](./backend/04-new-core-architecture.md)

## 前端文档

1. [前端总览与目录结构](./frontend/01-overview.md)
2. [路由表](./frontend/02-routes.md)
3. [组件清单（props / emits / 行为）](./frontend/03-components.md)
4. [Stores、工具函数与系统对话框](./frontend/04-stores-and-utils.md)
5. [工作区 UX 交互原型设计](./frontend/05-workspace-ux-prototype.md)

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

- 技术栈：Go 1.25 + Wails v3（beta.7），Vue 3 + TypeScript + Vite + Vuetify 4 + vue-i18n + vue-router（hash 模式）。
- 后端向 Wails 注册 3 个服务：`EnvService`（7 个方法）、`PackwizService`（8 个方法）、`PrismService`（25 个方法），共 40 个可调用方法。
- 前后端按契约直连真实绑定：mock 层已移除，文件/目录选择经 `utils/dialogs.ts`（`Dialogs.OpenFile`）。
- 错误只以 `err.*` 错误码从 Go 端传递，文案统一在 `frontend/src/locales/zh-CN.json`（414 个键，其中 65 个 `err.*`）。
- 持久化位置：全局 `%AppData%\PackGradle\config.toml`；项目级 `<项目目录>\packgradle.toml`。
