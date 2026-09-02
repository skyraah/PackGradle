package sync_test

// 票 #59 headless 流程测试：回滚计划面（Prepare/Resolve/Get/StageUserObject）。
//
// 覆盖（AC 对应）：
//  1. 判定矩阵流程面负例——CAS miss → user_object_required（old 行，rec=cas 但
//     CAS 无字节）、手放 mod → user_object_required（local 行，rec=redownload
//     出身但无重取数据，「重取性看数据不看出身」）、hash 不可验（jei 行）、
//     unrecoverable 阻止 exact（runtimeonly 行）；
//  2. prepare→resolve（exact 拒绝 / allow_partial+skip）→resolved 固化；
//     skip_invalid 与 exact_infeasible 拒绝路径；
//  3. StageUserObject：错字节 hash_mismatch（args {0}=期望摘要）可重试；对字节
//     staged=true 且 ExactFeasible 实时翻转；字节进 staging 不进 CAS；
//  4. confirmation_requirements 恒含 restore_acknowledge；
//  5. err.restore.commit_not_found、恢复期门禁、GetRestorePlan 读伴随与过期投影；
//  6. config 真 apply 场景：restorable_from_cas 正例（CAS 实存）+ 删除行 +
//     exact 决议成功（TestRestorePrepareResolveExactFlow）；
//  7. 假 CDN 探测：2xx → ok（newer_available 本地比对）、404 → 降标
//     cf_unavailable、慢响应超预算 → unknown 不阻塞 prepare
//    （TestRestoreProbeFakeCDN）。
//
// mod 场景的「历史提交」用注入三件套（baseline → 占位 plan → commit）构造：
// P2 物化只有 copy（mod jar 无取数通道，票 #60 接线），真 apply 无法产生含 mod
// 判定面的提交；注入走与真 apply 相同的仓储守卫（digest 重算一致），plan_json
// 往返同时验证 RestoreItems 持久化。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/core/ids"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/download"
	"packgradle/internal/errs"
	"packgradle/internal/store/sqlite"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
)

// ---- fixture（mod 场景）----

const (
	fxR59PackToml = `name = "R59"
author = "tester"
version = "1.0.0"
`
	// v0 jar 字节；v2 用替换函数生成。
	fxR59Jeiv0    = "fake jei jar v0"
	fxR59Chronov0 = "fake chrono jar v0"
	fxR59Localv0  = "fake local jar v0"
	fxR59Oldv0    = "fake old jar v0"
	fxR59RTOnlyv0 = "fake runtimeonly jar v0"

	// local 手放 metafile：无 [download] 无 [update]（重取性看数据不看出身的
	// 数据面——出身是 packwiz metafile，但没有重取数据）。
	fxR59LocalMeta = "name = \"本地小玩意\"\nfilename = \"local-thing-1.0.jar\"\nside = \"both\"\n"
)

func r59sha256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// r59CFMeta 生成 CF mod metafile（projectID/fileID/hashFormat/hash 为零值时省略段）。
func r59CFMeta(name, filename string, projectID, fileID int64, hashFormat, hash string) string {
	s := fmt.Sprintf("name = %q\nfilename = %q\nside = \"both\"\nversion = \"1.0.0\"\n", name, filename)
	if hashFormat != "" {
		s += fmt.Sprintf("\n[download]\nhash-format = %q\nhash = %q\n", hashFormat, hash)
	}
	if projectID > 0 {
		s += fmt.Sprintf("\n[update.curseforge]\nproject-id = %d\nfile-id = %d\n", projectID, fileID)
	}
	return s
}

// r59IndexMeta 生成 runtime 侧 .index 元数据（声明 hash 与 project 侧同值）。
func r59IndexMeta(name, filename, hashFormat, hash string) string {
	return fmt.Sprintf("name = %q\nfilename = %q\nside = \"both\"\n\n[download]\nhash-format = %q\nhash = %q\n",
		name, filename, hashFormat, hash)
}

// r59IndexToml 生成 index.toml（metafile 声明；files 参数为 mods/ 下 metafile 名）。
func r59IndexToml(metas ...string) string {
	s := "index = { file = \"index.toml\", hash-format = \"sha256\", hash = \"0\" }\n\n"
	for _, m := range metas {
		s += fmt.Sprintf("[[files]]\nfile = \"mods/%s\"\nhash = \"1\"\nmetafile = true\n\n", m)
	}
	return s
}

// r59InstanceCfg 是 Prism 实例身份文件。
const r59InstanceCfg = "[General]\nname=\"R59\"\niconKey=default\n"

