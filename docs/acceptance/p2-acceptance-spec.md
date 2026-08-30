# Phase 2 验收口径与恢复场景验收（P2-E2E）

> **状态：已决议（验收规格）。** 2026-08-31 经决策图票 #30 grilling 拍板定稿（四决策点全按推荐：口径骨架全沿用、恢复注入=随机强杀+不变式、性能=扫描沿用+apply/内存新门槛+UI P95 继续推迟、人工确认=单测+L1）；执行会话按本规格执行验收，不再做隐式决策。
> 来源：决策图「PackGradle 重构全路线决策图（Phase 1-4）」票 [#30](https://github.com/skyraah/PackGradle/issues/30)。
> 依据：[P1 验收规格](p1-acceptance-spec.md)（骨架沿用基准）、[ADR-0004](../adr/0004-phase2-apply-journal-and-recovery.md)（恢复协议事实源）、[契约 05](../contract/05-p2-apply-contract.md)（验收对象面）、检视报告 §6/§7、UX 原型 §15。

## 0. 两个口径（沿用 P1 §0）

- **A = 后端单独验收**：headless/transport 边界，前端未齐时可先行、可多轮执行。
- **B = Phase 2 完整验收**：= A + `frontend:build` + L1 增量清单全勾 + P1 回归不回退。**B 是 Phase 2 完成门槛**——无切换语义（ADR-0001 单发布已于 P1 完成），B 通过即 Phase 2 可标完成。

验收在 Windows 开发机执行，不建 CI；报告必须记录机器规格（§4）。

## 1. 分层与自动化边界（沿用 P1 §1）

**L0 = 无人值守自动化层**（headless，可重复执行，命令集挂 Taskfile）；**L1 = 真实窗口手工清单层**。不引入窗口驱动自动化（FlaUI/WinAppDriver）与 Playwright/vitest 组件自动化——技术债注记沿用 P1 §5.5。覆盖边界原则沿用：headless 能验的不上窗口层；窗口层只验 headless 验不了的（真实窗口生命周期、绑定桥、事件重连、恢复页交互、真实路径/权限场景）。

### 1.1 L0 命令集

| 命令 | 内容 | A | B |
| --- | --- | --- | --- |
| `task test` | `go test ./...`（**增量**：恢复协议单测族，§2.2/§2.3） | ✅ | ✅ |
| `task test:vet` | `go vet ./...` | ✅ | ✅ |
| `task test:race` | `go test -race ./...`（硬门槛，mingw-w64 gcc 前提沿用） | ✅ | ✅ |
| `task acceptance:headless` | **扩展 `-apply`**：全新数据目录 Register → Scan → PrepareSync → ResolvePlan → **ConfirmPlan → Apply 跑完** → 断言 GetApplyRun `committed`、ListCommits 含新记录；同目录第二遍复用 Relation 重扫重 apply（noop 场景断言成功收口且数据不变） | ✅ | ✅ |
| `task acceptance:recovery` | **新增**：apply 子进程随机时机强杀 ×5 轮 → 每轮重启走恢复管线 → 断言不变式（§2.1） | ✅ | ✅ |
| `task acceptance:perf` | 3,000 fixture 冷/热扫描（门槛沿用）+ **apply 度量**（§3） | ✅ | ✅ |
| `task frontend:build` | `vue-tsc --noEmit` + Vite 生产构建（`--configLoader runner` 口径沿用） | — | ✅ |

### 1.2 L1 手工清单（B 口径，契约 05 增量面，逐项勾选）

**Apply 链路**
- [ ] plans 页「应用同步」主操作由 `apply_sync` availability 门控（不可用时显示后端原因码文案）
- [ ] ConfirmPlan 后长任务移交任务中心：进度短语/计数推进、可离开页面（UX §7.9）、任务中心追踪
- [ ] committed 后计划投影 `applied`；工作区基线徽标推进、变化页 diff 归零口径正确

**历史**
- [ ] history 页签由 `history_view` 门控；ListCommits 列表（exact/partial、剩余差异数）
- [ ] 记录详情页逐资源变更渲染，空态/失败态符合页面状态互斥约定

**恢复流**
- [ ] recovery_required 工作区：列表行徽标 + 任务中心「处理恢复」双入口可达恢复详情页
- [ ] 恢复详情页：run 摘要（六阶段、acknowledged、commit_id）+ 操作清单；**无临时绝对路径、无 ownership proof 泄漏**（契约 05 硬约束 4）
- [ ] recovery_required 期间 `apply_sync`/`rebind` 入口不可用（`err.recovery.in_progress` 文案）
- [ ] 「确认人工处理」（AcknowledgeRecovery）→ 恢复态解除 → 重扫引导出现 → 重扫后 diff 归零
- [ ] 强杀后重启：恢复横幅/短状态按 UX §5.3 呈现，状态以查询 API 为准

**P1 回归不回退**
- [ ] 事件受控重查、页面状态互斥、locale 无原始 code/detail 泄漏、无 legacy 入口复活（P1 §1.3 清单抽验）

## 2. 恢复注入场景（P1 §5.4 兑现）

### 2.1 强杀注入（L0，`acceptance:recovery`）

每轮：fixture 数据目录起 apply 子进程 → **随机延迟**（覆盖 staging/applying/verifying 各相位）`taskkill /F` → 重启 headless 走恢复管线（ADR-0004 §4）→ 断言不变式：

1. **无「部分完成」假象**：终局要么 `committed`（完整复扫验证一致后才可能），要么 `recovery_required`；绝不出现基线推进而内容半途；
2. **收口后重扫 diff 归零**：probe 自动裁决（mark-applied/redo/compensate）或人工确认收口后，完整复扫与 fixture 确定性重放逐字节一致（generator 同 seed 重放特性）；
3. **收口后 apply 可重跑成功**；`recovery_required` 期间 apply 不可用（§1.2 对应 L1 项）；
4. **重复恢复幂等**：重启两次不产生重复补偿、重复删除或二次破坏性动作。

轮数固定 5；随机延迟种子入记录 JSON（可重放）。裁决结果逐轮记入记录（不做逐轮裁决断言——随机时机下裁决本就不定，不变式才是硬门槛）。

### 2.2 裁决矩阵定性（单测，施工票交付）

伪造 journal + 文件系统状态驱动 probe，逐路断言（ADR-0004 §4 矩阵）：

| 夹具 | 期望裁决 |
| --- | --- |
| 目标已达成 after digest + 所有权证明匹配 | mark-applied → 进入可验证路径 |
| 目标未写入 + staging 完整 + 前置条件成立 | 幂等 redo |
| 部分写入且可证归属本次 Apply | compensate 或续做（按操作类型） |
| 状态含糊 / 路径外部修改 / 无法证明所有权 | 保持 recovery_required（人工确认出口） |

含「不凭文件名/mtime/外观猜测」的负例断言（外部篡改文件不得被误判归属）。

### 2.3 磁盘写满与真断电

- **磁盘写满 = io 注入单测**：写失败 → 不推 Baseline、不建 Commit、staging 证据保留、`recovery_required`（ADR-0004 §5）。
- **真断电（硬件级）不做**：进程强杀 + WriteFileAtomic/CAS 落盘 fsync 语义已覆盖可自动化面；硬件掉电无法在 L0 复现，缺口记入报告技术债。

### 2.4 人工确认路径划线（Q4）

probe 含糊判定逻辑 → 单测（§2.2 第四行）；**交互** → L1 手工（§1.2 恢复流，用真实强杀产生的 recovery_required 状态）；**L0 不构造含糊夹具**——人工出口的本质由人验，pgheadless 预置注入路径的施工成本不换收益。

## 3. 性能基线（P1 §2 沿用 + 增量）

fixture（3,000 确定性生成器）与冷/热定义沿用 P1 §2.1/§2.2。门槛：

| 指标 | 门槛 | 来源 |
| --- | --- | --- |
| 冷扫描 ≤ 10s / 热扫描 ≤ 2s / 热命中率 ≥ 95% | 沿用 | P1 §2.3 |
| **冷 apply（initialize 全量 copy）≤ 30s** | 新增硬门槛 | Q3 |
| **Apply 峰值内存增量 < 256MiB** | 新增硬门槛 | Q3（检视报告 §11.3 项兑现） |
| UI 查询 P95 ≤ 200ms | **继续推迟** | Q3（绑定桥计时基建不存在，wails3 无窗口自动化；技术债注记沿用） |

- apply 度量进 pgheadless `-metrics` JSON：分相计时（staging/applying/verifying）+ 峰值内存采样 + 总耗时。
- 超标处置沿 P1 §2.3：记录原因与机器规格；环境显著异常（P1 先例：Defender 实时扫描高熵新文件，处置=加排除后重测）注明后重测，重测记录同归档。

## 4. 记录与报告（沿用 P1 §2.4/§3）

- 原始记录 `docs/acceptance/records/p2-perf-<date>-<machine>.json`（同日多轮用 `-tNN` 后缀区分，沿 P1 先例），含恢复注入逐轮记录（随机种子、裁决结果、收口路径）。
- 报告 `docs/acceptance/reports/p2-acceptance-<date>.md`，规格与执行报告分离。必填字段沿 P1 §3：机器规格四元组、L0 各命令结果（含 `-race` 与 `acceptance:recovery` 五轮）、性能记录链接与达标情况、L1 勾选（B）、缺口清单（跨平台、UI P95、真断电、窗口驱动自动化技术债）、结论。
- **A 口径通过** = L0 全绿（test/vet/race/headless -apply/recovery/perf）+ 性能达标 + 报告归档。
- **B 口径通过** = A + `frontend:build` + L1 增量清单全勾 + P1 回归不回退。

## 5. 明确不在 Phase 2 验收范围

1. 跨平台一致性深测（P1 §5.2 决议延续，Phase 2 无跨平台新增面）；
2. UI 查询 P95 与窗口驱动/组件自动化（技术债，wails3 稳定版或官方测试方案后再评估）；
3. 硬件级真断电（§2.3）；
4. Restore 链路与恢复计划（Phase 3）；Junction/hardlink 物化（Phase 4）；
5. watcher 增量扫描协议（Phase 4 雾区）。

## 6. 执行会话工作项（验收基建清单）

本规格定口径；以下基建由 Phase 2 执行会话实现：

1. pgheadless `-apply` 扩展（ConfirmPlan → Apply → committed/ListCommits 断言链 + 两遍幂等）；
2. `acceptance:recovery` 注入 harness（子进程管理、随机延迟种子、强杀、重启恢复、不变式断言、逐轮记录）；
3. 恢复协议单测族（裁决矩阵四象限 + 负例、journal 状态机、io 注入写满）；
4. pgheadless `-metrics` 增 apply 分相计时与峰值内存采样；
5. Taskfile 任务挂接（§1.1 命令集对齐）；
6. B 口径执行前 L1 walkthrough 数据准备（含真实强杀产生的 recovery_required 工作区、exact 与 partial 各一的历史记录）。
