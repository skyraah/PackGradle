package main

// pgheadless -restore（P3 票 #60；验收规格 §3）：A 口径回滚断言链——离线面
//（零 CDN，回滚行全部 restorable_from_cas / user_object / 删除行），在同一
// 数据目录顺序执行（③ 先于 ②：partial 的 skip 残留按 ADR-0006 §9 保持
// dirty，不干扰后续 exact 场景的就绪面）：
//
//	场景① exact 经 CAS：D1 漂移（改项目侧 pg-a + 删运行端 pg-b）→ c2 →
//	      回滚 c1 → 逐字节复验 + 历史不改写（ListCommits 新增 kind=restore
//	      行且原记录原样）；
//	场景③ 补全就绪面（红线②）：D2 删运行端 pg-c（v0 保全进 CAS）→ c3 →
//	      直删 CAS 对象构造 miss → user_object_required + exact_infeasible
//	      前置拒绝 → 错字节 hash_mismatch → 对字节 staged 翻转 → exact
//	      committed（写回零 CAS 回填）；
//	场景④ 重做语义（ADR-0006 §1）：回滚目标 = head（restore 提交，API 合法）
//	      → 空差异计划合法收口 committed；
//	场景⑤ metafile 捕获回滚（票 #88，ADR-0012 §2 出口①）：D5 项目侧
//	      fixture mod metafile 外部漂移（file-id 换代，jar 载体不变 → 写回侧
//	      仅 project）→ 捕获对象 CAS 命中、漂移态零对象 → write_project 侧
//	      行零网络零用户介入 exact 收口 + baseline_content 引用跨提交去重；
//	场景② partial（红线④）：D3 纯外部漂移（运行端 pg-e 改写 → CAS miss →
//	      user_object_required 行 + 手放 jar 的 deletion_warn 删除行）→ 回滚
//	      c1：skip + 删除行执行 → committed kind=partial + relation 保持
//	      dirty；
//	场景⑥ 存量降级 skip 链（票 #95，ADR-0012 §4 出口③）：造数手术对 c1 基线
//	      baseline_resources 项目侧 mod 表示直接置空 content 指针（模拟捕获
//	      上线前的旧基线）→ metafile 再漂移 → 行降 user_object_required +
//	      no_project_content（纯静态零探测）→ ExactFeasible=false + exact 前置
//	      拒绝 → jar 字节补全被拒（err.userobject.no_project_content）→
//	      allow_partial + skip → committed partial + relation 保持 dirty。
//
// 夹具：pgfixture -plain-mods N 生成「无 CF 声明 mod」变体（user_object 行
// 来源）；受管 config 面用 pg-*.toml 种子文件（config 规则由关系建议携带，
// 须在首次扫描前落位）。全链任一断言不符即返回 error（main 非零退出）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/cdnproc"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/errs"
)

// restore 轮询参数（沿用 -apply 节奏）。
const (
	restorePollInterval = 100 * time.Millisecond
	restorePollTimeout  = 2 * time.Minute
)

// 夹具种子文件内容（pg-*.toml，config 规则受管）。
const (
	rstPgA  = "pg_a = \"v1\"\n"
	rstPgA2 = "pg_a = \"v2\"\n"
	rstPgB  = "pg_b = \"v1\"\n"
	rstPgC  = "pg_c = \"v1\"\n"
	rstPgE  = "pg_e = \"v1\"\n"
	rstPgE2 = "pg_e = \"v2\"\n"
)

