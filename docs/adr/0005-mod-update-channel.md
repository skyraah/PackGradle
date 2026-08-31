---
status: accepted
date: 2026-09-01
---

# 0005 · mod 更新通道与自建下载物化

P2 收口后 mod 版本更新存在字节缺口：项目端 metafile 声明新版本后，运行端新 JAR 无处可取（物化模式仅 `["copy"]`，`apply_actions.go:19-24`，内容不可得即 `content_unavailable`）。2026-09-01 选型会话就三个候选（本项目全接管 / 交由 Prism 更新器 / packwiz-installer 启动接管）完成对比，本 ADR 记录结论。

事实基线（决议前提）：

- **Prism 更新器路线不可行**：`mods/.index/*.pw.toml` 是 Prism 自维护状态——外部注入的元数据在 Prism 交互时被整文件重写（找不到对应 jar 判定已删除并清理，初次安装可复现；Prism 自身的下载/更新/刷新也重写这些文件）。不推元数据则 Prism 更新器对 packwiz 管理的 mod 无从得知版本。外部写入者与 Prism 无稳定共存方式。
- **packwiz-installer 调研**（v0.5.14，2026-09-01 一手核对）：上游自 2024-04 起约 2.4 年零提交，46 个 open issue 基本无人回应；CF 模式（`metadata:curseforge`）必须经官方 API（`POST /v1/mods/files` + `X-API-Key`）取 `downloadUrl`，全库无 CDN 直链构造先例；写入非原子（全文件内存缓冲后 `Files.copy` 直接覆盖）、无多文件事务、孤儿删除有误删前科、CLI 一坏全停且退出码全 1；core 与 CLI/GUI 不分离，无稳定库 API。数据面（metafile→URL→hash 校验：CF 解析 + 五格式 hash + 格式模型）仅约 600 行 Kotlin，Go 生态全覆盖其依赖；复杂度大头（side/optional 状态机、manifest 缓存失效）在本项目由既有 baseline/三方差异模型天然替代。
- **无任何启动集成**：全仓无启动实例或调用 Prism CLI 的代码；架构职责边界「Prism 管实际运行实例，PackGradle 管同步」。
- **P2 管线价值**：暂存（`StageContent`）→ 原子写 → digest 复核 → 所有权证明 → journal → 恢复 → 提交全绿，任何走这条管的字节变更白拿崩溃安全与审计历史。

决议如下。

## 1. 更新闭环三层拆解

版本决策（查新版本 → 写项目端 metafile）归 packwiz CLI，发生在 pack 仓库/git 上游；上游变更察觉与物化/反推归 PackGradle 同步本体；运行端手改走复扫反推，双向受管。「mod 更新」在本项目语境＝物化通道问题。术语「上游变更」「快速更新」「授权模式」见 CONTEXT.md。

## 2. 自建下载物化引擎（v1 仅 CF 模式）

Go 自建下载器（否决 fork 与 subprocess 驯化 packwiz-installer、否决 Prism 更新器）：以 installer 数据面为**行为参照**（`CurseForgeSourcer` 的 API 协议与失败分桶、`HashFormat` 的 sha256/sha512/sha1/md5/murmur2 校验集），规范锚点为官方 pack-format 文档；下载字节经既有暂存路径进 Apply 管线。不移植其 UI、bootstrap、manifest 状态机与孤儿删除。

v1 范围＝CF 模式（`metadata:curseforge`）解析；URL 模式（含 Modrinth 的 `download.url` 直链）暂不实现，此类资源维持用户提供补全路径。CF key 策略（自有 key+密文 / CDN 免 key 构造 / 降级用户提供）由 research 票裁决。

## 3. 触发分期

本期＝快速更新手动入口：察觉上游变更后一次授权批量执行全部非冲突操作，冲突操作转为待确认计划。watcher（非冲突自动物化，上游变更自动同步的终态形态）维持 Phase 4 排期；启动前 hook（Prism pre-launch command 调 headless 同步）不做。

## 4. 授权模式

工作区级开关：开启后**一切**非冲突 Apply 免逐次确认（不按入口分叉）；冲突与删除操作永不进入自动面；恢复所需期间自动路径暂停生效（开关保留，恢复收口后自动恢复）。

## 5. `.index` 只读不写

`mods/.index/*.pw.toml` 仅作扫描侧信息源；更新到达运行端＝下载→暂存→Apply 直接写 JAR，不经任何元数据推送；Prism 事后自行重写与我方无关；权威判定只认文件字节与 digest，`.index` 内容不参与（回滚也不回写，Prism 自行重算）。

## 6. 版本物化盲区只提示

`pack.toml` `versions`（MC/加载器）与实例组件（`mmc-pack.json`，在 runtime root 之外的 Prism 自有元数据）不一致时仅 UI 提示，用户在 Prism 手动改；不做结构化物化。

## 7. mod 资源字节一律不进 CAS

下载的 after 字节与删除/覆盖的 before 字节均不留 CAS 副本（不提供 JAR 缓存）；数据库只登记 identity/hash/重取信息。mod 资源的恢复补偿与回滚统一走「远端重查 → 重新物化落盘」，失败降级用户提供/不可恢复。现状 before-preserve 若无条件存字节，须在票内核对并按此对齐。

## 8. 推翻 P3 绘图决议「不实现网络下载」

redownload 通道由「标记 `redownload_required` 后走用户提供补全或 partial」升级为「自建下载器优先、失败降级」。P3 图拓扑更新为：`research（CF key/CDN 构造验证/murmur2 选型/下载韧性）∥ 回滚语义 ADR ∥ CAS GC ADR → 契约 06 → 下载物化 → 验收`；CF 查询定位＝回滚可用性辅助（不做应用内独立更新检查）。

## 后果

- **回滚完备性让渡**：远端失效（下架）的 mod 无法自动回滚或补偿，直接降级用户提供/不可恢复。这是用磁盘负担换的（评估权重：稳定性 > 维护 ≈ 易用 > 周期），架构红线「不承诺保存所有 JAR」由此严格成立。
- fork/驯化 packwiz-installer 的提案六个月内必然复发（它看起来是现成方案）——重申：其终点就是本 ADR 的自建方案，且附带异语言构建链、Java 运行时依赖与上游死亡风险。
- 新错误面进入契约：网络失败的语义（下载失败 ≠ 崩溃恢复面，需可重试）在下载物化票内定义。
- 下载物化票须回答：物化模式表扩展（`copy` 之外加 `download`）、staging 取数接线、hash 校验失败与 `content_unavailable` 的归类、授权模式与快速更新的契约面。
