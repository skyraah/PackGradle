---
status: accepted
date: 2026-09-03
---

# 0012 · 重取语义缺口收口（metafile 字节入 CAS · 存量回滚降级）

决策图 [#70](https://github.com/skyraah/PackGradle/issues/70) 票 [#77](https://github.com/skyraah/PackGradle/issues/77) 的产物，收口 [#68](https://github.com/skyraah/PackGradle/issues/68) 调研留下的九决议点（清单见 `docs/research/p4-redownload-metafile-drift.md`，事实面勿重查；决议点归位：1→§1、2→§2、3→§3、4/5→§5、6→§4、7→§7、8→§6、9→§4）。锚点：ADR-0005 §7（mod 字节不进 CAS——本 ADR 裁边界读法，原文已加注记）、ADR-0006（回滚语义与 §10.1 GC 传递引用保护）、ADR-0007 §7（按体积切保全边界先例）、契约 06 §3.2（`marker_reason` 枚举注记 +1）。术语零新增（「回滚」「重取」「用户对象」现有词条已覆盖）。

## 0. 缺口与根因（沿用研究笔记 §1，摘要）

回滚计划行同时满足「CAS miss 且 CF 重取信息实存（marker=`redownload_required`）」与「metafile 漂移（目标提交的项目侧表示与当前不同）」时，`write_project` 侧无内容源——CAS 因 ADR-0005 §7 从不收、CDN 只有 jar 无 TOML、用户补全通道验收锚在 jar。现状三面堵死：exact 就绪面把 redownload 一律计就绪（放行后确定性整场 failed）、skip 对 redownload 行非法（`err.restore.skip_invalid`）、StageUserObject 拒收（`err.userobject.not_required`）——该行出现在计划中即本次回滚在产品内无任何完成路径。根因再深一层：扫描器 mod 分支不捕获 metafile 自身摘要，baseline 项目侧 mod 表示是纯语义投影；`restoreTargetDigest` 的声明 sha256 兜底取到的是 **jar 摘要（误标）**——sha256 声明子情形存在「降标+补全后 jar 字节整文件写入 metafile 路径、verify 复扫才拦截」的错写路径（字节已落盘）。

## 1. ADR-0005 §7 边界：目的读（metafile 非本条让渡对象）

§7 按**目的读**——让渡对象是「CDN 可取回的二进制」（JAR 缓存）。文本证据三：括注明写「不提供 JAR 缓存」；成本论证全程 JAR 量级；§7 承诺的「远端重查 → 重新物化落盘」对 metafile 事实不可用（CF file-id 直链只解析 jar，CDN 无 TOML）。metafile 是远端（pack git 仓库）未集成的文本清单，落在 §7 的盲缝上——正是缺口成因。先例：ADR-0007 §7「非 mod 单文件 >32 MiB 不保全」——按体积与可重得性切保全边界在本 ADR 体系内有直接先例。ADR-0005 §7 正文不动，已加一行边界注记指向本 ADR。

## 2. 出口①（主决议）：扫描期捕获 metafile 字节，CAS 对象承载

- **捕获时点＝扫描期**——`ScanOptions` 既有 `HashFile` 闭包（含 hash cache）接入扫描器 mod 分支（现仅 managedfiles 消费）；顺带消解 §0 的摘要缺席/误标根因（捕获只认实测，项目侧目标摘要从此有真锚）。
- **载体＝CAS 对象**，baseline 项目侧 mod 表示落 `Content *ContentRef` 指针（字段位已有）——写回分支（`afterFromCAS`）、`restorable_from_cas` 判定、StageContent 复核全部现成，跨提交按内容寻址去重。baseline JSON 内联（进 SQLite、无去重、规范化需处理编码）被否。
- **体积（实测入档，勿重测）**：300-mod 夹具 metafile 层共 88,810 B（均 296 B），加 index.toml 42,023 B 与 pack.toml 187 B ≈ 131 KB，≈ 同规模 jar 层（期望 1.0–1.1 GB）的 0.008%；每提交增量只有变更过的 metafile（典型上游 pull 触碰个位数 mod，KB 级）。
- **生效面**：捕获上线后的新提交。缺口行从此项目侧 CAS 命中＋jar CF 重取，回滚自动完成；四标记矩阵零新输入维度，exact 就绪公式不变（§6）。

## 3. GC 与容量：沿既有语义，零新参数

清单对象经 baseline 的 `Content` 指针被 `sync_commits.result_baseline` 传递引用，自动落 ADR-0006 §10.1 保护根（「GC 不得回收被任何提交快照传递引用的对象」）。ADR-0007 容量口径**零修订**——KB 级增量相对 jar 层 GB 级不构成容量面变化。执行核对项：ADR-0007 §4 引用图断言器是否需把 baseline 新引用形态纳入断言，归执行规格清单（§8.6）。

## 4. 出口③（收编）：存量基线确定性降级

旧基线（无捕获字节）不可追溯填充——重扫只产生新提交，不改写旧快照——其项目侧无源行统一诚实降级：

- **判定（宽判，prepare 期纯静态，零探测成本）**：凡「写回侧含 project ∧ 目标基线该侧表示无 `Content`」的行，**不区分原 marker**——错写修正（§7.2）落地后，redownload 成因与 user_object 成因的漂移行同根同死法（确认后整场 failed），统一语义最少特判。
- **形态（行级降标）**：marker 统一降 `user_object_required` ＋ `marker_reason="no_project_content"`（第 4 值，按**症状**命名——项目侧内容缺失不会变，「存量基线」只是当前唯一成因；契约 06 §3.2 已注记）。四标记矩阵零新维度。
- **通道**：skip 随 user_object 行自然合法（allow_partial 有路）；**补全通道对该值关闭**——补 jar 救不了项目侧，放行即复现「确认后必败」，skip 与手工（项目端改回目标语义后重新 prepare）是仅有的出口。拒绝时的错误码细分归执行规格（§8.3）。
- **代价记录**：行级降标使该行原本可 CF 自动重取的 jar 一并让位——仅存量行、过渡期语义，保守可接受。侧级拆分（jar 保留自动重取、仅项目侧标手工）需给矩阵加「项目侧可取性」常驻新维度，为注定消失的场景引入永久复杂度，不做。

## 5. 出口②：判不可行（留档）

- **工作树直读**：定义性不可行——缺口行前提即「当前 metafile ≠ 目标」，工作树每个 metafile 只有一个当前版本。
- **`.git` 历史读取**：三前置缺口——项目端可能非 git 仓库（导出包/解压目录无法排除）；baseline 无 git commit 映射（不知目标对应哪个 rev）；全仓零 git 集成，需新增 git 依赖面（subprocess 或 go-git）且历史 rewrite/prune 有窗口。
- **packwiz 再生成**：subprocess 先例已有（`internal/packwiz/cli.go`），但字节精确性无保证（生成器版本/格式漂移），按字节 digest 的写回验收走不通；改语义级验收等于改写回契约与 verifyRestore 复验面，不做。
- **结论**：执行期现取整体不立项。将来若项目端 git 集成进图（属目的地重绘），再议。

## 6. exact 就绪面：公式零修订，修输入不修公式

就绪公式（cas ∪ redownload ∪ (user_object ∧ staged)）本身无错，现状谎报源于输入。两处修正落地后公式自然如实：新基线缺口行项目侧 CAS 命中（redownload 计就绪名副其实）；存量行降标且补全关闭（永不就绪，含它的计划 exact 如实判 infeasible，allow_partial＋skip 是唯一路径）。不引入「runtime 可取 ∧ project 侧有源」的公式细化（与降标判定双处维护），不新增就绪态。

**原则条款：就绪面必须如实——resolve 放行的 exact，确认后不得确定性失败。**

## 7. 两项无条件修正（随本 ADR 立项，修法归执行规格）

1. **exact 谎报修正**（§6 原则的落地面）：`restoreExactReady` 对缺口行的就绪输入随 §2 捕获与 §4 降标自然修正；执行规格须验收「含存量无源行的计划 ExactFeasible=false」。
2. **摘要误标错写修正**：项目侧目标摘要**只认实测内容摘要（`Content`），声明哈希一律不作项目侧兜底**（`restoreTargetDigest` 的声明 sha256 兜底对项目侧 mod 表示删除）——错写链（误标 → 补全分支 digest 匹配通过 → jar 字节整文件写入 `.pw.toml` → verify 才拦）从锚上断掉；补全分支的侧别匹配是否加防（纵深），执行规格定。

## 8. 执行规格承接项清单

1. 扫描器 mod 分支接入 `HashFile`——捕获 metafile 字节与 sha256 入表示 `Content`（§2）；
2. prepare 期无源行宽判降标（§4）＋ `no_project_content` 枚举落地（`internal/core/model/restore.go` 枚举、契约 06 §3.2 注记、`internal/transport/dto.go` 投影注释、locale 文案——承认手工/剔除两路）；
3. StageUserObject 对 `no_project_content` 行拒收（错误码细分＋locale，§4 通道）；
4. `restoreTargetDigest` 项目侧声明兜底删除（§7.2）＋错写链回归测试（sha256 声明 × 降标 × 补全 → 断言拒绝而非落盘）；
5. exact 就绪面如实断言（§7.1）＋ 新基线缺口行自动完成链 ＋ 存量降标行 skip 链验收场景（挂 P4 验收规格扩展或执行规格，排期归执行会话）；
6. 引用图断言器对 baseline `Content` 引用形态的核对（§3）。

## 9. 后果

正面：缺口对新提交关闭（回滚自动完成）；存量行从「确认后必败」变「事前说实话」（降标＋skip 有路）；摘要误标根因消除——目标摘要、漂移对照、verify 复核同享实测真锚。
代价：CAS 每提交 KB 级增量；存量降级行的 jar 自动重取让位（手工或 skip）；补全通道对第 4 值行关闭。
边界：本 ADR 纯决策，零产品代码；执行面全数落 §8，随 P4 执行规格承接。
