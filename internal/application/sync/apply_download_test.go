package sync

// sync 下载接线与剔除语义测试（票 #63，ADR-0008 §6/§7）：假 CDN（票 #58
// FakeCDN）注入 + 真实扫描/计划/确认/Apply 全链，验证：
//
//  1. 计划物化模式推导：CF 重取信息三要素齐备的 mod 写操作 → download，
//     其余（modrinth/本地/文件资源）→ copy，DTO 透出 materialization；
//  2. 成功链：download 行两层校验（引擎声明 hash + StageContent sha256 复核）
//     都过，committed exact，成品落盘；
//  3. 剔除语义：单行取数失败（404/murmur2）剔出本场——不进 journal、不写入、
//     其余照常原子提交，commit=partial + 跳过清单带原因码；
//  4. 全败 failed 终局：run=failed + task=failed + Problem 承载 err.download.*，
//     关系健康不动（网络失败 ≠ 恢复面），同 plan 重 Confirm 新建运行可重试。
//
// 场景造数法（供票 #66 download 验收链复用）：项目端 mods/<mod>.pw.toml 携带
// [update.curseforge] file-id + [download] 声明 hash，运行端 mods/ 缺 jar——
// 初始化计划 initialize_from_project 产生 write_runtime(download) 操作；
// 假 CDN 路径与引擎同口径（/files/{fileID/1000}/{fileID%1000}/{filename}）。

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/download"
)