// rstSeedFiles 写入受管 config 种子：pg-a 项目侧单侧、pg-b 运行侧单侧、
// pg-c 双侧（initialize 后三者在基线中双侧 present，rec=cas）。
func rstSeedFiles(projectRoot, gameDir string) error {
	for path, content := range map[string]string{
		filepath.Join(projectRoot, "config", "pg-a.toml"): rstPgA,
		filepath.Join(gameDir, "config", "pg-b.toml"):     rstPgB,
		filepath.Join(projectRoot, "config", "pg-c.toml"): rstPgC,
		filepath.Join(gameDir, "config", "pg-c.toml"):     rstPgC,
		filepath.Join(gameDir, "config", "pg-e.toml"):     rstPgE,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runRestoreChain 执行 -restore 断言链。rel 为已登记 Relation（主链路已完成
// 首轮扫描——种子文件须在首次扫描前落位）；stack 供 CAS 实存与 object_refs
// 账目断言（场景⑤，票 #88）；cdn 是假 CDN 进程句柄（场景⑤ redownload 候选
// 行的探测端点，确定性零真网）。
func runRestoreChain(ctx context.Context, stack *bootstrap.Stack, cdn *cdnproc.Serve, app syncapp.Application, rel view.RelationView,
	projectRoot, instanceDir, dataRoot string) error {

	projCfg := filepath.Join(projectRoot, "config")
	gameDir := filepath.Join(instanceDir, "minecraft")
	gameCfg := filepath.Join(gameDir, "config")

	fmt.Println("== -restore == 场景链开始（离线面：cas / user_object / 删除行）")

	// ---- R0：initialize → c1（种子文件入基线，rec=cas）----
	// mod 冲突一律 skip（P2 -apply 划线先例：离线面不物化 mod——skip 只是不
	// 建写操作，基线随复扫收录现实，后续 restore 判定面不受影响）。
	c1, err := rstApplyRound(ctx, app, rel)
	if err != nil {
		return fmt.Errorf("R0 initialize: %w", err)
	}

	// ---- 场景①：exact 经 CAS ----
	// D1：改项目侧 pg-a（sync 传播运行端）+ 删运行端 pg-b（传播删项目侧）。
	if err := os.WriteFile(filepath.Join(projCfg, "pg-a.toml"), []byte(rstPgA2), 0o644); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(gameCfg, "pg-b.toml")); err != nil {
		return err
	}
	c2, err := rstApplyRound(ctx, app, rel)
	if err != nil {
		return fmt.Errorf("D1 apply: %w", err)
	}
	draft1, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景① PrepareRestore: %w", err)
	}
	if err := rstAssertMarker(draft1, "file:config/pg-a.toml", model.MarkerRestorableFromCAS); err != nil {
		return fmt.Errorf("场景①: %w", err)
	}
	if err := rstAssertMarker(draft1, "file:config/pg-b.toml", model.MarkerRestorableFromCAS); err != nil {
		return fmt.Errorf("场景①: %w", err)
	}
	if !draft1.ExactFeasible {
		return fmt.Errorf("场景① 计划应全部就绪（exact feasible）")
	}
	resolved1, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft1.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("场景① resolve exact: %w", err)
	}
	cR1, err := rstConfirmAndRestore(ctx, app, rel, resolved1.PlanID, model.TaskOutcomeExact)
	if err != nil {
		return fmt.Errorf("场景① restore: %w", err)
	}
	// 逐字节复验（验收红线①）：双端写回 c1 状态。
	for path, want := range map[string]string{
		filepath.Join(projCfg, "pg-a.toml"): rstPgA,
		filepath.Join(gameCfg, "pg-a.toml"): rstPgA,
		filepath.Join(projCfg, "pg-b.toml"): rstPgB,
		filepath.Join(gameCfg, "pg-b.toml"): rstPgB,
	} {
		got, rerr := os.ReadFile(path)
		if rerr != nil || string(got) != want {
			return fmt.Errorf("场景① 逐字节复验 %s = %q（err=%v），期望 %q", path, string(got), rerr, want)
		}
	}
	// 历史不改写：新增 kind=restore 行在前，原记录原样。
	if err := rstAssertHistory(ctx, app, rel.RelationID, cR1, c2, c1); err != nil {
		return err
	}
	if err := rstAssertClean(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景①: %w", err)
	}
	fmt.Println("== 场景① exact 经 CAS == 通过（逐字节+digest 复验 + 历史不改写）")

	// ---- 场景③：补全就绪面（红线②）----
	// D2：删运行端 pg-c（v0 保全进 CAS）→ c3 → 回滚 c1。
	if err := os.Remove(filepath.Join(gameCfg, "pg-c.toml")); err != nil {
		return err
	}
	c3, err := rstApplyRound(ctx, app, rel)
	if err != nil {
		return fmt.Errorf("D2 apply: %w", err)
	}
	_ = c3 // 提交事实由历史断言覆盖；票 #93 起引用计数断言以 c1 为基准
	// prepare#1：CAS 实存 → restorable_from_cas。
	draft3a, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景③ PrepareRestore: %w", err)
	}
	pgC := rstFindItem(draft3a, "file:config/pg-c.toml")
	if pgC == nil || pgC.Marker != model.MarkerRestorableFromCAS {
		return fmt.Errorf("场景③ pg-c 行应 cas: %+v", pgC)
	}
	// 直删 CAS 对象（验收规格 §3「CAS miss 构造」路径）→ prepare#2 降标
	// user_object_required + no_redownload_info，exact_infeasible + blocked_by。
	digest := sha256Hex([]byte(rstPgC))
	objectPath := filepath.Join(dataRoot, "objects", "sha256", digest[:2], digest)
	if err := os.Remove(objectPath); err != nil {
		return fmt.Errorf("场景③ 直删 CAS 对象: %w", err)
	}
	draft3, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景③ PrepareRestore(miss): %w", err)
	}
	pgC = rstFindItem(draft3, "file:config/pg-c.toml")
	if pgC == nil || pgC.Marker != model.MarkerUserObjectRequired || pgC.MarkerReason != model.MarkerReasonNoRedownloadInfo {
		return fmt.Errorf("场景③ CAS miss 应降标 user_object_required: %+v", pgC)
	}
	if draft3.ExactFeasible || len(draft3.BlockedBy) == 0 {
		return fmt.Errorf("场景③ 应 exact_infeasible + blocked_by 非空")
	}
	// exact 决议遇未就绪面前置拒绝（ADR-0006 §4）。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft3.PlanID, RequestedExactness: "exact"}); err == nil ||
		errs.CodeOf(err) != "err.restore.exact_infeasible" {
		return fmt.Errorf("场景③ exact 应前置拒绝: %v", err)
	}
	// 错字节 → hash_mismatch；对字节 → staged + ExactFeasible 翻转。
	wrong := filepath.Join(dataRoot, "rst-wrong.bin")
	right := filepath.Join(dataRoot, "rst-right.bin")
	if err := os.WriteFile(wrong, []byte("corrupted bytes"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(right, []byte(rstPgC), 0o644); err != nil {
		return err
	}
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft3.PlanID, ResourceID: string(pgC.ResourceID), SourcePath: wrong,
	}); err == nil || errs.CodeOf(err) != "err.userobject.hash_mismatch" {
		return fmt.Errorf("场景③ 错字节应 hash_mismatch: %v", err)
	}
	staged, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft3.PlanID, ResourceID: string(pgC.ResourceID), SourcePath: right,
	})
	if err != nil {
		return fmt.Errorf("场景③ 对字节补全: %w", err)
	}
	if item := rstFindItem(staged, "file:config/pg-c.toml"); item == nil || !item.Staged || !staged.ExactFeasible {
		return fmt.Errorf("场景③ staged/ExactFeasible 应翻转")
	}
	// staged 字节零 CAS 污染（对象文件保持缺失）。
	if _, err := os.Stat(objectPath); !os.IsNotExist(err) {
		return fmt.Errorf("场景③ 补全字节不得进 CAS（err=%v）", err)
	}
	resolved3, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft3.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("场景③ resolve exact: %w", err)
	}
	cR2, err := rstConfirmAndRestore(ctx, app, rel, resolved3.PlanID, model.TaskOutcomeExact)
	if err != nil {
		return fmt.Errorf("场景③ restore: %w", err)
	}
	if got, rerr := os.ReadFile(filepath.Join(gameCfg, "pg-c.toml")); rerr != nil || string(got) != rstPgC {
		return fmt.Errorf("场景③ pg-c 复验 = %q（err=%v）", string(got), rerr)
	}
	// staging 补全面零 CAS 污染（ADR-0005 §7）；写回后的提交收口期基线内容
	// 摄取（票 #93 泛化：项目侧全部表示统一入 CAS）使对象按 pg-c 内容重建——
	// 后续回滚到本提交时该行自动 restorable_from_cas，不再依赖补全。
	if _, err := os.Stat(objectPath); err != nil {
		return fmt.Errorf("场景③ 写回后 CAS 对象应由基线内容摄取重建（err=%v）", err)
	}
	fmt.Println("== 场景③ 补全就绪面 == 通过（CAS miss → hash_mismatch → staged 翻转 → exact committed）")

	// ---- 场景④：重做语义（ADR-0006 §1；先于场景②执行——partial 的 skip 残留按
	// ADR-0006 §9 保持 dirty，head 为目标的空差异断言要求基线与现实一致）----
	// 回滚目标 = head（cR2，restore 提交，API 合法）→ 空差异计划合法收口。
	draft4, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: cR2})
	if err != nil {
		return fmt.Errorf("场景④ PrepareRestore: %w", err)
	}
	if len(draft4.Items) != 0 {
		return fmt.Errorf("场景④ head 为目标应为空差异计划: %d 行", len(draft4.Items))
	}
	resolved4, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft4.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("场景④ resolve: %w", err)
	}
	cR3, err := rstConfirmAndRestore(ctx, app, rel, resolved4.PlanID, model.TaskOutcomeExact)
	if err != nil {
		return fmt.Errorf("场景④ restore: %w", err)
	}
	// 历史追加不改写：cR2 → cR3 相邻且 kind=restore。
	page, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 10})
	if err != nil {
		return err
	}
	if len(page.Items) < 2 || page.Items[0].CommitID != cR3 || page.Items[1].CommitID != cR2 ||
		page.Items[0].Kind != string(model.PlanRestore) {
		return fmt.Errorf("场景④ 历史头两行应 cR3/cR2（kind=restore）")
	}
	if err := rstAssertClean(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景④: %w", err)
	}
	fmt.Printf("== 场景④ 重做语义 == 通过（head 为目标空差异合法收口 cR3=%s）\n", cR3)

	// ---- 场景⑤：metafile 捕获回滚（票 #88，ADR-0012 §2 出口①）----
	// D5：项目侧 fixture-mod-0001.pw.toml（CF 身份 mod:curseforge:900001）
	// 外部漂移——packwiz 版本决策的真实形状（file-id 换代，纯外部写者，不经
	// sync；jar 载体字节不变 → 写回侧仅 project）。捕获链：c1 收口时 8 个
	// metafile 实测摘要已入 CAS（baseline_content 引用）；回滚 c1：漂移行四
	// 标记沿未改判定矩阵判 redownload_required（jar 载体重取语义不变，ADR-0012
	// §2「矩阵零新输入维度」），write_project 侧行从「无内容源」变 CAS 命中，
	// 零网络零用户介入 exact 收口。
	metaPath := filepath.Join(projectRoot, "mods", "fixture-mod-0001.pw.toml")
	metaV1, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("场景⑤ 读 metafile v0: %w", err)
	}
	const fileIDV1, fileIDV2 = "file-id = 1000001", "file-id = 1000002"
	if !strings.Contains(string(metaV1), fileIDV1) {
		return fmt.Errorf("场景⑤ fixture metafile 形状不符（缺 %s）", fileIDV1)
	}
	metaV2 := strings.Replace(string(metaV1), fileIDV1, fileIDV2, 1)
	if err := os.WriteFile(metaPath, []byte(metaV2), 0o644); err != nil {
		return err
	}
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return err
	}
	d1, d2 := sha256Hex(metaV1), sha256Hex([]byte(metaV2))
	// 探测端点确定性（零真网）：把 jar 载体字节登记进假 CDN 的直链路径，
	// PrepareRestore 对 redownload 候选行的 HEAD 探测得 2xx → availability=ok，
	// 行保持 redownload_required（矩阵乐观标记），可用性面不依赖真网形状。
	if cdn == nil {
		return fmt.Errorf("场景⑤ 需要假 CDN 进程（-cdn 或自动拉起）作探测端点")
	}
	const modFileID = int64(1000001) // pgfixture writeProjectMod i=1 的 file-id
	const jarName = "fixture-mod-0001-1.2.2.jar"
	jarBytes, err := os.ReadFile(filepath.Join(gameDir, "mods", jarName))
	if err != nil {
		return fmt.Errorf("场景⑤ 读 jar 载体: %w", err)
	}
	if err := cdn.SetFile(cdnproc.FilePath(modFileID, jarName), jarBytes); err != nil {
		return fmt.Errorf("场景⑤ 登记探测端点: %w", err)
	}
	// 捕获面断言：v0 摘要已在 CAS（c1 收口摄取）；v2 漂移态零对象（扫描期/
	// 未进提交的观测不落 CAS，ADR-0012 §2「扫描期不落对象」）。
	for digest, want := range map[string]bool{d1: true, d2: false} {
		ok, herr := stack.CAS.Has(ctx, digest)
		if herr != nil {
			return herr
		}
		if ok != want {
			return fmt.Errorf("场景⑤ CAS 实存 %s = %v，期望 %v", digest[:12], ok, want)
		}
	}
	draft5, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景⑤ PrepareRestore: %w", err)
	}
	metaRow := rstFindItem(draft5, "mod:curseforge:900001")
	if metaRow == nil || metaRow.ChangeKind != "modify" {
		return fmt.Errorf("场景⑤ 缺 metafile 漂移行: %+v", draft5.Items)
	}
	if metaRow.Marker != model.MarkerRedownloadRequired {
		return fmt.Errorf("场景⑤ metafile 行标记 = (%s, %q)（判定矩阵零修订，jar 载体重取语义）: %+v",
			metaRow.Marker, metaRow.MarkerReason, metaRow)
	}
	if metaRow.Availability != model.RestoreAvailabilityOK || metaRow.Redownload == nil {
		return fmt.Errorf("场景⑤ 探测面: availability=%q redownload=%+v，期望 ok+重取信息",
			metaRow.Availability, metaRow.Redownload)
	}
	if !metaRow.NewerAvailable {
		return fmt.Errorf("场景⑤ newer_available 应为 true（本地 file-id %s 比目标新）", fileIDV2)
	}
	if !draft5.ExactFeasible {
		return fmt.Errorf("场景⑤ 项目侧 CAS 命中后 exact 应可行")
	}
	// CAS 增量断言基准：票 #93 摄取面泛化（项目侧全部带内容指纹的表示：
	// metafile + 非模文本）后，各提交引用数随内容集演进而变（D1 删 pg-b 等），
	// 固定计数不可用——以 c1 收口的引用数为基准：场景⑤ restore 回到 c1 状态，
	// 结果基线内容集与 c1 一致 → cR4 引用数必须相同（跨提交去重不虚增）。
	baseRefs, err := rstBaselineContentRefs(stack.DB, c1)
	if err != nil {
		return fmt.Errorf("场景⑤ baseline_content 引用: %w", err)
	}
	if baseRefs <= 0 {
		return fmt.Errorf("场景⑤ c1 baseline_content 引用 = %d，应为正", baseRefs)
	}
	resolved5, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft5.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("场景⑤ resolve exact: %w", err)
	}
	// restore 摄取零新对象的基准：收口前全链去重 digest 数（票 #93 泛化面上
	// 写回内容 = c1 既有对象，收口后集合不变）。
	before, err := rstDistinctBaselineContentDigests(stack.DB, rel.RelationID)
	if err != nil {
		return err
	}
	if before <= 0 {
		return fmt.Errorf("场景⑤ 收口前去重 digest = %d，应为正", before)
	}
	cR4, err := rstConfirmAndRestore(ctx, app, rel, resolved5.PlanID, model.TaskOutcomeExact)
	if err != nil {
		return fmt.Errorf("场景⑤ restore: %w", err)
	}
	// 收口后逐字节复验 + 引用账目：v0 引用零新对象（内容寻址去重）。
	if got, rerr := os.ReadFile(metaPath); rerr != nil || string(got) != string(metaV1) {
		return fmt.Errorf("场景⑤ metafile 复验 = %q（err=%v），期望 v0 原字节", string(got), rerr)
	}
	if total, err := rstDistinctBaselineContentDigests(stack.DB, rel.RelationID); err != nil {
		return err
	} else if total != before {
		return fmt.Errorf("场景⑤ 全链 baseline_content 去重 digest = %d，期望 %d（restore 摄取零新对象）", total, before)
	}
	if n, err := rstBaselineContentRefs(stack.DB, cR4); err != nil {
		return err
	} else if n != baseRefs {
		return fmt.Errorf("场景⑤ restore 提交 baseline_content 引用 = %d，期望 %d（同一内容集）", n, baseRefs)
	}
	if err := rstAssertClean(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景⑤: %w", err)
	}
	fmt.Printf("== 场景⑤ metafile 捕获回滚 == 通过（CAS 命中写回 + 跨提交去重 %d digest + 零网络零介入 committed cR4=%s）\n", before, cR4)

	// ---- 场景②：partial（红线④）----
	// D3（纯外部漂移，不经 sync——restore 判定面直接观察）：运行端 pg-e 改写
	// + 手放 jar（deletion_warn 删除行来源；无 metafile 无 .index 的裸 jar 不进
	// sync 计划面）。手放 jar 与 pg-e 漂移同批进入 restore 输入快照。CAS miss
	// 由目标对象直删手术构造（票 #93 摄取面泛化后 v0 随收口入 CAS，见下）。
	if err := os.WriteFile(filepath.Join(gameCfg, "pg-e.toml"), []byte(rstPgE2), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gameDir, "mods", "hand-placed-1.0.jar"), []byte("hand placed jar"), 0o644); err != nil {
		return err
	}
	// CAS miss 构造（验收规格 §3 同款手术，场景③ 先例）：票 #93 摄取面泛化后，
	// 项目侧内容的 v0 已随 c1 收口入 CAS（「运行端漂移天然 miss」的前提不再
	// 成立——这本身是回滚承诺增强的体现）；user_object_required 行的构造路径
	// = 直删目标摘要对象，语义与「从未保全」一致（判定面只认 CAS 实存）。
	pgEDigest := sha256Hex([]byte(rstPgE))
	pgEObject := filepath.Join(dataRoot, "objects", "sha256", pgEDigest[:2], pgEDigest)
	if err := os.Remove(pgEObject); err != nil {
		return fmt.Errorf("场景② 直删 pg-e CAS 对象: %w", err)
	}
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return err
	}
	// 回滚 c1：pg-e 行 modify（运行侧漂移、目标对象经手术直删 → CAS miss →
	// user_object_required，未补全 → 合法 skip）；手放 jar 为删除行（目标
	// absent 当前 present、无重取信息 → deletion_warn，不可 skip，照删）。
	draft2, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景② PrepareRestore: %w", err)
	}
	var pgERow, handRow *view.RestorePlanItemView
	for i := range draft2.Items {
		it := &draft2.Items[i]
		switch {
		case it.ResourceID == "file:config/pg-e.toml":
			pgERow = it
		case it.ChangeKind == "delete" && it.DeletionWarn:
			handRow = it // 手放 jar：运行端-only、无重取信息 → 不可重取警示
		}
	}
	if pgERow == nil || pgERow.Marker != model.MarkerUserObjectRequired {
		return fmt.Errorf("场景② pg-e 行应 user_object_required: %+v", pgERow)
	}
	if pgERow.ExpectedDigest == "" {
		return fmt.Errorf("场景② 补全行应透出验收摘要")
	}
	if handRow == nil {
		return fmt.Errorf("场景② 计划缺手放 jar 的 deletion_warn 删除行: %+v", draft2.Items)
	}
	resolved2, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft2.PlanID, RequestedExactness: "allow_partial",
		SkipResourceIDs: []string{string(pgERow.ResourceID)},
	})
	if err != nil {
		return fmt.Errorf("场景② resolve allow_partial: %w", err)
	}
	cR5, err := rstConfirmAndRestore(ctx, app, rel, resolved2.PlanID, model.TaskOutcomePartial)
	if err != nil {
		return fmt.Errorf("场景② restore: %w", err)
	}
	// 红线④：partial 后 relation 保持 dirty（skip 行单侧差异对基线保持可见，
	// 不被复扫快照「收编」为 converged clean）。
	if err := rstAssertDirty(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景②: %w", err)
	}
	// skip 行保持漂移现状（v2）；手放 jar 照删。
	if got, rerr := os.ReadFile(filepath.Join(gameCfg, "pg-e.toml")); rerr != nil || string(got) != rstPgE2 {
		return fmt.Errorf("场景② skip 行应保持 v2 漂移现状: %q（err=%v）", string(got), rerr)
	}
	if _, err := os.Stat(filepath.Join(gameDir, "mods", "hand-placed-1.0.jar")); !os.IsNotExist(err) {
		return fmt.Errorf("场景② 手放 jar 应照删（err=%v）", err)
	}
	fmt.Printf("== 场景② partial + dirty == 通过（kind=partial + relation dirty + deletion_warn 删除行执行，cR5=%s）\n", cR5)

	// ---- 场景⑥：存量降级 skip 链（票 #95，ADR-0012 §4 出口③）----
	// 造数手术：c1 结果基线的项目侧 mod 表示直接置空 content 指针（模拟捕获
	// 上线前的旧基线，不做捕获开关）→ metafile 再漂移（写回侧仅 project）→
	// 行降 user_object_required + no_project_content（四标记判定后置覆写，
	// 纯静态零探测——验收摘要/重取信息/可用性全清）→ ExactFeasible=false +
	// exact 前置拒绝（ADR-0012 §6 就绪面如实）→ jar 字节补全被拒（声明
	// sha256 所指对象就是 jar，旧链此处收下字节并错写进 metafile 路径）→
	// allow_partial + skip → committed partial + relation 保持 dirty。
	var baseline6 string
	if err := stack.DB.QueryRow(`SELECT result_baseline_id FROM sync_commits WHERE id=?`, c1).Scan(&baseline6); err != nil {
		return fmt.Errorf("场景⑥ 读 c1 结果基线: %w", err)
	}
	stripped, err := rstStripBaselineProjectContent(stack.DB, baseline6)
	if err != nil {
		return fmt.Errorf("场景⑥ 造数手术: %w", err)
	}
	// 手术按构造剥离基线内全部带 content 的 mod 表示；stripped=0 即捕获未在
	// 该基线上线（计数走动态口径，#93 摄取泛化后无固定 8 的前提）。
	if stripped == 0 {
		return fmt.Errorf("场景⑥ 造数手术应置空项目侧 mod content 指针，实际 0（捕获未上线？）")
	}
	metaV3 := strings.Replace(string(metaV1), fileIDV1, "file-id = 1000003", 1)
	if err := os.WriteFile(metaPath, []byte(metaV3), 0o644); err != nil {
		return err
	}
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return err
	}
	draft6, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		return fmt.Errorf("场景⑥ PrepareRestore: %w", err)
	}
	metaRow6 := rstFindItem(draft6, "mod:curseforge:900001")
	if metaRow6 == nil || metaRow6.ChangeKind != "modify" {
		return fmt.Errorf("场景⑥ 缺 metafile 漂移行: %+v", draft6.Items)
	}
	if metaRow6.Marker != model.MarkerUserObjectRequired || metaRow6.MarkerReason != model.MarkerReasonNoProjectContent {
		return fmt.Errorf("场景⑥ 应降标 no_project_content: (%s, %q)", metaRow6.Marker, metaRow6.MarkerReason)
	}
	if metaRow6.ExpectedDigest != "" || metaRow6.Redownload != nil || metaRow6.Availability != "" {
		return fmt.Errorf("场景⑥ 降标行应清空验收摘要/重取信息/可用性: %+v", metaRow6)
	}
	blocked6 := false
	for _, b := range draft6.BlockedBy {
		if b.ResourceID == string(metaRow6.ResourceID) {
			blocked6 = true
		}
	}
	if draft6.ExactFeasible || !blocked6 {
		return fmt.Errorf("场景⑥ 就绪面应如实: feasible=%v 降标行在 blocked_by=%v", draft6.ExactFeasible, blocked6)
	}
	// exact 如实（ADR-0012 §6）：含存量无源行的计划 exact 前置拒绝。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft6.PlanID, RequestedExactness: "exact"}); err == nil ||
		errs.CodeOf(err) != "err.restore.exact_infeasible" {
		return fmt.Errorf("场景⑥ exact 应前置拒绝: %v", err)
	}
	// 补全通道关闭（ADR-0012 §4）：jar 字节=目标声明 sha256 所指对象，旧链在此
	// 收下 jar 字节并经补全分支错写进 metafile 路径；新码拒绝而非落盘。
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft6.PlanID, ResourceID: string(metaRow6.ResourceID), SourcePath: filepath.Join(gameDir, "mods", jarName),
	}); err == nil || errs.CodeOf(err) != "err.userobject.no_project_content" {
		return fmt.Errorf("场景⑥ 降标行补全应 no_project_content 拒收: %v", err)
	}
	// skip 链（出口通路）：allow_partial + 剔除全部降标行（无声明 hash 的
	// plain mod 行语义哈希随 Content 参与摘要，手术置空后与实测面如实显差，
	// 同落降标）→ committed partial 不谎报。#93 摄取泛化后文本行（pg-e 等）
	// 基线字节已入 CAS、随 exact 完成回写，不再进剔除面。
	var skip6 []string
	for i := range draft6.Items {
		if draft6.Items[i].MarkerReason == model.MarkerReasonNoProjectContent {
			skip6 = append(skip6, string(draft6.Items[i].ResourceID))
		}
	}
	if len(skip6) == 0 {
		return fmt.Errorf("场景⑥ 草稿应存在降标行，实际无")
	}
	resolved6, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID:             draft6.PlanID,
		RequestedExactness: "allow_partial",
		SkipResourceIDs:    skip6,
	})
	if err != nil {
		return fmt.Errorf("场景⑥ resolve allow_partial: %w", err)
	}
	cR6, err := rstConfirmAndRestore(ctx, app, rel, resolved6.PlanID, model.TaskOutcomePartial)
	if err != nil {
		return fmt.Errorf("场景⑥ restore: %w", err)
	}
	head6, err := app.GetCommit(ctx, rel.RelationID, cR6)
	if err != nil {
		return err
	}
	if head6.Summary.RemainingChangeCnt != len(skip6) {
		return fmt.Errorf("场景⑥ remaining = %d，期望 %d（partial 不谎报）", head6.Summary.RemainingChangeCnt, len(skip6))
	}
	if err := rstAssertDirty(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("场景⑥: %w", err)
	}
	if got, rerr := os.ReadFile(metaPath); rerr != nil || string(got) != metaV3 {
		return fmt.Errorf("场景⑥ skip 行应保持 v3 漂移现状（jar 字节绝不落 metafile）: %q（err=%v）", string(got), rerr)
	}
	fmt.Printf("== 场景⑥ 存量降级 skip 链 == 通过（降标 no_project_content + exact 如实拒绝 + 补全拒收 + 剔除 %d 行 partial dirty，cR6=%s）\n", len(skip6), cR6)
	fmt.Println("== -restore == 六场景全部通过（链末保持 dirty：存量降级 skip 残留的诚实投影）")
	return nil
}

