# P1 验收报告（A 口径 · 后端单独验收）

- **执行日期**：2026-08-30
- **执行人**：Skye（skyraah）
- **口径**：A = 后端单独验收（验收规格 [p1-acceptance-spec.md](../p1-acceptance-spec.md) §0），headless/transport 边界；P0-1/P0-2 前端部分显式 deferred（见下）
- **对应票**：[skyraah/PackGradle#25](https://github.com/skyraah/PackGradle/issues/25)（T15）

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
| `task test:race` | ✅ 全绿（27 包 ok，无 FAIL） | 硬门槛；CGO 经 mingw-w64 gcc。本机此前缺 gcc，本轮按规格 §6.5 用 winget 安装 WinLibs UCRT 后执行 |
| `task acceptance:headless` | ✅ 两遍通过 | 见 §2.1 |
| `task acceptance:perf` | ✅ 三项达标 | 见 §3 |

### 2.1 acceptance:headless

链路：Register（PrepareRelation → CreateRelation）→ Scan（StartScan）→ Snapshot → PrepareSync → ResolvePlan → GetPlan，在全新用户数据目录执行两遍：

1. 第一遍（`rm -rf` 后全新数据目录）：关系登记、全量扫描、draft 计划、18 个 `initialize_choice` 冲突按固定策略（project 侧优先）裁决 → resolved 计划读回一致；
2. 第二遍（同一数据目录紧接重跑）：既有 Relation 复用路径同样跑通全链路。

> **基建注记**：验收规格 §1.1 所列 `acceptance:headless` 任务在本票补建（此前仅 perf 有 Taskfile 任务）：pgheadless 增 `-resolve` 补全 ResolvePlan → GetPlan 链路（默认行为不变，perf 路径不受影响）；pgfixture 增 `-mods`/`-text-files` 规模参数供小规模 fixture；Taskfile 增任务、.gitignore 增 `build/headless/`。

## 3. 性能基线（验收规格 §2）

原始记录：[p1-perf-2026-08-30-Skye-t15.json](../records/p1-perf-2026-08-30-Skye-t15.json)（本验收轮；#24 首测记录同目录）。命名注记：规格 §2.4 形如 `p1-perf-<date>-<machine>.json`，本文件加 `-t15` 后缀以区分 #24 首测与 #25 验收轮两条同日记录。

| 指标 | 实测 | 门槛 | 结果 |
| --- | --- | --- | --- |
| 冷扫描（Scan → Normalize → DB 写入） | 5,485 ms | ≤ 10 s | ✅ |
| 热扫描 | 517 ms | ≤ 2 s | ✅ |
| 热扫描 hash 命中率 | 100%（2700 hit / 0 miss） | ≥ 95% | ✅ |

冷扫描方差注记：同机同 fixture 冷扫描跨运行方差显著（#24 三轮 3137/8505/8488 ms，runtime_scan 相独大，怀疑 Defender 实时扫描高熵新文件）；本轮单次 5485 ms 落在既往区间内，三项均有余量，无需重测。

## 4. P0-1/P0-2 deferred 显式标记（检视 §7 末条）

- **P0-1（新栈接管产品入口、legacy 写入口隐藏）**：**前端部分 deferred**，待 B 口径（T16 切换发布）与真实窗口走查（T17）收口；后端侧 legacy 关联识别（`legacy_shared` 等，ADR-0001）已由 `go test` 覆盖。
- **P0-2（locale 文案）**：**前端接入 deferred**；后端结构化 `err.*` code 契约已完成且由 T13 locale 一致性测试锁定（857 键双向校验），未误报为前端已完成。

## 5. 缺口与已知未验证项

1. **跨平台一致性未验证**：P1 只测 Windows（验收规格 Round 2 决议）；
2. **UI 查询 P95 ≤ 200ms**：推迟 Phase 2 验收（Round 2 决议）；
3. **窗口驱动自动化与 Playwright/vitest 组件自动化**：技术债，待 wails3 稳定版或官方测试方案（规格 §5.5）；
4. **L1 真实窗口手工清单**：B 口径范围，本轮不适用（T17 执行）；
5. `task frontend:build`：B 口径命令，本轮不适用。

## 6. 结论

**A 口径：通过。**

证据：L0 五命令全绿（含 `-race` 硬门槛）；`acceptance:headless` 全新数据目录可重复执行且链路含 ResolvePlan；性能冷/热/命中率三项达标（记录已归档）；P0-1/P0-2 前端部分已显式 deferred。A 口径四条验收标准全部满足，B 口径（A + frontend:build + L1 清单 + 检视 §6 九条）留待 T16/T17。

## 7. Phase 2 衔接（2026-08-31 补记）

P1 结论维持：本报告 A 口径通过（B 口径随后由 [p1-acceptance-2026-08-30-b.md](p1-acceptance-2026-08-30-b.md) 关闭），证据与缺口清单不因 Phase 2 变更。Phase 2 已于 [p2-acceptance-2026-08-31.md](p2-acceptance-2026-08-31.md) 通过 A/B 双口径验收关闭，P1 回归项在其 §8.4 做了不回退抽验。
