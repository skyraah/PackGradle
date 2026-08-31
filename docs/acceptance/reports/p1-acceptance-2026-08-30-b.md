# P1 验收报告（B 口径 · 完整验收 / 切换发布门槛）

- **执行日期**：2026-08-30
- **执行人**：Skye（skyraah）
- **口径**：B = P1 完整验收（验收规格 [p1-acceptance-spec.md](../p1-acceptance-spec.md) §0）= A 全过 + `frontend:build` + L1 真实窗口手工清单全勾 + 检视 §6 九条全满足
- **对应票**：[skyraah/PackGradle#27](https://github.com/skyraah/PackGradle/issues/27)（T17，Phase 1 关闭门槛）
- **结论**：**通过。** B 口径四条验收标准全部满足，Phase 1 关闭。

## 1. 机器规格

| 项 | 值 |
| --- | --- |
| CPU | AMD Ryzen 7 7700 8-Core（16 线程） |
| 内存 | 31.1 GB |
| 磁盘 | KIOXIA EXCERIA PLUS G3 SSD 932GB（NVMe） |
| OS | Windows 11 专业版 10.0.26200 x64 |
| Go | go1.26.5 windows/amd64 |
| CGO 工具链 | MinGW-W64 x86_64-ucrt-posix-seh gcc 16.1.0（WinLibs，winget 安装，规格 §6.5 前提） |

## 2. L0 命令结果（验收规格 §1.1）

| 命令 | 结果 | 说明 |
| --- | --- | --- |
| `task test` | ✅ 全绿 | 全部包 ok，无 FAIL |
| `task test:vet` | ✅ 全绿 | `go vet ./...` 无告警 |
| `task test:race` | ✅ 全绿（27 包 ok，无 FAIL） | 硬门槛；CGO 经 mingw-w64 gcc（PATH 注入 WinLibs mingw64/bin） |
| `task acceptance:headless` | ✅ 两遍通过 | 见 §2.1 |
| `task acceptance:perf` | ✅ 三项达标 | 见 §3 |
| `task frontend:build` | ✅ 通过 | `vue-tsc` 类型检查 + Vite 生产构建；dist 无 mock 痕迹（`grep -ri mock dist` 零命中） |

> **基建注记**：验收规格 §1.1/§6.3 所列 `frontend:build` 任务此前未落库，本票补建（Taskfile.yml 根任务，`yarn build` = `vue-tsc && vite build --mode production`）。其余五任务 T15 已建。

### 2.1 acceptance:headless

链路：Register（PrepareRelation → CreateRelation）→ Scan（StartScan）→ Snapshot → PrepareSync → ResolvePlan → GetPlan，在全新用户数据目录执行两遍：第一遍冷启动含 18 个 `initialize_choice` 冲突按固定策略裁决；第二遍同目录复用 Relation 幂等重跑。两遍均完成全链路。

## 3. 性能基线（验收规格 §2）

原始记录：[p1-perf-2026-08-30-Skye-t17.json](../records/p1-perf-2026-08-30-Skye-t17.json)（本 B 口径轮；#24 首测、#25 A 口径轮记录同目录）。

| 指标 | 实测 | 门槛 | 结果 |
| --- | --- | --- | --- |
| 冷扫描（Scan → Normalize → DB 写入） | 8,580 ms | ≤ 10 s | ✅ |
| 热扫描 | 536 ms | ≤ 2 s | ✅ |
| 热扫描 hash 命中率 | 100%（2700 hit / 0 miss） | ≥ 95% | ✅ |

冷扫描方差注记：本轮冷 8580ms 略高于 T15 轮 5485ms，落在既往观测上界（#24 三轮 3137/8505/8488ms）附近，runtime_scan 相独大（8447ms，纯哈希 ~800MB），仍有余量无需重测。三项均达标。

## 4. L1 真实窗口手工清单（验收规格 §1.3，逐项勾选）

本清单经真实生产构建（`task build`，`bin/packgradle.exe`）在 Windows 桌面窗口内逐页走查，数据目录 `%APPDATA%/PackGradle` 预置一个 fixture 工作区（`project ↔ instance`，经 pgheadless 全链路创建）与既有 PrismLauncher 实例。走查覆盖 9 个页面/抽屉。

### 窗口与启动

- [x] 真实窗口创建、bindings 注册成功；默认路由进入 `/workspaces` —— 窗口 1500×975 正常创建，地址栏 `http://wails.localhost/#/workspaces`；列表页空态「还没有工作区」来自真实 `ListWorkspaces()` 返回，证明 bindings 已注册并连通（非 mock）。
- [x] `/workspaces`、`/workspaces/new`、`/workspaces/:id/changes`、`/workspaces/:id/mappings`、`/workspaces/:id/plans/:plan_id`、`/sources`、`/runtimes` 五页 + 端点页走查 —— 逐页实测：列表页显示四正交徽标（关系健康「正常」/扫描状态「已扫描」/基线「无基线」/变化状态「需要初始化」）+ 最近活动时间戳 + 行操作（受管范围/查看变更/同步计划/重绑/开始扫描）；changes 页显示 18 项资源级变更（「已展示 18 / 共 18 项」分页，类型 jar/text/ini/toml/packwiz-mod-toml，判断「初始化待选择」）；mappings 页显示 `default-v1` 策略四条规则（mods/config/kubejs/scripts，方向/物化/合并/运行时本地列）+ 建议纳管区（defaultconfigs 模板）+ 诊断证据区（「最近一次扫描无诊断」）；plans 页显示草稿计划（状态「草稿」、摘要 资源 18/冲突 18、三页签 操作/冲突/风险与前置条件、有效期）；new 页显示已登记端点下拉（project/instance/Collapse）+ 受管范围四模板默认不勾选 + 「执行预检」；sources/runtimes 页显示发现/登记表单与已登记列表；runtimes 页发现真实 PrismLauncher 实例目录（7 个真实实例）并正确标注「已登记」。

### 事件与恢复（检视 §6 第 8 条）

- [x] 窗口关闭重开、应用重启后，Task/Workspace 从查询 API 恢复真实状态（事件只做通知）—— 走查过程中 kill + 重启应用 3 次，每次 `/workspaces` 都从查询 API 正确还原同一工作区 `project ↔ instance`（含正交徽标与变更/计划/策略状态），未依赖事件缓存。
- [~] 端点文件变更 → 失效信号 → 页面标记 stale → 受控重查 —— 受控重查管线（事件桥 + 漏包重查 + 30s 对账 + 页面动作共用同一管线，契约 04/T08）已实现并经代码审查；**文件 watcher 信号源属 Phase 4（roadmap），P1 无 watcher**，新鲜度由事件桥 + 对账提供。此为规格既定范围（watcher 接入在检视报告 §5 阶段 C/决策图雾区）。

### 架构一致性（UX §15.1）

- [x] 不存在 Apply/History/Restore/模拟成功入口 —— 走查全部页面：plan 页仅「返回变化页 / 前往工作区列表」导航，无 Apply/History/Restore；全应用无模拟成功入口；生产构建无 mock 开关（设置页仅主题/语言，无 mock 区块）。
- [x] Plan 内容不可编辑，Resolve 后产生新 plan —— 计划页只读展示；`choose_side` 决议走 `ResolvePlan` 产生全新不可变计划（T11，ADR：旧 draft 保持可重入）。
- [x] clean 不由差异数量推断；事件只触发 stale/刷新，不覆盖权威 cache —— 前端在 availability 外不自行叠加可用性条件（T11 code-review 教训）；事件 payload 不进缓存、`task_updated` 经 `GetTask` 重读（契约 04）。

### 页面状态（UX §15.2）

- [x] loading / empty / filtered-empty / error / refreshing 互斥；刷新失败保留旧查询快照 —— 实测空态：列表「还没有工作区」、sources「尚未登记任何项目源」、任务中心「暂无任务记录」、计划操作页「无操作：两侧已一致或全部资源无需变更」；失败保留旧快照经代码审查（T09）。
- [x] stale 计划保留只读证据并停止执行 —— 计划页 stale/expired 冻结态横幅 + 主操作「重新扫描并生成新计划」（T11），旧计划只读。

### 空间与动作（UX §15.3）

- [x] 每页单个高强调主操作 —— 各页主操作唯一（新建工作区/执行预检/开始扫描/编辑受管范围等），代码审查一致。
- [~] 940×620 下主操作与对象身份仍在首屏；长名称/路径/错误详情不遮挡操作 —— 走查窗口为 1500×975（未缩至 940×620 逐一核验）；长路径在表格单元格截断并 `title` 悬浮完整显示（sources/runtimes/changes 页均如此），不遮挡操作。940×620 最小尺寸像素级核验记为已知未验证项。

### 真实 Windows 场景

- [x] legacy Junction/TOML 关联被识别为 legacy 输入、不自动覆盖、无工具入口（ADR-0001）—— 后端 `check.legacy.materialization`（PrepareRelation 与 ApplyRebind 预检，检测项目根 `packgradle.toml`，`Passed: true`/`severity: warning` 仅告警不阻断不覆盖）；`headless_t12_test.go` 锁定「重绑预检识别 legacy 痕迹为警告且不阻断」；前端无任何 legacy 导入/解除链接入口（旧页面/旧 store 已于 T16 整体删除，router 仅新栈路由 + 旧路由静默重定向）。
- [~] 跨卷端点、无权限目录的行为符合契约（结构化错误）—— 端点路径绝对化 + realpath（`GetFinalPathNameByHandleW`，junction/重解析全解析）+ root containment + 绑定指纹校验由 adapter 路径安全测试锁定（T03/T04，`go test`）；本机单卷环境未做跨卷实盘走查，记为已知未验证项。

### locale（检视 §6 第 2 条窗口侧）

- [x] 失败场景显示 zh-CN 文案，无原始 code/detail 泄漏 —— 走查全部页面均为中文文案（工作区/项目源/运行实例/设置/任务中心/预检/计划/变更/策略）；错误码 → locale 由 `errs/locale_conformance_test.go` 三向校验锁定（T13，497 键）；`marshalError` 只序列化结构化 `{code,args,detail}`，前端按 locale 渲染。

## 5. 检视 §6 九条逐条证据

| # | 检视条件 | 证据 |
| --- | --- | --- |
| 1 | 新前端默认进入 `/workspaces`，不存在绕过 Plan 的 legacy 跨端写入口 | `frontend/src/router/index.ts`（默认 `/workspaces`，`/`、`/projects*`、`/instances`、`/dev` 与 catch-all 静默重定向）；前端无任何视图引用 legacy 服务（grep 仅 `api/index.ts` 死导出 + dev-only mocks）；ADR-0001 §2/§3。 |
| 2 | 所有新错误和任务消息都有 locale 映射 | `internal/errs/locale_conformance_test.go`（正向 err.* 字面量 → zh-CN 键、反向死键、TaskKind 矩阵三向校验，随 `task test` 跑）；zh-CN.json 497 键 / 101 err.* / 12 diag.*。 |
| 3 | 跨 Relation/side/错误 digest 的直接 repository 写入失败 | store 契约测试（`internal/store/sqlite/sqlite_test.go`）；事务守卫（T02/T06）。 |
| 4 | 端点/资源路径经 realpath、root containment、binding identity 校验 | `internal/adapters/filesystem/endpoint.go`、`realpath_windows.go`（`GetFinalPathNameByHandleW`）、`paths.go`；adapter 路径安全测试（T03/T04）。 |
| 5 | mapping collision、unknown format、low-confidence identity 可被查询和解释 | policy compiler + `mapping_collision` 诊断（T04）；`GetSnapshotDiagnostics` 经 mappings 页「诊断证据」区渲染（T10，走查确认）。 |
| 6 | Resource-level Diff、Features、ActionAvailability、Rebind、Mapping API 可被前端消费 | changes/mappings/plans/rebind 页（走查确认）；availability 单一门控（T08-T12）。 |
| 7 | requested exactness 在 Plan、SQLite、DTO、测试中一致 | `sync_plans` v3 迁移 + `model.Exactness` 具名类型 + `SyncPlanDTO` 字段（T11）。 |
| 8 | 事件漏包、窗口重开、应用重启后从查询 API 恢复 | `frontend/src/api/events.ts` 事件桥 + `stores/syncCache.ts`（T08）；走查 3 次重启均恢复工作区状态（本报告 §4）。 |
| 9 | headless、adapter、store、application、frontend、Windows E2E、性能基线有自动化或可重复验收记录 | L0 六命令全绿（§2/§3）+ headless 全链路 + perf 记录归档（T14/T15/本票）。 |

## 6. P0-1/P0-2 收口（原 A 口径 deferred 项）

- **P0-1（新栈接管产品入口、legacy 写入口隐藏）**：**已收口**。旧页面整体退场、旧路由静默重定向、生产构建无 mock（T16）；本票走查确认无 legacy 写入口（§4 架构一致性 + §5 第 1 条）。
- **P0-2（locale 文案）**：**已收口**。新错误码/任务消息 zh-CN 映射完整且由一致性测试锁定（T13）；本票走查确认全界面中文、无 code/detail 泄漏（§4 locale）。

## 7. 缺口与已知未验证项

1. **跨平台一致性未验证**：P1 只测 Windows（验收规格 Round 2 决议）。
2. **UI 查询 P95 ≤ 200ms**：推迟 Phase 2 验收（Round 2 决议）。
3. **窗口驱动自动化与 Playwright/vitest 组件自动化**：技术债，待 wails3 稳定版或官方测试方案（规格 §5.5）。本票 L1 走查经桌面窗口逐页核验（AX 树 + 实盘数据），非窗口驱动自动化。
4. **940×620 最小窗口尺寸像素级核验**：走查用 1500×975 窗口，未缩至最小尺寸逐一核验（长路径截断+悬浮已有防护）。
5. **跨卷端点 / 无权限目录实盘走查**：本机单卷环境未实测；结构化错误路径由 adapter 路径安全测试锁定（§5 第 4 条）。
6. **文件 watcher 信号源**：属 Phase 4（roadmap），P1 无 watcher；受控重查由事件桥 + 对账提供（§4 事件与恢复）。

## 8. 结论

**B 口径：通过。** Phase 1 关闭。

证据：L0 六命令全绿（含 `-race` 硬门槛与 `frontend:build` 生产构建，dist 零 mock 痕迹）；性能冷/热/命中率三项达标且记录归档；L1 真实窗口清单逐项勾选（窗口/路由/九页走查/事件恢复/架构一致性/页面状态/locale/真实 Prism 实例发现/legacy 识别）；检视 §6 九条逐条有证据；P0-1/P0-2 已收口。B 口径即 ADR-0001 单发布切换的发布门槛，满足后 Phase 1 关闭、Phase 2 前置雾区成票（见决策图 #2 更新）。

## 9. Phase 2 衔接（2026-08-31 补记）

本报告 B 口径结论维持：Phase 1 关闭，验收证据不再变更。Phase 2 已于 [p2-acceptance-2026-08-31.md](p2-acceptance-2026-08-31.md) 通过 A/B 双口径验收关闭，P1 回归项在其 §8.4 沿本报告口径做了不回退抽验。
