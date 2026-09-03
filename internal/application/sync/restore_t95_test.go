package sync_test

// 票 #95 黑盒回归（ADR-0012 §4/§6/§7.2；执行规格 §F3/§F4/§F5）：存量降级与
// 错写链修正的用例外部行为面——
//
//  1. 新基线 CAS 命中对照面（#88 捕获在场）：metafile 漂移行不降标，项目侧
//     写回从 CAS 命中零网络零用户介入 exact 收口；
//  2. 存量基线造数手术（同一基线直接置空 content 指针）→ 宽判降标
//     user_object_required + no_project_content（纯静态零探测）+ ExactFeasible
//     如实翻转 + exact 前置拒绝（err.restore.exact_infeasible）；
//  3. 补全通道关闭：jar 字节（目标声明 sha256 所指对象，旧链会收下并错写进
//     metafile 路径）被新码拒收（err.userobject.no_project_content）——拒绝
//     而非落盘；
//  4. skip 链：allow_partial + skip → committed 且 partial 不谎报
//     （commit=partial、remaining=1、relation 保持 dirty、metafile 保持漂移
//     现状）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/download"
	"packgradle/internal/errs"
)

// t95ProbeCounter 是记录探测请求数的 Probes 注入（零探测断言的数据面）。
type t95ProbeCounter struct {
	mu       sync.Mutex
	requests int
}

func (p *t95ProbeCounter) ProbeHead(_ context.Context, reqs []download.ProbeRequest,
	onResult func(download.ProbeRequest, download.ProbeResult)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests += len(reqs)
}

func (p *t95ProbeCounter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

// t95Fixture 搭单 CF-sha256 mod 夹具（chrono；jar 载体恒 v0，声明 sha256 即
// jar 实测摘要——错写链的行形状：目标声明摘要=jar 摘要，写回侧仅 project）。
func t95Fixture(t *testing.T) (projectRoot, instanceDir, dataRoot, jarPath string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")
	gameDir := filepath.Join(instanceDir, "minecraft")
	jarPath = filepath.Join(gameDir, "mods", "chrono-1.0.jar")

	meta := r59CFMeta("Chrono", "chrono-1.0.jar", 369812, 7654321, "sha256", r59sha256(t95JarV0))
	writeFile(t, filepath.Join(projectRoot, "pack.toml"), fxR59PackToml)
	writeFile(t, filepath.Join(projectRoot, "index.toml"), r59IndexToml("chrono.pw.toml"))
	writeFile(t, filepath.Join(projectRoot, "mods", "chrono.pw.toml"), meta)
	writeFile(t, filepath.Join(instanceDir, "instance.cfg"), r59InstanceCfg)
	writeFile(t, jarPath, t95JarV0)
	writeFile(t, filepath.Join(gameDir, "mods", ".index", "chrono-1.0.jar.pw.toml"),
		r59IndexMeta("Chrono", "chrono-1.0.jar", "sha256", r59sha256(t95JarV0)))
	dataRoot = filepath.Join(base, "userdata")
	return
}

const t95JarV0 = "fake chrono jar v0"

// t95DriftMeta 把 metafile 推进到漂移代（声明 sha256 与 file-id 同换——前者在
// mod 语义摘要内，是漂移可观测的维度；后者是版本决策的真实形状）。
func t95DriftMeta(t *testing.T, path, hashSalt string, fileID int64) string {
	t.Helper()
	meta := r59CFMeta("Chrono", "chrono-1.0.jar", 369812, fileID, "sha256", r59sha256(hashSalt))
	writeFile(t, path, meta)
	return meta
}

// t95ApplyRound 执行一轮同步（扫描 → 计划 → 冲突决议（mod 走 P2 划线 skip，
// 其余从项目初始化）→ 确认 → committed），返回新提交 id。
func t95ApplyRound(t *testing.T, app syncapp.Application, rel view.RelationView) string {
	t.Helper()
	ctx := context.Background()
	mustScanAndWait(t, app, rel.RelationID)
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	resolutions := make([]model.Resolution, 0, len(draft.Conflicts))
	for _, c := range draft.Conflicts {
		choice := model.ChoiceInitializeFromProject
		if normalize.KindOfResourceID(c.ResourceID) == model.ResourceMod {
			choice = model.ChoiceSkip // P2 划线：离线面不物化 mod
		}
		resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: choice})
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	tv := mustConfirm(t, app, resolved.PlanID)
	final := waitApplyTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("apply 任务终态 %s（problem=%+v）", final.Status, final.Problem)
	}
	head, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 1})
	if err != nil || len(head.Items) == 0 {
		t.Fatalf("ListCommits: %v", err)
	}
	return head.Items[0].CommitID
}

