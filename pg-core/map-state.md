# pg-core 决议快照（map-state）

来源：PackGradle 仓库 wayfinder 图 #114（GitHub issue `skyraah/PackGradle#114`）。本文件是图进度的可搬运快照，随每张票收口刷新；新项目记忆以本文件拿到来龙去脉。权威记录始终在 GitHub issue 上。

## Destination

pg-core（Java Core）重建的架构决策集：所有阻塞实现的决策各有 ADR/决议 + 模块边界图 + 迁移与验证路线，达到「可以开第一张实现票」的程度。图只出决策：prototype 票的产物是决策证据，不是生产代码。

## Notes（裁决环境）

- **Demo 锚点**：CLI 最小闭环（创建工作区→扫描→差异→计划→Apply→查历史→回滚），Core 独立进程运行；无 watcher、无凭据、无合并、无下载物化。
- **Java 公理（不重审）**：「java 对于我比较熟悉，且更方便抽象化/模块化，保持低耦合」——用户原话。
- **裁决尺（两案接近时按序优先）**：更容易理解 > 更容易调试 > 更容易重构 > 更少运行时魔法 > 更少隐式依赖 > 更少基础设施 > 更少需要记忆的规则。服务单人长期维护，防早期过度复杂化。
- **旧 ADR 处置**：0001–0015 全部重开；方式=强参考默认——Go ADR 决策列为现状候选，维持不需论证，推翻/简化需说清理由。pg-core 新决策落新仓库 docs/adr/ 续编号（0016 起）。
- **Go 栈状态**：立即停更，含 bug fix（2026-09-10 定）；数据迁移不做（开发期无存量数据）。Go 实现只作业务行为与边界情况参考，不机械翻译 package。
- **客户端深度**：CLI = v1 一等公民；VS Code = 只定接入契约；Desktop = React UI 照 ui/react 分支节奏自迁（用户自写），换芯（bindings→protocol 客户端）为图内后置票。
- **工作节奏**：非心脏票 = agent 备料（research/对比矩阵/草案）→ 用户收口 grilling；事实面子代理查，价值判断归用户。

## Decisions so far

- [#117 [research] Java 生态事实矩阵（JDK/库/打包/LSP）](https://github.com/skyraah/PackGradle/issues/117)：JDK 21/25 Premier 至 2028-09/2030-09（LTS 两年节奏）；sqlite-jdbc 活跃、Flyway-SQLite 已社区化（手写 PRAGMA user_version 为惯用）；Jackson 2.21/3.x 双线、picocli 4.7.7、lsp4j 1.0.0（jsonrpc 模块可独立使用）；JDK WatchService macOS 为轮询实现；jpackage + JEP 514/515 AOT cache。全文见票内评论与 research/java-eco-matrix 分支。
- [#126 [research] Java 系统保险柜生态（keyring/DPAPI）](https://github.com/skyraah/PackGradle/issues/126)：java-keyring 停更约 3 年且 Windows 后端 CredWriteA 逼近凭据管理器 2560 字节上限；JNA 自带 DPAPI 但无 Wincred/Keychain 绑定；macOS SecKeychain 被弃用（TN3137）且有 Gradle daemon 长跑失效 issue；纯 JDK 信封零新依赖可行（AES-GCM+PBKDF2 标准自带，HKDF 在 JDK 25 定稿、21 需手写 RFC 5869）。全文见票内评论与 research/java-keyring-eco 分支。
- [pg-core 领域模型与能力域重审（概念地图 + 分组）](https://github.com/skyraah/PackGradle/issues/115)（2026-09-10）：十核心概念全保留；时代词三删（legacy 识别/切换/退场，Java 对旧关联不识别当普通内容）；补五词条（扫描/扫描快照/差异/计划/同步范围，受管范围→同步范围更名）；契约面四词归 #119/#123；七域能力分组定稿（关系与端点 7/差异与决议 10/执行与恢复 8/历史与对象库 7/下载与凭据 4/监听 2/诊断 2）；概念地图落本目录 concept-map.md。「开新仓库」倾向已记录，#118 作现状候选。

## 票面快照（2026-09-10 · 票 #115 收口时；#117/#126 由并行会话同日收口）

| 票 | 标题 | 状态 |
|---|---|---|
| #115 | 领域模型与能力域重审 | **已关**（本票） |
| #116 | 进程模型与生命周期 | 边界可取 |
| #117 | [research] Java 生态事实矩阵 | **已关**（并行会话） |
| #118 | Java 技术基线与仓库物理形态 | 边界可取（#117 关闭后解锁） |
| #119 | Protocol 与事件流选型 | 被阻塞（2） |
| #120 | 领域语义重审·能力域组 | 边界可取（#115 关闭后解锁，按七域分组拆票） |
| #121 | 存储与持久化边界 | 边界可取（#117 关闭后解锁） |
| #122 | 任务编排与 watcher 架构 | 被阻塞（3） |
| #123 | 三客户端接入契约 | 被阻塞（1） |
| #124 | 迁移与行为对齐策略 | 被阻塞（1） |
| #125 | Desktop host 选型（低优先） | 被阻塞（1） |
| #126 | [research] Java 系统保险柜生态 | **已关**（并行会话） |
| #127 | 凭据机制重估 | 被阻塞（2） |

## Not yet specified（fog）

- 下载物化与 CF 通道的 Java 形态（免钥匙直链重估；旧 ADR-0005/0008）
- 诊断/日志/脱敏横切（承接已关 #113 的能力面 + ADR-0011 会话日志/别名路径重估）
- 打包分发（CLI 分发方式、jlink/jpackage/native-image 取舍、版本更新通道）
- i18n/本地化归属（Core vs 客户端）
- 性能基线（大包扫描、CAS 冷链路——承接已关 #69 的线索）
- 多客户端并发打开同一工作区
- 测试与 CI 形态（含 TS 客户端链）

## Out of scope

- 生产代码实现（图只出决策；spike 产物为证据）
- VS Code 扩展实现（只定契约，实现等 Core 稳定）
- 数据迁移/双栈兼容（无存量数据；Go 已停更）
- Desktop 前端 UI 迁移本身（照 ui/react 既有节奏走，不属本图）
- 语言重审（Java 为公理）
