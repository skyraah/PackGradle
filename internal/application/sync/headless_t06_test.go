package sync_test

// T06（票 #39）验收面：Apply 运行与历史读投影四方法（契约 05 §2/§3.2/§3.3/§3.5）。
// 不依赖 Apply 引擎——预置 apply_runs / operation_journal / sync_commits /
// commit_changes 行即测：
// ① GetApplyRun：created_at 最新运行头；关系无运行 → err.apply.no_run；
// ② ListApplyOperations：ordinal 升序 + operation_id cursor 跨页（GetChanges 同协议）；
//    task 不存在/跨关系 → err.apply.run_not_found；白名单序列化产物断言无
//    临时路径与 ownership proof（契约 05 §0 硬约束 4 / ADR-0004 §4）；
// ③ ListCommits：created_at DESC 分页 + commit_id cursor 跨页（同刻 id DESC 决胜）；
// ④ GetCommit：changes 全量 + before/after 表示摘要（nil ↔ omitempty null）；
//    不存在/跨关系 → err.commit.not_found；
// ⑤ 空 slices 归一 []（items/changes）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/store/sqlite"
)

// t06Fixture 是预置好的 Apply 历史数据（一次装配，四方法共用）。
type t06Fixture struct {
	rel       view.RelationView
	rel2      view.RelationView // 第二关系：只有端点，无运行/提交（跨关系拒绝与空页用）
	plan      view.SyncPlanView
	run1ID    string // committed（无 journal 行）
	run2ID    string // recovery_required（3 条 journal 行，含投毒列）
	opIDs     []string
	commitIDs []string // created_at 升序：c1(10:00)、c2(10:01)、c3(10:02)、c4(10:02 同刻后插)
	projResID string
	rtResID   string
	projRep   model.Representation
	rtRep     model.Representation
}