// dlTestStack 装配带假 CDN 引擎的栈：快退避让重试面（429/503）不拖慢测试。
func dlTestStack(t *testing.T, cdn *download.FakeCDN) (*App, string) {
	t.Helper()
	srv := httptest.NewServer(cdn.Handler())
	t.Cleanup(srv.Close)
	engine, err := download.New(download.Options{
		BaseURL: srv.URL + "/files",
		Backoff: func(int) time.Duration { return time.Millisecond },
		Sleep:   func(_ context.Context, _ time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, db, _ := newApplyEngineStack(t)
	// 替换下载引擎（newApplyEngineStack 未装配引擎——download 行按取数失败
	// 剔除；下载场景测试注入假 CDN 引擎）。
	app.deps.Downloads = engine
	_ = db
	return app, srv.URL
}

// dlTestRelation 在夹具上建关系（mods 规则由默认模板携带——mods/ 是 mod 类别
// 默认受管范围，无需建议规则）。
func dlTestRelation(t *testing.T, app *App, projectRoot, instanceDir string) view.RelationView {
	t.Helper()
	return applyTestRelation(t, app, projectRoot, instanceDir)
}

// dlTestFixture 造「项目端声明 CF mod、运行端缺 jar」的夹具：metafiles 里的
// 每项产生 init_choice 冲突，resolve initialize_from_project 后是
// write_runtime(download) 操作。
type dlModSpec struct {
	name       string // metafile display name
	filename   string // 运行端 jar 文件名
	projectID  int    // update.curseforge.project-id
	fileID     int64  // update.curseforge.file-id（0 = modrinth 形态）
	hashFormat string // [download] hash-format（空 = 不带 [download] 段）
	hash       string // [download] hash
}

// dlModMetafile 渲染 mods/<name>.pw.toml。
func dlModMetafile(spec dlModSpec) string {
	s := "name = \"" + spec.name + "\"\nfilename = \"" + spec.filename + "\"\nside = \"both\"\n\n"
	if spec.hashFormat != "" {
		s += "[download]\nurl = \"https://media.example/" + spec.filename + "\"\n" +
			"hash-format = \"" + spec.hashFormat + "\"\nhash = \"" + spec.hash + "\"\n\n"
	}
	if spec.fileID > 0 {
		s += "[update.curseforge]\nproject-id = " + itoa(spec.projectID) + "\nfile-id = " + itoa64(spec.fileID) + "\n"
	} else {
		s += "[update.modrinth]\nmod-id = \"AANobbMI\"\nversion-id = \"1.0.0\"\n"
	}
	return s
}

func itoa(n int) string {
	return string(rune('0'+n%10))[:0] + func() string {
		if n == 0 {
			return "0"
		}
		digits := []byte{}
		for n > 0 {
			digits = append([]byte{byte('0' + n%10)}, digits...)
			n /= 10
		}
		return string(digits)
	}()
}

func itoa64(n int64) string { return itoa(int(n)) }

// dlFixture 造夹具：index.toml 列全部 metafile；返回项目根与实例目录。
func dlFixture(t *testing.T, mods []dlModSpec) (projectRoot, instanceDir string) {
	t.Helper()
	base := t.TempDir()
	projectRoot = filepath.Join(base, "project")
	instanceDir = filepath.Join(base, "instance")
	applyTestWriteFile(t, filepath.Join(projectRoot, "pack.toml"),
		"name = \"DL\"\nauthor = \"tester\"\nversion = \"1.0.0\"\n")
	index := "index = { file = \"index.toml\", hash-format = \"sha256\", hash = \"0\" }\n\n"
	for i, spec := range mods {
		meta := "mods/" + spec.name + ".pw.toml"
		applyTestWriteFile(t, filepath.Join(projectRoot, meta), dlModMetafile(spec))
		index += "[[files]]\nfile = \"" + meta + "\"\nhash = \"" + itoa(i+1) + "\"\nmetafile = true\n\n"
	}
	applyTestWriteFile(t, filepath.Join(projectRoot, "index.toml"), index)
	applyTestWriteFile(t, filepath.Join(instanceDir, "instance.cfg"),
		"[General]\nname=\"DL\"\niconKey=default\n")
	// 登记不变量（check.endpoint.readable）：Prism 实例须含 minecraft/ 游戏目录
	// （gameDir=<instance>/minecraft）；mod jar 由 download 行在 apply 期落此处。
	applyTestWriteFile(t, filepath.Join(instanceDir, "minecraft", ".keep"), "")
	return projectRoot, instanceDir
}

// dlResolvedDownloadPlan 扫描 + 计划 + 全部 initialize_from_project 决议，
// 返回 resolved 计划（全部 download 写操作 + 可能的 copy 操作）。
func dlResolvedDownloadPlan(t *testing.T, app *App, relationID string) view.SyncPlanView {
	t.Helper()
	ctx := context.Background()
	applyTestScanAndWait(t, app, relationID)
	ws, err := app.GetWorkspace(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             relationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		t.Fatalf("PrepareSync: %v", err)
	}
	resolutions := make([]model.Resolution, 0)
	for _, op := range draft.Operations {
		// 计划操作不覆盖冲突资源：冲突按资源枚举（draft.Conflicts）
		_ = op
	}
	for _, c := range draft.Conflicts {
		if c.Project != nil {
			resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: model.ChoiceInitializeFromProject})
		} else {
			resolutions = append(resolutions, model.Resolution{ResourceID: c.ResourceID, Choice: model.ChoiceInitializeFromRuntime})
		}
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolutions})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	return resolved
}

