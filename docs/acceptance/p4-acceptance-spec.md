# Phase 4 验收口径与合并/监听/横切场景验收（P4-E2E）

> **状态：已决议（验收规格）。** 2026-09-03 经决策图票 #76 grilling 拍板定稿（七决策点全按推荐）；执行会话按本规格执行验收，不再做隐式决策。
> 来源：决策图「PackGradle Phase 4 决策图（合并·watcher·横切保留脱敏）」票 [#76](https://github.com/skyraah/PackGradle/issues/76)。
> 依据：[P3 验收规格](p3-acceptance-spec.md)（骨架沿用基准）、[ADR-0009](../adr/0009-merge-semantics-and-adapter.md)（合并事实模型）、[ADR-0010](../adr/0010-watcher-trigger-and-scan-protocol.md)（watcher 权威）、[ADR-0011](../adr/0011-crosscutting-retention-and-redaction.md)（横切决议）、[契约 07](../contract/07-p4-merge-watcher-contract.md)（验收对象面）、ADR-0005 §4/§5（冲突删除红线、`.index` 只读）。

## 0. 两个口径（沿用 P3 §0）

- **A = 后端单独验收**：headless/transport 边界，可先行、可多轮执行。P4 全部新面（merge/watcher/横切）均为后端面，A 口径全覆盖。
- **B = Phase 4 完整验收**：= A + `frontend:build` + L1 增量清单全勾 + **P1/P2/P3 三回归不回退** + **真网冒烟沿用 P3 归档**（Q1：P4 零新网络面——合并本地合成、监听本地文件系统、横切纯内部动作，下载字节路径零改动；引用 [P3 报告](reports/p3-acceptance-2026-09-01.md) §5.2 即算「已执行已记录」，下载面回归以 L0 假 CDN 链为准）。**B 是 Phase 4 完成门槛**——B 通过即 Phase 4 可标完成。

验收在 Windows 开发机执行，不建 CI；报告必须记录机器规格（§7）。

## 1. 分层与自动化边界（沿用 P2/P3 §1）

**L0 = 无人值守自动化层**（headless，可重复执行，命令集挂 Taskfile）；**L1 = 真实窗口手工清单层**。不引入窗口驱动自动化与组件自动化——技术债注记沿用。覆盖边界原则沿用：headless 能验的不上窗口层；窗口层只验 headless 验不了的。

### 1.1 L0 命令集

| 命令 | 内容 | A | B |
| --- | --- | --- | --- |
| `task test` | `go test ./...`（**增量**：merge 真值表 §3.1、watcher 触发器状态机 §4.1、横切 fake clock §5.1、脱敏断言 §5.4） | ✅ | ✅ |
| `task test:vet` / `task test:race` | 照跑（race 硬门槛，gcc 前提沿用） | ✅ | ✅ |
| `task acceptance:headless` | P3 `-apply`/`-restore` 全链照跑（回归） | ✅ | ✅ |
| `task acceptance:recovery` / `task acceptance:recovery:restore` | P2 apply 强杀×5 + P3 restore 强杀×5 照跑（回归） | ✅ | ✅ |
| `task acceptance:download` | P3 假 CDN 五场景照跑（回归） | ✅ | ✅ |
| `task acceptance:gc` | P3 引用图链照跑 + **孤儿快照扩展**（§5.3） | ✅ | ✅ |
| `task acceptance:merge` | **新增**：合并全链四场景（§3.2） | ✅ | ✅ |
| `task acceptance:watcher` | **新增**：监听不变式链（§4.2） | ✅ | ✅ |
| `task acceptance:crosscut` | **新增**：横切重启清理链（§5.2） | ✅ | ✅ |
| `task acceptance:perf` | P3 门槛照跑（restore 段 known-exceed 处置见 §6）+ P4 新面计量（只记录） | ✅ | ✅ |
| `task frontend:build` | `vue-tsc --noEmit` + Vite 生产构建（口径沿用） | — | ✅ |

### 1.2 L1 手工清单（B 口径，契约 07 §6 全量，逐项勾选）

**合并呈现**
- [ ] 计划页 `merged_clean` 行「将自动合并」徽标 + `take_merged` 默认推荐 + 「查看合并结果」入口 → 预览抽屉：合并后全文 + 语法高亮（toml/json/js/java，未识别退纯文本）+ 绿红黄行级标注（锚点=`base_content`）
- [ ] 冲突卡点开块列表（project/base/runtime 行片段 + 起始行号）；`manual` 兜底入口照旧（外部编辑后再扫描收编）

**快速更新入口（单调用三态）**
- [ ] 概览主操作改调 `QuickUpdate` 单调用：进行中统一 busy（scan/plan/apply 三阶段文案退役）；`no_diff` → 「已是最新」；`apply_started` → 任务中心移交；`awaiting_confirmation` → 导航计划页

**待确认与 watch 状态**
- [ ] 工作区列表行/概览「有待确认计划」角标（`pending_plan_id`）→ 计划页；`relation_invalidated` 到达即刷新
- [ ] `paused` 横幅「自动同步已暂停，手动快速更新一次即可恢复」；`unavailable` 横幅「监听不可用，已回退手动」+ `watch_failed` 一次性提示；`active` 不渲染

**系统通知（三条件 + 降级强制勾）**
- [ ] 真弹：自动链停靠待确认 ∧ 窗口不在前台 ∧ `pending_plan_id` 更新 → Win11 通知中心 toast；点击前置窗口直达 `/workspaces/:id/plans/:pending_plan_id`
- [ ] **降级**：系统设置关闭 PackGradle 通知 → 静默只亮角标，不报错不重试（Q5 强制项）

**P1/P2/P3 回归不回退**
- [ ] P2 §1.2 应用链路与恢复流、P3 §1.2 回滚链与 GC/设置、P1 §1.3 事件/互斥/locale/无 legacy 复活各抽验沿例

## 2. 红线场景化表（六条全落 L0 硬断言，Q7）

| 红线 | L0 断言落点 |
| --- | --- |
| ①冲突与删除永不自动执行 | `acceptance:watcher` 场景 3（含冲突必停待确认）；`acceptance:merge` 场景 3（冲突行永不进自动面）；QuickUpdate 停靠判定单测（§4.1） |
| ②合并产物一律入 CAS + 回滚零网络 | `acceptance:merge` 场景 1（含 mod metafile 例外启用，ADR-0009 §9）/ 场景 2 |
| ③未冲突区域字节级不变 | merge 真值表断言（§3.1）+ 链上对落盘文件直接断（§3.2 场景 1） |
| ④`.index` 只读不写 | merge 黑名单真值表（§3.1）+ 监听排除断言（§4.2 场景 1：写 `mods/.index` 不触发） |
| ⑤监听零新事件类型 | 事件类型枚举不变；自动链效果只经 `task_updated`/`relation_invalidated` 到达（§4.2 全链事件集断言）；`watch_failed` 沿契约 04 §2.5 预留形状启用（§4.1） |
| ⑥凭据永不进日志/诊断/错误 detail | R3 复核记录复跑归档（§5.4）+ 注入断言：凭据在场失败时日志/错误 detail/诊断输出零泄漏 |

## 3. merge 验收

### 3.1 真值表（单测族，表驱动纯函数）

| 两侧输入 | 预期 |
| --- | --- |
| 同改不重叠 + 类型校验通过 | `merged_clean`；合并产物确定性可复算 |
| 同改重叠（真冲突） | `conflict_modify` + detail hunk 数组 |
| 合并结果类型校验失败（toml/json 残缺） | 降级 `conflict_modify`，块证据保留（非错误） |
| 双侧 digest 相等 | `converged`，不走合并 |
| 一边删一边改 | `conflict_delete_modify`，维持选侧不合并 |
| 二进制资源 | 永不合并（黑名单） |
| `.index` | 永不合并（黑名单 + 只读） |

外加两条形状断言：**未冲突区域字节级不变**（hunk 之外前后 diff 为零，fixture 含手工注释/键序/空行/缩进——ADR-0009 §2 验收口径）；hunk detail JSON 定形（`{"hunks":[{"project":{"start":N,"lines":[...]},"base":{"start":N,"lines":[...]},"runtime":{"start":N,"lines":[...]}}]}`，域词汇 project/base/runtime）。

### 3.2 `acceptance:merge` 链（四场景）

fixture：pgfixture「双侧变更」变体（§3.3）。

1. **merged_clean 全链**：造双侧同改 → 扫描 → 断言分类 `merged_clean` + `take_merged` 默认推荐 + `merged_clean_count` → resolve `take_merged` → confirm → committed → 断言：双端落盘字节=确定性重算产物；未冲突区域字节级不变（对落盘文件直接断）；产物入 CAS（**含 mod metafile**：断言 CAS 存在该 digest 对象）；
2. **回滚零网络**：回滚到场景 1 提交 → merged 行 `restorable_from_cas` → 零网络零用户介入 committed；
3. **授权模式口径**：授权开态 → 快速更新含 `merged_clean` 行随非冲突批量免确认直达 committed；构造含冲突块行 → 永不自动、停 `awaiting_confirmation`；
4. **预览与错误码**：`GetMergedPreview` 对 `merged_clean` 行返回 `content`/`base_content` 两段全文且 `content` 与场景 1 落盘字节一致（「所见即所写」）；stale/expired 计划仍可预览（只读）；对非 merged_clean 行 → `err.merge.not_mergeable`（{0}=resource_id）。

### 3.3 夹具需求

pgfixture 增：双侧变更构造入口（对同一 toml 注入两侧不同改动）、含手工注释 toml 样本、二进制资源样本。

## 4. watcher/快速更新验收

### 4.1 触发器状态机（单测族，假时钟/注入）

- 静默期 1.5s 聚合 + 10s 上限强制触发（常数施工可调，断言行为不卡毫秒）；
- inflight 单飞 + 新失效标 dirty + 本轮结束补一轮至干净（风暴 ≤2 轮）；
- 连败 2 次暂停自动面 + 手动快速更新成功复位；
- 恢复期挂载保持、触发只标脏不物化；
- 管辖目录消失 → 回退最近存在的父目录 → 再现重挂；监听异常 → 有限重建 → 仍败发 `watch_failed`（envelope 带 relation_id、payload `{}`）；
- 监听面 = MappingPolicy 函数（项目侧 `pack.toml`+mods+config/kubejs/scripts/defaultconfigs、运行侧 `minecraft/` 同名管辖目录；排除 logs/saves/Prism 自有元数据/`mods/.index`）；
- **QuickUpdate 停靠判定**：draft 含冲突 / `confirmation_requirements` 非空 / 授权关闭 → `awaiting_confirmation`；requirements 空 ∧ 授权开 → `apply_started`；无差异 → `no_diff` 不建计划；
- **系统通知三条件判定**（纯逻辑）：自动链停靠 ∧ 窗口不在前台 ∧ `pending_plan_id` 更新（无→有或换新；同计划重复停靠不重弹）；toast 发送失败/被拒 → 静默降级不报错不重试（错误注入）。

### 4.2 `acceptance:watcher` 链（真 fsnotify 真文件写入，只断不变式）

前置基建：pgheadless 常驻监听模式（§9.1）；编排进程外部写入 + 事件/扫描轮数时间线记录。

1. **触发与收敛**：外部写管辖目录文件 → 静默期后自动链触发 → 授权开态 committed → 写盘自触发重扫 `no_diff` 收敛（轮数有界）；同场景写 `mods/.index` → **不触发**（红线④）；
2. **去抖上界**：以 <1.5s 间隔持续写 ≥30s → 扫描轮数有上界（10s 上限量级，不卡具体毫秒）；
3. **停靠待确认**：授权关态（或构造损失面差异）→ 自动链停 `awaiting_confirmation` → `pending_plan_id` 就绪 + 收口点 `relation_invalidated` 发射（契约 07 §4 新发射点）；含冲突差异 → 必停（红线①）；
4. **连败暂停与复位**：构造自动执行终态 failed（注入手段归执行规格，候选=假 CDN 全挂使全部 redownload 失败）×2 → `watch_status=paused` + 无第三次自动执行 + 监听保持（文件变化仍标脏）→ 手动快速更新成功 → 复位 `active`；
5. **恢复期只标脏**：构造 `recovery_required` → 触发文件变化 → 无自动物化；
6. **并发 join**：`QuickUpdate` 同 relation 并发双调 → 等待并返回同一结果（双击/双窗口安全）；其他来源活跃任务照常互斥（`err.scan.already_running` 透传）。

全链事件集断言：链内只发既有 `task_updated`/`relation_invalidated`（红线⑤）。

### 4.3 系统通知真弹面 → L1

通知中心是 OS UI，无人值守断不了：判定与降级归 §4.1 单测，真弹/点击直达/系统设置关闭后的静默降级归 L1（§1.2）。

## 5. 横切验收

### 5.1 保留期限 fake clock 单测族（ADR-0011 §1–§4）

- 日志：保 20 会话（最近 3 明文 `.log` + 更早 17 份压缩 `.log.gz`）；**100MB 总量硬顶优先于份数**（造超顶体积 → 从最旧会话删至限内，允许低于 20 份）；
- `task_events`：10,000 条窗口截断；`stream_sequence` 从 MAX+1 续；清全表从 1 重来（前端不误判漏包）；
- 旧数据行：`sync_plans` expired/stale 物理删、applied 行随提交存亡；`preparations` 过期/consumed 删；终态 `tasks` 保 200；**`apply_runs` 永不删**（墓碑计数分子）；
- 孤儿快照判定函数：不被存活提交（`verified_*_snapshot_id`）/计划（`input_*_snapshot_id`）引用且非任一端最新 → 孤儿。

### 5.2 `acceptance:crosscut` 链（真重启）

惰性清理挂在「启动时 + 任务终态」两个时机，单测够不着真实启动路径，必须真跑：

1. 造超量数据（>20 会话日志目录含超 100MB、>10k `task_events`、>200 终态 `tasks`、过期 `sync_plans`/`preparations`）→ 重启 headless → 断言启动通道清理生效；
2. 任务终态通道：驱动一任务收口 → 断言终态后清理触发；
3. 脱敏断言（R1/R2）：构造含绝对路径的端点错误 → 断言新写 `Diagnostic.Detail` 为别名路径、无用户名（历史行不追溯，不断言）；`-metrics` 输出无 `Host`（OS/Arch/GoVersion/CPUs 保留）。

### 5.3 `acceptance:gc` 孤儿快照扩展（ADR-0011 §4）

P3 引用图链照跑 + 增：提交被修剪 → 其验证快照自然转孤儿一并删；从未进提交的中间扫描快照除最新外同删；`resource_representations` 随快照行级联删（PK 前缀即 snapshot_id）；引用图不变式对账扩展至快照账面。

### 5.4 R3 凭据复核（记录面 + 断言面）

复核记录随执行规格复跑并归档进验收报告（现行口径见 ADR-0011 §9）；单测断言：注入 `curseforge_api_key` 在场的失败 → 日志/错误 detail/诊断输出零泄漏（红线⑥）。

## 6. 性能门槛（沿 P3 §7 数字 + P4 处置）

| 指标 | 门槛 | 来源 |
| --- | --- | --- |
| 冷扫描 ≤10s / 热扫描 ≤2s / 热命中率 ≥95% | 沿用 | P1 §2.3 |
| 冷 apply ≤30s / 峰值内存增量 <256MiB | 沿用 | P2 §3 |
| restore 全链路冷 ≤30s / 内存 <256MiB | 沿用；**known-exceed 豁免挂 #69**（Q6：照跑照记录，#69 关闭前不判 A/B 失败；#69 回报若触发门槛修订回图票则随改） | P3 §7 |
| GC ≤30s | 沿用 | P3 §7 |
| download 相位 | 只记录 | P3 §7 |
| **P4 新面**：merge 分相（diff3/校验/写盘）、watcher 触发→链收口墙钟、快速更新链整体 | **只记录不设门槛**（`-metrics` 增量）；内存照断言（<256MiB） | Q6（ADR-0010 §8 瓶颈回图预留） |

UI 查询 P95 ≤200ms 继续推迟（技术债注记沿用）。超标处置沿 P2 §3（记录原因与机器规格，环境异常注明后重测）。

## 7. 记录与报告（沿 P3 §4 模式）

- 原始记录：`docs/acceptance/records/p4-merge-<date>-<host>.json`、`p4-watcher-<date>-<host>.json`（含事件/扫描轮数时间线）、`p4-crosscut-<date>-<host>.json` + P3 各记录沿格式照跑（同日多轮 `-tNN` 后缀）。
- 报告 `docs/acceptance/reports/p4-acceptance-<date>.md`，必填沿 P3 §4（机器规格、超标处置注记）；**无新真网节**（引用 P3 归档）；R3 复核记录归档于本报告。
- **A 口径通过** = L0 全绿（test/vet/race + headless/recovery/recovery:restore/download/gc/merge/watcher/crosscut/perf）+ 性能达标（restore known-exceed 豁免除外）+ 报告归档。
- **B 口径通过** = A + `frontend:build` + L1 全勾 + P1/P2/P3 回归不回退 + 真网冒烟沿 P3 归档引用。

## 8. 明确不在 Phase 4 验收范围

1. 跨平台一致性深测、UI P95、窗口驱动自动化（P1–P3 技术债延续）；
2. 真网冒烟重跑与真网自动化门槛（Q1：P4 零新网络面；P3 §5.2 沿用）；
3. toast 真弹的自动化断言（OS UI，归 L1）；
4. watcher 时序硬断言（Q3：不变式+轮数上界打法，不卡毫秒）；
5. P4 新面性能门槛（Q6：只记录；ADR-0010 §8 瓶颈回图）；
6. 诊断包导出验收（ADR-0011 §6 不做）；
7. staging 恢复证据保留（ADR-0011 §5 雾区，待 #69 回图联审）；
8. 增量枚举协议（ADR-0010 §8 不做）。

## 9. 执行会话工作项（验收基建清单）

本规格定口径；以下基建由 Phase 4 执行会话实现：

1. pgheadless 常驻监听模式（挂 watcher + 自动链；编排进程外部写入 + 事件/轮数时间线记录；授权开/关态变体）；
2. pgfixture「双侧变更」变体 + 含手工注释 toml 样本 + 二进制样本（§3.3）；
3. `acceptance:merge` 链（§3.2 四场景断言）；
4. `acceptance:watcher` 链（§4.2 六场景 + 全链事件集断言；连败注入手段）；
5. `acceptance:crosscut` 链（§5.2 + 超量造数）；
6. `acceptance:gc` 孤儿快照扩展（§5.3）；
7. 单测族：merge 真值表、watcher 触发器状态机（假时钟）、QuickUpdate 停靠判定、通知三条件+降级、横切 fake clock、脱敏断言；
8. `-metrics` 增量：merge 分相、watcher 触发→收口墙钟、快速更新链相位；
9. Taskfile 任务挂接（§1.1 对齐）；
10. B 口径前 L1 walkthrough 数据准备（双侧变更工作区、待确认计划、`paused`/`unavailable` 态构造、授权开态、通知降级环境）。