// rstStripBaselineProjectContent 存量基线造数手术（票 #95，ADR-0012 §4）：对
// baseline_resources 的项目侧 mod 表示 JSON 直接置空 content 指针，模拟捕获
// 上线前的旧基线（不做捕获开关）。文件/文本资源 Content v1 已在不缺，mod
// metafile 捕获是 ADR-0012 §2 新增——手术只落 mod 行。返回置空的表示数。
func rstStripBaselineProjectContent(db *sql.DB, baselineID string) (int, error) {
	rows, err := db.Query(`
SELECT resource_id, project_representation_json FROM baseline_resources
WHERE baseline_id=? AND project_representation_json IS NOT NULL`, baselineID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type repUpdate struct{ id, raw string }
	var updates []repUpdate
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return 0, err
		}
		if !strings.HasPrefix(id, "mod:") {
			continue
		}
		var rep map[string]any
		if err := json.Unmarshal([]byte(raw), &rep); err != nil {
			return 0, err
		}
		if _, ok := rep["content"]; !ok {
			continue
		}
		delete(rep, "content")
		b, err := json.Marshal(rep)
		if err != nil {
			return 0, err
		}
		updates = append(updates, repUpdate{id, string(b)})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, u := range updates {
		if _, err := db.Exec(`
UPDATE baseline_resources SET project_representation_json=?
WHERE baseline_id=? AND resource_id=?`, u.raw, baselineID, u.id); err != nil {
			return 0, err
		}
	}
	return len(updates), nil
}

