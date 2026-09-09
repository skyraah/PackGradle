# pg-core 参考资料索引

票 #115 收口时整理（2026-09-10）。链接指向 PackGradle 仓库 `skyraah/PackGradle` 的 `ui/react` 分支（写作时含全部 ADR 与实现）。Go 语义细节以代码为准，本索引只做粗粒度指路，#120 逐域重审时精化。

## ADR 索引（按能力域归组）

### 关系与端点
- [ADR-0002 关系初始修订号语义](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0002-relation-initial-revision-semantics.md)：修订号（Revision）的代次与并发防护——**修订号简化悬念挂 #116/#120**
- [ADR-0003 元数据多步单事务](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0003-metadata-multistep-single-transaction.md)：预检（Preparation）两段式 Prepare→Apply、限时有效、仅可消费一次
- [ADR-0001 P1 前端切换与 legacy 退场](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0001-p1-frontend-cutover-and-legacy-retirement.md)：**纯历史记录**——其时代概念（切换/退场/legacy 识别）已随票 #115 删除，不再约束 pg-core

### 差异与决议
- [ADR-0013 冲突决议忽略=持久排除](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0013-resolution-ignore-persistent-exclusion.md)：忽略（Ignore）/手动处理（Manual）/同步范围规则落库
- [ADR-0009 合并语义与 adapter](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0009-merge-semantics-and-adapter.md)：合并（Merge）/冲突块（Conflict Hunk）/干净合并免确认口径
- [ADR-0010 监听触发与扫描协议](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0010-watcher-trigger-and-scan-protocol.md)：扫描协议部分（静默期聚合、触发式扫描）

### 执行与恢复
- [ADR-0004 Apply 操作日志与恢复](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0004-phase2-apply-journal-and-recovery.md)：Apply 运行/操作日志/暂存/恢复探测/恢复所需/所有权证明全套
- [ADR-0012 metafile 内容捕获与回滚降级](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0012-metafile-content-capture-and-restore-degradation.md)：mod 重取降级链

### 历史与对象库
- [ADR-0006 回滚语义](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0006-restore-rollback-semantics.md)：回滚（Restore）走完整计划+确认流程、产生新提交
- [ADR-0007 CAS 保留与 GC](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0007-cas-retention-gc.md)：保留窗口/垃圾回收/孤儿对象/回收站
- [ADR-0011 横切保留与脱敏](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0011-crosscutting-retention-and-redaction.md)：孤儿快照部分

### 下载与凭据
- [ADR-0005 模组更新通道](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0005-mod-update-channel.md)：自建 Go 下载物化胜出的原始决策——**Java 形态重估在父图 fog**
- [ADR-0008 CF 下载物化](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0008-cf-download-materialization.md)：免钥匙直链/下载物化/降级用户自备
- [ADR-0015 凭据存储](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0015-credential-storage-keyring-envelope.md)：凭据/凭据槽位/系统保险柜+信封加密——**机制重估走票 #127**

### 监听
- [ADR-0010 监听触发与扫描协议](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0010-watcher-trigger-and-scan-protocol.md)：监听（Watch）/自动快速更新/失败暂停阈值——**架构归票 #122**

### 诊断
- [ADR-0011 横切保留与脱敏](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0011-crosscutting-retention-and-redaction.md)：会话日志/别名路径

### 其他（不属本图域但为背景）
- [ADR-0014 前端 React 重写](https://github.com/skyraah/PackGradle/blob/ui/react/docs/adr/0014-frontend-react-rewrite.md)：Desktop 客户端现状

## Go 参考包（internal/，粗粒度）

| 域 | 包 | 说明 |
|---|---|---|
| 关系与端点 | `internal/adapters/prism`、`internal/adapters/packwiz` | 双端点 adapter |
| | `internal/adapters/filesystem`、`internal/adapters/managedfiles` | 文件系统与纳管文件访问 |
| 差异与决议 | `internal/core/diff`、`internal/core/plan`、`internal/core/merge`、`internal/core/normalize` | 三方差异、计划生成、合并、规范化 |
| | `internal/pgignore` | 忽略规则 |
| 执行与恢复 | `internal/syncstage` | Apply 阶段推进 |
| | `internal/application`、`internal/service` | 应用编排与服务装配 |
| 历史与对象库 | `internal/core/gc`、`internal/store/objectstore`、`internal/store/sqlite` | GC、CAS 对象库、SQLite |
| 下载与凭据 | `internal/download`、`internal/cdnproc`、`internal/curseforge` | 下载链 |
| | `internal/secrets` | 凭据（keyring+信封加密） |
| 监听 | `internal/adapters/fsnotifywatch`、`internal/notify` | fsnotify 适配与通知 |
| 诊断 | `internal/sessionlog`、`internal/errs` | 会话日志与错误面 |
| 其他 | `internal/core/model`、`internal/core/ids` | 领域类型与标识 |
| | `internal/transport` | 事件传输（协议面参考，票 #119） |
| | `internal/singleinstance`、`internal/bootstrap`、`internal/appconfig` | 进程/启动/配置（票 #116） |
| | `internal/junction` | **已取消概念的残留**，仅历史参考 |
| | `internal/envutil`、`internal/fsutil`、`internal/perffixture` | 工具与性能夹具 |

## 契约文档（客户端面，票 #119/#123 参考）

- `docs/contract/03-p1-contract.md`：能力（Feature）/可用性（Availability）/重绑契约
- `docs/contract/04-p1-event-protocol.md`：事件流协议、事件流序号、受控重查

## 研究票（已收口，结论在票内评论与研究分支）

- [#117 Java 生态事实矩阵（JDK/库/打包/LSP）](https://github.com/skyraah/PackGradle/issues/117)：#118 的输入（已关，2026-09-10；要点见 map-state.md）
- [#126 Java 系统保险柜生态（keyring/DPAPI）](https://github.com/skyraah/PackGradle/issues/126)：#127 的输入（已关，2026-09-10；要点见 map-state.md）