// makeRestoreFixtures 搭建 mod 回滚 fixture（v0 状态）：3+2 个 CF/手放 mod。
// 返回后测试用 r59EvolveV2 推进到 v2 产生差异。
func makeRestoreFixtures(t *testing.T) (projectRoot, instanceDir, dataRoot string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")
	gameDir := filepath.Join(instanceDir, "minecraft")

	jeiV0 := r59CFMeta("JEI", "jei-19.5.jar", 228525, 5566778, "murmur2", "1122334455")
	chronoV0 := r59CFMeta("Chrono", "chrono-1.0.jar", 369812, 7654321, "sha256", r59sha256(fxR59Chronov0))
	oldV0 := r59CFMeta("Old", "old-thing-1.0.jar", 0, 0, "", "")

	writeFile(t, filepath.Join(projectRoot, "pack.toml"), fxR59PackToml)
	writeFile(t, filepath.Join(projectRoot, "index.toml"), r59IndexToml("jei.pw.toml", "chrono.pw.toml", "local.pw.toml", "old.pw.toml"))
	writeFile(t, filepath.Join(projectRoot, "mods", "jei.pw.toml"), jeiV0)
	writeFile(t, filepath.Join(projectRoot, "mods", "chrono.pw.toml"), chronoV0)
	writeFile(t, filepath.Join(projectRoot, "mods", "local.pw.toml"), fxR59LocalMeta)
	writeFile(t, filepath.Join(projectRoot, "mods", "old.pw.toml"), oldV0)

	writeFile(t, filepath.Join(instanceDir, "instance.cfg"), r59InstanceCfg)
	writeFile(t, filepath.Join(gameDir, "mods", "jei-19.5.jar"), fxR59Jeiv0)
	writeFile(t, filepath.Join(gameDir, "mods", "chrono-1.0.jar"), fxR59Chronov0)
	writeFile(t, filepath.Join(gameDir, "mods", "local-thing-1.0.jar"), fxR59Localv0)
	writeFile(t, filepath.Join(gameDir, "mods", "old-thing-1.0.jar"), fxR59Oldv0)
	writeFile(t, filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar"), fxR59RTOnlyv0)
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "jei-19.5.jar.pw.toml"), r59IndexMeta("JEI", "jei-19.5.jar", "murmur2", "1122334455"))
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "chrono-1.0.jar.pw.toml"), r59IndexMeta("Chrono", "chrono-1.0.jar", "sha256", r59sha256(fxR59Chronov0)))

	dataRoot = filepath.Join(base, "userdata")
	return
}