// ---- 链路辅助 ----

// rstApplyRound 执行一轮同步（扫描 → 计划 → 决议 → 确认 → Apply → committed），
// 返回新提交 id。initialize 轮按「mod skip、其余 project 优先/运行时兜底」决议
// （P2 -apply 划线先例：离线面不物化 mod）；sync 轮无冲突直接决议。
func rstApplyRound(ctx context.Context, app syncapp.Application, rel view.RelationView) (string, error) {
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return "", err
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return "", err
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		return "", fmt.Errorf("PrepareSync: %w", err)
	}
	resolutions := make([]model.Resolution, 0, len(draft.Conflicts))
	for _, c := range draft.Conflicts {
		choice := model.ChoiceInitializeFromRuntime
		if c.Project != nil {
			if normalize.KindOfResourceID(c.ResourceID) == model.ResourceMod {
				choice = model.ChoiceSkip // P2 划线：离线面不物化 mod
			} else {
				choice = model.ChoiceInitializeFromProject
			}
		}
		resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: choice})
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		return "", fmt.Errorf("ResolvePlan: %w", err)
	}
	for _, op := range resolved.Operations {
		fmt.Println("[DBG-RST]", op.ID, op.Kind, op.Materialization, op.ResourceID, op.Preconditions)
	}
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		return "", fmt.Errorf("ConfirmPlan: %w", err)
	}
	final, err := rstWaitTask(ctx, app, tv.TaskID)
	if err != nil {
		return "", err
	}
	if final.Status != model.TaskStatusSucceeded {
		return "", fmt.Errorf("apply 任务终态 %s（problem=%+v）", final.Status, final.Problem)
	}
	head, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 1})
	if err != nil || len(head.Items) == 0 {
		return "", fmt.Errorf("ListCommits: %w", err)
	}
	return head.Items[0].CommitID, nil
}

