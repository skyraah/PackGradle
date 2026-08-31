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