// r59EvolveV2 把 fixture 推进到 v2（产生回滚差异面）：
//   - jei：pw.toml 声明 murmur2 换值 → project 侧语义变（hash 不可验行）
//   - chrono：pw.toml 声明 sha256 换值 + file-id 8765432 → redownload 行 +
//     newer_available 比对数据
//   - local：runtime jar 换字节 → runtime 侧语义变（手放行）
//   - old：双侧删除 → 目标 present 当前 absent 的重建行（CAS miss 负例主角）
//   - runtimeonly：runtime jar 换字节 → unrecoverable 行主角
func r59EvolveV2(t *testing.T, projectRoot, instanceDir string) {
	t.Helper()
	gameDir := filepath.Join(instanceDir, "minecraft")
	writeFile(t, filepath.Join(projectRoot, "mods", "jei.pw.toml"),
		r59CFMeta("JEI", "jei-19.5.jar", 228525, 5566778, "murmur2", "deadbeef"))
	writeFile(t, filepath.Join(projectRoot, "mods", "chrono.pw.toml"),
		r59CFMeta("Chrono", "chrono-1.0.jar", 369812, 8765432, "sha256", r59sha256(fxR59Chronov0+"v2")))
	writeFile(t, filepath.Join(gameDir, "mods", "local-thing-1.0.jar"), "fake local jar v2")
	writeFile(t, filepath.Join(gameDir, "mods", "runtimeonly-1.0.jar"), "fake runtimeonly jar v2")
	if err := os.Remove(filepath.Join(projectRoot, "mods", "old.pw.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(gameDir, "mods", "old-thing-1.0.jar")); err != nil {
		t.Fatal(err)
	}
}

// r59Recs 是注入基线的恢复途径表（判定矩阵的「可恢复途径」维度的测试输入）。
var r59Recs = map[model.ResourceID]model.Recoverability{
	"mod:curseforge:228525":       model.RecoverabilityRedownload,    // jei：出身 mod 且有 CF 数据，hash 不可验
	"mod:curseforge:369812":       model.RecoverabilityRedownload,    // chrono：redownload_required 主角
	"mod:path:mods/local.pw.toml": model.RecoverabilityRedownload,    // 手放：出身 metafile 无重取数据
	"mod:jar:runtimeonly-1.0.jar": model.RecoverabilityUnrecoverable, // unrecoverable 主角
	"mod:path:mods/old.pw.toml":   model.RecoverabilityCAS,           // CAS miss 负例主角（CAS 无其字节）
}

// ---- 注入三件套（mod 场景的「历史提交」）----

// mustInjectRestoreTarget 从当前最新双端快照构造「历史提交」三件套注入
// （baseline → kind=restore 占位 plan → sync commit），全部走仓储完整性守卫
// （digest 重算一致）。返回 commit id（PrepareRestore 的回滚目标）。
func mustInjectRestoreTarget(t *testing.T, app syncapp.Application, db *sql.DB,
	rel view.RelationView, defaultRec model.Recoverability,
	recs map[model.ResourceID]model.Recoverability) string {
	t.Helper()
	ctx := context.Background()
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	snaps := sqlite.NewSnapshotRepository(db)
	snapP, err := snaps.Get(ctx, ws.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	snapR, err := snaps.Get(ctx, ws.LatestRuntimeSnapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}

	// 基线：表示照抄观测（不猜 merge 形态），恢复途径按测试表指定。
	idsSet := map[model.ResourceID]struct{}{}
	for id := range snapP.Resources {
		idsSet[id] = struct{}{}
	}
	for id := range snapR.Resources {
		idsSet[id] = struct{}{}
	}
	resources := make(map[model.ResourceID]model.BaselineResource, len(idsSet))
	for id := range idsSet {
		rec := defaultRec
		if r, ok := recs[id]; ok {
			rec = r
		}
		pRep := r59RepOf(snapP, id)
		rRep := r59RepOf(snapR, id)
		pSem, err := r59Semantic(id, pRep)
		if err != nil {
			t.Fatal(err)
		}
		rSem, err := r59Semantic(id, rRep)
		if err != nil {
			t.Fatal(err)
		}
		resources[id] = model.BaselineResource{
			State:                 "present",
			LogicalDigest:         normalize.LogicalDigest(pSem, rSem),
			ProjectRepresentation: pRep,
			RuntimeRepresentation: rRep,
			Recoverability:        rec,
		}
	}
	base := model.SyncBaseline{
		SchemaVersion:        model.CurrentSchemaVersion,
		BaselineID:           ids.New("base_"),
		RelationID:           rel.RelationID,
		CreatedAt:            time.Now().UTC().Format(time.RFC3339),
		NormalizationVersion: normalize.NormalizationVersion,
		Resources:            resources,
	}
	if base.BaselineDigest, err = normalize.BaselineDigest(base); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewBaselineRepository(db).Insert(ctx, base); err != nil {
		t.Fatalf("注入基线: %v", err)
	}

	// 占位 plan（kind=restore）：commit 引用完整性守卫要求 plan 行存在；
	// plan_json 往返同时验证 RestoreItems 可持久化。
	plan := model.SyncPlan{
		SchemaVersion:              model.CurrentSchemaVersion,
		PlanID:                     ids.New("plan_"),
		RelationID:                 rel.RelationID,
		Kind:                       model.PlanRestore,
		BaseBaselineID:             base.BaselineID,
		BaseBaselineDigest:         base.BaselineDigest,
		InputProjectSnapshotID:     snapP.SnapshotID,
		InputRuntimeSnapshotID:     snapR.SnapshotID,
		InputProjectSnapshotDigest: snapP.SnapshotDigest,
		InputRuntimeSnapshotDigest: snapR.SnapshotDigest,
		RelationRevision:           ws.State.RelationRevision,
		ExpectedBindings: model.ExpectedBindings{
			Project: rel.Project.BindingFingerprint,
			Runtime: rel.Runtime.BindingFingerprint,
		},
		Status:    model.PlanApplied,
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	if plan.PlanDigest, err = normalize.PlanDigest(plan); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewPlanRepository(db).Insert(ctx, plan); err != nil {
		t.Fatalf("注入占位计划: %v", err)
	}

	commit := model.SyncCommit{
		CommitID:                  ids.New("commit_"),
		RelationID:                rel.RelationID,
		CreatedAt:                 time.Now().UTC().Format(time.RFC3339),
		PlanID:                    plan.PlanID,
		VerifiedProjectSnapshotID: snapP.SnapshotID,
		VerifiedRuntimeSnapshotID: snapR.SnapshotID,
		ResultBaselineID:          base.BaselineID,
		CommitKind:                string(model.PlanSync),
		Completeness:              model.TaskOutcomeExact,
	}
	if err := sqlite.NewCommitRepository(db).Insert(ctx, commit); err != nil {
		t.Fatalf("注入提交: %v", err)
	}
	return commit.CommitID
}

// r59RepOf 取快照资源表示副本（absent 为 nil）。
func r59RepOf(s model.ObservedSnapshot, id model.ResourceID) *model.Representation {
	obs, ok := s.Resources[id]
	if !ok {
		return nil
	}
	rep := obs.Representation
	return &rep
}

// r59Semantic 计算基线侧语义摘要（与 restore.go 判定同口径）。
func r59Semantic(id model.ResourceID, rep *model.Representation) (string, error) {
	if rep == nil {
		return "", nil
	}
	return normalize.SemanticDigest(normalize.KindOfResourceID(id), *rep, normalize.IdentityFromResourceID(id))
}

// ---- 断言辅助 ----

// r59Item 按 ResourceID 查判定行。
func r59Item(t *testing.T, p view.RestorePlanView, resourceID string) *view.RestorePlanItemView {
	t.Helper()
	for i := range p.Items {
		if string(p.Items[i].ResourceID) == resourceID {
			return &p.Items[i]
		}
	}
	t.Fatalf("计划缺少资源 %s 的判定行: %+v", resourceID, p.Items)
	return nil
}

// r59AssertMarker 断言行标记与原因。
func r59AssertMarker(t *testing.T, it *view.RestorePlanItemView, marker model.RestoreMarker, reason string) {
	t.Helper()
	if it.Marker != marker || it.MarkerReason != reason {
		t.Fatalf("资源 %s 判定 = (%s, %q)，期望 (%s, %q)",
			it.ResourceID, it.Marker, it.MarkerReason, marker, reason)
	}
}

// r59AssertAcknowledge 断言确认要求恒含 restore_acknowledge（severity=warning）。
func r59AssertAcknowledge(t *testing.T, p view.RestorePlanView) {
	t.Helper()
	for _, r := range p.ConfirmationRequirements {
		if r.Code == model.ConfirmRestoreAcknowledge {
			if r.Severity != model.ConfirmSeverityWarning {
				t.Fatalf("restore_acknowledge severity = %s，期望 warning", r.Severity)
			}
			return
		}
	}
	t.Fatalf("confirmation_requirements 缺少恒非空的 restore_acknowledge: %+v", p.ConfirmationRequirements)
}

// r59WriteTempFile 写临时文件（StageUserObject 的 source_path）。
func r59WriteTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provide.bin")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- 测试 ----

// TestRestorePrepareJudgeStageResolveFlow 覆盖 mod 场景全链路：四标记流程面
// 负例（CAS miss / 手放 mod / hash 不可验 / unrecoverable）、决议拒绝路径、
// StageUserObject 验收与就绪面翻转、restore_acknowledge 恒非空、门禁与拆码。
func TestRestorePrepareJudgeStageResolveFlow(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeRestoreFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	// 注入「历史提交」（v0 快照照抄为基线）。
	commitID := mustInjectRestoreTarget(t, app, db, rel, model.RecoverabilityCAS, r59Recs)

	// 推进到 v2 并重扫（当前观测 = v2）。
	r59EvolveV2(t, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)

	// ---- PrepareRestore：draft 与四标记判定 ----
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commitID})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	if draft.Status != "draft" || draft.TargetCommitID != commitID || draft.PlanID == "" {
		t.Fatalf("draft 计划头: status=%s target=%s id=%s", draft.Status, draft.TargetCommitID, draft.PlanID)
	}

	// jei：rec=redownload 出身 + CF 重取数据实存，但 murmur2 不可验 →
	// user_object_required + hash_format_unsupported（不验不装）。写回侧为
	// project metafile：#88 起捕获其自身实测 sha256 → expected_digest = v0
	// metafile 摘要（用户补全通道有了真验收口径；jar 载体的 murmur2 不可验
	// 语义不变）。
	jei := r59Item(t, draft, "mod:curseforge:228525")
	r59AssertMarker(t, jei, model.MarkerUserObjectRequired, model.MarkerReasonHashFormatUnsupported)
	jeiV0Meta := r59CFMeta("JEI", "jei-19.5.jar", 228525, 5566778, "murmur2", "1122334455")
	if jei.ExpectedDigest != r59sha256(jeiV0Meta) || jei.ChangeKind != "modify" {
		t.Fatalf("jei 行形状: %+v", jei)
	}

	// chrono：重取数据实存且 sha256 可验 → 乐观 redownload_required；
	// expected_digest 仅 user_object_required 行透出（契约 06 §3.2）；
	// 无 Probes 注入 → 不标 availability（本轮探测面由假 CDN 用例覆盖）。
	chrono := r59Item(t, draft, "mod:curseforge:369812")
	r59AssertMarker(t, chrono, model.MarkerRedownloadRequired, "")
	if chrono.Availability != "" || chrono.ExpectedDigest != "" {
		t.Fatalf("chrono 行形状: availability=%q digest=%q", chrono.Availability, chrono.ExpectedDigest)
	}

	// local：rec=redownload 出身但 metafile 无 [update]（重取性看数据不看出身）
	// → user_object_required + no_redownload_info；验收摘要 = 目标 runtime jar。
	local := r59Item(t, draft, "mod:path:mods/local.pw.toml")
	r59AssertMarker(t, local, model.MarkerUserObjectRequired, model.MarkerReasonNoRedownloadInfo)
	if local.ExpectedDigest != r59sha256(fxR59Localv0) {
		t.Fatalf("local 验收摘要 = %q，期望 v0 jar sha256", local.ExpectedDigest)
	}

	// old：rec=cas 但 CAS 无该字节（从未保全）→ CAS miss 负例：
	// user_object_required + no_redownload_info（凭目标 digest 验收用户提供字节）。
	old := r59Item(t, draft, "mod:path:mods/old.pw.toml")
	r59AssertMarker(t, old, model.MarkerUserObjectRequired, model.MarkerReasonNoRedownloadInfo)
	if old.ChangeKind != "create" {
		t.Fatalf("old 行（目标 present 当前 absent）应为 create: %s", old.ChangeKind)
	}

	// runtimeonly：rec=unrecoverable → unrecoverable（默认阻止 exact）。
	rtOnly := r59Item(t, draft, "mod:jar:runtimeonly-1.0.jar")
	r59AssertMarker(t, rtOnly, model.MarkerUnrecoverable, "")

	// 就绪面与阻塞清单：ExactFeasible=false；blocked_by = 4 个非就绪重建行。
	if draft.ExactFeasible {
		t.Fatal("存在未就绪行时 ExactFeasible 应为 false")
	}
	if len(draft.BlockedBy) != 4 {
		t.Fatalf("blocked_by 应含 4 行（jei/local/old/runtimeonly），got %d: %+v", len(draft.BlockedBy), draft.BlockedBy)
	}
	// confirmation_requirements 恒含 restore_acknowledge。
	r59AssertAcknowledge(t, draft)

	// ---- 拆码与门禁 ----
	if _, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: "commit_missing"}); err == nil {
		t.Fatal("期望 commit_not_found")
	} else if code := errs.CodeOf(err); code != "err.restore.commit_not_found" {
		t.Fatalf("错误码: %s", code)
	}
	if _, err := app.GetRestorePlan(ctx, "plan_missing"); err == nil || errs.CodeOf(err) != "err.plan.not_found" {
		t.Fatalf("GetRestorePlan 拆码: %v", err)
	}
	// 恢复所需期间 restore 与 apply 同门禁（ADR-0006 §8）。
	if _, err := db.Exec(`UPDATE relations SET health='recovery_required' WHERE id=?`, rel.RelationID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commitID}); err == nil ||
		errs.CodeOf(err) != "err.recovery.in_progress" {
		t.Fatalf("恢复期门禁: %v", err)
	}
	if _, err := db.Exec(`UPDATE relations SET health='healthy' WHERE id=?`, rel.RelationID); err != nil {
		t.Fatal(err)
	}

	// ---- Resolve：exact 前置拒绝 + skip 合法性 ----
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "exact",
	}); err == nil || errs.CodeOf(err) != "err.restore.exact_infeasible" {
		t.Fatalf("exact 应被就绪面前置拒绝: %v", err)
	}
	// skip 对 redownload 行不合法。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "allow_partial", SkipResourceIDs: []string{"mod:curseforge:369812"},
	}); err == nil || errs.CodeOf(err) != "err.restore.skip_invalid" {
		t.Fatalf("skip(redownload 行) 应拒绝: %v", err)
	}

	// ---- StageUserObject：验收与就绪面 ----
	// 对 redownload 行补全 → not_required（仅 user_object_required 行合法）。
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft.PlanID, ResourceID: "mod:curseforge:369812", SourcePath: r59WriteTempFile(t, "x"),
	}); err == nil || errs.CodeOf(err) != "err.userobject.not_required" {
		t.Fatalf("StageUserObject(redownload 行) 应拒绝: %v", err)
	}
	// 对 unrecoverable 行补全 → not_required。
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft.PlanID, ResourceID: "mod:jar:runtimeonly-1.0.jar", SourcePath: r59WriteTempFile(t, "x"),
	}); err == nil || errs.CodeOf(err) != "err.userobject.not_required" {
		t.Fatalf("StageUserObject(unrecoverable 行) 应拒绝: %v", err)
	}
	// 错字节 → hash_mismatch，args {0}=期望摘要（可重试）。
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft.PlanID, ResourceID: "mod:path:mods/local.pw.toml", SourcePath: r59WriteTempFile(t, "corrupted bytes"),
	}); err == nil {
		t.Fatal("错字节应 hash_mismatch")
	} else {
		var appErr *errs.AppError
		if !errors.As(err, &appErr) || appErr.Code != "err.userobject.hash_mismatch" {
			t.Fatalf("错字节错误码: %v", err)
		}
		if len(appErr.Args) != 1 || appErr.Args[0] != r59sha256(fxR59Localv0) {
			t.Fatalf("hash_mismatch args = %v，期望 [期望摘要]", appErr.Args)
		}
	}
	// 对字节重试 → staged=true；unrecoverable 在场，ExactFeasible 仍 false。
	staged1, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draft.PlanID, ResourceID: "mod:path:mods/local.pw.toml", SourcePath: r59WriteTempFile(t, fxR59Localv0),
	})
	if err != nil {
		t.Fatalf("对字节补全: %v", err)
	}
	if !r59Item(t, staged1, "mod:path:mods/local.pw.toml").Staged {
		t.Fatal("对字节后 staged 应为 true")
	}
	if staged1.ExactFeasible {
		t.Fatal("unrecoverable 未跳过时 ExactFeasible 应保持 false")
	}
	// 字节进 staging 绑 plan，不进 CAS（ADR-0005 §7 红线）。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM objects WHERE digest=?`, r59sha256(fxR59Localv0)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("用户补全字节不得进 CAS")
	}
	// 已 staged 的行不可 skip。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "allow_partial",
		SkipResourceIDs: []string{"mod:jar:runtimeonly-1.0.jar", "mod:path:mods/local.pw.toml"},
	}); err == nil || errs.CodeOf(err) != "err.restore.skip_invalid" {
		t.Fatalf("skip(staged 行) 应拒绝: %v", err)
	}

	// ---- resolve allow_partial + skip(unrecoverable + 未补全行) → resolved 固化 ----
	// jei 行未补全（skip 对未 staged 的 user_object 行合法），与 unrecoverable
	// 同属合法 skip。
	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "allow_partial",
		SkipResourceIDs: []string{"mod:jar:runtimeonly-1.0.jar", "mod:curseforge:228525"},
	})
	if err != nil {
		t.Fatalf("ResolveRestorePlan: %v", err)
	}
	if resolved.Status != "resolved" || resolved.RequestedExactness != "allow_partial" {
		t.Fatalf("resolved 计划头: status=%s exactness=%s", resolved.Status, resolved.RequestedExactness)
	}
	if resolved.PlanID == draft.PlanID {
		t.Fatal("resolved 应为新不可变计划（旧 draft 只读）")
	}
	if !r59Item(t, resolved, "mod:jar:runtimeonly-1.0.jar").Skipped {
		t.Fatal("skip 决议应投影到行 Skipped")
	}
	if resolved.ExactFeasible {
		t.Fatal("jei/old 未 staged，ExactFeasible 应为 false")
	}
	r59AssertAcknowledge(t, resolved)

	// resolved 后仍可补全（confirm 前补齐）：skip 行不计入就绪面，old 入暂存
	// 后剩余行（chrono=redownload、local=draft 期已 staged、old）全部就绪，
	// ExactFeasible 实时翻转 true（暂存锚继承：draft 期的 local 字节可见）。
	staged2, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: resolved.PlanID, ResourceID: "mod:path:mods/old.pw.toml", SourcePath: r59WriteTempFile(t, fxR59Oldv0),
	})
	if err != nil {
		t.Fatalf("resolved 后补全: %v", err)
	}
	if !staged2.ExactFeasible {
		t.Fatal("skip 后剩余行全部就绪，ExactFeasible 应翻转 true")
	}

	// GetRestorePlan 对称读伴随：resolved 计划可读、判定行完整。
	got, err := app.GetRestorePlan(ctx, resolved.PlanID)
	if err != nil {
		t.Fatalf("GetRestorePlan: %v", err)
	}
	if got.Status != "resolved" || len(got.Items) != len(resolved.Items) {
		t.Fatalf("GetRestorePlan 投影: status=%s items=%d", got.Status, len(got.Items))
	}

	// 类别门禁：restore 计划不得经 apply 计划面消费（ConfirmPlan/ResolvePlan
	// 按计划类别拒绝，not_found 同口径不泄露形状；ConfirmRestorePlan 归票 #60）。
	if _, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID}); err == nil ||
		errs.CodeOf(err) != "err.plan.not_found" {
		t.Fatalf("ConfirmPlan(restore 计划) 应拒绝: %v", err)
	}
	if _, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID}); err == nil ||
		errs.CodeOf(err) != "err.plan.not_found" {
		t.Fatalf("ResolvePlan(restore 计划) 应拒绝: %v", err)
	}
}

