---
status: accepted
date: 2026-09-04
---

# 0013 · 冲突决议忽略=持久移出受管范围（skip 语义升级 · 提交期单文件规则）

票 [#100](https://github.com/skyraah/PackGradle/issues/100) 的决策产物，收口用户报告的初始化死角。现象与根因（票内全录，摘要）：runtime 独有文件在初始化计划里必然成为 `initialize_choice` 冲突且项目侧证据为空，前端只渲染择侧两项（`frontend/src/views/WorkspacesPlanView.vue:139-151`）且要求全选才可提交（`:522`）——被迫逐文件选「以运行实例初始化」，否则基线建不起来、工作区永卡「需要初始化」。后端 `validChoice` 本就接受 `skip`/`manual`（`internal/core/plan/plan.go:360-376`），架构文档 §6.3 与 UX 原型都设计了四选，locale 自认「当前版本仅支持择侧」（zh-CN.json:496）。盘问 14 问（Q1–Q14）决议全记录见票。

锚点：ADR-0002 决议 2（revision 唯一递增源是 policy 修改——本 ADR 新增提交期写入点，存储层同一实现）、ADR-0003（重绑不触碰策略）、ADR-0009 §（冲突决议口径）、ADR-0010 §3（policy 修改 → 重挂监听）。

## 0. 决议清单

- **Q1-c** 三类冲突（`initialize_choice`/`modify_modify`/`delete_modify`）补齐四选：择侧 ×2 + 忽略 + 手动处理。
- **Q2+Q9-a（用户改答）** 忽略 = 持久、完全移出同步管理，与 `direction=ignore` 同口径；恢复入口唯一 = 受管范围页。
- **Q10-a** 恢复管理只做在受管范围页（规则即清单，零新界面）。
- **Q11-a** 随提交生效：决议只是草稿意图，计划停用/过期即蒸发，不留持久痕迹。
- **Q12-a** `manual` 承接原一次性语义（本次手动处理、基线吸收）；文案「忽略此文件（移出受管范围）」「本次手动处理」。
- **Q13-a/Q14-a** 直接开票 ready-for-agent + 立 ADR（本文）。
- **Q6-a** `redesign:165`（「skip 资源沿用旧 Baseline、保持 dirty/conflicted」）以本决议为准改写。
- **Q8-a** `RuntimeLocalPolicy` 死字段另行决议（另票），本 ADR 不动。

## 1. 语义

**忽略（决议 `ChoiceSkip`，用户面「忽略此文件」）**：该资源自此移出同步管理——差异、计划、快速更新、changes 页不再出现，直至受管范围页显式改回。与映射规则 `direction=ignore` 完全同口径（既有行为：仍进快照、计划面剔除，`plan.go:114-117`）。

**手动处理（决议 `ChoiceManual`）**：本次不生成操作，双端现状随提交吸收进新基线（即现行 skip 的 `buildVerifiedBaseline` 吸收行为，`apply_actions.go:602-654`——「absent tombstone 不入新基线」）；此后任一侧再变更照常回到差异面（单侧变即普通写操作，双侧变即重新冲突）。

两者提交的 `completeness` 均计 partial（验证复扫计 remaining，`apply_actions.go:747-756`）。

## 2. 载体：提交期合成单文件规则（线路一）

- 忽略随 committed 事务生效：在 `apply.go:337-369` 提交事务内经 `repos.Mappings.SavePolicy` 追加一条指向该文件的单文件规则。`SavePolicy` 事务感知，「处于 RunInTx 事务域内时加入外层事务」（`mapping_repo.go:73`、`:81`）。
- 规则形状：两侧前缀 = 资源路径（`WalkDir` 起点为文件时只访问它自身，`managedfiles.go:60-96`，兄弟路径不受影响；备选 include 精确 glob，锚定正则不跨 `/`，`glob.go:87-157`——两种方式都不误伤同前缀其他文件）；direction=ignore；kind 取观察 kind。最长前缀胜出（`compiler.go:252-280`）使其恰好覆盖该文件、与模板规则不并列（不触发 `diag.mapping.collision`）。
- 约束与副作用：
  - 保存前过 `policy.Validate`（`mapping.go:75-79`，失败即 `err.mapping.compile_failed`）——合成规则须满足全部编译约束（唯一 ID、恰好一条 mod 规则、非 `mods/` 前缀等）。
  - `SavePolicy` 存储层硬编码递增 relation revision（`mapping_repo.go:97-104`）——同关系其它 draft/resolved 计划投影 stale，属预期且与 ADR-0002 决议 2 一致；被提交计划本身已过 ConfirmPlan 修订门（`confirm.go:80-81`），事务中段递增不影响本次提交。
  - `kickWatch` 随行（ADR-0010 §3 先例：`mapping.go:96-98` policy 修改 → 重挂监听）。

## 3. 策略盲区修正（忽略语义的成立前提，随本 ADR 立项）

`direction=ignore` 今天只影响计划面，四处差异面策略盲，全部须修正——否则要么 apply 被炸、要么忽略后工作区永远显示差异（均背离 Q9-a）：

1. `verifyRescan` 裸 `diff.ThreeWay` 无 policy：被忽略但与基线分歧的资源触发 `unselected` violation 使整场 apply 失败（`apply_actions.go:722`、`:747-760`）；
2. 工作区 `diff_state`（`workspace.go:143-158`）；
3. changes 页分类（`changes.go:88-115`）；
4. QuickUpdate `no_diff` 判定（`quickupdate.go:209-235`）。

修法（过滤点位置）归执行；验收口径见票。

## 4. 边界

- **mod 资源不提供忽略**：编译器禁文件规则入 `mods/` 前缀（`compiler.go:222-224`），合成规则无法表达；mod 冲突仍提供择侧 + 手动处理。mod 的持久排除另票再议。
- 恢复管理后从既有基线续算（吸收行为不因忽略改变，基线仍记录该资源），无需重初始化。
- 重绑清基线但策略保留（`rebind.go:290`、ADR-0003），忽略跨重绑存活。
- 存量关系无迁移面：忽略是新增决议能力，旧计划里的 skip 决议按既有证据只读保留。

## 5. 执行承接清单（票 #100 范围，修法归执行）

1. 前端四选 + hint + locale（含删 zh-CN.json:496 注记；mod kind 隐藏忽略）；
2. 提交事务内规则合成写入（Validate 前置、kickWatch 随行）；
3. §3 四处策略盲修正；
4. 提交详情页「已忽略」「手动处理」清单分列（现 skipped 清单是物化取数剔除项，`projections.go:182-210`，不含用户决议）；
5. `redesign:165` 改写；CONTEXT.md 词条已随本 ADR 收编（忽略/手动处理）；
6. 测试：真值表补「基线单侧表示」用例（`diff_test.go` 只有 `bothBase`/`absentBase` helper 的缺口）；忽略链端到端（决议 → 提交 → 规则落库 → 四面安静 → 改回 → 差异恢复）；手动处理链（吸收 → 单侧再变回来）；「从不存在的一侧初始化」拒绝不回归（`plan_test.go:748-781`）。

## 6. 后果

正面：初始化不再被迫择侧，「选择性同步」（旧栈 REQ-3.6 口径）在新栈获得对应物 = 决议忽略 + 受管范围规则；忽略/手动处理两出口职责清晰（永久不管 vs 本次自处置）。
代价：含忽略决议的提交使 relation revision +1（同关系其它计划 stale 需重生成）；四处差异面须带策略过滤（一次性成本）；mod 资源暂无忽略；提交期出现 `UpdateMappingPolicy` 之外的第二个策略写入点（受控于提交事务内、Validate 前置）。
边界：本 ADR 纯决策，零产品代码；执行面全数落票 #100。
