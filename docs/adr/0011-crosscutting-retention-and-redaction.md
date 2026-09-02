---
status: accepted
date: 2026-09-02
---

# 0011 · 横切保留与脱敏（日志 · Task 事件 · 旧数据 · 诊断面）

决策图 #70 票 [#74](https://github.com/skyraah/PackGradle/issues/74) 的产物，收口 #32 十决议点（清单见 `docs/research/crosscutting-retention.md` §5，事实面勿重查）。锚点：ADR-0004「普通用户视图不得暴露临时绝对路径」的 root-relative 先例；ADR-0007 保留窗口与 GC 引擎已落地（两层模型、连续前缀修剪、孤儿清扫）；ADR-0008 CF 凭据面。本 ADR 决议均为后端内部动作，不触服务面 wire（契约 07 无新增项）。术语「会话日志」「别名路径」「孤儿快照」见 CONTEXT.md。

## 1. 日志：slog + 会话目录 + 双参数

引入 `log/slog`（现全仓 129 个标准库 log 调用点，迁移面归执行规格；本 ADR 锁形态与参数）。落地形态=**按会话分目录**：每次启动一份 `logs/<启动时间戳>/`（启用既有预留 LogsDir），slog 结构化 JSON 落会话文件——Windows GUI 子系统下 stderr 无处落地，桌面形态运行期日志今天事实丢失，watcher 常驻（ADR-0010）后不可接受。

参数双轴，**总量硬顶优先于份数**：

- 份数（常态）：保最近 **20 个会话**，最近 3 份明文 `.log`、更早 17 份压缩 `.log.gz`；
- 体积（硬顶）：**总量 100MB**，超顶从最旧会话删起直至回到限内（允许低于 20 份）。

理由：「只按份数清理」是两个真实事故的共同盲区（VS Code 342GB / Modrinth App 145GB，用户日志撑爆盘）。

## 2. task_events：条数窗口 + 惰性清理

表只写不读（无重放 API，前端 `lastSeenSeq` 不持久化），截断零功能风险。决议：

- 窗口=**保最近 10,000 条**（数值施工可调，数量级不变）；
- 时机=**启动时 + 任务终态后惰性清理**，无定时器；
- 截断后 `stream_sequence` 从 MAX+1 继续（既有硬约束；清全表则从 1 重来，前端重启以首个事件建基线，皆不误判漏包）。

## 3. 旧数据行：过期 ∧ 无存活引用即删

与 §2 同一惰性清理通道（启动 + 任务终态），同一判定哲学——**过期 ∧ 无存活引用**：

- `sync_plans`：expired/stale 历史行物理删除；applied 计划行随其提交存亡（`sync_commits.plan_id` 外键，提交被 GC 修剪后方可删）；
- `preparations`：按既有 `expires_at` 过期即删（consumed 行同理；表无外键被引用）；
- `tasks`：终态行保最近 **200 条**（`commit_id` 已由 GC 置空解除外键）；
- **`apply_runs` 永不删**：墓碑计数（PrunedBeforeCount=committed 运行数−现存提交数）的分子依赖，观测口径基石。

## 4. 扫描快照：不可回滚即删（挂 GC）

回滚走提交/基线（保留窗口词条），快照只是证据、不是回滚目标。故「该版本快照无法被回滚」在机制上=**孤儿快照**：不被任何存活提交（`verified_*_snapshot_id`）、不被任何计划（`input_*_snapshot_id`）引用、且不是任一端最新一份（最新快照是 diff/回滚/工作区视图的当前状态读取面）。

- 执行挂**既有 GC 引擎清扫阶段**（与 `.tmp-*` 同位的账面孤儿清扫）：提交被修剪后其验证快照自然转孤儿一并删；从未进提交的中间扫描快照（反复扫描不 Apply）除最新外同删；
- `resource_representations` 随快照行级联删（PK 前缀即 snapshot_id）；
- **零新保留参数**：快照窗口=提交保留窗口（ADR-0007 KeepCommits/KeepDays），不引入独立快照策略。

## 5. staging 保留：未决（雾区）

`recovery_required` 运行的暂存证据保留多久、确认后清理时机、超期处置——**本票不决**（涉恢复安全，需与 ADR-0004 §4 恢复矩阵联审），待 #69 restore 冷链路执行侧回报后回图成票。ADR-0004 §7 空档维持。

## 6. 诊断包：P4 不做，内容边界先锁

诊断包导出功能 P4 **不做**（`exports/` 预留不动，无 telemetry）。将来建导出面时的硬约束随本 ADR 成文：

- 默认内容=**结构化摘要**（DB 摘要 + 最近会话日志 + 任务/事件摘要），非整库副本（R5 最小化）；
- 分享侧保留参照 mclo.gs 惯例（自最后查看起 90 天）——仅记参照，不构成承诺；
- 历史行补偿义务见 §7 R1。

## 7. 脱敏规则表（R1–R7）

- **R1 绝对路径（必做，现在落地）**：`Diagnostic.Detail` 新写入即**别名路径**化——落点在构造侧（endpoint 错误串构造），非查询侧后处理：单一事实源，UI 与将来导出同数据、无分档渲染。**历史行不追溯**（已落库的含用户名绝对路径行保持原样，本机数据不外泄则风险可控）；将来建导出面时**必须**对历史行按当时端点根前缀兜底替换——此补偿义务随 R1 成文。
- **R2 机器名**：`pgheadless -metrics` 删 `machineInfo.Host`（不再采集 os.Hostname）；OS/Arch/GoVersion/CPUs 保留（通用环境信息）。
- **R3 凭据（硬规则）**：config 值（含 curseforge_api_key）**永不进日志、诊断、错误 detail**；错误出口统一 errs.AppError（code/args/detail 白名单化），禁止底层错误体原样透传。复核记录见 §9。
- **R4 内容摘要/digest**：内容身份非机器身份，可导出；文档标注此定性。
- **R5 配置/策略内容**：导出最小化（§6），默认不含 policy_json 整库副本。
- **R6 临时绝对路径**：并入 R1——root-relative/别名路径是全诊断面默认；恢复裁决等必须绝对路径的场景仅限恢复面显示、不进导出。
- **R7 IP/网络信息**：当前唯一网络面=CF API 出站，无 IP 采集；下载通道扩展（Modrinth/URL，图外）时补占位规则。

## 8. 容量红线：双指标承载 + 埋点勘误

红线承载指标=**`cas_total_bytes`（CAS 总量）+ `free_disk_bytes`（卷剩余）**双指标；阈值数值后置至有观测数据后随执行规格拍（研究中既定口径）。

**勘误**：票面锚点「GetStorageStats 观测口径（Phase 2 已埋点）」不实——现仅 `GetHashCacheStats`（票 #17），存储指标**未埋**；`GetStorageStats` 连同研究笔记 §4 口径表（cas_object_count / cas_tmp_leftovers / staging_* / task_events_count / db_size_bytes / free_disk_bytes）随执行规格新增。staging 侧指标待 §5 决议后补。

## 9. R3 复核记录（2026-09-02，main @ 21aee86）

现行 129 个 log 调用点（调研时 31 个，P3 apply/gc/restore 施工后增长）复核：全部只渲染任务/运行 ID 与错误值；密钥唯一流转=config → `internal/curseforge/client.go:50` 请求 header，URL 不携带；错误串仅含 URL（*url.Error）或状态码（errs 白名单）；appconfig 与 curseforge 包零 log 语句；无 `%v` 渲染 config 结构体。**结论：现行无泄漏点，R3 为前瞻规则**——slog 迁移（§1）与下载通道扩展时随执行规格复跑本复核。
