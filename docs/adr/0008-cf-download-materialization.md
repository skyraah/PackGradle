---
status: accepted
date: 2026-09-01
---

# 0008 · CF 下载物化引擎（免钥匙直链）

ADR-0005 已定自建 Go 下载物化、v1 仅 CF 模式、mod 字节不进 CAS、下载走暂存→Apply 管线；本 ADR 回答其遗留的工程细案：通道（key 策略）、直链构造、hash 校验、韧性参数、失败语义、staging 接线。来源：决策图「PackGradle Phase 3 决策图（回滚·CAS GC·下载物化）」票 #54 十五题烤问（2026-09-01）；喂料：研究笔记 `docs/research/p3-cf-download-channel.md`（分支 `research/p3-cf-download`，票 #50）、ADR-0005、契约 06。评估权重沿用：稳定性 > 维护难度 ≈ 易用性 > 开发周期。

事实基线（决议前提）：

- **CDN 免钥匙直链实测成立**：`mediafilez.forgecdn.net` 免 key（2026-09-01 实测 206/HEAD/Range，UA 无关），`edge.forgecdn.net` 要 key（无 key 401 "A valid api-key is required"，GDLauncher-Carbon 源码注释同证：mediafilez「open, stay keyless」）；生态先例十年（CKAN 元数据 2014 起直存、CurseFetch、packwiz 生态实现）。
- **API 下载链路钥匙死结**：官方 API 需 `x-api-key`（console.curseforge.com 审核制签发；ToS 明文 key 不可转让、不得向第三方分发），返回的 `downloadUrl` 指向 edge 域名**同样要 key**——一次下载＝两次带钥匙请求。钥匙要么编译进分发应用（违约），要么让每个玩家申请开发者钥匙（审核制门槛，不现实）。
- **murmur2 现状**：全代码库无 murmur2 计算实现——扫描器只把声明格式/值当字符串携带（语义对比两侧同声明时可比，`normalize/semantic.go`），校验 digest 域固定 sha256（`apply_actions.go` `declaredSHA256` 只认声明 sha256）。packwiz 现役 CF metafile 均写 sha1，murmur2 是老包格式。
- **契约 06 已定面**（本 ADR 不重议）：`err.download.*` 四错误码、failed 终局（restore 域）、CF 探测内嵌计划行（availability ok|unknown，unavailable＝prepare 时点降标 user_object_required）、`materialization_modes=["copy","download"]`、模式选用后端推导。
- **所有权证明与字节来源无关**：run 级 HMAC（`syncstage.Proof`），fetcher 接线零特殊处理。

决议如下。

## 1. 通道：v1 纯 CDN 免钥匙直链，零 API 调用

下载闭环全程不接触 CF API：无 API client 组件、不消费任何 key、不移植 403 体嗅探（API 面现象）。元数据需求不存在：查新归 packwiz CLI（ADR-0005 §1），newer_available 走本地比对（§8）。未来「用户提供 key」（C 方案）为纯新增模块，落点＝全局 config 现有 `curseforge_api_key` 字段（legacy 遗产，v1 继续闲置）；启用条件与实现另票。

## 2. 直链构造与 fileID 越界

```
https://mediafilez.forgecdn.net/files/{file-id / 1000}/{file-id % 1000}/{filename}
```

- 输入只有 packwiz metafile 的 `filename` 与 `update.curseforge.file-id`（project-id 不需要）；
- **整数除法、不补零**（实测钉死：8778011 → `8778/11` 得 206，`8778/011` 补零得 403）；
- 构造集中**单函数＋单测**（向量：7 位常规例＋余数 <100 例＋小编号例）。生态两派对照：整数除法派（BrassworksLauncher packwiz crate，带单测）与本口径一致；字符串切片派（CurseFetch 的 `[:4]/[4:]`）在余数 <100 时有死链 bug——实测已仲裁，不盲从任何一派；
- **fileID ≥ 10^7（8 位）越界记日志、不换口径**：8 位分段两说分叉（`12345/678` vs `1234/5678`）且当前不存在可实测对象；错 URL 只表现为 404→降级（§5），hash 兜底保证错内容装不进去。届现实测后改单函数即可。

## 3. hash 校验集与两层校验位置

- **v1 校验集＝md5/sha1/sha256/sha512**（stdlib 四格式）。**murmur2 不实现**：metafile 声明 murmur2 或未识别格式 ⇒ 该资源**计划阶段直接标 `user_object_required`**，`marker_reason="hash_format_unsupported"`（新枚举值）；不验不装，不做无校验下载。撞上真实 murmur2 pack 再补 ~40 行实现＋向量单测（口径＝SMHasher 无符号标准语义＋去空白＋seed 1；向量：meza 两例＋高尾字节例＋空输入，研究笔记 §2 已备）。
- **两层各有职责**：① fetcher 落 `.part` 完成即验**声明格式** hash＝「取对了」（来源正确性；本 ADR 新增的唯一校验工作）；② StageContent 后既有 sha256 digest 复核＝「写对了」（落盘正确性；P2 既有机器，零新增代码）。权威只认字节+digest（ADR-0005 §5）不变。

## 4. 韧性与并发参数

| 参数 | 值 | 依据 |
| --- | --- | --- |
| 并发 | **默认 6，用户可配** | Prism/Modrinth 锚点，详见下 |
| 单文件重试 | 4 次，指数退避 1s→30s＋jitter，尊重 Retry-After | go-retryablehttp 口径 |
| 连接超时 | 30s | GDLauncher-Carbon |
| 读停顿 | 120s 无字节推进即断（非绝对读超时） | 同上 |
| 断点续传 | `.part`＋Range（`Accept-Ranges` 实测支持） | 研究笔记 §1.3 |
| UA | `PackGradle/<version>` | 实测 UA 无关，取可识别值 |

