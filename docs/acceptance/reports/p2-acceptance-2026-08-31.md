# P2 验收报告（A 口径 · 后端单独验收）

- **执行日期**：2026-08-31
- **执行人**：Skye（skyraah）
- **口径**：A = 后端单独验收（验收规格 [p2-acceptance-spec.md](../p2-acceptance-spec.md) §0）：L0 六命令全绿 + 性能五门槛达标 + 报告归档；**B 口径（= A + `frontend:build` + L1 增量清单全勾 + P1 回归不回退）待 L1 执行**，衔接 T13。
- **对应票**：[skyraah/PackGradle#44](https://github.com/skyraah/PackGradle/issues/44)（T12，Phase 2 后端完成门槛）
- **结论**：**A 口径通过。** B 口径待 L1（frontend:build 与 §1.2 手工清单勾选，本报告不含）。

## 1. 机器规格

| 项 | 值 |
| --- | --- |
| CPU | AMD Ryzen 7 7700 8-Core（16 线程） |
| 内存 | 31.1 GB |
| 磁盘 | KIOXIA-EXCERIA PLUS G3 SSD（NVMe） |
| OS | Windows 11 专业版 10.0.26200 x64 |
| Go | go1.26.5 windows/amd64 |
| CGO 工具链 | MinGW-W64 x86_64-ucrt-posix-seh gcc 16.1.0（WinLibs，winget 安装，验收前提；`-race` 前 PATH 注入 mingw64/bin） |

## 2. L0 命令结果（验收规格 §1.1）

| 命令 | 结果 | 说明 |
| --- | --- | --- |
| `task test` | ✅ 全绿（30 包 ok） | 含恢复协议单测族（裁决矩阵四象限 + 负例、journal 状态机、io 注入写满）与 locale conformance 三向校验 |
| `task test:vet` | ✅ 全绿 | `go vet ./...` 无告警 |
| `task test:race` | ✅ 全绿（30 包 ok，`go clean -testcache` 后连续两轮全新实跑 EXIT=0） | 硬门槛；CGO 经 mingw-w64 gcc。首轮 invocation 出现一次未复现的瞬时 FAIL（输出未完整捕获），随即两轮全新实跑全绿，按环境瞬时方差处理 |
| `task acceptance:headless` | ✅ 四遍通过（EXIT=0） | 见 §2.1 |
| `task acceptance:recovery` | ✅ 五轮四不变式全过（EXIT=0） | 见 §2.2；逐轮记录 [p2-recovery-2026-08-31-Skye-t12.json](../records/p2-recovery-2026-08-31-Skye-t12.json) |
| `task acceptance:perf` | ✅ 五门槛全过 | 见 §3；记录 [p2-perf-2026-08-31-Skye-t12.json](../records/p2-perf-2026-08-31-Skye-t12.json)（含 Defender 方差处置与重测序列） |
| `task frontend:build` | —（B 口径） | 不在 A 口径；L1 一并执行（T13） |

> 提交门槛：`go build ./... && go test ./... && go vet ./...` 在 worktree 根全绿（30 包 ok / vet 无告警）。

### 2.1 acceptance:headless（-resolve 两遍 + -apply 两遍）

链路：Register（PrepareRelation → CreateRelation）→ Scan → PrepareSync → GetPlan，同一数据目录四遍：`-resolve` 两遍（P1 链路回归不回退，全链路完成含 ResolvePlan → GetPlan）→ `-apply` 第一遍 initialize 12 操作（18 冲突）committed partial 剩余 6（链路 205ms）→ `-apply` 第二遍复用 Relation 复扫 noop，kind=sync 0 操作 committed exact 剩余 0（链路 103ms）。七步断言链（GetApplyRun committed、逐操作 verified、ListCommits、GetCommit、GetPlan 投影 applied、收口 diff 归零）两遍全过。

### 2.2 acceptance:recovery（强杀注入五轮）

种子 20260831（可重放），每轮全新 fixture + 数据目录，pgheadless -apply 子进程按目标相位标记后随机延迟 `taskkill /F` 真强杀，重启走恢复管线后再重启两次核对幂等，四不变式（I1 无部分完成假象 / I2 收口后重扫 diff 归零 / I3 恢复门禁与 apply 可重跑 / I4 重复恢复幂等）逐轮断言：

| 轮 | 调度目标 | 击杀落点相位 | 延迟 | 裁决 | 收口路径 | I1-I4 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | staging | staging | 134ms | committed | auto_committed（redo 重放） | ✅✅✅✅ |
| 2 | applying | verifying（相位推进过目标） | 902ms | committed | auto_committed | ✅✅✅✅ |
| 3 | verifying | verifying | 64ms | committed | auto_committed（mark-applied） | ✅✅✅✅ |
| 4 | verifying | verifying | 70ms | committed | auto_committed | ✅✅✅✅ |
| 5 | verifying | verifying | 179ms | committed | auto_committed（committed 崩溃窗口簿记重建） | ✅✅✅✅ |

五轮 kill_verified 全真、退出码 0。**逐轮裁决分布注记**：本轮五轮全部 auto_committed（T08 开发轮同种子曾出现 2 轮 recovery_required→acknowledge→复跑收口）——随机时机下裁决本就不定，规格 §2.1 明确不做逐轮裁决断言，四不变式才是硬门槛；两条收口路径（probe 自动收口 / AcknowledgeRecovery 人工确认）分别由 T08 记录与恢复协议单测族覆盖。

## 3. 性能基线（验收规格 §3，五门槛）

原始记录：[p2-perf-2026-08-31-Skye-t12.json](../records/p2-perf-2026-08-31-Skye-t12.json)（本 A 口径轮，含四轮全序列与方差处置；#46/#48 首测与达标轮记录同目录）。本记录轮（修复后二进制，末次沉降重测）：

| 指标 | 实测 | 门槛 | 结果 |
| --- | --- | --- | --- |
| 冷扫描 | 2,968 ms | ≤ 10 s | ✅ |
| 热扫描 | 498 ms | ≤ 2 s | ✅ |
| 热扫描 hash 命中率 | 100%（2700 hit / 0 miss） | ≥ 95% | ✅ |
| **冷 apply（initialize 全量 copy，2400 操作）** | 27,682 ms（staging 11745 / applying 7105 / verifying 7183） | ≤ 30 s | ✅ |
| **Apply 峰值内存增量** | 17.0 MiB（37.5→54.5 MiB，274 样本） | < 256 MiB | ✅ |

**方差处置注记（沿 P1 §2.3 / T14 先例）**：修复后三轮冷 apply 29495 / 28060 / 27682ms 均过硬门槛，但未回到 T14 安静窗口（17672 / 22221ms，staging 6173ms）——本验收会话在 perf 之前已连跑 headless -apply 两遍与 recovery 五轮（每轮重建 fixture，各产生数万新文件），Defender 实时扫描队列持续在途；分相证据 applying 稳定 4.0-7.3s、verifying 7183ms 与 T14 安静轮 7111ms 一致（代码路径未回退），唯独纯新文件写路径的 staging（11.7-15.0s）与冷扫描被垫高，随沉降逐档回落（记录轮冷扫描 2968ms 已回 T14 量级），判定为环境方差非代码回退。五门槛在全部修复后轮次均达标，A 口径达标成立；安静窗口宽裕度结论以 T14 记录（同代码 17672ms、裕度 41%）为准。

### 3.1 T14 观察项（LastScanTiming 竞态）——已复现并随票修复

T14 记录的技术债观察项（metrics 偶发 `scan_phases_ms` 全 0 而 `run_total_ms` 真值，LastScanTiming 疑似竞态）在**本轮验收 perf 第一轮复现**：metrics-cold 的 scan_phases_ms 全 0、冷扫描门槛空过，而同轮 apply 分相与 run_total 均为真值。

- **根因**：扫描分相计时在 `runScan` 返回时的 deferred `recordScanTiming` 落账，晚于任务成功终态落库；pgheadless `waitScan` 以「无活跃任务」为完成信号，任务终态可见即可读 LastScanTiming，窗口内读到零值。
- **最小修复**（`internal/application/sync/scan.go`）：成功路径把计时落账移到成功终态写入之前——同一 goroutine 序 + 互斥保证「无活跃任务」即蕴含计时已可见；失败/取消路径仍由 deferred 落账兜底。
- **回归单测**：`internal/application/sync/scan_t12_timing_test.go` `TestScanTimingVisibleWhenNoActiveTasks`——按消费方同一完成信号（ListTasks 活跃清空）断言计时非零且分相齐全，修复后为确定性断言。
- 修复后重跑 perf 三轮（含两档沉降重测），scan_phases_ms 均正常落账；`go build/test/vet` 全绿（含 -race）。

## 4. 缺口与技术债（沿规格 §5 划线 + 本轮新发现）

1. **跨平台一致性未验证**：沿用 P1 §5.2 决议，Phase 2 无跨平台新增面，仅 Windows。
2. **UI 查询 P95 ≤ 200ms**：继续推迟（绑定桥计时基建不存在，wails3 无窗口自动化）。
3. **硬件级真断电不做**：进程强杀 + WriteFileAtomic/CAS fsync 语义已覆盖可自动化面（§2.3）。
4. **窗口驱动自动化与组件自动化**：技术债沿用 P1 §5.5，待 wails3 稳定版或官方测试方案。
5. **Defender 实时扫描方差**：perf 连续重跑时 staging/冷扫描被逐文件拦截垫高（P1 §2.3 同源）；处置=沉降重测，Defender 排除目录需管理员权限未实施（T14 同注记）。
6. **LastScanTiming 竞态**：**本轮已复现并修复**（§3.1），随票带回回归单测；观察来源 T14 记录的技术债项就此收口。

## 5. 结论

**A 口径：通过。** 证据：L0 六命令全绿（`task test` / `test:vet` / `test:race` 硬门槛 / `acceptance:headless` 四遍 / `acceptance:recovery` 五轮四不变式 / `acceptance:perf` 五门槛）；性能五项达标且记录归档（超标处置与重测序列入 variance_note）；恢复五轮逐轮记录含种子与裁决可重放；T14 遗留的 LastScanTiming 观察项复现即修并带回归单测。**B 口径（= A + frontend:build + L1 增量清单全勾 + P1 回归不回退）待 L1 执行，衔接 T13**——完成后 Phase 2 可标完成（验收规格 §0）。

---

# P2 验收报告（B 口径 · L1 增量清单 · Phase 2 完成门槛）

> 以下为 T13（票 #45）追加的 B 口径段；上方 §1-§5 为 T12 A 口径内容，原样保留。B 口径 = A + `frontend:build` + L1 增量清单全勾 + P1 回归不回退（验收规格 §0），**B 过即 Phase 2 标完成**。

- **执行日期**：2026-08-31
- **口径**：B = Phase 2 完整验收（§0），A 口径（§2-§4）已过
- **对应票**：[skyraah/PackGradle#45](https://github.com/skyraah/PackGradle/issues/45)（T13）
- **机器规格**：同 §1（Windows 11 / Ryzen 7 7700 / 31.1 GB / NVMe / go1.26.5）
- **结论**：**B 口径通过，Phase 2 完成。**

## 6. frontend:build（验收规格 §1.1）

- [x] `task frontend:build`（`vue-tsc --noEmit` + Vite 生产构建）全绿；走查期间随缺陷修复重建多轮均绿
- [x] 生产 dist 无 mock 回潮：`grep -ri mock dist/` 零命中；设置页实走仅主题/语言，无模拟区块（AX 树核验）

## 7. L1 数据准备（验收规格 §6.6）

**工作区拓扑**：单一用户数据目录 `%APPDATA%/PackGradle` + 单工作区时序复用。规划中的「第三工作区」不可行——prism 运行时端点 `adapter_identity` 为常量 `instance`，`UNIQUE(adapter, adapter_identity)` 使同一数据目录无法登记第二个 prism 实例（第二次注册被 `err.relation.invalid_endpoint` 拒绝，实测）；故 exact/partial 历史与 recovery_required 在同一工作区按时序演进，状态语义与规格要求逐一对应。

**fixture 波次**（pgfixture 确定性生成，项目侧每波追加新名文件制造增量变更）：

| 波次 | 内容 | 产物 |
| --- | --- | --- |
| 基础 | 小 fixture（6 mods + 12 text，18 冲突） | pgheadless `-apply` 第一遍 committed **partial**（12 操作，剩余 6，历史 partial 记录） |
| wave-2 | 项目侧追加 1200 文件 | `-resolve` 留已决议计划 → **UI 驱动 apply** committed exact（历史 exact 记录） |
| wave-3/4 | 追加 1600 + 1800 文件 | UI/CLI apply → committed exact ×2（后续走查弹药） |
| 强杀 | wave-4 的 1800 操作 apply **真实强杀** | recovery_required 工作区 |
| wave-5 | 漂移演示 600 文件 | verify_mismatch 恢复（见 §8.3） |

**真实强杀**：pgheadless `-apply` 子进程 stdout 轮询至 `== ConfirmPlan ==` 标记后 1.5s `taskkill /F /PID`（taskkill 输出「成功: 已终止 PID」入记录），落点 **staging 0/1800**——journal 行在 staging 全量完成后一次落库，击杀时操作日志为空 → 重启恢复管线按 ADR-0004 §4 判「运行无操作日志（崩溃于意图落库前）」→ 含糊阻塞 → **recovery_required**（确定性窗口，与 acceptance:recovery harness 同手法）。

## 8. L1 增量清单逐项勾选（验收规格 §1.2，契约 05 增量面）

走查手法沿 P1 先例：真实生产构建窗口（`bin/packgradle.exe`，WailsWebviewWindow 1500×975）+ **读辅助功能树逐项核对**（Windows UI Automation / UIAutomationClient 读 WebView2 AX 树，InvokePattern 驱动交互；文本输入未使用，交互以点击/读取为主）。全程 zh-CN 文案，无一处原始 code/detail 裸露。

### 8.1 Apply 链路

- [x] plans 页「应用同步」主操作由 `apply_sync` availability 门控——可用：resolved 计划（同步分析「已决议」徽标 + 有效期 + 摘要 1218 资源/新增 1200/冲突 0）显主按钮，确认区提示「确认后将创建应用任务：按计划写入两侧文件，可离开本页，在任务中心追踪进度」；不可用：恢复门期间同区域显「应用同步当前不可用：恢复流程进行中，操作暂不可用」（后端 `err.recovery.in_progress` 经 locale 渲染，无裸码）
- [x] ConfirmPlan 后长任务移交任务中心——点击后跳转变化页（URL 实测 `/changes`）继续追踪；任务中心抽屉实拍进行中条目（「应用 / project ↔ instance / 进行中 / 正在验证应用结果… / 取消任务」），变化页头部同步显示活跃任务短语「正在验证应用结果…」；期间可自由离开页面浏览列表/历史，任务继续推进
- [x] committed 后投影推进——计划 applied 投影、停用态收敛；列表行基线徽标「基线过期→基线就绪」推进、变化状态「有变化→无变化」（两轮 UI apply 均复现）；变化页归零口径正确（终态 全部 5218 / 无操作 5218 / 新增 0 / 修改 0 / 删除 0 / 冲突 0）

### 8.2 历史

- [x] history 页签由 `features.history_view` 门控——列表页行走查；`ListCommits` 游标分页（「已展示 5 项」），列表列（时间/类型/完整性/剩余差异）齐全：**同步/完整完成 ×4 与 初始化/部分完成「仍有 6 项差异」×1，exact 与 partial 各一**、剩余差异数如实显示
- [x] 记录详情页逐资源变更渲染——partial 记录（12 项变更、剩余差异 6、来源计划 plan_id 可跳转）与 exact 记录（1200 项变更、剩余差异 0、逐资源 before→after 表示含 sha256 digest 与变更类型「新增」分页表）实走；恢复详情操作清单空态「该运行没有操作记录」符合状态互斥约定

### 8.3 恢复流（真实强杀产生，非注入）

- [x] 强杀后重启呈现——应用冷启动同步执行恢复管线，列表行徽标「需要恢复」（关系健康列）+ 行内「处理恢复」主上下文动作，状态全部来自查询 API（重启两次呈现一致，重复恢复幂等，无重复补偿/二次破坏）
- [x] 双入口可达恢复详情页——列表行「处理恢复」与任务中心恢复任务条目「处理恢复」（含「需恢复」徽标、「应用未完成，需要处理恢复」消息）均导航 `/workspaces/:id/recoveries/:task_id`（run_id=task_id，契约 05 §5 D2）
- [x] 恢复详情页 run 摘要 + 操作清单——六阶段 state 徽标（需要恢复）、「未确认/已人工确认 + 时间戳」（acknowledged）、提交记录「—」（无 commit_id，基线绝不推进）、来源计划超链接、计划摘要 sha256、操作数、暂存「保留」；**无临时绝对路径、无 ownership proof 泄漏**（契约 05 硬约束 4：AX 树全文 grep staging 路径/temp_relative/ownership 零命中，页面仅显 task_id/plan_id/digest 类身份标识）；操作清单空态如实（staging 期击杀操作日志为空）
- [x] recovery_required 期间 `apply_sync`/`rebind` 入口不可用——plans 页主操作区显 `err.recovery.in_progress` 文案（§8.1）；列表行「重绑」保留位置禁点（enabled=False 实测）并以 tooltip 显后端原因码
- [x] 「确认人工处理」→ 恢复态解除 → 重扫引导 → diff 归零——内联确认条（确认收口/取消）沿 mappings 页先例；确认后 acknowledged_at 落账、snackbar「已确认人工处理，工作区回到健康态」、徽标复位「正常」、「处理恢复」入口消失；重扫引导卡「下一步：重新扫描」（诚实注明「人工确认不推进基线，两侧可能仍有差异」）→ 重新扫描 → 重计划 → **UI 驱动 apply committed exact → 变化页 diff 归零（§8.1 第三条同一终态）**
- [+] **计划外增益——漂移拒绝活体演示**：走查中于扫描后、apply 前向 fixture 追加 600 个计划外文件，引擎 verifying 相复扫检出 `verify_mismatch`（600 项「计划外文件」逐项列证）→ 诚实进入 recovery_required 且**零提交**，任务中心条目 3 秒内「进行中/正在验证应用结果…」→「需恢复/处理恢复」翻转被 AX 树连拍实录——验收规格 §2.1 不变式 1「无部分完成假象」在 UI 层的即席实证；随后 ack → 重扫 → 重计划 → apply → diff 归零完整走通

### 8.4 P1 回归不回退（P1 §1.3 抽验）

- [x] 事件受控重查——全程未手动刷新：apply committed 后列表徽标/计划 applied 投影自动推进，恢复收口后 relation_invalidated 驱动徽标复位与入口剔除；「重新查询」按钮与 30s 对账在位
- [x] 页面状态互斥——空态实走：任务中心「暂无任务记录」（活跃缓存语义）、恢复操作清单「该运行没有操作记录」、变化页「无操作 N」收敛态；loading/error/refreshing 互斥沿 T09 代码审查结论不回退
- [x] locale 无原始 code/detail 泄漏——全部页面（工作区/变化/计划/历史/记录详情/恢复详情/映射/运行实例/设置/任务中心）zh-CN 文案，AX 树全文无 err.*/msg.* 裸码
- [x] 无 legacy 入口复活——生产 dist 零 mock（§6）；无 Apply/History/Restore 模拟入口；旧路由静默重定向沿 router 既有实现（catch-all + /projects* + /instances + /dev）；运行实例页发现 7 个真实 PrismLauncher 实例（1.20.1 / Create-Delight-Remake 系 / Collapse / MNALab）与 P1 一致

## 9. 走查中发现并随票修复的缺陷（4 项，均为前端小缺陷）

1. **0 冲突草稿无应用入口（阻断级）**——`prepareSync` 恒产草稿，冲突决议控件仅「draft 且有冲突」开放，纯 UI 流程中 0 冲突草稿（纯新增变更这一最常见场景）永远到不了 resolved，「应用同步」不可达。修复：计划页对 0 冲突草稿自动提交空决议走既有 ResolvePlan（导航新 plan_id），用户仍显式点「应用同步」，计划不可编辑语义不变。
2. **history 页 UI 不可达**——T10 路由注释「入口在工作区列表行操作由 T11 承接」而 T11 未落，历史页无任何入口。修复：工作区列表行补「同步历史」行操作（`features.history_view` 唯一门控）+ `workspaces.historyAction` locale 键。
3. **冷启动任务中心恢复入口断链**——恢复任务保留规则仅覆盖同轮缓存重建；强杀后应用重启，活跃列表与上轮缓存都无恢复任务，双入口之一缺失（恰好是本验收的核心场景）。修复：syncCache 对恢复门内关系以 `GetApplyRun` 最近运行发现 → `GetTask` 重读经既有 recovery_required 例外入缓存（查询 API 为事实源，契约 05 §5）。
4. **任务中心抽屉导航不收起**——处理恢复/查看计划/查看工作区三个跨页动作 router.push 后 Sheet 遮罩盖在目标页上。修复：导航前收起抽屉。

修复后均复走验证：§8 各勾选项在含修复的构建上完成。

## 10. 缺口与技术债终版（沿 §4 + B 口径无新增缺口）

1. 跨平台一致性未验证（沿 §4.1）；
2. UI 查询 P95 ≤ 200ms 推迟（沿 §4.2，本票 L1 走查未引入绑定桥计时基建）；
3. 硬件级真断电不做（沿 §4.3）；
4. 窗口驱动自动化与组件自动化技术债（沿 §4.4）——本票 L1 走查经桌面窗口 + UIA 辅助功能树逐页核验（真实窗口生命周期/绑定桥/真实路径场景均已覆盖），非窗口驱动自动化；
5. Defender 实时扫描方差（沿 §4.5，仅影响 perf 计时，不影响功能走查）。

## 11. 结论（B 口径）

**B 口径：通过，Phase 2 完成。** 证据链：`frontend:build` 绿且生产 dist 零 mock 痕迹（§6）；L1 数据准备含真实 `taskkill /F` 强杀产生的 recovery_required 工作区与 exact/partial 历史各一（§7）；§1.2 四组清单逐项勾选留痕（§8），其中恢复流以真实强杀状态完整走通双入口/无泄漏/门禁文案/人工确认收口/diff 归零，并额外实证 verify_mismatch 漂移拒绝的不变式 1；P1 回归四项抽验不回退（§8.4）；走查发现的 4 个前端小缺陷当场修复随票提交并复走（§9）；缺口清单无新增（§10）。A 口径（§5）+ 本 B 口径 = 验收规格 §0 的 Phase 2 完成门槛全部满足。
