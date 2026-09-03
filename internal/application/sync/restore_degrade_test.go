package sync

// 票 #95 单测（ADR-0012 §4/§6/§7.2；执行规格 §F3/§F4/§F5）：错写链四层锁定
// 与存量宽判降标的确定函数面——判定是 ADR 定义的确定函数，全格表驱动覆盖：
//
//	第一层 兜底删除：restoreTargetDigest 项目侧只认实测 Content，声明 sha256
//	       一律不作项目侧兜底（jar 摘要误标的源头删除）；runtime 侧兜底保留
//	       （jar 载体的合法锚）。
//	第二层 降标行拒收：no_project_content 行 StageUserObject 拒收在黑盒面
//	       （restore_t95_test.go），本文件锁纯函数覆写面。
//	第三层 digest 等值自然失配：兜底删除后降标行 ExpectedDigest=""，用户补全
//	       分支的跨侧等值匹配无从成立；新基线实测 Content 行补全通道照常。
//	第四层 verify 复扫「缺失或无内容指纹」断言保留（兜底防线不动）。
//	另锁 #88 的 sync 侧防线不回退：项目侧 metafile 的实测 Content 绝不作
//	写盘内容源（防 .pw.toml 字节当 jar 落盘）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"packgradle/internal/core/model"
)

// t95DegradRep 构造项目侧 metafile 表示（content 实存与否是降标判定的分格）。
func t95DegradRep(withContent bool) *model.Representation {
	rep := &model.Representation{RelativePath: "mods/x.pw.toml", Format: "packwiz-mod-toml"}
	if withContent {
		rep.Content = &model.ContentRef{Algorithm: "sha256", Digest: "dd44", Size: 10}
	}
	return rep
}

// TestNoProjectContentDegradeFullGrid 宽判降标判定全格（ADR-0012 §4：写回侧含
// project ∧ 目标基线项目侧表示无实测 Content）。
func TestNoProjectContentDegradeFullGrid(t *testing.T) {
	cases := []struct {
		name                string
		projectInWriteSides bool
		projRep             *model.Representation
		want                bool
	}{
		{"写回侧含project+项目侧无Content", true, t95DegradRep(false), true},
		{"写回侧含project+项目侧表示nil", true, nil, true},
		{"写回侧含project+项目侧有Content(新基线)", true, t95DegradRep(true), false},
		{"写回侧仅runtime+项目侧无Content", false, t95DegradRep(false), false},
		{"写回侧仅runtime+项目侧有Content", false, t95DegradRep(true), false},
	}
	for _, tc := range cases {
		if got := noProjectContentDegrade(tc.projectInWriteSides, tc.projRep); got != tc.want {
			t.Fatalf("%s: noProjectContentDegrade = %v，期望 %v", tc.name, got, tc.want)
		}
	}
}

// TestDegradeNoProjectRowOverrideFullGrid 后置覆写全格：四标记原值 × 分格——
// 命中即统一降 user_object_required + no_project_content（不区分原 marker，
// 重取信息与验收摘要一并清空）；未命中原行零触碰。
func TestDegradeNoProjectRowOverrideFullGrid(t *testing.T) {
	markers := []model.RestoreMarker{
		model.MarkerRestorableFromCAS, model.MarkerRedownloadRequired,
		model.MarkerUserObjectRequired, model.MarkerUnrecoverable,
	}
	for _, marker := range markers {
		for _, inWrite := range []bool{true, false} {
			for _, hasContent := range []bool{true, false} {
				item := model.RestorePlanItem{
					ResourceID:     "mod:path:mods/x.pw.toml",
					Marker:         marker,
					MarkerReason:   "seed_reason",
					ExpectedDigest: "seed_digest",
					Redownload:     &model.RedownloadInfo{FileID: 1, Filename: "x.jar"},
				}
				got := degradeNoProjectRow(item, inWrite, t95DegradRep(hasContent))
				if !(inWrite && !hasContent) {
					// 未命中：原行零触碰（矩阵判定即终局）
					if got.Marker != marker || got.MarkerReason != "seed_reason" ||
						got.ExpectedDigest != "seed_digest" || got.Redownload == nil {
						t.Fatalf("(%s, %v, %v): 未分格行被误覆写: %+v", marker, inWrite, hasContent, got)
					}
					continue
				}
				// 命中：统一降标，重取信息与验收摘要清空
				if got.Marker != model.MarkerUserObjectRequired ||
					got.MarkerReason != model.MarkerReasonNoProjectContent {
					t.Fatalf("(%s, %v, %v): 降标 = (%s, %q)", marker, inWrite, hasContent, got.Marker, got.MarkerReason)
				}
				if got.ExpectedDigest != "" || got.Redownload != nil {
					t.Fatalf("(%s, %v, %v): 验收摘要/重取信息应清空: %+v", marker, inWrite, hasContent, got)
				}
			}
		}
	}
}

