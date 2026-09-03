// 合并预览用例（票 #94，契约 07 §3.4；ADR-0009 §8）：GetMergedPreview 对
// merged_clean 行实时计算两段全文——合并后全文 + 基线全文（前端行级增删改
// 标注的比对锚点）——不落库，计划期不落字节纪律不变。
//
// 「所见即所写」由确定性保证：三侧全文取数与 #87 判定同源（Base=按基线表示
// 内容指纹从 CAS 取对象、Project/Runtime=端点活文件，即 mergeSources 同一装配），
// 合并产物直接调 core/merge.Texts（与暂存期重算同一函数，同算法同输入同输出）。
// 行合法性按「判定同源」复核：在计划锁定的输入快照 + 基线上重放 diff 真值表
// （双侧同改不同指纹的候选前置）与合并结论（零冲突块 ∧ 类型校验通过），与判定
// 时同输入必同结论；任一环节不成立 → err.merge.not_mergeable（{0}=resource_id）。
// 预览时取数/指纹复核失败属外部竞态：错误透传零新码，绝不静默编内容、也绝不
// 把「重取失败」谎报为「非合并行」。
package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"packgradle/internal/application/view"
	"packgradle/internal/core/diff"
	"packgradle/internal/core/merge"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// CodeMergeNotMergeable 是 GetMergedPreview 作用于非 merged_clean 行的错误码
// （契约 07 §3.4/§5 的 P4 唯一新错误码；args {0}=resource_id）。
const CodeMergeNotMergeable = "err.merge.not_mergeable"

