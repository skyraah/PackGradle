package sync

// content_ingest_test.go 覆盖提交收口期对象摄取通道（票 #88，ADR-0012 §2；
// 覆盖面自票 #93 起泛化到项目侧全部带内容指纹的表示——规格偏差记录见
// content_ingest.go 文件头）：
//   - 摘要命中 → Put 入 CAS + 引用行（purpose=baseline_content）；
//   - 跨提交内容寻址去重：对象已实存跳过 Put；
//   - 读取摘要与快照不符（外部写者竞态）/文件缺失 → 表示退无 Content + 诊断
//     + 不失败提交；
//   - CAS.Put 失败 → Content 保留 + 诊断，不产引用行（引用完整性前提）；
//   - 覆盖面＝项目侧全部表示（mod metafile + 非 mod 文本）；runtime 侧表示
//     不摄取（jar 字节不入库，ADR-0005 §7 红线不动）。
// 另覆盖 deriveApplyFilePlans 的防误源修复：mod 写运行端行的 after digest 恒
// 取声明 hash，项目侧 metafile 的实测 Content 绝不作写盘内容源。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/adapters/filesystem"
	"packgradle/internal/adapters/packwiz"
	"packgradle/internal/adapters/prism"
	"packgradle/internal/core/ids"
	"packgradle/internal/core/model"
	"packgradle/internal/store"
	"packgradle/internal/store/objectstore"
	"packgradle/internal/store/sqlite"
)