// rstConfirmAndRestore 确认回滚计划并轮询至终态，断言任务 kind 与提交头账目
// （kind=restore、完整性），返回新提交 id。
func rstConfirmAndRestore(ctx context.Context, app syncapp.Application, rel view.RelationView,
	planID string, wantOutcome string) (string, error) {

	tv, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: planID})
	if err != nil {
		return "", fmt.Errorf("ConfirmRestorePlan: %w", err)
	}
	if tv.Kind != model.TaskKindRestore {
		return "", fmt.Errorf("任务 kind = %s，期望 restore", tv.Kind)
	}
	final, err := rstWaitTask(ctx, app, tv.TaskID)
	if err != nil {
		return "", err
	}
	if final.Status != model.TaskStatusSucceeded || final.Outcome != wantOutcome {
		return "", fmt.Errorf("restore 任务终态 %s/%s（problem=%+v），期望 succeeded/%s",
			final.Status, final.Outcome, final.Problem, wantOutcome)
	}
	head, err := app.GetCommit(ctx, rel.RelationID, final.CommitID)
	if err != nil {
		return "", err
	}
	if head.Summary.Kind != string(model.PlanRestore) || head.Summary.Completeness != wantOutcome {
		return "", fmt.Errorf("提交头 kind=%s completeness=%s", head.Summary.Kind, head.Summary.Completeness)
	}
	return final.CommitID, nil
}