// GetMergedPreview 合并预览：仅 merged_clean 行合法；stale/expired（以及 applied）
// 计划仍可预览（只读）——预览零推进面，不校验修订/绑定/有效期，输入一律以
// 计划锁定的快照与基线 ID 为准。
func (a *App) GetMergedPreview(ctx context.Context, input view.GetMergedPreviewInput) (view.MergedPreviewView, error) {
	notMergeable := func() (view.MergedPreviewView, error) {
		return view.MergedPreviewView{}, errs.New(CodeMergeNotMergeable, input.ResourceID)
	}
	plan, err := a.deps.Plans.Get(ctx, input.PlanID)
	if err != nil {
		return view.MergedPreviewView{}, errs.New(CodePlanNotFound, input.PlanID)
	}
	// restore 计划行是四标记判定行，无 merged_clean 行形态（合并判定/预览只
	// 属于 sync/initialize 计划面）；跨类按 not_mergeable 同一口径。
	if plan.Kind != model.PlanSync && plan.Kind != model.PlanInitialize {
		return notMergeable()
	}
	rel, err := a.deps.Relations.Get(ctx, plan.RelationID)
	if err != nil {
		return view.MergedPreviewView{}, errs.New(CodeRelationNotFound, plan.RelationID)
	}
	projEnd, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	rtEnd, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	// 计划锁定的输入快照（判定时点双侧观察）与基线：快照被计划 input_* 引用
	// 而受 GC 保护，读取失败属存储异常，透传。
	snapP, err := a.deps.Snapshots.GetForRelation(ctx, plan.InputProjectSnapshotID, plan.RelationID, model.SideProject)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	snapR, err := a.deps.Snapshots.GetForRelation(ctx, plan.InputRuntimeSnapshotID, plan.RelationID, model.SideRuntime)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	// 无基线计划（初始化前面）无双侧同改形态，不存在 merged_clean 行。
	if plan.BaseBaselineID == "" {
		return notMergeable()
	}
	base, err := a.deps.Baselines.Get(ctx, plan.BaseBaselineID)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	id := model.ResourceID(input.ResourceID)

	// 判定同源复核（前置）：同一快照对 + 基线重放 diff 真值表。不注入合并面
	// 取数缝——双侧同改不同指纹在此现形为 conflict_modify，即合并候选行；
	// 其余分类（noop/单侧/converged/删除改/初始化）均非合并行。
	res, err := diff.ThreeWay(diff.Input{RelationID: plan.RelationID, Base: &base, Project: snapP, Runtime: snapR})
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	var d *diff.ResourceDiff
	for i := range res.Diffs {
		if res.Diffs[i].ResourceID == id {
			d = &res.Diffs[i]
			break
		}
	}
	if d == nil || d.Classification != diff.ClassConflictModify {
		return notMergeable()
	}

	projObs, projOK := snapP.Resources[id]
	rtObs, rtOK := snapR.Resources[id]
	if !projOK || !rtOK {
		return notMergeable()
	}
	// 展示路径与取数路径同源（判定口径：project 优先回退 runtime）。
	relPath := projObs.Representation.RelativePath
	if relPath == "" {
		relPath = rtObs.Representation.RelativePath
	}
	baseRes, ok := base.Resources[id]
	if !ok {
		return notMergeable()
	}
	// 基线表示与 diff.baseEvidence 同序：project 优先、缺失回退 runtime。
	baseRep := baseRes.ProjectRepresentation
	if baseRep == nil {
		baseRep = baseRes.RuntimeRepresentation
	}
	// 行形状闸门（与判定同判据）：无路径 / 永不合并黑名单 / 任一侧无内容指纹，
	// 判定时即被「合并面不可用」排除，不可能是 merged_clean 行。
	if relPath == "" || baseRep == nil || baseRep.Content == nil ||
		projObs.Representation.Content == nil || rtObs.Representation.Content == nil ||
		!merge.Mergeable(d.Kind, relPath) {
		return notMergeable()
	}

	// 三侧全文取数（与 #87 判定同源：mergeSources 装配），指纹逐一复核；
	// 失败/失配一律透传——外部竞态不得静默降级判定结论，也不得编造内容。
	src := a.mergeSources(ctx, projEnd.RootPath, rtEnd.RootPath)
	baseText, err := previewVerified(func() ([]byte, error) { return src.Base(baseRep.Content.Digest) },
		id, *baseRep.Content)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	projText, err := previewVerified(func() ([]byte, error) { return src.Project(relPath) },
		id, *projObs.Representation.Content)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	rtText, err := previewVerified(func() ([]byte, error) { return src.Runtime(relPath) },
		id, *rtObs.Representation.Content)
	if err != nil {
		return view.MergedPreviewView{}, err
	}
	// 疑似二进制内容（含 NUL）与 diff.mergeVerdict 同口径：判定时按「合并面
	// 不可用」排除，此类行不可能是 merged_clean。
	if isBinaryPreviewContent(baseText) || isBinaryPreviewContent(projText) || isBinaryPreviewContent(rtText) {
		return notMergeable()
	}
	// 合并结论复核（判定同源）：含冲突块 = 真冲突行；类型校验失败 = 降级
	// conflict_modify 行——两者在判定时都不是 merged_clean，预览按同一结论拒绝。
	out := merge.Texts(baseText, projText, rtText)
	if len(out.Hunks) > 0 || merge.ValidateMerged(relPath, out.Merged) != nil {
		return notMergeable()
	}
	return view.MergedPreviewView{
		PlanID:       plan.PlanID,
		ResourceID:   input.ResourceID,
		RelativePath: relPath,
		Content:      string(out.Merged),
		BaseContent:  string(baseText),
	}, nil
}

// previewVerified 取数并复核 sha256 与内容指纹一致（与 diff.fetchVerified 同
// 口径）。预览语义与判定不同：失败/失配不降级、以错误透传（零新码）——
// merged_clean 行的存在已证明判定时三侧可得，预览时取不到属外部竞态。
func previewVerified(fetch func() ([]byte, error), id model.ResourceID, ref model.ContentRef) ([]byte, error) {
	if !strings.EqualFold(ref.Algorithm, "sha256") {
		return nil, fmt.Errorf("merge preview: 资源 %s 内容指纹算法 %q 非 sha256", id, ref.Algorithm)
	}
	data, err := fetch()
	if err != nil {
		return nil, fmt.Errorf("merge preview: 资源 %s 三侧取数失败: %w", id, err)
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), ref.Digest) {
		return nil, fmt.Errorf("merge preview: 资源 %s 字节与快照指纹不符（外部写者竞态）", id)
	}
	return data, nil
}

// isBinaryPreviewContent 报告字节串是否疑似二进制（含 NUL，与 diff.mergeVerdict
// 的二进制防线同口径；diff 侧未导出，预览侧按同判据复核）。
func isBinaryPreviewContent(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0
}
