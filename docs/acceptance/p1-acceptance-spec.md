# P1 验收口径与 E2E 方式（P1-E2E）

> **状态：已决议（验收规格）。** 2026-08-28 经决策图票 #6 两轮 grilling 拍板定稿（Round 1 Q1-Q4 全按推荐；Round 2：UI 查询 P95 ≤200ms 推迟 Phase 2、跨平台只测 Windows）。执行会话按本规格执行验收，不再做隐式决策。
> 来源：决策图「PackGradle 重构全路线决策图（Phase 1-4）」票 [#6](https://github.com/skyraah/PackGradle/issues/6)。
> 依据：检视报告 §6「P1 重新验收条件」、§7「后端单独验收口径」、§1 验证结果现状；roadmap §7 验收清单、Step 8；架构 §11.3 非功能目标、§12 测试策略、§13.1 Phase 1 退出条件；UX 原型 §15 交互验收清单。

## 0. 两个口径

- **A = 后端单独验收**：headless/transport 边界，前端延期期间可先行、可多轮执行。范围与门槛见 §1/§2/§3。
- **B = P1 完整验收**：= A + 前端构建 + 真实窗口手工清单（L1）。一次性执行，是「单发布切换」（ADR-0001）的发布门槛；对应检视报告 §6 全部九条。

验收在 Windows 开发机执行，不建 CI；报告必须记录机器规格（见 §3）。

## 1. 分层与自动化边界

**L0 = 无人值守自动化层**（headless，可重复执行，命令集须挂 Taskfile 任务）；**L1 = 真实窗口手工清单层**。不引入窗口驱动自动化（FlaUI/WinAppDriver 等）与 Playwright/vitest 组件自动化：wails v3.0.0-beta.7 无 browser 模式、无官方窗口测试框架，UI 尚未建成；记入报告技术债，待 wails3 稳定版或官方测试方案后再评估。

覆盖边界原则：headless 能验的不上窗口层；窗口层只验 headless 验不了的东西（真实窗口生命周期、绑定桥、事件重连、真实路径/权限场景）。

### 1.1 L0 命令集

| 命令 | 内容 | A | B |
| --- | --- | --- | --- |
| `task test` | `go test ./...`（含 Windows 专属与路径安全测试） | ✅ | ✅ |
| `task test:vet` | `go vet ./...` | ✅ | ✅ |
| `task test:race` | `go test -race ./...`（硬门槛；执行机需 mingw-w64 gcc 提供 CGO 工具链，见 §5.6） | ✅ | ✅ |
| `task acceptance:headless` | pgheadless 全链路：全新用户数据目录上跑 Register → Scan → Snapshot → PrepareSync → ResolvePlan → GetPlan，可重复执行 | ✅ | ✅ |
| `task acceptance:perf` | 3,000 fixture 生成 + pgheadless `-metrics` 冷/热两轮（§2） | ✅ | ✅ |
| `task frontend:build` | `vue-tsc --noEmit` + Vite 生产构建，命令口径固定为 `--configLoader runner` | — | ✅ |

### 1.2 L0 覆盖映射（检视报告 §7 后端单独验收口径）

| §7 条目 | 载体 |
| --- | --- |
| 新 core/application/store/adapters 不依赖 legacy Service、旧 TOML 状态、项目内 `.packgradle/` | `go test`（依赖方向测试）+ `go vet` |
| Register → Scan → Snapshot → PrepareSync → ResolvePlan → GetPlan headless 链路可重复执行 | `task acceptance:headless`（每次全新数据目录、重跑幂等） |
| repository 拒绝跨 Relation、跨 side、错误 parent、伪造 digest 的对象引用 | `go test`（store 契约测试，检视 P0-3 补齐） |
| 端点/资源路径经绝对化、realpath、root containment、binding identity 校验 | `go test`（adapter 路径安全测试，检视 P0-4 补齐） |
| MappingPolicy 编译校验，冲突规则输出 `mapping_collision` 诊断 | `go test`（检视 P1-POLICY 补齐） |
| `requested_exactness`、Relation revision、policy/snapshot/plan digest 在 model/SQLite/transport 一致 | `go test`（检视 P1-PLAN 契约测试） |
| preparation 消费与 Relation 创建具备事务或可恢复提交语义 | `go test`（按票 #8 决议落地后补齐） |
| Task、错误码、事件 envelope 稳定，前端 locale 可后续直接接入 | `go test`（err.\* code 契约测试；事件按票 #7 决议落地后补齐） |
| P0-1/P0-2 前端部分显式标记 deferred，不误报后端已完成 | 报告必填字段（§3） |

### 1.3 L1 手工清单（B 口径，逐项勾选）

**窗口与启动**
- [ ] 真实窗口创建、bindings 注册成功；默认路由进入 `/workspaces`
- [ ] `/workspaces`、`/workspaces/new`、`/workspaces/:id/changes`、`/workspaces/:id/mappings`、`/workspaces/:id/plans/:plan_id`、`/sources`、`/runtimes` 五页 + 端点页走查

**事件与恢复（检视 §6 第 8 条）**
- [ ] 端点文件变更 → watcher 失效信号 → 页面标记 stale → 受控重查
- [ ] 窗口关闭重开、应用重启后，Task/Workspace 从查询 API 恢复真实状态（事件只做通知）

**架构一致性（UX §15.1）**
- [ ] 不存在 Apply/History/Restore/模拟成功入口
- [ ] Plan 内容不可编辑，Resolve 后产生新 plan
- [ ] clean 不由差异数量推断；事件只触发 stale/刷新，不覆盖权威 cache

**页面状态（UX §15.2）**
- [ ] loading / empty / filtered-empty / error / refreshing 互斥；刷新失败保留旧查询快照
- [ ] stale 计划保留只读证据并停止执行

**空间与动作（UX §15.3）**
- [ ] 每页单个高强调主操作；940×620 下主操作与对象身份仍在首屏
- [ ] 长名称、路径、错误详情不遮挡操作

**真实 Windows 场景**
- [ ] 跨卷端点、无权限目录的行为符合契约（结构化错误）
- [ ] legacy Junction/TOML 关联被识别为 legacy 输入（`legacy_shared` 等），不自动覆盖、无对应工具入口（ADR-0001）

**locale（检视 §6 第 2 条窗口侧）**
- [ ] 失败场景显示 zh-CN 文案，无原始 code/detail 泄漏

## 2. 性能基线

### 2.1 fixture（3,000 受管资源）

- 仓库内确定性 Go 生成器（入 git；生成产物不入 git），固定 seed、无网络、内容伪随机（禁止全零/全同文件，避免 hash cache 行为失真）；生成命令文档化、单命令可重放。
- 构成：Project 侧 `pack.toml` 300 个 mod 条目（混合 CurseForge/Modrinth/URL 来源）→ 300 个 mod LogicalResource；Runtime 侧 2,700 个受管文件（300 个对应 JAR + 2,400 个 config/kubejs/scripts 文件，含文本与小型二进制）。
- 大小：JAR 大头 5~20MB 约占 10%、其余 200KB~5MB；文本/配置 1KB~100KB。

### 2.2 冷/热定义

- **冷扫描**：全新用户数据目录（空 DB + 空 hash cache）上的首次全量扫描。
- **热扫描**：同一数据目录、同一 fixture、端点内容未变，紧接二次扫描（不得删除 hash cache）。

### 2.3 指标、门槛与超标处置

- 分项记录：Project 扫描 / Runtime 扫描 / Normalize / DB 写入 / 总耗时 / hash 命中计数。
- 门槛（架构 §11.3）：冷扫描（Scan → Normalize → DB 写入全链路）≤ 10s；热扫描 ≤ 2s；热扫描命中率 ≥ 95%（同内容理论 100%，留边界余量）。
- **硬门槛**：任一超标 = P1 不得标完成。报告必须记录机器规格与原因分析；因环境显著异常可注明后重测，重测记录同样归档。

### 2.4 记录方式

- 扩展 pgheadless 增加 `-metrics`：输出分项 JSON（含 hash 命中计数；命中度量依赖检视 P1-5 的 hash cache 命中计数落地）。
- 原始记录存 `docs/acceptance/records/p1-perf-<date>-<machine>.json`，入 git；报告引用记录文件。

## 3. 报告

- 规格（本文件）与执行报告分离；报告存 `docs/acceptance/reports/p1-acceptance-<date>.md`，入 git。
- A 口径每轮执行产出一份报告；B 口径为切换发布前最终一份。

报告必填字段：

1. 执行日期、执行人、机器规格（CPU / 内存 / 磁盘 / OS 版本 / Go 版本）；
2. L0 各命令结果（含 `-race`）；
3. 性能记录文件链接与冷/热结果、命中率；
4. L1 清单逐项勾选（B 口径）；
5. **P0-1/P0-2 deferred 显式标记**（A 口径，检视 §7 末条）；
6. 缺口与已知未验证项：跨平台一致性未验证（P1 只测 Windows）、UI 查询 P95 ≤200ms 推迟 Phase 2、窗口驱动自动化技术债；
7. 结论：通过 / 不通过 + 证据。

## 4. 通过门槛

- **A 口径通过** = L0 全绿（test / vet / race / headless / perf）+ 性能达标 + 报告归档 + P0-1/P0-2 deferred 显式标记。
- **B 口径通过** = A 全过 + `frontend:build` 通过 + L1 清单全勾 + 检视 §6 九条全部满足（含无 legacy 跨端写入口、locale 完整）。B 通过是单发布切换（ADR-0001）的发布门槛。

## 5. 明确不在 P1 验收范围

1. UI 查询接口 P95 ≤200ms（架构 §11.3）→ 推迟 Phase 2 验收（Round 2 决议）。
2. 跨平台一致性（§11.3 末条）→ P1 只测 Windows，跨平台不做深测（Round 2 决议）。
3. Junction/hardlink 物化 capability E2E → Phase 4（roadmap §13 阶段表）；P1 只验 legacy 识别。
4. 断电/磁盘写满/进程强杀恢复、Apply 内存增量 <256MiB → Phase 2（随 operation journal 落地）。
5. 窗口驱动自动化与 Playwright/vitest 组件自动化 → 技术债，wails3 稳定版或官方测试方案出现后评估。

## 6. 执行会话工作项（验收基建清单）

本规格定口径；以下基建由后续执行会话实现（阶段 A 之 P1-E2E）：

1. 3,000 资源确定性生成器（§2.1）；
2. pgheadless `-metrics` 扩展（§2.4，依赖 P1-5 命中度量）；
3. Taskfile 任务集：`test` / `test:vet` / `test:race` / `acceptance:headless` / `acceptance:perf` / `frontend:build`；
4. 检视 P0-3/P0-4/P1-POLICY/P1-PLAN 对应测试补齐（按其「下一步」执行）；
5. 执行机安装 mingw-w64 gcc（`-race` 硬门槛前提）；
6. B 口径执行前：按 §1.3 清单准备 walkthrough 数据（含 legacy 关联、跨卷、无权限场景）。
