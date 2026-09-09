# pg-core 概念地图

来源：PackGradle 仓库 wayfinder 图 #114 票 #115（2026-09-10 收口）。本文件是 pg-core（Java Core 重建）的领域概念定稿与能力域分组；术语定义见同目录 `CONTEXT.md` 快照（母本为 PackGradle 仓库根 `CONTEXT.md`）。

**Demo 锚点**（父图 #114 Notes）：CLI 最小闭环——创建工作区→扫描→差异→计划→Apply→查历史→回滚；Core 独立进程运行。无 watcher、无凭据、无合并、无下载物化。

**口径**：强参考默认——Go ADR 决策为现状候选，维持不需论证，推翻/简化需说清理由。本图只定概念与分组；域内语义逐域重审走票 #120（按本图分组拆票）。

## 能力域分组（7 域 · 40 概念）

每概念后标 Demo 范围：`[demo]` = Demo 闭环需要；`[demo 外]` = 产品需要、Demo 不做。

### 1. 关系与端点（7）

| 概念 | Demo | 备注 |
|---|---|---|
| 工作区（Relation） | demo | 领域中心概念：一条受管同步关系的产品投影 |
| 项目源（Project） | demo | Packwiz `pack.toml` 端点 |
| 运行实例（Runtime） | demo | Prism 实例端点 |
| 预检（Preparation） | demo | 创建工作区走 Prepare→Apply 两段式 |
| 重绑（Rebind） | demo 外 | Demo 只列创建，不列重绑 |
| 修订号（Revision） | demo | 并发防护语义在 daemon 单进程语境下是否简化 → 挂 #116 进程模型下游，#120 关系域重审时裁 |
| 策略集版本（Policy Set Version） | demo | 模板演进字段，随关系创建携带 |

### 2. 差异与决议（10）

| 概念 | Demo | 备注 |
|---|---|---|
| 扫描（Scan） | demo | 闭环第二步 |
| 扫描快照（Scan Snapshot） | demo | 差异的输入 |
| 差异（Diff） | demo | 闭环第三步；三方比较 |
| 计划（Plan） | demo | 闭环第四步 |
| 上游变更（Upstream Change） | demo | 差异的来源叙事，无独立机制 |
| 忽略（Ignore） | demo | 计划面基础决议语义 |
| 手动处理（Manual） | demo | 计划面基础决议语义 |
| 同步范围（Sync Scope） | demo | 映射策略声明的管理边界（原「受管范围」，2026-09-10 更名） |
| 合并（Merge） | demo 外 | ADR-0009 全套语义保留 |
| 冲突块（Conflict Hunk） | demo 外 | 随合并域 |

### 3. 执行与恢复（8）

| 概念 | Demo | 备注 |
|---|---|---|
| Apply 运行（Apply Run） | demo | 闭环第五步 |
| 操作日志（Operation Journal） | demo | Apply 内部事实记录 |
| 暂存（Staging） | demo | Apply 隔离区 |
| 恢复探测（Recovery Probe） | demo | 崩溃恢复裁决 |
| 恢复所需（Recovery Required） | demo | 安全未证明的状态 |
| 所有权证明（Ownership Proof） | demo | 归属判定的唯一依据 |
| 快速更新（Quick Update） | demo 外 | 一键批量入口 |
| 授权模式（Authorized Mode） | demo 外 | 免逐次确认开关 |

### 4. 历史与对象库（7）

| 概念 | Demo | 备注 |
|---|---|---|
| 同步提交（Sync Commit） | demo | 闭环第六步（查历史） |
| 回滚（Restore） | demo | 闭环第七步 |
| 保留窗口（Retention Window） | demo | 回滚可用范围，闭环依赖 |
| 孤儿快照（Orphan Snapshot） | demo 外 | GC 机器 |
| 孤儿对象（Orphan Object） | demo 外 | GC 机器 |
| 回收站（Trash） | demo 外 | GC 机器 |
| 垃圾回收（Garbage Collection） | demo 外 | GC 机器 |

### 5. 下载与凭据（4）

| 概念 | Demo | 备注 |
|---|---|---|
| 免钥匙直链（Keyless Direct Link） | demo 外 | Java 形态重估在父图 fog（旧 ADR-0005/0008） |
| 下载物化（Download Materialization） | demo 外 | 同上 |
| 凭据（Credential） | demo 外 | 机制重估走票 #127 |
| 凭据槽位（Credential Slot） | demo 外 | 同上 |

### 6. 监听（2）

| 概念 | Demo | 备注 |
|---|---|---|
| 监听（Watch） | demo 外 | 架构归票 #122 |
| 自动快速更新（Auto Quick Update） | demo 外 | 触发归本域，链路共用执行域快速更新 |

### 7. 诊断（2）

| 概念 | Demo | 备注 |
|---|---|---|
| 会话日志（Session Log） | demo 外 | 对应父图 fog「诊断/日志/脱敏横切」 |
| 别名路径（Aliased Path） | demo 外 | 同上 |

## 契约面概念（不进本图）

能力（Feature）、可用性（Availability）、受控重查（Controlled Re-query）、事件流序号（Stream Sequence）是客户端契约概念，技术形态归协议票 #119 与接入契约票 #123 重审，本图不收录、不分组。

## 跨域关系

- 差异与决议的计划 → 驱动执行与恢复的 Apply 运行。
- 执行与恢复的 Apply 运行收口 → 产生历史与对象库的同步提交；失败 → 进入恢复所需。
- 历史与对象库的回滚 → 走差异与决议的计划生成 + 执行与恢复的完整确认流程，产生新提交。
- 监听的自动快速更新 → 触发执行域快速更新同一条编排链。
- 下载与凭据的下载物化 → 取数字节走执行域暂存→Apply 既有管线。
- 历史与对象库的垃圾回收受执行域「恢复所需」与暂存所有权证明约束，只在两者皆无时执行。
- 诊断的别名路径贯穿全部域的错误与诊断信息面。
- 关系与端点的忽略决议落地 → 差异域同步范围规则。

## 已删除与挂起

- **删除**：legacy 识别、切换（Cutover）、退场（Retirement）三个时代概念（2026-09-10，票 #115）。Java 重做对旧 Junction/TOML 关联不识别、当普通内容；CONTEXT.md 词条已移除；ADR-0001 转纯历史记录。
- **挂起**：修订号并发防护语义 → #116 进程模型下游 + #120 关系域重审。
- **信号**：「开新仓库」倾向已在票 #115 记录，#118（仓库物理形态）开审时作现状候选。
