---
status: accepted
date: 2026-09-10
---

# 0016 · pg-core 进程模型：无共享按需进程（不设 daemon）

pg-core（Java Core）重建图（wayfinder #114）票 #116 的决策产物（grilling 2026-09-10，一轮四问均按推荐收口）。裁决依据：图定裁决尺（更容易理解 > 更容易调试 > 更容易重构 > 更少运行时魔法 > 更少隐式依赖 > 更少基础设施 > 更少需要记忆的规则）、Demo 锚点「Core 独立进程运行」、CLI 一等公民。

决策时点的 Go 现状参考（事实面，子代理扫描在案）：Core 与 Desktop 同进程（wails 服务注册，`main.go:146-167`），生产代码零 IPC 面；pgheadless 以库方式复用同一 bootstrap 装配、另开进程直连同一 SQLite（`cmd/pgheadless/main.go:170`）——跨进程并存今天已由 WAL + busy_timeout(5000) + 每进程 MaxOpenConns=1 撑住，无文件锁（`internal/store/sqlite/open.go:20-35`）；单实例 = Windows named mutex 全应用范围（`internal/singleinstance`），动机是防双 GUI 各持内存配置互相覆盖（「删除后配置复活」）；任务互斥全在数据库层（active-task 检查，跨进程有效）；中断恢复 = 启动时 RecoverInterruptedTasks（`internal/bootstrap/bootstrap.go:208`，pgrecovery 杀窗口验收过）。

## 0. 决议清单

- **Q1-A 进程形态：无共享、按需进程**。Core 是独立可执行；每个客户端 spawn 自己的 Core 子进程——CLI 每条命令一个、命令完成即退；Desktop / VS Code 打开期间各持一个。无用户级常驻服务、无跨客户端共享进程、无服务发现机制。所有客户端关闭后没有任何后台行为（与 Go 现状一致：GUI 关 = watcher 停）。拉起用的线协议归 #119；各客户端具体带法归 #123。
- **Q2-b 单实例收窄到同类型 GUI 客户端**：Desktop 单实例；VS Code 天然单扩展宿主；CLI 随意多开。跨类型并存（如 Desktop 开着时跑 CLI）允许，靠数据库任务排他兜底。配套硬约束：配置管理改为读时加载 + 保存原子写（临时文件 + rename），禁止依赖进程内长期持有的内存配置副本——Go 当年加 named mutex 的动机「删除后配置复活」正源于双进程各持内存配置。互斥的执行者（host 层做还是 Core 提供原语）归 #123。
- **Q3-a 存储协调沿用 Go，不引入跨进程文件锁**：SQLite WAL + busy_timeout + 每进程单连接；任务互斥 = active-task 数据库检查；CAS 写入 = 临时文件 + fsync + rename 原子替换。**数据库是唯一跨进程协调点**。多客户端并发打开同一工作区的更细行为语义（如扫描快照归属）留在图 Not yet specified，待 #119/#122 毕业。
- **Q4-a 断连即终止，恢复靠下次启动**：客户端退出 → 其 Core 子进程随之退出（断连感知机制归 #119）；在途任务中断，由下次任何进程启动时标记 interrupted 恢复（直接沿用 Go 的 RecoverInterruptedTasks + pgrecovery 验收路径）；不设宽限窗口——崩溃场景本来就需要中断恢复，宽限窗口只覆盖其中一小角。
- **watcher 归属（由 Q1-A 直接推出）**：watcher 是 Core 进程内资源，归持有它的 GUI 客户端；CLI 短进程不启 watcher。两个 GUI 客户端同时打开同一批关联关系目录时双 watcher 并存，靠任务级数据库排他去重（与 Q3-a 同口径）。

## 1. 为什么不是 daemon 或共享进程

- **用户级常驻 daemon 否决**：多一层服务生命周期管理（自启、孤儿进程清理、运行中旧 Core 对新 CLI 的版本错配），违反「更少基础设施 / 更少运行时魔法」；其独有价值「GUI 全关后继续后台同步」未被列为需求。
- **共享按需进程否决**：服务发现点、双客户端同时 spawn 的竞争仲裁、末客户端断开后的宽限期时长——三条额外要记的规则，换来的共享收益有限：状态本来就在数据库里（Go 事件桥既有口径「事件不是事实源，查询 API 恢复状态」，`internal/transport/events.go:22`），进程分开不丢真相。
- **已知代价**：CLI 每条命令付一次 JVM 冷启动。#117 事实矩阵：jpackage + JEP 514/515 AOT cache 后对 CLI 可接受。

## 2. 边界与非目标

- 本 ADR 只锁「谁拥有进程」：线协议（stdio / 本地 socket）、断连感知细节归 #119；三客户端各自带法（含 Desktop 窗口隐藏 / 最小化是否算断连——Core 随客户端**进程**而非窗口存活）归 #123；Core 与 CLI 是同一构建产物两种运行模式还是分开打包，归 #118。
- 「所有客户端关闭后无后台同步」是设计取舍而非缺陷；若未来需要后台同步，重开此 ADR 而非在客户端里夹带常驻进程。