// t95StripBaselineContent 存量基线造数手术：对 baseline_resources 的项目侧
// 表示 JSON 直接置空 content 指针（模拟捕获上线前的旧基线；pgheadless -restore
// 场景⑥同款手段，不做捕获开关）。
func t95StripBaselineContent(t *testing.T, db *sql.DB, baselineID string) {
	t.Helper()
	rows, err := db.Query(
		`SELECT resource_id, project_representation_json FROM baseline_resources
		 WHERE baseline_id=? AND project_representation_json IS NOT NULL`, baselineID)
	if err != nil {
		t.Fatal(err)
	}
	type repUpdate struct{ id, raw string }
	var updates []repUpdate
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "mod:") {
			continue // 旧基线只缺 mod metafile 捕获（文件/文本资源 Content v1 已在）
		}
		var rep map[string]any
		if err := json.Unmarshal([]byte(raw), &rep); err != nil {
			t.Fatal(err)
		}
		if _, ok := rep["content"]; !ok {
			continue
		}
		delete(rep, "content")
		b, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		updates = append(updates, repUpdate{id, string(b)})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, u := range updates {
		if _, err := db.Exec(
			`UPDATE baseline_resources SET project_representation_json=? WHERE baseline_id=? AND resource_id=?`,
			u.raw, baselineID, u.id); err != nil {
			t.Fatal(err)
		}
	}
}