// TestRestorePrepareResolveExactFlow 覆盖 config 文件真 apply 场景：
// restorable_from_cas 正例（round2 的 before 保全使 CAS 实存）、删除行警示面、
// exact 决议成功与 GetRestorePlan 过期投影。
func TestRestorePrepareResolveExactFlow(t *testing.T) {
	projectRoot, instanceDir, dataRoot := makeApplyFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustRelationForApply(t, app, projectRoot, instanceDir)

	// round1 initialize → commit1（a/b/c 三资源入基线）。
	plan1 := mustResolveApplyPlan(t, app, rel, round1Choices)
	tv := mustConfirm(t, app, plan1.PlanID)
	final1 := waitApplyTask(t, app, tv.TaskID)
	if final1.Status != model.TaskStatusSucceeded || final1.CommitID == "" {
		t.Fatalf("round1 终态: %+v", final1)
	}
	commit1 := final1.CommitID

	// round2 sync：删 runtime b（传播删 project 副本）、改 project c → commit2。
	// 删除/覆盖的 before 字节（b、c 的 v0）保全进 CAS。
	if err := os.Remove(filepath.Join(instanceDir, "minecraft", "config", "b.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "config", "c.toml"), []byte(fxApplyC2), 0o644); err != nil {
		t.Fatal(err)
	}
	mustScanAndWait(t, app, rel.RelationID)
	plan2 := mustResolveApplyPlan(t, app, rel, nil)
	tv2 := mustConfirm(t, app, plan2.PlanID)
	if waitApplyTask(t, app, tv2.TaskID).Status != model.TaskStatusSucceeded {
		t.Fatal("round2 应成功")
	}

	// runtime 侧新增手工文件 → 回滚时成为删除行（非 mod：不警示、可找回）。
	writeFile(t, filepath.Join(instanceDir, "minecraft", "config", "handmade.toml"), "handmade = true\n")
	mustScanAndWait(t, app, rel.RelationID)

	// 回滚到 commit1：b（双侧重建）、c（modify）、handmade（删除）。
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commit1})
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	b := r59Item(t, draft, "file:config/b.toml")
	r59AssertMarker(t, b, model.MarkerRestorableFromCAS, "")
	c := r59Item(t, draft, "file:config/c.toml")
	r59AssertMarker(t, c, model.MarkerRestorableFromCAS, "")
	handmade := r59Item(t, draft, "file:config/handmade.toml")
	if handmade.ChangeKind != "delete" || handmade.DeletionWarn {
		t.Fatalf("handmade 删除行: kind=%s warn=%v（非 mod 走 before-preserve 不警示）",
			handmade.ChangeKind, handmade.DeletionWarn)
	}
	if !draft.ExactFeasible || len(draft.BlockedBy) != 0 {
		t.Fatalf("全部行就绪: feasible=%v blocked=%+v", draft.ExactFeasible, draft.BlockedBy)
	}
	r59AssertAcknowledge(t, draft)

	// skip 对 restorable_from_cas 行不合法。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draft.PlanID, RequestedExactness: "allow_partial", SkipResourceIDs: []string{"file:config/b.toml"},
	}); err == nil || errs.CodeOf(err) != "err.restore.skip_invalid" {
		t.Fatalf("skip(cas 行) 应拒绝: %v", err)
	}

	// exact 决议成功：固化 requested_exactness，status→resolved。
	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"})
	if err != nil {
		t.Fatalf("ResolveRestorePlan(exact): %v", err)
	}
	if resolved.Status != "resolved" || resolved.RequestedExactness != "exact" || !resolved.ExactFeasible {
		t.Fatalf("resolved: status=%s exactness=%s feasible=%v", resolved.Status, resolved.RequestedExactness, resolved.ExactFeasible)
	}

	// GetRestorePlan 读伴随 + 过期读取时投影（不写库）。
	if _, err := db.Exec(`UPDATE sync_plans SET expires_at='2000-01-01T00:00:00Z',
		plan_json=json_set(plan_json, '$.expires_at', '2000-01-01T00:00:00Z') WHERE id=?`, resolved.PlanID); err != nil {
		t.Fatal(err)
	}
	expired, err := app.GetRestorePlan(ctx, resolved.PlanID)
	if err != nil {
		t.Fatalf("GetRestorePlan(expired): %v", err)
	}
	if expired.Status != "expired" {
		t.Fatalf("过期计划投影 = %s，期望 expired", expired.Status)
	}
}