- 并发可配置：全局 config.toml `[download] concurrency`，合法 1–16，越界报错（与 `[retention]` 校验同风格）；SettingsService 增读写项（服务面/UI 细节归执行规格）。其余参数编译期常量，不暴露设置。

## 5. 失败分桶与错误码映射

- **重试面＝网络错误/408/429/5xx**；重试耗尽→`err.download.network`（429/503→`err.download.rate_limited`）。
- **403/404 一律不重试→`err.download.unavailable`**：CDN 403＝路径不可达（实测错分段即 403）；API 面的「403＋traffic 体嗅探＝频控」不移植（v1 零 API）。
- **hash 不匹配→清 `.part` 全量重取一次，仍败→`err.download.hash_mismatch`**。
- 下载本身不做 HEAD 预检（HEAD 只用于 §8 探测）。

## 6. staging 接线与 `.part` 生命周期

- fetcher＝物化数据源的一种（与 copy 同位）：产「已过声明 hash 校验的字节」喂 StageContent；原子写/sha256 复核/所有权证明/journal/恢复**全复用，不另起写路径**（ADR-0005 §2 的机制化）。
- download 模式推导＝「资源带 `update.curseforge` 段 ∧ 数据源无目标字节 → download；其余 → copy」（契约 06 §3.7 推导细则兑现）；restore 计划行 marker 已承载等价信息，无推导面。
- `.part` 置运行暂存目录 `downloads/` 子目录；**run 内续传，跨 run 不复用**（failed 重试算新运行，清 `.part` 重下；崩溃随暂存目录按 ADR-0004 恢复矩阵处置）。跨 run 复用的 stale 校验负担不值省下的流量。

## 7. sync 失败语义：跳过＋报告＋重试（本次唯一语义反转）

- **单操作取数失败（copy/download 一条规矩）⇒ 剔出本场**：不暂存、不写入、不进 journal；其余操作照常走完、照常原子提交（单场内部仍全有或全无）。
- run 结果＝「成功 N、跳过 M」＋跳过清单带原因（`err.download.*`）；**全部失败（无可提交）才 failed 终局**。
- 前端报告跳过清单＋**重试按钮**＝重新快速更新：重扫后新计划天然只剩未更新项（契约 06 纯前端编排），不新造「部分重试」机制。
- 已知代价（决议时明示接受）：跳过后实例处于「有新有旧」混合态，可能起不来游戏；重试是出口。
- **restore 不变**：整场退出 failed 终局（契约 06 §3.4 权威）——回滚要求精确还原历史，半截回滚无意义；快速更新是优化流程，能更新多少先更新多少合理。两侧不对称是有意设计。
- 契约 06 修订：sync 域 staging 失败语义新增本节（原 Q8 failed 终局条目明确划归 restore 域）。

## 8. restore 探测机制与 newer_available 本地比对

- **探测**（availability）：对 redownload_required 候选行 HEAD 构造 URL——2xx→`ok`（顺带 Content-Length）；404/403→unavailable→prepare 时点降标 `user_object_required`＋`marker_reason="cf_unavailable"`（契约 06 §5 已定）；网络错误/超时→`unknown` 不阻塞。参数：单请求 5s、行间并发 4、总预算 10s，耗尽剩余行一律 unknown；编译期常量。
- **newer_available＝本地比对，零网络**：同一 mod 在 head metafile 与回滚目标 commit metafile 的 file-id 不同 ⇒ true。语义＝「与当前 pack 版本不同」（pack 被手动降级时亦 true——UI 文案避免绝对化「有新版本」）。packwiz 的查询结果即 metafile 本体（无报告文件），本地 git 比对天然可得。契约 06 修订：来源从「CF 探测所得」改为本地比对；CF 探测只管 availability。

## 9. 不移植清单（重申＋扩充）

installer 的 UI/bootstrap/manifest 状态机/孤儿删除（ADR-0005 已定）；新增：**API client**（`POST /v1/mods/files` 协议面不移植，v1 零 API）、**403 体嗅探**、**murmur2**（§3 条件后门）。

## 契约 06 修订项汇总（执行规格承接）

1. `err.download.rate_limited` 触发＝429/503（去体嗅探）；
2. `newer_available` 来源＝本地比对（head vs 目标 commit 的 file-id）；
3. `marker_reason` 枚举增 `hash_format_unsupported`；
4. sync 域 staging 失败语义＝跳过＋报告＋重试（§7）；
5. SettingsService 增 `[download] concurrency` 读写。

## 后果

- **v1 下载闭环零钥匙零 API**：无凭据保管负担，无 ToS key 条款适用面。代价是押注未文档化通道，缓解三层：hash 校验（错内容装不进去）、404/403 降级用户自备、构造单函数＋越界日志（通道关门时改一个函数接 C 方案）。
- **murmur2 pack 短板**：老包该类 mod 恒走用户自备。发生频率预期低（现役 metafile 均 sha1），实测撞上再补 40 行。
- **sync 部分成功成为一等状态**：实例混合态由用户经重试闭环消化，换来 CF 单点故障不再废掉整场更新。
- **fileID 8 位是定时炸弹但有安全网**：口径赌错只表现为降级，不写坏文件；越界日志保证届时可见。