// TestRestoreTargetDigestMislabelFallbackRemoved 第一层锁：项目侧只认实测
// Content，声明 sha256 兜底删除（jar 摘要不再误标为 metafile 目标摘要）；
// runtime 侧兜底保留（声明 hash 所指对象就是 jar，补全分支的合法锚）。
func TestRestoreTargetDigestMislabelFallbackRemoved(t *testing.T) {
	const declared, measured = "cc33", "dd44"
	repWithContent := func() *model.Representation {
		return &model.Representation{
			RelativePath: "mods/x.pw.toml",
			Content:      &model.ContentRef{Algorithm: "sha256", Digest: measured, Size: 10},
			Metadata:     map[string]string{model.MetaDeclaredHashAlgo: "sha256", model.MetaDeclaredHashValue: declared},
		}
	}
	repDeclaredOnly := func() *model.Representation {
		return &model.Representation{
			RelativePath: "mods/x.pw.toml",
			Metadata:     map[string]string{model.MetaDeclaredHashAlgo: "sha256", model.MetaDeclaredHashValue: declared},
		}
	}
	cases := []struct {
		name string
		side model.Side
		rep  *model.Representation
		want string
	}{
		{"project+实测Content", model.SideProject, repWithContent(), measured},
		{"project+仅声明sha256(误标源已删)", model.SideProject, repDeclaredOnly(), ""},
		{"project+无Content无声明", model.SideProject, &model.Representation{RelativePath: "mods/x.pw.toml"}, ""},
		{"project+nil表示", model.SideProject, nil, ""},
		{"runtime+实测Content", model.SideRuntime, repWithContent(), measured},
		{"runtime+仅声明sha256(合法兜底保留)", model.SideRuntime, repDeclaredOnly(), declared},
		{"runtime+无Content无声明", model.SideRuntime, &model.Representation{RelativePath: "mods/x.jar"}, ""},
	}
	for _, tc := range cases {
		if got := restoreTargetDigest(tc.side, tc.rep); got != tc.want {
			t.Fatalf("%s: restoreTargetDigest = %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// TestDeriveRestorePlansMiswriteChainDigestMismatch 第三层锁：错写链执行面——
// 存量降标行（ExpectedDigest=""、项目侧操作无内容引用）补全分支的 digest 等值
// 匹配无从成立：项目侧操作在推导期即 blocked（content_unavailable）、暂存锚上
// 的 jar 字节不被任何操作取用；对照面＝新基线实测 Content 行的补全通道照常。
func TestDeriveRestorePlansMiswriteChainDigestMismatch(t *testing.T) {
	const jarDigest, metaDigest = "aa1111", "bb2222"
	projRoot, rtRoot := t.TempDir(), t.TempDir()
	anchor := t.TempDir()
	// 暂存锚上放 jar 字节（旧链里它会被错写进 metafile 路径的字节源）。
	if err := os.MkdirAll(filepath.Join(anchor, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "mods", "legacy.pw.toml"), []byte("jar bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 存量降级行：目标基线项目侧无 Content（旧基线），runtime 侧 jar 有实测。
	legacyID := model.ResourceID("mod:path:mods/legacy.pw.toml")
	base := &model.SyncBaseline{Resources: map[model.ResourceID]model.BaselineResource{
		legacyID: {
			State: "present",
			ProjectRepresentation: &model.Representation{
				RelativePath: "mods/legacy.pw.toml",
				Metadata:     map[string]string{model.MetaDeclaredHashAlgo: "sha256", model.MetaDeclaredHashValue: jarDigest},
			},
			RuntimeRepresentation: &model.Representation{
				RelativePath: "mods/legacy-1.0.jar",
				Content:      &model.ContentRef{Algorithm: "sha256", Digest: jarDigest, Size: 9},
			},
		},
	}}
	// 计划：降标行（user_object_required + no_project_content + ExpectedDigest=""）
	// 带项目/运行两侧写回操作——项目侧操作无 sha256 引用（兜底删除的产物）。
	plan := model.SyncPlan{
		PlanID: "plan_t", RelationID: "rel_t", Kind: model.PlanRestore,
		Operations: []model.PlannedOperation{
			{
				ID: "op_0001", Kind: model.OpWriteProject, ResourceID: legacyID,
				Preconditions: []model.Precondition{{ResourceID: legacyID, Side: "project", Existence: "present"}},
			},
			{
				ID: "op_0002", Kind: model.OpWriteRuntime, ResourceID: legacyID,
				ObjectRefs:    []model.ContentRef{{Algorithm: "sha256", Digest: jarDigest}},
				Preconditions: []model.Precondition{{ResourceID: legacyID, Side: "runtime", Existence: "present"}},
			},
		},
		RestoreItems: []model.RestorePlanItem{{
			ResourceID: legacyID, ChangeKind: "modify", StageRel: "mods/legacy.pw.toml",
			Marker: model.MarkerUserObjectRequired, MarkerReason: model.MarkerReasonNoProjectContent,
		}},
	}
	plans, _ := deriveRestoreFilePlans(plan, base, model.ObservedSnapshot{}, model.ObservedSnapshot{},
		projRoot, rtRoot, anchor)
	if len(plans) != 2 {
		t.Fatalf("应推导 2 个文件计划: %d", len(plans))
	}
	byOp := map[string]applyFilePlan{}
	for _, fp := range plans {
		byOp[fp.op.ID] = fp
	}
	// 项目侧操作：无内容源即推导期 blocked（staging 相位整场 failed，零写入），
	// 暂存锚字节绝不被取用——「拒绝而非落盘」。
	proj := byOp["op_0001"]
	if proj.blockedCode != resultContentUnavailable || proj.sourceRel != "" || proj.afterDigest != "" {
		t.Fatalf("项目侧操作应推导期 blocked 且零内容源: blocked=%q source=%q after=%q",
			proj.blockedCode, proj.sourceRel, proj.afterDigest)
	}
	// 运行侧操作：补全分支 digest 等值自然失配（"" ≠ jar digest）→ 不取暂存锚
	// 字节，落 CAS 缺省分支（对象缺失在 staging 边界暴露，行为与既有降级一致）。
	rt := byOp["op_0002"]
	if rt.sourceRel != "" || rt.afterFromCAS != jarDigest {
		t.Fatalf("运行侧操作不得取暂存锚字节: source=%q cas=%q", rt.sourceRel, rt.afterFromCAS)
	}
}

// TestDeriveRestorePlansFreshBaselineCompletionChannel 对照面：新基线实测
// Content 行（ExpectedDigest=项目侧实测摘要，与操作目标一致）的补全分支照常
// 命中——修输入不修通道，降级只落在无 Content 的存量行。
func TestDeriveRestorePlansFreshBaselineCompletionChannel(t *testing.T) {
	const metaDigest = "cc3333"
	freshID := model.ResourceID("mod:path:mods/fresh.pw.toml")
	projRoot, rtRoot := t.TempDir(), t.TempDir()
	anchor := t.TempDir()
	if err := os.MkdirAll(filepath.Join(anchor, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "mods", "fresh.pw.toml"), []byte("meta bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := &model.SyncBaseline{Resources: map[model.ResourceID]model.BaselineResource{
		freshID: {
			State: "present",
			ProjectRepresentation: &model.Representation{
				RelativePath: "mods/fresh.pw.toml",
				Content:      &model.ContentRef{Algorithm: "sha256", Digest: metaDigest, Size: 10},
			},
		},
	}}
	plan := model.SyncPlan{
		PlanID: "plan_t", RelationID: "rel_t", Kind: model.PlanRestore,
		Operations: []model.PlannedOperation{{
			ID: "op_0001", Kind: model.OpWriteProject, ResourceID: freshID,
			ObjectRefs:    []model.ContentRef{{Algorithm: "sha256", Digest: metaDigest}},
			Preconditions: []model.Precondition{{ResourceID: freshID, Side: "project", Existence: "present"}},
		}},
		RestoreItems: []model.RestorePlanItem{{
			ResourceID: freshID, ChangeKind: "modify", StageRel: "mods/fresh.pw.toml",
			Marker: model.MarkerUserObjectRequired, MarkerReason: model.MarkerReasonNoRedownloadInfo,
			ExpectedDigest: metaDigest,
		}},
	}
	fps, _ := deriveRestoreFilePlans(plan, base, model.ObservedSnapshot{}, model.ObservedSnapshot{},
		projRoot, rtRoot, anchor)
	if len(fps) != 1 {
		t.Fatalf("应推导 1 个文件计划: %d", len(fps))
	}
	fp := fps[0]
	if fp.blockedCode != "" || !strings.HasSuffix(fp.sourceRel, "/mods/fresh.pw.toml") {
		t.Fatalf("新基线行补全分支应照常: blocked=%q source=%q", fp.blockedCode, fp.sourceRel)
	}
}

// TestVerifyRestoreNoContentFingerprintGuard 第四层锁：verify 复扫「缺失或无
// 内容指纹」violation 兜底防线保留——上游三道锁全部失效的字节错写最终在
// verifying 相位拦截（ADR-0012 §7.2 纵深裁定：不加新侧别匹配机制）。
func TestVerifyRestoreNoContentFingerprintGuard(t *testing.T) {
	id := model.ResourceID("mod:path:mods/x.pw.toml")
	plan := model.SyncPlan{PlanID: "plan_t", RelationID: "rel_t", Kind: model.PlanRestore}
	plans := []applyFilePlan{{
		op:     model.PlannedOperation{ID: "op_0001", Kind: model.OpWriteProject, ResourceID: id},
		action: applyActionModify, targetSide: model.SideProject, afterDigest: "aa", targetRel: "mods/x.pw.toml",
	}}
	// 复扫观察无内容指纹（jar 字节写进 .pw.toml 后扫描器解析失败的形状）。
	rescanP := model.ObservedSnapshot{Resources: map[model.ResourceID]model.ResourceObservation{
		id: {Kind: model.ResourceMod, Representation: model.Representation{RelativePath: "mods/x.pw.toml"}},
	}}
	violations, _, err := verifyRestore(plan, plans, rescanP, model.ObservedSnapshot{}, nil, nil)
	if err != nil {
		t.Fatalf("verifyRestore: %v", err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], "缺失或无内容指纹") {
		t.Fatalf("兜底防线应报无内容指纹: %v", violations)
	}
}

// TestDeriveApplyPlansProjectMetafileContentNotWriteSource 两道防线之二
// （#88 sync 侧防线不回退锁）：mod 写运行端行的 after digest 恒取声明 hash，
// 项目侧 metafile 的实测 Content 绝不作写盘内容源（防 .pw.toml 字节当 jar
// 落盘）；与 restore 侧兜底删除构成错写链的两侧封堵。
func TestDeriveApplyPlansProjectMetafileContentNotWriteSource(t *testing.T) {
	const jarDigest, metaDigest = "dd4444", "ee5555"
	id := model.ResourceID("mod:curseforge:42")
	metaObs := model.ResourceObservation{
		Kind: model.ResourceMod,
		Representation: model.Representation{
			RelativePath: "mods/x.pw.toml",
			Content:      &model.ContentRef{Algorithm: "sha256", Digest: metaDigest, Size: 10},
			Metadata: map[string]string{
				model.MetaFilename: "x-1.2.2.jar", model.MetaCFFileID: "42",
				model.MetaDeclaredHashAlgo: "sha256", model.MetaDeclaredHashValue: jarDigest,
			},
		},
	}
	snapP := model.ObservedSnapshot{Resources: map[model.ResourceID]model.ResourceObservation{id: metaObs}}
	plan := model.SyncPlan{
		PlanID: "plan_t", RelationID: "rel_t", Kind: model.PlanSync,
		Operations: []model.PlannedOperation{{
			ID: "op_0001", Kind: model.OpWriteRuntime, ResourceID: id,
			Preconditions: []model.Precondition{{ResourceID: id, Side: "project", Existence: "present"}},
		}},
	}
	fp := deriveApplyFilePlans(plan, snapP, model.ObservedSnapshot{}, nil, t.TempDir(), t.TempDir())[0]
	if fp.afterDigest != jarDigest || fp.sourceRel != "" {
		t.Fatalf("sync 侧防线漂移: after=%q（期望声明 %s）source=%q", fp.afterDigest, jarDigest, fp.sourceRel)
	}
}