// t95TargetBaselineID 读提交的结果基线 id（造数手术的定位面）。
func t95TargetBaselineID(t *testing.T, db *sql.DB, commitID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT result_baseline_id FROM sync_commits WHERE id=?`, commitID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRestore95LegacyNoProjectContentSkipChain(t *testing.T) {
	projectRoot, instanceDir, dataRoot, jarPath := t95Fixture(t)
	probes := &t95ProbeCounter{}
	app, db := newStack(t, dataRoot, func(d *syncapp.AppDeps) { d.Probes = probes })
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	metaPath := filepath.Join(projectRoot, "mods", "chrono.pw.toml")
	metaV0, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	// initialize → c1（捕获收口：c1 基线项目侧表示带实测 Content，metafile 字节
	// 经 ingestBaselineProjectContent 入 CAS——新基线 CAS 命中面的数据前提）。
	c1 := t95ApplyRound(t, app, rel)

	// ---- 对照面 A（新基线 CAS 命中，#88 出口①）：metafile 漂移行不降标，----
	// ---- exact 零网络零用户介入收口，metafile 写回 v0 字节            ----
	t95DriftMeta(t, metaPath, "drift-v2", 8765432)
	mustScanAndWait(t, app, rel.RelationID)
	probesBeforeA := probes.count()
	draftA, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		t.Fatalf("对照面 PrepareRestore: %v", err)
	}
	rowA := r59Item(t, draftA, "mod:curseforge:369812")
	r59AssertMarker(t, rowA, model.MarkerRedownloadRequired, "")
	if !draftA.ExactFeasible {
		t.Fatalf("新基线行不应降标: (%s, %q) feasible=%v", rowA.Marker, rowA.MarkerReason, draftA.ExactFeasible)
	}
	if probes.count() == probesBeforeA {
		t.Fatal("对照面 redownload 候选行应被探测（探测通道在场）")
	}
	resolvedA, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draftA.PlanID, RequestedExactness: "exact"})
	if err != nil {
		t.Fatalf("对照面 resolve exact: %v", err)
	}
	taskA := r60MustConfirmRestore(t, app, resolvedA.PlanID)
	if taskA.Status != model.TaskStatusSucceeded || taskA.Outcome != model.TaskOutcomeExact {
		t.Fatalf("对照面 restore 终态 %s/%s（problem=%+v）", taskA.Status, taskA.Outcome, taskA.Problem)
	}
	if got, _ := os.ReadFile(metaPath); string(got) != string(metaV0) {
		t.Fatalf("对照面 metafile 应经 CAS 写回 v0: %q", string(got))
	}

	// ---- 主面 B（存量降级 skip 链，ADR-0012 §4 出口③）----
	// 造数手术：c1 基线的项目侧 mod 表示直接置空 content 指针（模拟旧基线）。
	t95StripBaselineContent(t, db, t95TargetBaselineID(t, db, c1))
	t95DriftMeta(t, metaPath, "drift-v3", 9876543)
	mustScanAndWait(t, app, rel.RelationID)
	probesBeforeB := probes.count()
	draftB, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: c1})
	if err != nil {
		t.Fatalf("存量面 PrepareRestore: %v", err)
	}
	rowB := r59Item(t, draftB, "mod:curseforge:369812")
	r59AssertMarker(t, rowB, model.MarkerUserObjectRequired, model.MarkerReasonNoProjectContent)
	if rowB.ExpectedDigest != "" || rowB.Redownload != nil || rowB.Availability != "" {
		t.Fatalf("降标行应清空验收摘要/重取信息/可用性: %+v", rowB)
	}
	if draftB.ExactFeasible || len(draftB.BlockedBy) == 0 {
		t.Fatalf("就绪面应如实: feasible=%v blocked=%+v", draftB.ExactFeasible, draftB.BlockedBy)
	}
	// 纯静态零探测：降标后置覆写清空 Redownload，探测通道在场但不发请求。
	if probes.count() != probesBeforeB {
		t.Fatalf("降标行应零探测: %d → %d", probesBeforeB, probes.count())
	}
	// exact 如实（ADR-0012 §6）：含存量无源行的计划 exact 前置拒绝。
	if _, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draftB.PlanID, RequestedExactness: "exact"}); err == nil ||
		errs.CodeOf(err) != "err.restore.exact_infeasible" {
		t.Fatalf("含存量无源行的计划 exact 应前置拒绝: %v", err)
	}
	// 补全通道关闭（ADR-0012 §4 第二道锁）：jar 字节=目标声明 sha256 所指对象，
	// 旧链在此收下 jar 字节并错写进 metafile 路径；新码拒绝而非落盘。
	if _, err := app.StageUserObject(ctx, view.StageUserObjectInput{
		PlanID: draftB.PlanID, ResourceID: string(rowB.ResourceID), SourcePath: jarPath,
	}); err == nil || errs.CodeOf(err) != "err.userobject.no_project_content" {
		t.Fatalf("降标行补全应 no_project_content 拒收: %v", err)
	}
	// skip 链（出口通路）：allow_partial + skip → committed partial 不谎报。
	resolvedB, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{
		PlanID: draftB.PlanID, RequestedExactness: "allow_partial",
		SkipResourceIDs: []string{string(rowB.ResourceID)},
	})
	if err != nil {
		t.Fatalf("resolve allow_partial: %v", err)
	}
	taskB := r60MustConfirmRestore(t, app, resolvedB.PlanID)
	if taskB.Status != model.TaskStatusSucceeded || taskB.Outcome != model.TaskOutcomePartial {
		t.Fatalf("skip 链终态 %s/%s（problem=%+v），期望 succeeded/partial", taskB.Status, taskB.Outcome, taskB.Problem)
	}
	head, err := app.GetCommit(ctx, rel.RelationID, taskB.CommitID)
	if err != nil {
		t.Fatal(err)
	}
	if head.Summary.Kind != string(model.PlanRestore) || head.Summary.Completeness != model.TaskOutcomePartial ||
		head.Summary.RemainingChangeCnt != 1 {
		t.Fatalf("提交头 kind=%s completeness=%s remaining=%d，期望 restore/partial/1（partial 不谎报）",
			head.Summary.Kind, head.Summary.Completeness, head.Summary.RemainingChangeCnt)
	}
	// 红线④：partial 后 relation 保持 dirty（skip 残留诚实投影）。
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if ws.State.DiffState != "dirty" {
		t.Fatalf("partial 后 diff_state = %s，期望 dirty", ws.State.DiffState)
	}
	// 拒绝而非落盘：metafile 保持漂移现状（jar 字节绝不写入 metafile 路径）。
	if got, rerr := os.ReadFile(metaPath); rerr != nil || string(got) != t95DriftMetaV3 {
		t.Fatalf("skip 行应保持 v3 漂移现状: %q（err=%v）", string(got), rerr)
	}
}

// t95DriftMetaV3 是主面 B 漂移代的 metafile 字节（与写盘值同一来源生成）。
var t95DriftMetaV3 = r59CFMeta("Chrono", "chrono-1.0.jar", 369812, 9876543, "sha256", r59sha256("drift-v3"))