// rstAssertHistory 断言历史追加不改写：新增 kind=restore 行在前，原两行原样。
func rstAssertHistory(ctx context.Context, app syncapp.Application, relationID, newID, first, second string) error {
	page, err := app.ListCommits(ctx, relationID, ports.PageRequest{Limit: 10})
	if err != nil {
		return err
	}
	if len(page.Items) < 3 {
		return fmt.Errorf("历史应 ≥3 行，got %d", len(page.Items))
	}
	if page.Items[0].CommitID != newID || page.Items[0].Kind != string(model.PlanRestore) {
		return fmt.Errorf("新头行应 kind=restore 的 %s: %+v", newID, page.Items[0])
	}
	if page.Items[1].CommitID != first || page.Items[2].CommitID != second {
		return fmt.Errorf("原历史被改写: %+v", page.Items[1:3])
	}
	return nil
}

// rstAssertClean / rstAssertDirty 断言关系 diff 面（exact 收口归零 / partial 保持 dirty）。
func rstAssertClean(ctx context.Context, app syncapp.Application, relationID string) error {
	ws, err := app.GetWorkspace(ctx, relationID)
	if err != nil {
		return err
	}
	if ws.State.DiffState != "clean" {
		return fmt.Errorf("diff_state = %s，期望 clean", ws.State.DiffState)
	}
	return nil
}