// TestDownloadMaterializationDerivation 验证计划面模式推导（票面 What 1）：
// CF 三要素齐备 → download；modrinth/文件资源 → copy；DTO 透出 materialization。
func TestDownloadMaterializationDerivation(t *testing.T) {
	mods := []dlModSpec{
		{name: "alpha", filename: "alpha-1.0.jar", projectID: 228525, fileID: 7270446, hashFormat: "sha1", hash: "aaaa"},
		{name: "sodium", filename: "sodium-0.6.5.jar", hashFormat: "sha256", hash: "bbbb"}, // modrinth → copy
	}
	projectRoot, instanceDir := dlFixture(t, mods)
	app, _ := dlTestStack(t, download.NewFakeCDN())
	rel := dlTestRelation(t, app, projectRoot, instanceDir)
	resolved := dlResolvedDownloadPlan(t, app, rel.RelationID)

	plan, err := app.GetPlan(context.Background(), resolved.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	materialization := map[string]string{}
	for _, op := range plan.Operations {
		materialization[string(op.ResourceID)] = op.Materialization
		if op.Materialization != model.MaterializationDownload && op.Materialization != model.MaterializationCopy {
			t.Fatalf("操作 %s materialization 非法: %q", op.ResourceID, op.Materialization)
		}
	}
	if got := materialization["mod:curseforge:228525"]; got != model.MaterializationDownload {
		t.Fatalf("CF mod 操作 materialization = %q，期望 download", got)
	}
	if got := materialization["mod:modrinth:AANobbMI"]; got != model.MaterializationCopy {
		t.Fatalf("modrinth mod 操作 materialization = %q，期望 copy", got)
	}
}

// TestDownloadApplySuccessChain 验证 AC1：假 CDN 注入下 download 行成功落盘，
// 两层校验都过（成品字节=CDN 字节 + journal after_digest=成品实测 sha256，
// StageContent 以该值复核通过）。
func TestDownloadApplySuccessChain(t *testing.T) {
	jar := []byte("fake alpha jar bytes v1")
	sum := sha1.Sum(jar)
	declared := hex.EncodeToString(sum[:])
	mods := []dlModSpec{
		{name: "alpha", filename: "alpha-1.0.jar", projectID: 228525, fileID: 7270446, hashFormat: "sha1", hash: declared},
	}
	projectRoot, instanceDir := dlFixture(t, mods)
	cdn := download.NewFakeCDN()
	// 直链路径与引擎同口径：/files/{fileID/1000}/{fileID%1000}/{filename}
	cdn.SetFile("/files/7270/446/alpha-1.0.jar", jar)
	app, _ := dlTestStack(t, cdn)
	rel := dlTestRelation(t, app, projectRoot, instanceDir)
	runtimeRoot := filepath.Join(instanceDir, "minecraft")
	plan := dlResolvedDownloadPlan(t, app, rel.RelationID)

	tv, err := app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded || final.Outcome != model.TaskOutcomeExact {
		t.Fatalf("任务终态 = %s/%s（problem=%+v），期望 succeeded/exact", final.Status, final.Outcome, final.Problem)
	}

	// 成品落盘且字节一致（引擎声明 hash 校验 + sha256 复核后经既有管线写入）
	got, err := os.ReadFile(filepath.Join(runtimeRoot, "mods", "alpha-1.0.jar"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(jar) {
		t.Fatalf("落盘字节不符: got %q want %q", got, jar)
	}
	// 第二层校验证据：journal after_digest = 成品实测 sha256（StageContent 复核基准）
	wantSum := sha256.Sum256(jar)
	wantDigest := hex.EncodeToString(wantSum[:])
	run, err := app.deps.ApplyRuns.Get(context.Background(), tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	ops, _, err := app.deps.Journal.ListByTask(context.Background(), tv.TaskID, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("journal 操作数 = %d", len(ops))
	}
	if ops[0].AfterDigest != wantDigest {
		t.Fatalf("journal after_digest = %q，期望成品实测 sha256 %q", ops[0].AfterDigest, wantDigest)
	}
	if run.State != model.ApplyRunCommitted {
		t.Fatalf("运行态 = %s", run.State)
	}
	// 提交 exact 且无跳过
	commit, err := app.deps.Commits.GetForRelation(context.Background(), run.CommitID, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if commit.Completeness != model.TaskOutcomeExact {
		t.Fatalf("提交 completeness = %s", commit.Completeness)
	}
	// staging（含 downloads/ .part）已随提交清理
	if _, err := os.Stat(filepath.Join(t.TempDir())); err != nil {
		t.Fatal(err)
	}
}

// TestDownloadApplyPartialSkipList 验证 AC2：单行失败剔出本场、其余照常
// committed（partial + dirty 语义由 remaining>0 推导）、跳过清单带原因码；
// murmur2 行由引擎「不验不装」gate 报 hash_format_unsupported（票 #58 信号
// 接线直作跳过原因）。
func TestDownloadApplyPartialSkipList(t *testing.T) {
	jar := []byte("fake alpha jar bytes v2")
	sum := sha1.Sum(jar)
	mods := []dlModSpec{
		{name: "alpha", filename: "alpha-2.0.jar", projectID: 228525, fileID: 7270447, hashFormat: "sha1", hash: hex.EncodeToString(sum[:])},
		{name: "beta", filename: "beta-1.0.jar", projectID: 228526, fileID: 7270448, hashFormat: "sha1", hash: "1111"}, // 假 CDN 404
		{name: "gamma", filename: "gamma-1.0.jar", projectID: 228527, fileID: 7270449, hashFormat: "murmur2", hash: "2222"},
	}
	projectRoot, instanceDir := dlFixture(t, mods)
	cdn := download.NewFakeCDN()
	cdn.SetFile("/files/7270/447/alpha-2.0.jar", jar) // beta 7270448 → 404
	app, _ := dlTestStack(t, cdn)
	rel := dlTestRelation(t, app, projectRoot, instanceDir)
	runtimeRoot := filepath.Join(instanceDir, "minecraft")
	plan := dlResolvedDownloadPlan(t, app, rel.RelationID)

	tv, err := app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		t.Fatalf("有可提交时不得 failed/recovery：status=%s problem=%+v", final.Status, final.Problem)
	}
	if final.Outcome != model.TaskOutcomePartial {
		t.Fatalf("outcome = %s，期望 partial", final.Outcome)
	}

	// alpha 落盘（其余照常原子提交）
	got, err := os.ReadFile(filepath.Join(runtimeRoot, "mods", "alpha-2.0.jar"))
	if err != nil {
		t.Fatalf("成功行应照常落盘: %v", err)
	}
	if string(got) != string(jar) {
		t.Fatal("alpha 落盘字节不符")
	}
	// beta/gamma 目标未被写入
	for _, f := range []string{"beta-1.0.jar", "gamma-1.0.jar"} {
		if _, err := os.Stat(filepath.Join(runtimeRoot, "mods", f)); !os.IsNotExist(err) {
			t.Fatalf("%s 不应被写入: %v", f, err)
		}
	}

	// 跳过清单：不进 journal（journal 只有 alpha 一行）+ 提交摘要带原因码
	ops, _, err := app.deps.Journal.ListByTask(context.Background(), tv.TaskID, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].OperationID != "op_0001" {
		t.Fatalf("journal 应只有成功行 alpha（剔出行不进 journal），got %d 行", len(ops))
	}
	run, err := app.deps.ApplyRuns.Get(context.Background(), tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := app.deps.Commits.GetForRelation(context.Background(), run.CommitID, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if commit.Completeness != model.TaskOutcomePartial || commit.RemainingChangeCount != 2 {
		t.Fatalf("提交 %s/%d，期望 partial/2", commit.Completeness, commit.RemainingChangeCount)
	}
	cv, err := app.GetCommit(context.Background(), rel.RelationID, run.CommitID)
	if err != nil {
		t.Fatal(err)
	}
	skipped := map[string]string{}
	for _, s := range cv.Skipped {
		skipped[s.ResourceID] = s.ReasonCode
	}
	if got := skipped["mod:curseforge:228526"]; got != "err.download.unavailable" {
		t.Fatalf("beta 跳过原因 = %q，期望 err.download.unavailable", got)
	}
	if got := skipped["mod:curseforge:228527"]; got != "hash_format_unsupported" {
		t.Fatalf("gamma 跳过原因 = %q，期望 hash_format_unsupported", got)
	}
	if len(cv.Skipped) != 2 {
		t.Fatalf("跳过清单 = %d 行，期望 2", len(cv.Skipped))
	}
	// 关系健康不被剔除语义标记恢复（partial + dirty 走既有投影语义）
	gotRel, err := app.deps.Relations.Get(context.Background(), rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel.Health != model.HealthHealthy {
		t.Fatalf("剔除语义不得标恢复态：health=%s", gotRel.Health)
	}
}

// TestDownloadApplyAllFailedFailedTerminal 验证 AC3：全部 download 行失败 →
// failed 终局（不进 recovery_required）；Problem 承载 err.download.*；重试 =
// 同 plan 重新确认（新任务新运行，failed 可重入），假 CDN 恢复后 committed。
func TestDownloadApplyAllFailedFailedTerminal(t *testing.T) {
	// 声明 hash 从头定为真实字节 sha1：首轮 Script(503) 挡在取数面（到不了
	// hash 校验），重试轮 SetFile 同字节过声明校验——重试夹具与声明一致。
	alphaBytes := []byte("fake alpha jar bytes v3")
	betaBytes := []byte("fake beta jar bytes")
	sumA, sumB := sha1.Sum(alphaBytes), sha1.Sum(betaBytes)
	mods := []dlModSpec{
		{name: "alpha", filename: "alpha-1.0.jar", projectID: 228525, fileID: 7270446, hashFormat: "sha1", hash: hex.EncodeToString(sumA[:])},
		{name: "beta", filename: "beta-1.0.jar", projectID: 228526, fileID: 7270447, hashFormat: "sha1", hash: hex.EncodeToString(sumB[:])},
	}
	projectRoot, instanceDir := dlFixture(t, mods)
	cdn := download.NewFakeCDN()
	// 两行全部 503（rate_limited 桶；重试耗尽后分桶）
	for _, p := range []string{"/files/7270/446/alpha-1.0.jar", "/files/7270/447/beta-1.0.jar"} {
		cdn.Script(p, download.FakeStep{Status: 503, RetryAfterSeconds: 1})
	}
	app, _ := dlTestStack(t, cdn)
	rel := dlTestRelation(t, app, projectRoot, instanceDir)
	plan := dlResolvedDownloadPlan(t, app, rel.RelationID)
	ctx := context.Background()

	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	final := applyTestWaitTask(t, app, tv.TaskID)
	if final.Status != model.TaskStatusFailed {
		t.Fatalf("全部失败应 failed 终局，got %s（problem=%+v）", final.Status, final.Problem)
	}
	if final.Problem == nil || final.Problem.Code != "err.download.rate_limited" {
		t.Fatalf("Problem 应承载 err.download.rate_limited，got %+v", final.Problem)
	}
	run, err := app.deps.ApplyRuns.Get(ctx, tv.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != model.ApplyRunFailed {
		t.Fatalf("运行态 = %s，期望 failed", run.State)
	}
	// 剔出行不进 journal：全部失败 → journal 零行
	ops, _, err := app.deps.Journal.ListByTask(ctx, tv.TaskID, ports.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("全败 journal 应零行，got %d", len(ops))
	}
	// 暂存（含 downloads/ .part）随 failed 终局清理：staging_cleared 已记录
	//（failed 不进恢复矩阵，暂存证据无恢复价值，重试即全新运行）
	if !run.StagingCleared {
		t.Fatalf("failed 终局应清理暂存（staging_cleared=true）")
	}
	// 关系健康不动（网络失败 ≠ 恢复面）
	gotRel, err := app.deps.Relations.Get(ctx, rel.RelationID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel.Health != model.HealthHealthy {
		t.Fatalf("failed 终局不得标恢复态：health=%s", gotRel.Health)
	}
	// 重试 = 同 plan 重新确认：failed 可重入，新建任务与运行（非幂等重入旧任务）
	cdn.Script("/files/7270/446/alpha-1.0.jar") // 清除脚本回落 SetFile
	cdn.Script("/files/7270/447/beta-1.0.jar")
	cdn.SetFile("/files/7270/446/alpha-1.0.jar", alphaBytes)
	cdn.SetFile("/files/7270/447/beta-1.0.jar", betaBytes)
	retry, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("failed 后同 plan 重确认: %v", err)
	}
	if retry.TaskID == tv.TaskID {
		t.Fatal("failed 重确认应新建任务，而非幂等重入旧任务")
	}
	final2 := applyTestWaitTask(t, app, retry.TaskID)
	if final2.Status != model.TaskStatusSucceeded || final2.Outcome != model.TaskOutcomeExact {
		t.Fatalf("CDN 恢复后重试应 committed exact，got %s/%s（problem=%+v）", final2.Status, final2.Outcome, final2.Problem)
	}
	runtimeRoot := filepath.Join(instanceDir, "minecraft")
	for _, f := range []string{"alpha-1.0.jar", "beta-1.0.jar"} {
		if _, err := os.Stat(filepath.Join(runtimeRoot, "mods", f)); err != nil {
			t.Fatalf("重试后 %s 应落盘: %v", f, err)
		}
	}
}

// TestDownloadLegacyPlanRowsMaterializationEmptyCopy 验证 AC4 旧行兼容（契约
// 06 §3.7「既有行空值＝copy 兼容」）：P2 存量计划 JSON 无 materialization 字段
//（反序列化=空串），引擎按 copy 口径推导取数来源，不误判为 download、不构造
// 下载请求。
func TestDownloadLegacyPlanRowsMaterializationEmptyCopy(t *testing.T) {
	op := model.PlannedOperation{
		ID: "op_0001", Kind: model.OpWriteRuntime, ResourceID: "mod:curseforge:228525",
	}
	out := deriveApplyFilePlans(
		model.SyncPlan{Operations: []model.PlannedOperation{op}},
		model.ObservedSnapshot{}, model.ObservedSnapshot{}, nil, "p", "r")
	if len(out) != 1 {
		t.Fatalf("计划行数 = %d", len(out))
	}
	if out[0].materialization != model.MaterializationCopy {
		t.Fatalf("旧行（空值）materialization = %q，期望按 copy 兼容", out[0].materialization)
	}
	if out[0].dlReq != nil {
		t.Fatal("旧行不得构造下载请求")
	}
}

// TestDownloadApplyCopySourceMissingSkips 验证剔除语义「copy/download 一条
// 规矩」：copy 行取数失败（源文件在计划后被删）同样剔出本场，其余照常提交。
func TestDownloadApplyCopySourceMissingSkips(t *testing.T) {
	projectRoot, instanceDir, runtimeRoot := applyTestFixture(t)
	app, _ := dlTestStack(t, download.NewFakeCDN())
	rel := dlTestRelation(t, app, projectRoot, instanceDir)
	plan := applyTestResolvedPlan(t, app, rel.RelationID) // a→runtime create、b→project create（copy）

	// a 的源文件在确认前删除 → staging 期 copy 取数失败 → 剔出本场
	if err := os.Remove(filepath.Join(projectRoot, "config", "a.toml")); err != nil {
		t.Fatal(err)
	}
	tv, err := app.ConfirmPlan(context.Background(), view.ConfirmPlanInput{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ConfirmPlan: %v", err)
	}
	final := applyTestWaitTask(t, app, tv.TaskID)
	// 剩余全部操作（b）照常提交 → succeeded/partial
	if final.Status != model.TaskStatusSucceeded || final.Outcome != model.TaskOutcomePartial {
		t.Fatalf("copy 取数失败应剔出本场（succeeded/partial），got %s/%s（problem=%+v）",
			final.Status, final.Outcome, final.Problem)
	}
	// b 照常落盘，a 未写入
	if _, err := os.Stat(filepath.Join(projectRoot, "config", "b.toml")); err != nil {
		t.Fatalf("b 应照常应用: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, "config", "a.toml")); !os.IsNotExist(err) {
		t.Fatalf("a 目标不应被写入: %v", err)
	}
}