// mustJSON 序列化测试数据（严格失败）。
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// seedTaskRow 预置一行 apply 任务（apply_runs.task_id 外键悬挂需要）。
func seedTaskRow(t *testing.T, db *sql.DB, taskID, relationID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO tasks(id, relation_id, kind, status, phase, sequence, can_cancel,
		completed, total, message_key, message_args_json, created_at, updated_at)
		VALUES(?, ?, 'apply', 'succeeded', '', 7, 0, 0, 0, '', '[]', ?, ?)`, taskID, relationID, now, now); err != nil {
		t.Fatal(err)
	}
}

// seedT06 装配应用 + 双关系 + 预置运行头/操作日志/提交历史。
func seedT06(t *testing.T) (syncapp.Application, *sql.DB, t06Fixture) {
	t.Helper()
	projectRoot, instanceDir, dataRoot := makeFixtures(t)
	app, db := newStack(t, dataRoot)
	ctx := context.Background()
	rel := mustPrepareAndCreate(t, app, projectRoot, instanceDir)
	scanAndWait(t, app, rel.RelationID)
	plan := prepareDraftPlan(t, app, rel.RelationID)

	// 第二对端点 → 第二个关系（跨关系拒绝断言用；不预置运行与提交）
	project2 := filepath.Join(filepath.Dir(projectRoot), "project2")
	instance2 := filepath.Join(filepath.Dir(instanceDir), "Collapse2")
	writeFile(t, filepath.Join(project2, "pack.toml"), fxPackToml)
	writeFile(t, filepath.Join(instance2, "instance.cfg"), "[General]\nname=\"Collapse2\"\niconKey=default\n")
	writeFile(t, filepath.Join(instance2, "minecraft", "mods", "placeholder.jar"), "placeholder")
	rel2 := mustPrepareAndCreate(t, app, project2, instance2)

	// 两侧 verified 快照的真实表示（GetCommit 表示摘要的数据源）
	snaps := sqlite.NewSnapshotRepository(db)
	snapP, err := snaps.Get(ctx, plan.InputProjectSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	snapR, err := snaps.Get(ctx, plan.InputRuntimeSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	fx := t06Fixture{rel: rel, rel2: rel2, plan: plan}
	fx.projResID, fx.projRep = pickT06Resource(t, snapP, "mod:path:mods/local.pw.toml")
	fx.rtResID, fx.rtRep = pickT06Resource(t, snapR, "mod:jar:runtimeonly-1.0.jar")

	// 任务行（运行头外键）
	fx.run1ID, fx.run2ID = "task_t06_run1", "task_t06_run2"
	seedTaskRow(t, db, fx.run1ID, rel.RelationID)
	seedTaskRow(t, db, fx.run2ID, rel.RelationID)

	// 结果基线（sync_commits.result_baseline_id 外键；守卫只要求存在且同关系）
	if _, err := db.Exec(`INSERT INTO sync_baselines(id, relation_id, parent_id, created_at,
		baseline_digest, normalization_version) VALUES('base_t06_seed', ?, NULL, ?, 'sha256:seed', 1)`,
		rel.RelationID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	// 提交历史：created_at 升序 c1<c2<c3=c4（c4 同刻后插，ULID 单调 → id 更大）
	commits := sqlite.NewCommitRepository(db)
	fx.commitIDs = []string{"commit_t06_c1", "commit_t06_c2", "commit_t06_c3", "commit_t06_c4"}
	insertCommit := func(id, createdAt string, changes []model.CommitChange) {
		t.Helper()
		if err := commits.Insert(ctx, model.SyncCommit{
			CommitID: id, RelationID: rel.RelationID, CreatedAt: createdAt,
			PlanID:                    plan.PlanID,
			VerifiedProjectSnapshotID: plan.InputProjectSnapshotID,
			VerifiedRuntimeSnapshotID: plan.InputRuntimeSnapshotID,
			ResultBaselineID:          "base_t06_seed",
			CommitKind:                "sync", Completeness: "exact",
			RemainingChangeCount: len(changes), Changes: changes,
		}); err != nil {
			t.Fatalf("预置提交 %s: %v", id, err)
		}
	}
	insertCommit(fx.commitIDs[0], "2026-08-31T10:00:00Z", nil)
	insertCommit(fx.commitIDs[1], "2026-08-31T10:01:00Z", []model.CommitChange{
		{
			ResourceID: model.ResourceID(fx.projResID), ChangeKind: "modify",
			ProjectBefore: &fx.projRep, ProjectAfter: &fx.projRep,
		},
		{
			ResourceID: model.ResourceID(fx.rtResID), ChangeKind: "create",
			RuntimeAfter: &fx.rtRep,
		},
	})
	insertCommit(fx.commitIDs[2], "2026-08-31T10:02:00Z", nil)
	insertCommit(fx.commitIDs[3], "2026-08-31T10:02:00Z", nil)

	// 操作日志：run2 三行；temp_relative_path 与 ownership_proof 投毒，
	// 断言投影序列化产物绝不携带（硬约束 4）。
	journal := sqlite.NewOperationJournalRepository(db)
	fx.opIDs = []string{"op_t06_1", "op_t06_2", "op_t06_3"}
	if err := journal.InsertBatch(ctx, []model.JournalOperation{
		{
			TaskID: fx.run2ID, OperationID: fx.opIDs[0], Ordinal: 1,
			Status: model.OperationStatusPending,
			TargetRelativePath: "mods/local.pw.toml",
			TempRelativePath:   "staging/" + fx.run2ID + "/poison-op1.bin",
			OwnershipProof:     json.RawMessage(`{"evidence":"poison-proof"}`),
			Operation: mustJSON(t, model.PlannedOperation{
				ID: fx.opIDs[0], Kind: model.OpWriteRuntime,
				ResourceID: model.ResourceID(fx.projResID),
			}),
		},
		{
			TaskID: fx.run2ID, OperationID: fx.opIDs[1], Ordinal: 2,
			Status: model.OperationStatusPending,
			TargetRelativePath: "mods/jei-19.5.jar",
			Operation: mustJSON(t, model.PlannedOperation{
				ID: fx.opIDs[1], Kind: model.OpRemoveRuntime,
				ResourceID: model.ResourceID(fx.rtResID),
			}),
			Result: json.RawMessage(`{"code":"target_conflict"}`),
		},
		{
			TaskID: fx.run2ID, OperationID: fx.opIDs[2], Ordinal: 3,
			Status: model.OperationStatusPending,
			TargetRelativePath: "config/options.txt",
			Operation: mustJSON(t, model.PlannedOperation{
				ID: fx.opIDs[2], Kind: model.OpMaterialize,
				ResourceID: model.ResourceID(fx.projResID),
			}),
		},
	}, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// 逐操作状态沿单调路径推进：op2 → failed；op3 → verified（ADR-0004 §2）
	now := time.Now().UTC().Format(time.RFC3339)
	for _, step := range []struct{ op, status string }{
		{fx.opIDs[1], model.OperationStatusRunning},
		{fx.opIDs[1], model.OperationStatusFailed},
		{fx.opIDs[2], model.OperationStatusRunning},
		{fx.opIDs[2], model.OperationStatusApplied},
		{fx.opIDs[2], model.OperationStatusVerified},
	} {
		if err := journal.AdvanceStatus(ctx, fx.run2ID, step.op, step.status, now, nil); err != nil {
			t.Fatalf("推进 %s 至 %s: %v", step.op, step.status, err)
		}
	}

	// 运行头：run1 committed（较早）、run2 recovery_required（created_at 最新）
	runs := sqlite.NewApplyRunRepository(db)
	for _, run := range []model.ApplyRun{
		{
			TaskID: fx.run1ID, RelationID: rel.RelationID, PlanID: plan.PlanID,
			PlanDigest: "sha256:plan-seed-a", RelationRevision: 1,
			State: model.ApplyRunCommitted, OperationCount: 2, StagingCleared: true,
			CommitID: fx.commitIDs[0],
			CreatedAt: "2026-08-31T09:00:00Z", UpdatedAt: "2026-08-31T09:05:00Z",
		},
		{
			TaskID: fx.run2ID, RelationID: rel.RelationID, PlanID: plan.PlanID,
			PlanDigest: "sha256:plan-seed-b", RelationRevision: 2,
			State: model.ApplyRunRecoveryRequired, OperationCount: 3,
			CreatedAt: "2026-08-31T11:00:00Z", UpdatedAt: "2026-08-31T11:01:00Z",
		},
	} {
		if err := runs.Insert(ctx, run); err != nil {
			t.Fatalf("预置运行 %s: %v", run.TaskID, err)
		}
	}
	return app, db, fx
}

// pickT06Resource 取预置资源：偏好 ID 命中，否则字典序最小兜底。
func pickT06Resource(t *testing.T, snap model.ObservedSnapshot, preferred string) (string, model.Representation) {
	t.Helper()
	if obs, ok := snap.Resources[model.ResourceID(preferred)]; ok {
		return preferred, obs.Representation
	}
	keys := make([]string, 0, len(snap.Resources))
	for id := range snap.Resources {
		keys = append(keys, string(id))
	}
	sort.Strings(keys)
	for _, k := range keys {
		obs := snap.Resources[model.ResourceID(k)]
		if obs.Representation.RelativePath != "" {
			return k, obs.Representation
		}
	}
	t.Fatalf("快照无可表示资源: %d 个", len(snap.Resources))
	return "", model.Representation{}
}

// TestHeadlessT06ApplyRunProjection 验证运行头投影：created_at 最新一次运行、
// 字段逐项读回；关系无运行 → err.apply.no_run。
func TestHeadlessT06ApplyRunProjection(t *testing.T) {
	app, _, fx := seedT06(t)
	ctx := context.Background()

	// rel2 无任何运行 → err.apply.no_run（args {0}=relation_id）
	_, err := app.GetApplyRun(ctx, fx.rel2.RelationID)
	if code := errCode(t, err); code != "err.apply.no_run" {
		t.Fatalf("无运行错误码: %s", code)
	}
	// 关系不存在 → err.relation.not_found（读投影前置校验，GetChanges 同口径）
	if _, err := app.GetApplyRun(ctx, "rel_missing"); errCode(t, err) != "err.relation.not_found" {
		t.Fatal("relation.not_found")
	}

	run, err := app.GetApplyRun(ctx, fx.rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	// created_at 最新（run2 11:00 > run1 09:00），非 task_id 字典序
	if run.TaskID != fx.run2ID {
		t.Fatalf("应返回最新运行 %s, got %s", fx.run2ID, run.TaskID)
	}
	if run.SchemaVersion != model.CurrentSchemaVersion {
		t.Fatalf("schema_version = %d", run.SchemaVersion)
	}
	if run.RelationID != fx.rel.RelationID || run.PlanID != fx.plan.PlanID {
		t.Fatalf("运行头身份不一致: %+v", run)
	}
	if run.PlanDigest != "sha256:plan-seed-b" || run.State != model.ApplyRunRecoveryRequired {
		t.Fatalf("运行头状态不一致: %+v", run)
	}
	if run.OperationCount != 3 || run.StagingCleared {
		t.Fatalf("运行头计数不一致: %+v", run)
	}
	if run.AcknowledgedAt != "" || run.CommitID != "" {
		t.Fatalf("未收口运行不应有 acknowledged_at/commit_id: %+v", run)
	}
	if run.CreatedAt != "2026-08-31T11:00:00Z" || run.UpdatedAt != "2026-08-31T11:01:00Z" {
		t.Fatalf("运行头时间戳不一致: %+v", run)
	}
}

// TestHeadlessT06ApplyOperationsPaging 验证逐操作清单：ordinal 升序分页、
// operation_id cursor 跨页（GetChanges 同协议）、未知 cursor 宽容回首页。
func TestHeadlessT06ApplyOperationsPaging(t *testing.T) {
	app, _, fx := seedT06(t)
	ctx := context.Background()

	page1, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: fx.rel.RelationID, TaskID: fx.run2ID, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 2 || page1.SchemaVersion != model.CurrentSchemaVersion {
		t.Fatalf("第一页应为 2 行: %+v", page1)
	}
	for i, want := range []int{1, 2} {
		if page1.Items[i].Ordinal != want {
			t.Fatalf("ordinal 升序: 第 %d 行 ordinal = %d", i, page1.Items[i].Ordinal)
		}
	}
	if page1.NextCursor != fx.opIDs[1] {
		t.Fatalf("NextCursor 应为上一页末条 operation_id %s, got %q", fx.opIDs[1], page1.NextCursor)
	}

	page2, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: fx.rel.RelationID, TaskID: fx.run2ID, Cursor: page1.NextCursor, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.Items[0].OperationID != fx.opIDs[2] {
		t.Fatalf("第二页应为末条: %+v", page2.Items)
	}
	if page2.NextCursor != "" {
		t.Fatalf("末页不应有 NextCursor: %q", page2.NextCursor)
	}

	// 未知 cursor（陈旧/未知）→ 从首页开始（GetChanges 宽容口径）
	again, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: fx.rel.RelationID, TaskID: fx.run2ID, Cursor: "op_unknown", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != 2 || again.Items[0].OperationID != fx.opIDs[0] {
		t.Fatalf("未知 cursor 应回首页: %+v", again.Items)
	}
}

// TestHeadlessT06ApplyOperationsWhitelist 验证白名单投影（硬约束 4）：
// ResourceID/ChangeKind 取自 operation_json（计划操作身份）、ResultCode 取自
// result_json 顶层 code；序列化产物断言无 temp_relative_path/ownership_proof
// 键，且投毒值不泄漏。
func TestHeadlessT06ApplyOperationsWhitelist(t *testing.T) {
	app, _, fx := seedT06(t)
	ctx := context.Background()

	page, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{
		RelationID: fx.rel.RelationID, TaskID: fx.run2ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("应 3 行: %d", len(page.Items))
	}
	op1 := page.Items[0]
	if op1.OperationID != fx.opIDs[0] || op1.Status != model.OperationStatusPending {
		t.Fatalf("op1 投影不一致: %+v", op1)
	}
	if op1.ChangeKind != string(model.OpWriteRuntime) || op1.ResourceID != fx.projResID {
		t.Fatalf("op1 应携带计划操作身份: %+v", op1)
	}
	if op1.RelativePath != "mods/local.pw.toml" {
		t.Fatalf("relative_path 应为 root-relative 目标: %+v", op1)
	}
	op2 := page.Items[1]
	if op2.Status != model.OperationStatusFailed || op2.ResultCode != "target_conflict" {
		t.Fatalf("op2 应带终局摘要码: %+v", op2)
	}
	op3 := page.Items[2]
	if op3.Status != model.OperationStatusVerified || op3.ResultCode != "" {
		t.Fatalf("verified 操作摘要码应为空: %+v", op3)
	}

	// 白名单序列化断言：键集合 ⊆ 白名单，投毒值不出现
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	for _, forbidden := range []string{"temp_relative_path", "ownership_proof", "poison", "before_digest", "after_digest", "recovery_ref"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("序列化产物泄漏 %q: %s", forbidden, serialized)
		}
	}
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) == 0 {
		t.Fatal("反序列化得到空 items")
	}
	allowed := map[string]bool{
		"operation_id": true, "ordinal": true, "status": true,
		"resource_id": true, "relative_path": true, "change_kind": true, "result_code": true,
	}
	for i, item := range doc.Items {
		for k := range item {
			if !allowed[k] {
				t.Fatalf("第 %d 行出现白名单外键 %q", i, k)
			}
		}
	}
}

// TestHeadlessT06ApplyOperationsErrors 验证 err.apply.run_not_found（task 不存在
// 或跨关系）与关系不存在前置校验。
func TestHeadlessT06ApplyOperationsErrors(t *testing.T) {
	app, _, fx := seedT06(t)
	ctx := context.Background()

	// task 不存在
	_, err := app.ListApplyOperations(ctx, view.ListApplyOperationsInput{RelationID: fx.rel.RelationID, TaskID: "task_missing"})
	if code := errCode(t, err); code != "err.apply.run_not_found" {
		t.Fatalf("task 不存在错误码: %s", code)
	}
	// 跨关系：run2 属 rel1，用 rel2 查询
	_, err = app.ListApplyOperations(ctx, view.ListApplyOperationsInput{RelationID: fx.rel2.RelationID, TaskID: fx.run2ID})
	if code := errCode(t, err); code != "err.apply.run_not_found" {
		t.Fatalf("跨关系错误码: %s", code)
	}
	// 关系不存在 → err.relation.not_found
	_, err = app.ListApplyOperations(ctx, view.ListApplyOperationsInput{RelationID: "rel_missing", TaskID: fx.run2ID})
	if code := errCode(t, err); code != "err.relation.not_found" {
		t.Fatalf("关系不存在错误码: %s", code)
	}
}

// TestHeadlessT06CommitHistoryPaging 验证历史列表：created_at DESC 分页、
// commit_id cursor 跨页、同刻提交 id DESC 决胜、空关系 items 归一 []。
func TestHeadlessT06CommitHistoryPaging(t *testing.T) {
	app, _, fx := seedT06(t)
	ctx := context.Background()

	page1, err := app.ListCommits(ctx, fx.rel.RelationID, pageReqT06("", 2))
	if err != nil {
		t.Fatal(err)
	}
	// DESC + 同刻决胜：c4 与 c3 同 created_at，c4 后插入（id 更大）在前
	want := []string{fx.commitIDs[3], fx.commitIDs[2]}
	if len(page1.Items) != 2 {
		t.Fatalf("第一页应 2 行: %+v", page1.Items)
	}
	for i, id := range want {
		if page1.Items[i].CommitID != id {
			t.Fatalf("第 %d 行 = %s, 期望 %s（created_at DESC, 同刻 id DESC）", i, page1.Items[i].CommitID, id)
		}
	}
	if page1.NextCursor != fx.commitIDs[2] {
		t.Fatalf("NextCursor = %q, 期望 %s", page1.NextCursor, fx.commitIDs[2])
	}
	if page1.Items[0].Kind != "sync" || page1.Items[0].Completeness != "exact" {
		t.Fatalf("列表行字段不一致: %+v", page1.Items[0])
	}
	if page1.Items[0].RemainingChangeCnt != 0 || page1.Items[0].CreatedAt != "2026-08-31T10:02:00Z" {
		t.Fatalf("列表行摘要不一致: %+v", page1.Items[0])
	}

	page2, err := app.ListCommits(ctx, fx.rel.RelationID, pageReqT06(page1.NextCursor, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 2 || page2.Items[0].CommitID != fx.commitIDs[1] || page2.Items[1].CommitID != fx.commitIDs[0] {
		t.Fatalf("第二页应为 [c2 c1]: %+v", page2.Items)
	}
	if page2.NextCursor != "" {
		t.Fatalf("末页不应有 NextCursor: %q", page2.NextCursor)
	}

	// 空关系：items 归一 []（序列化非 null）
	empty, err := app.ListCommits(ctx, fx.rel2.RelationID, pageReqT06("", 10))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("空关系 items 应归一 []: %#v", empty.Items)
	}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Fatalf("空页应序列化 items:[]: %s", raw)
	}
	// 关系不存在 → err.relation.not_found
	if _, err := app.ListCommits(ctx, "rel_missing", pageReqT06("", 10)); errCode(t, err) != "err.relation.not_found" {
		t.Fatal("relation.not_found")
	}
}

// TestHeadlessT06CommitDetail 验证提交详情：changes 全量、表示摘要格式、
// nil 表示 ↔ omitempty null、零变化 changes 归一 []、err.commit.not_found。
func TestHeadlessT06CommitDetail(t *testing.T) {
	app, _, fx := seedT06(t)
	ctx := context.Background()

	detail, err := app.GetCommit(ctx, fx.rel.RelationID, fx.commitIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if detail.SchemaVersion != model.CurrentSchemaVersion {
		t.Fatalf("schema_version = %d", detail.SchemaVersion)
	}
	if detail.Summary.CommitID != fx.commitIDs[1] || detail.Summary.Kind != "sync" ||
		detail.Summary.Completeness != "exact" || detail.Summary.RemainingChangeCnt != 2 {
		t.Fatalf("摘要不一致: %+v", detail.Summary)
	}
	if detail.PlanID != fx.plan.PlanID {
		t.Fatalf("plan_id = %q, 期望 %s", detail.PlanID, fx.plan.PlanID)
	}
	if len(detail.Changes) != 2 {
		t.Fatalf("changes 应全量 2 行: %d", len(detail.Changes))
	}

	// 表示摘要："<relative_path> <format>[ <algorithm>:<digest>]"（Content 缺省
	// 时无第三段，如项目侧 packwiz 元文件）；nil 侧 → 键缺省。
	wantProjSummary := fx.projRep.RelativePath + " " + fx.projRep.Format
	if fx.projRep.Content != nil {
		wantProjSummary += " " + fx.projRep.Content.Algorithm + ":" + fx.projRep.Content.Digest
	}
	// 运行时 jar 的摘要独立复核：fixture 字节是确定的
	rtDigest := sha256.Sum256([]byte("fake runtimeonly bytes"))
	wantRtSummary := "mods/runtimeonly-1.0.jar jar sha256:" + hex.EncodeToString(rtDigest[:])
	byResource := map[string]view.CommitChangeView{}
	for _, ch := range detail.Changes {
		byResource[ch.ResourceID] = ch
	}
	proj := byResource[fx.projResID]
	if proj.ChangeKind != "modify" ||
		proj.ProjectBefore == nil || *proj.ProjectBefore != wantProjSummary ||
		proj.ProjectAfter == nil || *proj.ProjectAfter != wantProjSummary ||
		proj.RuntimeBefore != nil || proj.RuntimeAfter != nil {
		t.Fatalf("项目侧变更行不一致: %+v\n期望摘要 %q", proj, wantProjSummary)
	}
	rt := byResource[fx.rtResID]
	if rt.ChangeKind != "create" || rt.RuntimeAfter == nil || *rt.RuntimeAfter != wantRtSummary ||
		rt.ProjectBefore != nil || rt.ProjectAfter != nil || rt.RuntimeBefore != nil {
		t.Fatalf("运行时侧变更行不一致: %+v\n期望摘要 %q", rt, wantRtSummary)
	}

	// nil ↔ omitempty null：序列化产物中缺省侧无键
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Changes []map[string]any `json:"changes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Changes) != 2 {
		t.Fatalf("反序列化 changes: %d", len(doc.Changes))
	}
	for _, ch := range doc.Changes {
		if ch["resource_id"] == fx.projResID {
			if _, ok := ch["runtime_before"]; ok {
				t.Fatalf("nil 侧应缺省（无键）: %v", ch)
			}
			if ch["project_before"] != wantProjSummary {
				t.Fatalf("表示摘要不一致: %v", ch["project_before"])
			}
		}
	}

	// 零变化提交：changes 归一 []（序列化非 null）
	emptyDetail, err := app.GetCommit(ctx, fx.rel.RelationID, fx.commitIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if emptyDetail.Changes == nil || len(emptyDetail.Changes) != 0 {
		t.Fatalf("零变化 changes 应归一 []: %#v", emptyDetail.Changes)
	}
	emptyRaw, err := json.Marshal(emptyDetail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(emptyRaw), `"changes":[]`) {
		t.Fatalf("零变化应序列化 changes:[]: %s", emptyRaw)
	}

	// 错误口径：不存在 / 跨关系 → err.commit.not_found；关系不存在 → err.relation.not_found
	if _, err := app.GetCommit(ctx, fx.rel.RelationID, "commit_none"); errCode(t, err) != "err.commit.not_found" {
		t.Fatal("commit.not_found（不存在）")
	}
	if _, err := app.GetCommit(ctx, fx.rel2.RelationID, fx.commitIDs[1]); errCode(t, err) != "err.commit.not_found" {
		t.Fatal("commit.not_found（跨关系）")
	}
	if _, err := app.GetCommit(ctx, "rel_missing", fx.commitIDs[1]); errCode(t, err) != "err.relation.not_found" {
		t.Fatal("relation.not_found")
	}
}

// pageReqT06 构造分页请求（测试简写）。
func pageReqT06(cursor string, limit int) ports.PageRequest {
	return ports.PageRequest{Cursor: cursor, Limit: limit}
}