func rstAssertDirty(ctx context.Context, app syncapp.Application, relationID string) error {
	ws, err := app.GetWorkspace(ctx, relationID)
	if err != nil {
		return err
	}
	if ws.State.DiffState != "dirty" {
		return fmt.Errorf("partial 后 diff_state = %s，期望 dirty（红线④：不谎报 clean）", ws.State.DiffState)
	}
	return nil
}

// rstScan 扫描至完成。
func rstScan(ctx context.Context, app syncapp.Application, relationID string) error {
	tv, err := app.StartScan(ctx, relationID)
	if err != nil {
		return fmt.Errorf("StartScan: %w", err)
	}
	if _, err := rstWaitTask(ctx, app, tv.TaskID); err != nil {
		return err
	}
	return nil
}

// rstWaitTask 轮询任务至任一终态（循环本体收敛到 wait.go waitTask；restore 面
// 的节奏/超时参数包，restoretarget/download 链共用）。
func rstWaitTask(ctx context.Context, app syncapp.Application, taskID string) (view.TaskView, error) {
	return waitTask(ctx, app, taskID, taskWait{interval: restorePollInterval, timeout: restorePollTimeout})
}

// rstFindItem 按 ResourceID 查判定行（缺失返回 nil）。
func rstFindItem(p view.RestorePlanView, resourceID string) *view.RestorePlanItemView {
	for i := range p.Items {
		if string(p.Items[i].ResourceID) == resourceID {
			return &p.Items[i]
		}
	}
	return nil
}