func newIngestStack(t *testing.T) (*App, *objectstore.CAS) {
	t.Helper()
	base := t.TempDir()
	dataRoot := filepath.Join(base, "userdata")
	layout, err := store.EnsureLayout(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(layout.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(context.Background(), db, dataRoot); err != nil {
		t.Fatal(err)
	}
	cas, err := objectstore.Open(layout.ObjectsDir, db)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(AppDeps{
		Endpoints:     sqlite.NewEndpointRepository(db),
		Relations:     sqlite.NewRelationRepository(db),
		Snapshots:     sqlite.NewSnapshotRepository(db),
		Baselines:     sqlite.NewBaselineRepository(db),
		Plans:         sqlite.NewPlanRepository(db),
		Tasks:         sqlite.NewTaskRepository(db),
		Mappings:      sqlite.NewMappingRepository(db),
		Preparations:  sqlite.NewPreparationRepository(db),
		HashCache:     sqlite.NewHashCacheRepository(db),
		Events:        sqlite.NewEventRepository(db),
		ApplyRuns:     sqlite.NewApplyRunRepository(db),
		Journal:       sqlite.NewOperationJournalRepository(db),
		Commits:       sqlite.NewCommitRepository(db),
		CAS:           cas,
		StagingRoot:   layout.StagingDir,
		GC:            sqlite.NewGCRepository(db),
		GCTrash:       cas,
		Tx:            sqlite.NewUnitOfWork(db),
		ProjectScan:   packwiz.New(),
		RuntimeScan:   prism.New(),
		Hasher:        filesystem.NewHasher(),
		Fingerprinter: filesystem.NewFingerprinter(),
		EndpointPaths: filesystem.PathNormalizer{},
		IDs:           ids.New,
		Now:           time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, cas
}

func ingestSha256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func ingestBaseline(rep *model.Representation) *model.SyncBaseline {
	return &model.SyncBaseline{
		SchemaVersion: model.CurrentSchemaVersion,
		BaselineID:    "base_test",
		RelationID:    "rel_test",
		Resources: map[model.ResourceID]model.BaselineResource{
			"mod:path:mods/test.pw.toml": {
				State:                 "present",
				LogicalDigest:         "sha256:logical",
				ProjectRepresentation: rep,
				Recoverability:        model.RecoverabilityRedownload,
			},
		},
	}
}

func TestIngestBaselineProjectContent(t *testing.T) {
	app, cas := newIngestStack(t)
	ctx := context.Background()
	projectRoot := t.TempDir()
	metaRel := filepath.Join("mods", "test.pw.toml")
	if err := os.MkdirAll(filepath.Join(projectRoot, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}

	// ---- 命中：摘要与工作树一致 → Put 入 CAS + baseline_content 引用行 ----
	if err := os.WriteFile(filepath.Join(projectRoot, metaRel), []byte("meta v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := &model.Representation{
		RelativePath: "mods/test.pw.toml", Format: "packwiz-mod-toml",
		Content: &model.ContentRef{Algorithm: "sha256", Digest: ingestSha256("meta v1"), Size: 7},
	}
	refs, diags := app.ingestBaselineProjectContent(ctx, projectRoot, ingestBaseline(rep))
	if len(diags) != 0 {
		t.Fatalf("命中路径不应有诊断: %+v", diags)
	}
	if len(refs) != 1 || refs[0].Digest != ingestSha256("meta v1") ||
		refs[0].Purpose != objectRefPurposeBaselineContent || refs[0].Size != 7 {
		t.Fatalf("引用行形状: %+v", refs)
	}
	if ok, err := cas.Has(ctx, ingestSha256("meta v1")); err != nil || !ok {
		t.Fatalf("对象应已入 CAS: ok=%v err=%v", ok, err)
	}
	if rep.Content == nil {
		t.Fatal("命中路径表示 Content 应保留")
	}

	// ---- 去重：对象已实存 → 跳过 Put 零成本，引用行照产 ----
	refs, diags = app.ingestBaselineProjectContent(ctx, projectRoot, ingestBaseline(rep))
	if len(diags) != 0 || len(refs) != 1 {
		t.Fatalf("去重路径: refs=%v diags=%v", refs, diags)
	}

	// ---- 竞态：工作树被外部改写 → 表示退无 Content + 诊断，不产引用 ----
	// （独立 CAS：竞态语义要求目标 digest 尚无对象——若上节已摄取过则 Has=true，
	// 引用行合法，不构成竞态场景。）
	app2, _ := newIngestStack(t)
	if err := os.WriteFile(filepath.Join(projectRoot, metaRel), []byte("meta v2 external"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep2 := &model.Representation{
		RelativePath: "mods/test.pw.toml", Format: "packwiz-mod-toml",
		Content: &model.ContentRef{Algorithm: "sha256", Digest: ingestSha256("meta v1"), Size: 7},
	}
	refs, diags = app2.ingestBaselineProjectContent(ctx, projectRoot, ingestBaseline(rep2))
	if len(refs) != 0 {
		t.Fatalf("竞态降级不应产引用行: %+v", refs)
	}
	if len(diags) != 1 || diags[0].Code != "diag.commit.content_mismatch" {
		t.Fatalf("竞态诊断: %+v", diags)
	}
	if rep2.Content != nil {
		t.Fatal("竞态降级应把表示退无 Content")
	}

	// ---- 文件缺失 → content_unreadable + 退无 Content ----
	rep3 := &model.Representation{
		RelativePath: "mods/absent.pw.toml", Format: "packwiz-mod-toml",
		Content: &model.ContentRef{Algorithm: "sha256", Digest: ingestSha256("x"), Size: 1},
	}
	refs, diags = app2.ingestBaselineProjectContent(ctx, projectRoot, ingestBaseline(rep3))
	if len(refs) != 0 || rep3.Content != nil || len(diags) != 1 || diags[0].Code != "diag.commit.content_unreadable" {
		t.Fatalf("文件缺失降级: refs=%v content=%v diags=%+v", refs, rep3.Content, diags)
	}
}

// TestIngestBaselineProjectContentReplicaShape 覆盖摄取覆盖面（票 #93 泛化后）：
// 项目侧全部带内容指纹的表示（mod metafile 与非 mod 文本）同通道摄取；
// runtime 侧表示一律不摄取（jar 字节不入库，ADR-0005 §7 红线不动）。
func TestIngestBaselineProjectContentReplicaShape(t *testing.T) {
	app, cas := newIngestStack(t)
	ctx := context.Background()
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "config", "a.toml"), []byte("toml bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseline := &model.SyncBaseline{
		SchemaVersion: model.CurrentSchemaVersion,
		BaselineID:    "base_test",
		RelationID:    "rel_test",
		Resources: map[model.ResourceID]model.BaselineResource{
			"mod:path:mods/a.pw.toml": {
				State:         "present",
				LogicalDigest: "sha256:l1",
				// runtime 侧表示带 Content（jar 实测摘要）：摄取面之外，零摄取。
				RuntimeRepresentation: &model.Representation{
					RelativePath: "mods/a-1.0.jar", Format: "jar",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: ingestSha256("jar bytes"), Size: 9},
				},
			},
			"file:config/a.toml": {
				State:         "present",
				LogicalDigest: "sha256:l2",
				// 项目侧文本表示（票 #93 泛化面）：工作树字节在 → 摄取 + 引用行。
				ProjectRepresentation: &model.Representation{
					RelativePath: "config/a.toml", Format: "toml",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: ingestSha256("toml bytes"), Size: 10},
				},
			},
		},
	}
	refs, diags := app.ingestBaselineProjectContent(ctx, projectRoot, baseline)
	if len(diags) != 0 {
		t.Fatalf("零降级: diags=%v", diags)
	}
	if len(refs) != 1 || refs[0].Digest != ingestSha256("toml bytes") ||
		refs[0].Purpose != objectRefPurposeBaselineContent {
		t.Fatalf("引用行形状: %+v", refs)
	}
	if ok, err := cas.Has(ctx, ingestSha256("toml bytes")); err != nil || !ok {
		t.Fatalf("文本表示应入 CAS: ok=%v err=%v", ok, err)
	}
	if ok, err := cas.Has(ctx, ingestSha256("jar bytes")); err != nil || ok {
		t.Fatalf("runtime 侧 jar digest 不应入 CAS: ok=%v err=%v", ok, err)
	}
}

// TestDeriveApplyFilePlansModSourceDigest 锁死防误源修复：mod 写运行端行的
// after digest 恒取声明 hash——项目侧 metafile 的实测 Content（ADR-0012 §2
// 捕获）绝不可经源侧前置条件变成写盘内容源（否则 .pw.toml 字节会被当 jar 写
// 到运行端）。文件资源行为不变（源侧前置条件即写盘内容摘要）。
func TestDeriveApplyFilePlansModSourceDigest(t *testing.T) {
	const (
		metaV1   = "meta v1"
		declared = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // jar 载体声明 sha256（占位）
		jarCur   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // 运行端现有 jar（与声明不符 → 漂移行）
	)
	metaContent := &model.ContentRef{Algorithm: "sha256", Digest: ingestSha256(metaV1), Size: 7}
	snapP := model.ObservedSnapshot{
		Resources: map[model.ResourceID]model.ResourceObservation{
			"mod:curseforge:42": {
				ResourceID: "mod:curseforge:42", Kind: model.ResourceMod,
				Representation: model.Representation{
					RelativePath: "mods/x.pw.toml", Format: "packwiz-mod-toml", Content: metaContent,
					Metadata: map[string]string{
						model.MetaCFFileID: "5566778", model.MetaFilename: "x-1.2.2.jar",
						model.MetaDeclaredHashAlgo: "sha256", model.MetaDeclaredHashValue: declared,
					},
				},
			},
			"file:config/a.toml": {
				ResourceID: "file:config/a.toml", Kind: model.ResourceTextFile,
				Representation: model.Representation{
					RelativePath: "config/a.toml", Format: "toml",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: ingestSha256("toml v1"), Size: 8},
				},
			},
		},
	}
	snapR := model.ObservedSnapshot{
		Resources: map[model.ResourceID]model.ResourceObservation{
			"mod:curseforge:42": {
				ResourceID: "mod:curseforge:42", Kind: model.ResourceMod,
				Representation: model.Representation{
					RelativePath: "mods/x-1.2.2.jar", Format: "jar",
					Content: &model.ContentRef{Algorithm: "sha256", Digest: jarCur, Size: 100},
				},
			},
		},
	}

	// mod 写运行端行：源侧前置条件携带 metafile 实测摘要（捕获后的计划形状）。
	metaPre := *metaContent
	modOp := model.PlannedOperation{
		ID: "op_0001", Kind: model.OpWriteRuntime, ResourceID: "mod:curseforge:42",
		Materialization: model.MaterializationDownload,
		Preconditions: []model.Precondition{
			{ResourceID: "mod:curseforge:42", Side: "project", Existence: "present", Expected: &metaPre},
			{ResourceID: "mod:curseforge:42", Side: "runtime", Existence: "present"},
		},
	}
	// 文件写运行端行：源侧前置条件 = 源文件实测摘要（既有行为）。
	filePre := model.ContentRef{Algorithm: "sha256", Digest: ingestSha256("toml v1"), Size: 8}
	fileOp := model.PlannedOperation{
		ID: "op_0002", Kind: model.OpWriteRuntime, ResourceID: "file:config/a.toml",
		Preconditions: []model.Precondition{
			{ResourceID: "file:config/a.toml", Side: "project", Existence: "present", Expected: &filePre},
			{ResourceID: "file:config/a.toml", Side: "runtime", Existence: "absent"},
		},
	}
	plan := model.SyncPlan{
		PlanID: "plan_t", RelationID: "rel_t", Kind: model.PlanSync,
		Operations: []model.PlannedOperation{modOp, fileOp},
	}

	plans := deriveApplyFilePlans(plan, snapP, snapR, nil, t.TempDir(), t.TempDir())
	if len(plans) != 2 {
		t.Fatalf("应推导 2 个文件计划: %d", len(plans))
	}

	// mod 行：after = 声明 sha256（jar 载体），源路径绝不可指 metafile；内容源
	// 分派落 download（CF 重取），copy/CAS 通道不可命中。
	mod := plans[0]
	if mod.afterDigest != declared {
		t.Fatalf("mod 行 after digest = %s，期望声明 hash %s", mod.afterDigest, declared)
	}
	if mod.sourceRel != "" {
		t.Fatalf("mod 行内容源 = %q，metafile 字节绝不可作写盘源", mod.sourceRel)
	}
	if mod.dlReq == nil {
		t.Fatal("mod 行应落 download 物化分支（CF 重取）")
	}
	// 声明 hash 缺失的手放 mod 行：after 为空 → copy 不可得 → 取数失败剔除
	//（与捕获前一致），不得退化为把 metafile 当内容源。
	modOpNoHash := modOp
	modOpNoHash.ID = "op_0003"
	modOpNoHash.Materialization = ""
	snapPNoHash := snapP
	obs := snapP.Resources["mod:curseforge:42"]
	obs.Representation.Metadata = map[string]string{model.MetaFilename: "x-1.2.2.jar"}
	snapPNoHash.Resources = map[model.ResourceID]model.ResourceObservation{
		"mod:curseforge:42": obs,
	}
	planNoHash := plan
	planNoHash.Operations = []model.PlannedOperation{modOpNoHash}
	plain := deriveApplyFilePlans(planNoHash, snapPNoHash, snapR, nil, t.TempDir(), t.TempDir())[0]
	if plain.afterDigest != "" || plain.blockedCode != resultContentUnavailable {
		t.Fatalf("无声明手放行应 copy 不可得剔除: after=%q blocked=%q", plain.afterDigest, plain.blockedCode)
	}

	// 文件行：源侧前置条件即内容摘要 → 源路径照常命中（既有行为零变化）。
	file := plans[1]
	if file.afterDigest != ingestSha256("toml v1") || file.sourceRel != "config/a.toml" {
		t.Fatalf("文件行行为漂移: after=%q source=%q", file.afterDigest, file.sourceRel)
	}
}
