# Phase 4 前置调研：TOML/text diff3 合并库与冲突表示

> 状态：research 票 [#31](https://github.com/skyraah/PackGradle/issues/31) 供数，未决议。元数据采集日 2026-08-31（GitHub API + 本仓库 go.mod 1.25.0 实测 + 本机 git 2.49.0.windows.1）。选库决议留待后续 ADR。

## 1. Go 生态 diff3 / 三方合并候选库盘点

### 1.1 总览表

| 候选 | Stars | 最近 push / 最新版本 | 维护状态 | 许可证 | 传递依赖 | Windows/纯 Go | 形态 |
|---|---|---|---|---|---|---|---|
| [epiclabs-io/diff3](https://github.com/epiclabs-io/diff3) | 16 | 2026-05-20；**无 tag/release** | 活跃（2018-12 创建） | MIT | **零依赖**（go.mod 仅 module+go 1.23） | 纯 Go | 纯库，hunk 级结构化冲突 |
| [devsisters/go-diff3](https://github.com/devsisters/go-diff3) | 1 | 2025-08-19 | 2025-03 fork 自 epiclabs，改进已回流上游（epiclabs README 署名致谢） | MIT | 零依赖 | 纯 Go | 纯库 |
| [nasdf/diff3](https://github.com/nasdf/diff3) | 23 | 2024-02-04 | 停滞（3 commits，1 open issue） | MIT | sergi/go-diff | 纯 Go | 纯库，标记式文本输出 |
| [CivNode/diff3-go](https://github.com/CivNode/diff3-go) | 0 | 2026-04-24 | alpha（2026-04-19 创建，自述"may change before v1.0"） | MIT | 零依赖（stdlib only；go.mod 要求 go ≥1.22） | 纯 Go | 纯库，Git 风格标记输出 |
| [sergi/go-diff](https://github.com/sergi/go-diff) | 2086 | 2025-06-05（tag v1.4.0 = 2025-06-05） | 维护中但节奏慢 | MIT | 零依赖 | 纯 Go | 纯库，diff-match-patch 移植，仅 2-way |
| [aymanbagabas/go-udiff](https://github.com/aymanbagabas/go-udiff) (µDiff) | 235 | 2026-07-16（v0.4.1 = 2026-07-16） | 活跃 | BSD-3（Go 作者） | 零依赖 | 纯 Go | 纯库，Myers diff + unified 生成 + **apply**，仅 2-way |
| [hexops/gotextdiff](https://github.com/hexops/gotextdiff) | 5 | 2023-09-23 | 停滞（README 自述只接受 upstream 改动，不再本仓维护） | BSD-3 | 零依赖 | 纯 Go | 纯库，x/tools 旧内部包复制，仅 2-way |
| [sourcegraph/go-diff](https://github.com/sourcegraph/go-diff) | 454 | 2026-07-26（v0.8.0 = 2026-04-24） | 活跃 | MIT（LICENSE 文件核实） | 少量 | 纯 Go | 纯库，unified diff **解析/打印**（不能 apply） |
| [bluekeyes/go-gitdiff](https://github.com/bluekeyes/go-gitdiff) | 155 | 2026-08-08（v0.9.0 = 2026-07-18） | 活跃 | MIT | **零依赖**（go.mod 无 require） | 纯 Go | 纯库，解析并**应用** git 格式 patch |
| [go-git/go-git](https://github.com/go-git/go-git) | 7690 | 2026-08-30（v5.19.2 = 2026-07-29；v6.0.0-alpha.5） | 非常活跃 | Apache-2.0 | 自身依赖树中等 | 纯 Go | 仓库级 git 实现：**无内容级三方合并**（见 1.3） |
| git 二进制外壳（`git merge-file`） | — | 本机 git 2.49.0.windows.1 | 随 Git 发布 | 需外部进程 | — | Windows 需 Git for Windows（自备或随包分发） | 外壳：进程外调用，见 1.4 |
| ~~phillipberndt/Go-diff3~~ | — | — | **不存在**：GitHub API 404，phillipberndt 名下无此仓库；票面猜测名不成立，以 epiclabs-io/diff3 等为等价实现 | — | — | — | — |

排除项：git2go/libgit2 走 CGO，与本项目既有纯 Go 依赖栈（`modernc.org/sqlite`，见 `go.mod`）冲突，不纳入。生态动态：[golang/go#68765](https://github.com/golang/go/issues/68765)（2024-08-07 开，已关闭）提议 x/tools 公开三方合并，最终以 **internal 包**落地（CL 647798），**不可 import**——即官方造了轮子但没发布。

### 1.2 epiclabs-io/diff3（结构化 hunk 冲突的唯一纯 Go 纯库）

血缘：bhousel/node-diff3（JS）→ Synchrotron（Tony Garnock-Jones）→ epiclabs 移植；devsisters 2025 年 fork 加入泛型 API/Myers/并行 diff 后已回流。README 核实的关键 API：

```go
Merge(a, o, b io.Reader, detailed bool, labelA string, labelB string) (*MergeResult, error)
// 泛型 slice API（接受任何 comparable，可对 token/行合并）：
Diff3Merge[T comparable](aLines, oLines, bLines []T, detailed bool) []*Diff3MergeResult[T]
// 每个 result 为 Ok（干净块）或 Conflict{A, O, B, AIndex, OIndex, BIndex}——hunk 级三态证据
// MergeWithOptions 可选：Algorithm: Hunt-McIlroy(默认) | Myers；ExcludeFalseConflicts；LabelA/B
```

要点：冲突以**结构化对象**返回（两侧片段 + base 片段 + 各自起始下标），正是映射 `conflict_kind` 需要的形状；`ExcludeFalseConflicts` 直接处理"双侧相同改动"误报。风险：**无 tag/release，只能 pin commit 伪版本**；社区极小（16 stars），实质是单人库。

### 1.3 go-git plumbing：能提供什么、不能提供什么

克隆 v5.19.2 实测（COMPATIBILITY.md 原文）：

- `plumbing/object/merge_base.go` 的 `Commit.MergeBase(other) ([]*Commit, error)`：仅两 commit 的最佳公共祖先（对应 `merge-base` partial；`--independent/--is-ancestor/--fork-point/--octopus` 均不支持）。
- `merge`：**"⚠️ (partial) Fast-forward only"**；`mergetool`：❌；`pull`：仅 fast-forward。
- 结论：go-git **没有内容级/文本级三方合并**，对 PackGradle 的 pack.toml 合并只能贡献"版本图祖先计算"，而我们的三态（baseline/project/runtime）不需要 commit 图祖先——go-git 在本票用途上没有增益，除非未来要读真实 git 仓库。

### 1.4 git 二进制外壳路线

本机 `git merge-file -h`（git 2.49）核实可用面：`-p`（stdout）、`--diff3`、`--zdiff3`（2.35+）、`--ours/--theirs/--union`、`--diff-algorithm`、`-L`×3 标签、`--object-id`。退出码（git-scm.com/docs/git-merge-file 原文）："negative on error, and the number of conflicts otherwise (truncated to 127 ...); If the merge was clean, the exit value is 0"。

- 优点：xdl_merge 是业界最成熟的 diff3 实现，zdiff3 的"公共前后缀提出冲突块"对 UI 友好。
- 缺点：外部进程 + 解析标记文本（冲突证据要反解析 stdout）；Windows 桌面分发要么要求用户装 Git，要么随包携带 Git for Windows（体积/许可负担）；失败模式（git 不存在/版本差异）比进程内库多一整层。

## 2. TOML 解析库的注释与格式保留回写

| 库 | 版本/日期 | 结构化回写保注释？ | 保格式编辑能力 | 备注 |
|---|---|---|---|---|
| BurntSushi/toml（本项目已在用） | v1.6.0（2025-12-18） | **否** | 无 | 本地 `go doc` 核实：`MetaData` 仅 `IsDefined/Keys/PrimitiveDecode/Type/Undecoded`——**无注释访问、无位置/格式保留**；Encode 按结构体重排，注释全丢 |
| [pelletier/go-toml/v2](https://github.com/pelletier/go-toml) | v2.4.3（2026-07-05），MIT，仓库 1982 stars，push 2026-08-03 | 否 | `unstable` AST 自 v2.4.x 起含 `Comment` 节点；**`unstable/edit`（保留注释/空白/键序的就地编辑）在 main 分支已存在但 v2.4.3 发布物中没有（未发布）** | README 原文："Only the bytes expressing an edit are rewritten, everything else is kept byte-for-byte"；API 明示不稳定，需盯 v2.5.0 |
| [creachadair/tomledit](https://github.com/creachadair/tomledit) | v0.0.29，MIT，10 stars，push 2026-08-25 | —（其定位即保格式编辑） | 有：README 原文 "preserve the complete structure of its input, including declaration order and comments ... without loss" | 自述 "work-in-progress ... not ready for production"；go.mod 要求 **go ≥1.26.0**（本项目 1.25.0，需升 toolchain）；少量依赖（atomicfile/command/go-cmp） |
| [naoina/toml](https://github.com/naoina/toml) | 295 stars，push 2022-08-08，MIT | 否 | AST+位置但年久失修 | 不推荐 |

**packwiz 官方先例（重要）**：packwiz 源码核实（core/pack.go `LoadPack`→`toml.DecodeFile`，写回 `toml.NewEncoder` + `enc.Indent = ""` + `Encode`；core/index.go `Index.Write()` 同法）——**packwiz 自己重写 pack.toml/index.toml 时就是 BurntSushi 结构化重写，注释与键序同样丢失**。即上游生态把这两个文件当"生成物"。

### 两条路线对比

- **路线 A：文本级 patch / diff3 直接合并 TOML 文本**。未冲突区域字节不变 → 注释、键序、空行、缩进天然保真，正面满足"modlist 手工注释不能丢"的硬约束；packwiz 格式行导向、每 mod 一个条目块，diff3 干净合并命中率预期高。代价：TOML 不感知（可能把"改一行值"呈现为行删+行增，两侧同区改动即 hunk 冲突——这本来就是真冲突）；合并结果必须过一遍 TOML 解析校验合法性（复用已有 BurntSushi/toml 解码即可，零新增依赖）。
- **路线 B：结构化重写**（BurntSushi/go-toml v2 Marshal）：值级冲突语义清晰、键级 diff 容易，但**必然丢注释与键序**，违反票面约束；除非引入 tomledit（WIP + go1.26）或等 go-toml v2.5 的 unstable/edit（不稳定 + 未发布）。
- 结论倾向：**A 为主，B 的解析器只做校验/值提取**；将 go-toml v2.5 unstable/edit、tomledit 列为观察项（见决议点 2）。

## 3. 现成 Packwiz 工具生态

- [packwiz/packwiz](https://github.com/packwiz/packwiz)（官方 CLI，980 stars，MIT，push 2026-02-18）：**无任何 merge/conflict 命令**。issue 检索 "merge" 6 条、"conflict" 6 条，无一实现合并语义；最接近的是 [#163 Modpack extension/inheritance](https://github.com/packwiz/packwiz/issues/163)（2022-11-14，open，"生存/创造变体继承基包"），只讨论变体派生，无冲突解决设计。
- 社区工具均不涉及合并：packwiz-installer（Kotlin 安装器，71 stars，push 2024-06-27）、PW-GUI（Java GUI，24 stars，2026-07-21）、PackVulcan（Kotlin 构建器，11 stars，2023-10）、packwiz-gui（Python，12 stars，2022-09）、packwiz2nix（32 stars）。
- 可借鉴的是**格式性质**而非工具：官方自述 "git-friendly TOML format"（仓库描述原文）——即 packwiz 协作场景本身就靠 git 行级合并解决冲突，佐证路线 A；条目粒度天然对齐（pack.toml 顶层键、index.toml `[[files]]` 数组、每 mod 一个 `mods/*.pw.toml` + murmur2 hash，packwiz go.mod 用 aviddiviner/go-murmur），Phase 4 适配器可把 index 条目/单 mod 文件映射为子资源，使**大多数 Packwiz 冲突实际发生在"条目"粒度而非行粒度**。

## 4. 冲突粒度 → conflict_kind 映射

现状（本仓库实读）：

- `internal/core/diff/diff.go`：分类粒度=**资源（文件）级**，取值 `noop/converged/adopt_equal/init_choice/project_to_runtime/runtime_to_project/remove_runtime_candidate/remove_project_candidate/conflict_modify/conflict_delete_modify`；证据是整文件 `SemanticDigest`。
- `internal/core/model/model.go`：`ConflictKind` = `modify_modify/delete_modify/initialize_choice/identity_ambiguous/mapping_collision`；`Conflict` 已有 `Detail string` 字段（JSON 自由证据位）。
- `internal/store/sqlite/schema_v1.go`：`CREATE TABLE conflicts (... PRIMARY KEY(plan_id, resource_id))` —— **每资源每计划只能落一行冲突**。
- `ResolutionChoice` 仅资源级整取：`take_project/take_runtime/skip/manual/...`。

三层粒度与映射：

| 粒度 | 载体 | → conflict_kind / Classification | 映射成本与信息损失 |
|---|---|---|---|
| L1 行级 | diff3 引擎内部（ LCS/Myers 行对齐） | 不落库 | 零成本；仅引擎内部表示 |
| L2 hunk 级 | epiclabs `Conflict{A,O,B,AIndex,OIndex,BIndex}`；或 git merge-file 需反解析标记文本 | 1 个冲突 hunk → 资源级 `conflict_kind=modify_modify`；hunk 内一删一改 → `delete_modify`；证据进 `detail` JSON（如 `[{a:[..],o:[..],b:[..],idx:{a,o,b}}]`） | **零 schema 变更**（Detail 已存在）。损失：hunk 序号/侧别折叠进字符串，UI 需重新解析；无法对同一文件表达"hunk1 取 project、hunk2 取 runtime"（ResolutionChoice 无 hunk 级） |
| L3 值级 | 解析合并结果后按 TOML key/条目定位（如 `version` 双侧不同） | 借 Packwiz 条目子资源化，可把单键冲突升级为独立冲突行（若未来放宽 PK）；否则仍在 `detail` 补 `key_path` | 若要求每 hunk/每键一行 conflicts 记录，**必须改 PK**（如加 `conflict_seq`）+ 扩 ResolutionChoice → 成本高；值级无法从行级结果零损还原（行级冲突可能横跨多键） |

`identity_ambiguous/mapping_collision` 与文本合并正交，仍由 Packwiz 身份层产生。信息损失要点：资源级一行 + detail JSON 是**无损存储、有损查询**；真要 UI 逐 hunk 呈现与逐 hunk 选择，代价落在 schema 与 choice 模型两处。

## 5. 推荐短名单

1. **首选：[epiclabs-io/diff3](https://github.com/epiclabs-io/diff3)（MIT、零依赖、纯 Go、活跃、泛型结构化 hunk 冲突）做文本三方合并引擎**，对 TOML 走文本级合并（注释保真），合并结果用**已有的** BurntSushi/toml 解码做合法性校验（零新增 TOML 依赖）。理由：唯一同时满足"纯 Go + 零依赖 + 结构化冲突对象 + 仍在维护"的候选；hunk 级 `Conflict` 形状与 `conflict_kind` 映射路径最短。
2. **备选 A：git 二进制外壳**（`git merge-file --zdiff3 -p`）：算法最成熟、zdiff3 输出对 UI 好；代价是进程外依赖 + Windows 分发问题 + 证据需反解析。仅在允许运行时要求 git 时可选。
3. **备选 B：nasdf/diff3 / CivNode/diff3-go**（零/低门槛、标记式输出）：无结构化冲突对象 → L2 映射要自己解析标记文本，成本高于首选；CivNode 太新（0 star alpha）。
4. **观察项**：go-toml v2.5 的 `unstable/edit`（发布后重评"保格式结构化编辑"）；tomledit（等成熟 + go1.26 升级窗口）。
5. **配套（按需）**：aymanbagabas/go-udiff（unified 生成+apply，若需"把合并后差异打成 patch 展示/应用"）；sourcegraph/go-diff 或 bluekeyes/go-gitdiff（解析/应用 git patch，仅外壳路线或导出用）。go-git 本票无增益。

## 决议点清单（后续 ADR 需要拍板）

1. **合并引擎**：epiclabs-io/diff3 vs git merge-file 外壳 vs 自研（epiclabs 零依赖可 vendor，但无 release 需 pin commit，接受与否）。
2. **TOML 回写路线**：文本级 diff3 + BurntSushi 校验（推荐）vs 保格式结构化编辑（等 go-toml v2.5/tomledit 成熟）；"注释不丢"的验收口径是"合并输出保留注释"还是"未冲突区域字节级不变"。
3. **冲突行粒度**：每资源一行 + `detail` JSON 承载 hunk 数组（推荐，零 schema 变更）vs 改 conflicts 表 PK 支持每 hunk 一行。
4. **分类扩展**：diff3 干净合并双侧修改的资源，`Classification` 是否新增 `merged_clean`（当前十值表达不了"双侧都改但自动合并成功"）；`conflict_kind` 侧是否保持不动。
5. **ResolutionChoice 粒度**：Phase 4 是否扩展 hunk 级选择（partially take_project/take_runtime），还是维持整文件取侧 + manual。
6. **git 依赖政策**：桌面分发是否允许要求/捆绑系统 git（决定备选 A 的存废；牵动 CGO-free 原则——git2go 已排除）。
7. **Packwiz 条目子资源化**：index.toml `[[files]]`/`mods/*.pw.toml` 是否在 Phase 4 就映射为子 resource_id（决定值级冲突能否独立落行）。