// TestRestoreProbeFakeCDN 覆盖 CF 尽力探测三态（契约 06 §5）：2xx → ok +
// newer_available 本地比对；404 → prepare 时点降标 cf_unavailable；慢响应超
// 预算 → unknown 保持乐观标记，且 prepare 不被阻塞。
func TestRestoreProbeFakeCDN(t *testing.T) {
	base := t.TempDir()
	projectRoot := filepath.Join(base, "project")
	instanceDir := filepath.Join(base, "instance")
	dataRoot := filepath.Join(base, "userdata")
	gameDir := filepath.Join(instanceDir, "minecraft")

	// 4 个 sha256 CF mod：ok（file-id 不变 → newer=false）、newer（file-id 变 →
	// newer=true）、gone（404 → 降标）、slow（挂住 → unknown）。
	type probeMod struct {
		name, jar, meta string
		projectID, v0ID int64
	}
	mods := []probeMod{
		{"OK", "probe-ok-1.0.jar", "probe-ok.pw.toml", 11, 7654321},
		{"Newer", "probe-newer-1.0.jar", "probe-newer.pw.toml", 12, 3333333},
		{"Gone", "probe-gone-1.0.jar", "probe-gone.pw.toml", 13, 1111111},
		{"Slow", "probe-slow-1.0.jar", "probe-slow.pw.toml", 14, 2222222},
	}
	jarV0 := map[string]string{}
	index := "index = { file = \"index.toml\", hash-format = \"sha256\", hash = \"0\" }\n\n"
	writeFile(t, filepath.Join(projectRoot, "pack.toml"), fxR59PackToml)
	for _, m := range mods {
		jarV0[m.jar] = "fake " + m.name + " jar v0"
		writeFile(t, filepath.Join(projectRoot, "index.toml"), index) // 占位，稍后统一重写
	}
	index = "index = { file = \"index.toml\", hash-format = \"sha256\", hash = \"0\" }\n\n"
	for _, m := range mods {
		index += fmt.Sprintf("[[files]]\nfile = \"mods/%s\"\nhash = \"1\"\nmetafile = true\n\n", m.meta)
		meta := r59CFMeta(m.name, m.jar, m.projectID, m.v0ID, "sha256", r59sha256(jarV0[m.jar]))
		writeFile(t, filepath.Join(projectRoot, "mods", m.meta), meta)
		writeFile(t, filepath.Join(gameDir, "mods", m.jar), jarV0[m.jar])
		writeFile(t, filepath.Join(gameDir, "mods", ".index", m.jar+".pw.toml"), r59IndexMeta(m.name, m.jar, "sha256", r59sha256(jarV0[m.jar])))
	}
	writeFile(t, filepath.Join(projectRoot, "index.toml"), index)
	writeFile(t, filepath.Join(instanceDir, "instance.cfg"), r59InstanceCfg)
	dataRoot = filepath.Join(dataRoot, "userdata")

	// 假 CDN + 引擎注入 Probes（生产同款构造，BaseURL 指向假 CDN）。
	cdn := download.NewFakeCDN()
	srv := httptest.NewServer(cdn.Handler())
	t.Cleanup(srv.Close)
	cdn.SetFile("/files/7654/321/probe-ok-1.0.jar", []byte("available"))
	cdn.SetFile("/files/3333/333/probe-newer-1.0.jar", []byte("available"))
	// gone 不登记 → 404；slow 响应前延迟 6s（> 单请求 5s 探测超时常量，
	// HEAD 不消费响应体，须在头之前阻塞）——验证「超时 → unknown 不阻塞
	// prepare」（预算耗尽与单请求超时在 ProbeHead 内同归 unknown 回调）。
	cdn.Script("/files/2222/222/probe-slow-1.0.jar", download.FakeStep{Delay: 6 * time.Second})
	engine, err := download.New(download.Options{BaseURL: srv.URL + "/files"})
	if err != nil {
		t.Fatal(err)
	}
	app, db := newStack(t, dataRoot, func(d *syncapp.AppDeps) { d.Probes = engine })

	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)
	recs := map[model.ResourceID]model.Recoverability{}
	for _, m := range mods {
		recs[model.ResourceID(fmt.Sprintf("mod:curseforge:%d", m.projectID))] = model.RecoverabilityRedownload
	}
	commitID := mustInjectRestoreTarget(t, app, db, rel, model.RecoverabilityCAS, recs)

	// v2：全部换声明 hash；仅 newer 的 file-id 变化（newer_available 比对数据）。
	for _, m := range mods {
		newID := m.v0ID
		if m.name == "Newer" {
			newID = 4444444
		}
		writeFile(t, filepath.Join(projectRoot, "mods", m.meta),
			r59CFMeta(m.name, m.jar, m.projectID, newID, "sha256", r59sha256(jarV0[m.jar]+"v2")))
	}
	scanAndWait(t, app, rel.RelationID)

	// 探测尽力而为：slow 行等待单请求超时（5s 常量）后按 unknown 回调，
	// prepare 整体在超时后继续落库返回（不阻塞、不失败）。
	start := time.Now()
	draft, err := app.PrepareRestore(context.Background(), view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: commitID})
	if err != nil {
		t.Fatalf("PrepareRestore(带探测): %v", err)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Second || elapsed > 15*time.Second {
		t.Fatalf("探测耗时应介于单请求超时与可放弃之间: %v", elapsed)
	}

	// ok 行：availability=ok + newer_available 本地比对（零网络）。
	ok := r59Item(t, draft, "mod:curseforge:11")
	r59AssertMarker(t, ok, model.MarkerRedownloadRequired, "")
	if ok.Availability != "ok" || ok.NewerAvailable {
		t.Fatalf("ok 行: availability=%q newer=%v，期望 ok/false（file-id 未变）", ok.Availability, ok.NewerAvailable)
	}
	newer := r59Item(t, draft, "mod:curseforge:12")
	if newer.Availability != "ok" || !newer.NewerAvailable {
		t.Fatalf("newer 行: availability=%q newer=%v，期望 ok/true（head 8765432≠目标 file-id）",
			newer.Availability, newer.NewerAvailable)
	}
	// 404 行：prepare 时点降标 user_object_required + cf_unavailable。
	gone := r59Item(t, draft, "mod:curseforge:13")
	r59AssertMarker(t, gone, model.MarkerUserObjectRequired, model.MarkerReasonCFUnavailable)
	if gone.Availability != "" || gone.NewerAvailable {
		t.Fatalf("降标行不应携带探测面: %+v", gone)
	}
	// 慢响应行：超预算 → unknown 保持乐观标记，不阻塞 prepare。
	slow := r59Item(t, draft, "mod:curseforge:14")
	r59AssertMarker(t, slow, model.MarkerRedownloadRequired, "")
	if slow.Availability != "unknown" {
		t.Fatalf("slow 行 availability = %q，期望 unknown", slow.Availability)
	}
}