// rstAssertMarker 断言行标记。
func rstAssertMarker(p view.RestorePlanView, resourceID string, marker model.RestoreMarker) error {
	it := rstFindItem(p, resourceID)
	if it == nil {
		return fmt.Errorf("计划缺少 %s 判定行", resourceID)
	}
	if it.Marker != marker {
		return fmt.Errorf("%s 判定 = %s，期望 %s", resourceID, it.Marker, marker)
	}
	return nil
}

// rstBaselineContentRefs 单提交的 baseline_content 引用数（去重 digest；
// 场景⑤ CAS 增量断言的账目面）。
func rstBaselineContentRefs(db *sql.DB, commitID string) (int, error) {
	var n int
	err := db.QueryRow(
		"SELECT COUNT(DISTINCT digest) FROM object_refs WHERE owner_type='commit' AND owner_id=? AND purpose=?",
		commitID, "baseline_content").Scan(&n)
	return n, err
}

// rstDistinctBaselineContentDigests 关系全链 baseline_content 引用的去重
// digest 数（跨提交内容寻址去重断言：restore 收口摄取回 v0 引用零新对象）。
func rstDistinctBaselineContentDigests(db *sql.DB, relationID string) (int, error) {
	var n int
	err := db.QueryRow(`
SELECT COUNT(DISTINCT rf.digest) FROM object_refs rf
JOIN sync_commits c ON c.id = rf.owner_id AND rf.owner_type='commit'
WHERE c.relation_id=? AND rf.purpose=?`, relationID, "baseline_content").Scan(&n)
	return n, err
}
