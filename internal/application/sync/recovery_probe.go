package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/syncstage"
)

// 恢复 probe 与四路裁决（票 #38，ADR-0004 §4）：对非终态 Apply 运行的逐操作
// 证据探测（目标 stat + before/after digest + staging 内容 + ownership proof）
// 与裁决执行（mark-applied / 幂等 redo / compensate / 含糊保持 recovery_required）。
//
// 铁律（ADR-0004 §4 原文）：
//   - 不依据文件名、mtime、目录数量或「看起来相同」进行猜测——只认 sha256
//     digest 与经 HMAC 校验的所有权证明；
//   - 无法证明归属时不得删除、覆盖或再次执行破坏性动作——证明校验失败一律
//     判含糊，目标绝不被触碰；
//   - 重复恢复幂等——终态运行不再裁决（recovery_required 交人工确认出口
//     AcknowledgeRecovery），补偿只对「本运行已落地且可证归属」的写入做一次。

// opVerdictKind 是单操作 probe 的裁决类别（ADR-0004 §4 矩阵行；compensate 是
// 运行级受阻时对 mark-applied 操作的处置，见 compensateProvableWrites）。
type opVerdictKind string

const (
	opVerdictMarkApplied opVerdictKind = "mark_applied" // 目标已达 after（digest+证明双匹配）
	opVerdictRedo        opVerdictKind = "redo"         // 目标未写入 + staging 完整 + 前置成立
	opVerdictAmbiguous   opVerdictKind = "ambiguous"    // 含糊/外部修改/无法证明归属
)

// opVerdict 是单操作的裁决结论与执行输入。
type opVerdict struct {
	kind      opVerdictKind
	reason    string                     // 裁判依据（日志与 journal detail）
	proof     syncstage.OwnershipProof   // 已通过校验的所有权证明
	planned   model.PlannedOperation     // journal 行固化的计划操作
	action    string                     // create|modify|delete
	targetAbs string                     // 目标绝对路径（端点 canonical root 内）
	stagedAbs string                     // 暂存副本绝对路径（create/modify redo 内容源）
}

// recoveryPipeline 对非终态运行执行恢复裁决。入口前提：关系已标记
// recovery_required（禁新 Apply，ADR-0004 §4 第一句）。
//
// 裁决流程：
//  1. staging 运行重入（同钥；密钥缺失 = 证据不完整 → 含糊，不换钥续签）；
//  2. 逐未终态操作 probe（probeOperation）；
//  3. 任一含糊 → compensate 可证已落地写入 → run→recovery_required（人工出口）；
//  4. 全部可解 → 落 mark-applied 事实 + 幂等 redo → run 沿成功链推到 verifying，
//     复用引擎「复扫验证 + committed 单事务」收口（ADR-0004 §4 第一行的
//     可验证路径）。
func (a *App) recoveryPipeline(ctx context.Context, active model.Task, run model.ApplyRun, rel model.Relation) {
	blocked := func(code string, cause error) {
		a.blockRecoveredRun(ctx, active, run, code, cause)
	}

	stgRun, err := syncstage.OpenRun(a.deps.StagingRoot, run.TaskID)
	if err != nil {
		// 运行密钥缺失等暂存证据不完整：证明体系失效，一切操作按含糊处理
		blocked(applyResultCode(err), fmt.Errorf("重入暂存运行: %w", err))
		return
	}

	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	rt, err2 := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil || err2 != nil {
		blocked(resultIOError, fmt.Errorf("读取端点: %v/%v", err, err2))
		return
	}
	rootBySide := map[model.Side]string{model.SideProject: proj.RootPath, model.SideRuntime: rt.RootPath}

	ops, err := a.listAllJournalOps(ctx, run.TaskID)
	if err != nil {
		blocked(applyResultCode(err), err)
		return
	}
	if len(ops) == 0 {
		// 无任何已持久化操作意图（如 prepared 崩溃于 staging 前）：无可裁决事实，
		// 也无可续做证据（staging 与 journal 的关联尚未落库）→ 人工出口。
		blocked(resultPreconditionViolated, errors.New("运行无操作日志（崩溃于意图落库前）"))
		return
	}

	// 计划与输入快照：redo 前置条件复核的数据面（读不到时 redo 判定退化为含糊，
	// 不影响 mark-applied 判定——后者只依赖目标 digest 与证明）。
	var snaps map[model.Side]model.ObservedSnapshot
	if plan, err := a.deps.Plans.Get(ctx, run.PlanID); err == nil {
		snapP, e1 := a.deps.Snapshots.Get(ctx, plan.InputProjectSnapshotID)
		snapR, e2 := a.deps.Snapshots.Get(ctx, plan.InputRuntimeSnapshotID)
		if e1 == nil && e2 == nil {
			snaps = map[model.Side]model.ObservedSnapshot{model.SideProject: snapP, model.SideRuntime: snapR}
		}
	}

	// 逐操作 probe（只读：不落库、不触文件）。
	verdicts := make([]opVerdict, len(ops))
	ambiguous := 0
	for i := range ops {
		verdicts[i] = a.probeOperation(run, stgRun, ops[i], rootBySide, snaps)
		if verdicts[i].kind == opVerdictAmbiguous {
			ambiguous++
			log.Printf("recovery: 运行 %s 操作 %s 判含糊: %s", run.TaskID, ops[i].OperationID, verdicts[i].reason)
		}
	}

	if ambiguous > 0 {
		// 含糊（矩阵第四行）：运行不可自动完成。对「本运行已落地且可证归属」的
		// 写入做补偿回滚（copy 场景），其余操作原样保留；run→recovery_required
		// 等人工确认（AcknowledgeRecovery）。
		a.compensateProvableWrites(ctx, run, ops, verdicts)
		blocked(resultTargetModified, fmt.Errorf("%d 个操作状态含糊，需人工确认", ambiguous))
		return
	}

	// 全部可解：先推运行相位到 applying（逐操作裁决的合法宿主相位），
	// 再落 mark-applied 事实与幂等 redo。
	if err := a.advanceRunToApplying(ctx, run.TaskID, run.State); err != nil {
		blocked(applyResultCode(err), fmt.Errorf("推进运行相位: %w", err))
		return
	}
	actionsBySide, err := a.recoveryActions(stgRun, rootBySide)
	if err != nil {
		blocked(applyResultCode(err), err)
		return
	}
	for i := range ops {
		op, v := ops[i], verdicts[i]
		if v.kind == opVerdictMarkApplied {
			if err := a.journalizeMarkApplied(ctx, run.TaskID, op); err != nil {
				blocked(applyResultCode(err), fmt.Errorf("操作 %s 落 mark-applied 事实: %w", op.OperationID, err))
				return
			}
			continue
		}
		if err := a.redoOperation(ctx, stgRun, run, op, v, actionsBySide); err != nil {
			// redo 失败：外部篡改族（目标在探测与执行之间被改）视同含糊受阻——
			// 补偿本运行已落地写入；io/基础设施失败保留诚实部分完成（与引擎
			// 失败面同口径），staging 证据保留。
			if errors.Is(err, syncstage.ErrTargetModified) || errors.Is(err, syncstage.ErrProofInvalid) {
				a.compensateProvableWrites(ctx, run, ops, verdicts)
			}
			blocked(applyResultCode(err), fmt.Errorf("操作 %s 幂等 redo 失败: %w", op.OperationID, err))
			return
		}
	}

	a.completeRecoveredRun(ctx, active, run, rel, ops, verdicts, stgRun)
}

// probeOperation 对单个未终态操作做证据探测与四路裁决。
//
// 判定序（照 ADR-0004 §4 矩阵行序）：
//  0. 所有权证明门：证明缺失/伪造/跨运行/字段与 journal 行不符/类别与操作不符
//     → 含糊（铁律：无有效证明绝不动作，即使 digest 看似匹配）；
//  1. 目标已达 after 态（create/modify：digest==after；delete：目标缺席）且证明
//     有效 → mark-applied；
//  2. 目标未写入（create：缺席；modify/delete：digest==before）+ staging 完整
//     （create/modify：暂存副本 digest==after；delete 无需暂存）+ 计划前置条件
//     在磁盘上仍成立 → 幂等 redo；
//  3. 其余一切（目标内容既非 before 也非 after、暂存不完整、前置条件不成立、
//     stat/读取失败、目标非普通文件）→ 含糊。
func (a *App) probeOperation(run model.ApplyRun, stgRun *syncstage.Run, op model.JournalOperation,
	rootBySide map[model.Side]string, snaps map[model.Side]model.ObservedSnapshot) opVerdict {

	amb := func(reason string) opVerdict {
		return opVerdict{kind: opVerdictAmbiguous, reason: reason}
	}

	// 0) 所有权证明门。
	var proof syncstage.OwnershipProof
	if len(op.OwnershipProof) == 0 || string(op.OwnershipProof) == "{}" {
		return amb("journal 行无所有权证明")
	}
	if err := json.Unmarshal(op.OwnershipProof, &proof); err != nil {
		return amb(fmt.Sprintf("所有权证明解析失败: %v", err))
	}
	if err := stgRun.VerifyOwnershipProof(proof); err != nil {
		// 伪造/跨运行/任一字段篡改一律 ErrProofInvalid（T02 契约）→ 含糊
		return amb(fmt.Sprintf("所有权证明校验失败: %v", err))
	}
	// 证明与 journal 行互验：同钥重签的异字段记录仍是伪证，字段必须逐一对齐。
	if proof.TargetPath != op.TargetRelativePath ||
		proof.BeforeDigest != op.BeforeDigest || proof.AfterDigest != op.AfterDigest {
		return amb("证明字段与 journal 行不符（目标路径或 digest 不一致）")
	}
	if proof.RelationID != run.RelationID {
		return amb("证明归属其他关系")
	}

	var planned model.PlannedOperation
	if len(op.Operation) > 0 && string(op.Operation) != "{}" {
		if err := json.Unmarshal(op.Operation, &planned); err != nil {
			return amb(fmt.Sprintf("计划操作解析失败: %v", err))
		}
	}
	_, tgtSide, known := applySideForOp(planned.Kind)
	if !known {
		return amb(fmt.Sprintf("操作类别 %q 不可恢复裁决", planned.Kind))
	}
	action := ""
	switch {
	case op.BeforeDigest == "" && op.AfterDigest != "":
		action = applyActionCreate
	case op.AfterDigest == "" && op.BeforeDigest != "":
		action = applyActionDelete
	case op.BeforeDigest != "" && op.AfterDigest != "":
		action = applyActionModify
	default:
		return amb("before/after digest 同时为空")
	}
	if proof.Kind() != action {
		return amb(fmt.Sprintf("证明类别 %s 与操作类别 %s 不符", proof.Kind(), action))
	}
	root, ok := rootBySide[tgtSide]
	if !ok || root == "" {
		return amb("目标侧端点 root 不可得")
	}
	targetAbs := filepath.Join(root, filepath.FromSlash(op.TargetRelativePath))

	// 1) 目标探测：Lstat + sha256（不凭 mtime/外观）。
	targetDigest := ""
	targetExists := false
	if st, err := os.Lstat(targetAbs); err == nil {
		plain := st.Mode().IsRegular() && st.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
		if !plain {
			return amb("目标存在但不是普通文件")
		}
		ref, err := syncstage.HashFile(targetAbs)
		if err != nil {
			return amb(fmt.Sprintf("目标内容读取失败: %v", err))
		}
		targetExists, targetDigest = true, ref.Digest
	} else if !errors.Is(err, fs.ErrNotExist) {
		return amb(fmt.Sprintf("目标 stat 失败: %v", err))
	}

	// 2) staging 内容探测（create/modify 幂等 redo 的唯一合法内容源）。
	stagedAbs := ""
	stagedReady := true
	if action != applyActionDelete {
		if op.TempRelativePath == "" {
			return amb("写操作 journal 行无暂存路径")
		}
		abs, err := stgRun.StageAbs(op.TempRelativePath)
		if err != nil {
			return amb(fmt.Sprintf("暂存路径非法: %v", err))
		}
		stagedAbs = abs
		sref, err := syncstage.HashFile(abs)
		if err != nil || sref.Digest != op.AfterDigest {
			stagedReady = false // 暂存副本缺失或 digest 不符：staging 不完整
		}
	}

	// 3) 四路裁决。
	switch {
	case action == applyActionDelete:
		if !targetExists {
			// 删除的 after 态 = 目标缺席（幂等重放同口径，ApplyDelete already_applied）
			return opVerdict{kind: opVerdictMarkApplied, reason: "删除目标已缺席", proof: proof,
				planned: planned, action: action, targetAbs: targetAbs}
		}
		if targetDigest == op.BeforeDigest {
			return opVerdict{kind: opVerdictRedo, reason: "删除未执行且目标与 before 一致", proof: proof,
				planned: planned, action: action, targetAbs: targetAbs}
		}
		return amb(fmt.Sprintf("删除目标内容与 before 不符（外部修改嫌疑 got=%s）", targetDigest))

	default: // create / modify
		if targetExists && targetDigest == op.AfterDigest {
			return opVerdict{kind: opVerdictMarkApplied, reason: "目标已达 after digest 且证明匹配", proof: proof,
				planned: planned, action: action, targetAbs: targetAbs, stagedAbs: stagedAbs}
		}
		notWritten := !targetExists || targetDigest == op.BeforeDigest
		if notWritten && stagedReady {
			if code, _ := verifyApplyPreconditions(planned, snaps, rootBySide); code == "" {
				return opVerdict{kind: opVerdictRedo, reason: "目标未写入、staging 完整、前置条件成立", proof: proof,
					planned: planned, action: action, targetAbs: targetAbs, stagedAbs: stagedAbs}
			}
			return amb("目标未写入但计划前置条件已不成立")
		}
		if targetExists {
			return amb(fmt.Sprintf("目标内容既非 before 也非 after（got=%s，外部修改或不可证归属）", targetDigest))
		}
		if !stagedReady {
			return amb("目标未写入且暂存证据不完整，无法幂等续做")
		}
		return amb("目标状态不可判定")
	}
}

// spillReader 是自带清理的溢出文件读句柄（Close 时删除临时文件）。
type spillReader struct {
	*os.File
	path string
}

func (s *spillReader) Close() error {
	err := s.File.Close()
	os.Remove(s.path)
	return err
}

// spoolStaged 把暂存副本复制到运行目录内的溢出临时文件并打开（Close 即清理）。
func spoolStaged(stgRun *syncstage.Run, stagedAbs string) (*spillReader, error) {
	src, err := os.Open(stagedAbs)
	if err != nil {
		return nil, fmt.Errorf("打开暂存副本: %w", err)
	}
	defer src.Close()
	spill, err := os.CreateTemp(stgRun.Dir(), "redo-spill-*")
	if err != nil {
		return nil, fmt.Errorf("创建 redo 溢出文件: %w", err)
	}
	if _, err := io.Copy(spill, src); err != nil {
		spill.Close()
		os.Remove(spill.Name())
		return nil, fmt.Errorf("复制暂存副本: %w", err)
	}
	if _, err := spill.Seek(0, io.SeekStart); err != nil {
		spill.Close()
		os.Remove(spill.Name())
		return nil, err
	}
	return &spillReader{File: spill, path: spill.Name()}, nil
}

// journalizeMarkApplied 把 mark-applied 裁决落为 journal 事实：补 running 意图
// （pending→running 合法迁移先行）再收口 applied；已 applied 的操作不重复推进。
func (a *App) journalizeMarkApplied(ctx context.Context, taskID string, op model.JournalOperation) error {
	if op.Status == model.OperationStatusApplied {
		return nil
	}
	detail := marshalJSONRaw(map[string]string{
		"intent": "recovery_mark_applied", "evidence": "target_digest==after_digest && ownership_proof",
	})
	if op.Status == model.OperationStatusPending {
		if err := a.deps.Journal.AdvanceStatus(ctx, taskID, op.OperationID,
			model.OperationStatusRunning, a.nowStr(), detail); err != nil {
			return err
		}
	}
	return a.deps.Journal.AdvanceStatus(ctx, taskID, op.OperationID,
		model.OperationStatusApplied, a.nowStr(), detail)
}

// redoOperation 幂等重放单操作的文件动作（ADR-0004 §4 第二行）：内容取暂存副本
// （探测期已复核 digest==after）；动作原语自证（证明校验 + 目标 digest 判定 +
// 原子落地 + 落地复核），目标已达成时 already_applied 不重写。意图先行：
// pending 先落 running 再执行（ADR-0004 §2）。
func (a *App) redoOperation(ctx context.Context, stgRun *syncstage.Run, run model.ApplyRun,
	op model.JournalOperation, v opVerdict, actionsBySide map[model.Side]*syncstage.Actions) error {

	if op.Status == model.OperationStatusPending {
		intent := marshalJSONRaw(map[string]string{"intent": "recovery_redo", "action": v.action, "target": op.TargetRelativePath})
		if err := a.deps.Journal.AdvanceStatus(ctx, run.TaskID, op.OperationID,
			model.OperationStatusRunning, a.nowStr(), intent); err != nil {
			return err
		}
	}
	var content io.Reader
	if v.action != applyActionDelete {
		// redo 内容源 = 暂存副本（探测期已复核 digest==after，ADR-0004 §4 第二行）。
		// 经运行目录内的溢出临时文件间接提供：动作原语会把内容重新原子暂存到
		// 同一路径，直接持有原文件句柄会在 Windows 上与 rename 冲突；溢出盘
		// 中转保持大文件零内存峰值（测试 seam 亦可注入）。
		spill, err := spoolStaged(stgRun, v.stagedAbs)
		if err != nil {
			return err
		}
		defer spill.Close()
		content = spill
	}
	_, tgtSide, _ := applySideForOp(v.planned.Kind)
	res, execErr := applyActionRunner(actionsBySide[tgtSide], v.action, v.proof, content)
	if execErr != nil {
		return execErr
	}
	outcome := marshalJSONRaw(map[string]string{"intent": "recovery_redo", "outcome": string(res.Outcome)})
	return a.deps.Journal.AdvanceStatus(ctx, run.TaskID, op.OperationID,
		model.OperationStatusApplied, a.nowStr(), outcome)
}

// compensateProvableWrites 对受阻运行的「本运行已落地且可证归属」写入做补偿
// （ADR-0004 §4 第三行，copy 场景）：
//   - create：删除本运行新建的文件（补偿前重核目标 digest==after）；
//   - modify/delete：以运行级恢复引用中的 CAS before 保全恢复旧内容
//     （恢复流独立复核 digest，失配不标记补偿）。
//
// 只处理 probe 判 mark-applied 的操作（目标已达 after 且证明有效）；redo 类操作
// 无落地写入、含糊类操作不可证归属——均原样保留。逐操作 best-effort：单操作
// 失败保留现状并记日志，不阻断其余补偿，不臆造状态。重复恢复幂等：补偿完成后
// 运行即终态，后续恢复不再进入本路径。
func (a *App) compensateProvableWrites(ctx context.Context, run model.ApplyRun,
	ops []model.JournalOperation, verdicts []opVerdict) {

	casByOp := casRefsByOperation(run)
	for i := range ops {
		op, v := ops[i], verdicts[i]
		if v.kind != opVerdictMarkApplied {
			continue
		}
		code := a.compensateOne(ctx, run, op, v, casByOp[op.OperationID])
		if code == "" {
			continue
		}
		log.Printf("recovery: 运行 %s 操作 %s 补偿未完成（%s），保留现状交人工", run.TaskID, op.OperationID, code)
	}
}

// compensateOne 执行单操作补偿；返回空串 = 补偿完成（failed→compensated 落库），
// 非空 = 跳过原因（目标已变/无保全引用/复核失配），操作状态保持不变。
func (a *App) compensateOne(ctx context.Context, run model.ApplyRun, op model.JournalOperation,
	v opVerdict, casDigest string) string {

	markCompensated := func() error {
		detail := marshalJSONRaw(map[string]string{"intent": "recovery_compensate", "action": v.action})
		if err := a.deps.Journal.AdvanceStatus(ctx, run.TaskID, op.OperationID,
			model.OperationStatusFailed, a.nowStr(), detail); err != nil {
			return err
		}
		if err := a.deps.Journal.AdvanceStatus(ctx, run.TaskID, op.OperationID,
			model.OperationStatusCompensated, a.nowStr(), detail); err != nil {
			return err
		}
		return a.deps.Journal.MarkResult(ctx, run.TaskID, op.OperationID,
			marshalJSONRaw(map[string]string{"code": "recovery_compensated"}))
	}

	switch actionChangeKind(v.action) {
	case model.ChangeCreate:
		// 删除本运行新建文件；补偿前重核归属证据（目标仍为 after 内容）。
		if ref, err := syncstage.HashFile(v.targetAbs); err != nil || ref.Digest != op.AfterDigest {
			return "target no longer at after digest"
		}
		if err := os.Remove(v.targetAbs); err != nil {
			return fmt.Sprintf("删除新建文件失败: %v", err)
		}
	case model.ChangeModify, model.ChangeDelete:
		// 以 CAS before 保全恢复旧内容；无引用（策略豁免保全，如 mod redownload）
		// 则无恢复对象——保留现状（delete 的缺席本就是计划目标态）。
		if casDigest == "" {
			return "no CAS before-preservation reference"
		}
		if casDigest != op.BeforeDigest {
			return "CAS reference does not match before digest"
		}
		if err := a.restoreFromCAS(ctx, casDigest, v.targetAbs, op.BeforeDigest); err != nil {
			return fmt.Sprintf("恢复旧内容失败: %v", err)
		}
	default:
		return "unknown action"
	}
	if err := markCompensated(); err != nil {
		return fmt.Sprintf("补偿状态落库失败: %v", err)
	}
	return ""
}

// restoreFromCAS 把 CAS before 保全对象原子写回目标路径并独立复核 digest。
func (a *App) restoreFromCAS(ctx context.Context, casDigest, targetAbs, wantDigest string) error {
	rc, err := a.deps.CAS.Open(ctx, casDigest)
	if err != nil {
		return fmt.Errorf("打开 CAS 保全对象: %w", err)
	}
	defer rc.Close()
	if err := syncstage.WriteFileAtomic(targetAbs, rc); err != nil {
		return err
	}
	return syncstage.VerifyFileDigest(targetAbs, wantDigest)
}

// casRefsByOperation 从运行级恢复引用提取逐操作的 CAS before 保全 digest
// （引用形状由 T04 引擎定义：{operation_id, kind:"cas", algorithm, digest, purpose}）。
func casRefsByOperation(run model.ApplyRun) map[string]string {
	out := map[string]string{}
	for _, ref := range recoveryRefEntries(run) {
		if ref["kind"] == "cas" {
			if opID, _ := ref["operation_id"].(string); opID != "" {
				if digest, _ := ref["digest"].(string); digest != "" {
					out[opID] = digest
				}
			}
		}
	}
	return out
}

// casRefRows 从运行级恢复引用重建 object_refs 引用行（kind=cas 条目；
// purpose 沿引用行原值——引擎路径为 before_preservation）。
func casRefRows(run model.ApplyRun) []ports.ObjectRefRow {
	var out []ports.ObjectRefRow
	for _, ref := range recoveryRefEntries(run) {
		if ref["kind"] != "cas" {
			continue
		}
		algorithm, _ := ref["algorithm"].(string)
		digest, _ := ref["digest"].(string)
		purpose, _ := ref["purpose"].(string)
		if digest == "" {
			continue
		}
		out = append(out, ports.ObjectRefRow{Algorithm: algorithm, Digest: digest, Purpose: purpose})
	}
	return out
}

// recoveryRefEntries 解析运行级恢复引用 JSON 为键值条目切片；解析失败返回空
// （引用是恢复辅助面，解析失败不阻断裁决——操作行内证据已够判定）。
func recoveryRefEntries(run model.ApplyRun) []map[string]any {
	if len(run.RecoveryRefs) == 0 {
		return nil
	}
	var refs []map[string]any
	if err := json.Unmarshal(run.RecoveryRefs, &refs); err != nil {
		return nil
	}
	return refs
}

// completeRecoveredRun 把全部可证运行推完 verifying→committed（ADR-0004 §4 第一行
// 「随后进入可验证路径」；引擎收口复用）：复扫验证 → committed 单事务（验证快照 +
// 新 Baseline + SyncCommit + object_refs + Relation head + 运行终态 + 操作 verified
// + 确认令牌消费 + 关系健康复位）→ 提交成功后才清 staging → 任务 succeeded →
// 发布 relation_invalidated（契约 05 §4 恢复收口发射点）。验证不一致或事务失败
// 按 blocked 收口（不推 Baseline、不建 Commit、staging 证据保留、不做补偿——
// 已落地写入是诚实部分完成，与引擎失败面同口径）。
func (a *App) completeRecoveredRun(ctx context.Context, active model.Task, run model.ApplyRun,
	rel model.Relation, ops []model.JournalOperation, verdicts []opVerdict, stgRun *syncstage.Run) {

	blocked := func(code string, cause error) {
		a.blockRecoveredRun(ctx, active, run, code, cause)
	}

	plan, err := a.deps.Plans.Get(ctx, run.PlanID)
	if err != nil {
		blocked(resultPreconditionViolated, fmt.Errorf("读取计划: %w", err))
		return
	}
	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	rt, err2 := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil || err2 != nil {
		blocked(resultIOError, fmt.Errorf("读取端点: %v/%v", err, err2))
		return
	}

	if run.State != model.ApplyRunVerifying {
		if err := a.deps.ApplyRuns.AdvanceState(ctx, run.TaskID, model.ApplyRunVerifying, a.nowStr()); err != nil {
			blocked(applyResultCode(err), fmt.Errorf("推进运行至 verifying: %w", err))
			return
		}
	}

	// verifying：受管范围完整复扫，快照与计划目标一致（复用引擎验证管线）。
	rescanP, rescanR, err := a.rescanEndpoints(ctx, rel, proj, rt)
	if err != nil {
		blocked(resultIOError, fmt.Errorf("验证复扫失败: %w", err))
		return
	}
	var base *model.SyncBaseline
	if plan.BaseBaselineID != "" {
		b, err := a.deps.Baselines.Get(ctx, plan.BaseBaselineID)
		if err != nil {
			blocked(resultPreconditionViolated, fmt.Errorf("读取基线: %w", err))
			return
		}
		base = &b
	}
	plans := make([]applyFilePlan, len(verdicts))
	for i := range verdicts {
		v := verdicts[i]
		_, tgtSide, _ := applySideForOp(v.planned.Kind)
		plans[i] = applyFilePlan{op: v.planned, action: v.action, targetSide: tgtSide}
	}
	violations, remaining, err := verifyRescan(plan, plans, rescanP, rescanR, base, nil)
	if err != nil {
		blocked(resultVerifyMismatch, fmt.Errorf("验证比较失败: %w", err))
		return
	}
	if len(violations) > 0 {
		blocked(resultVerifyMismatch, fmt.Errorf("复扫与计划目标不一致: %v", violations))
		return
	}

	completeness := model.TaskOutcomeExact
	if remaining > 0 {
		completeness = model.TaskOutcomePartial
	}

	// 输入快照（提交变化行的 before 表示数据源）。
	snapP, err := a.deps.Snapshots.Get(ctx, plan.InputProjectSnapshotID)
	rtSnapID := plan.InputRuntimeSnapshotID
	snapR, err2 := a.deps.Snapshots.Get(ctx, rtSnapID)
	if err != nil || err2 != nil {
		blocked(resultIOError, fmt.Errorf("读取输入快照: %v/%v", err, err2))
		return
	}

	commitID := a.deps.IDs("commit_")
	baselineID := a.deps.IDs("base_")
	nowStr := a.nowStr()
	newBaseline, err := buildVerifiedBaseline(rel.RelationID, plan.BaseBaselineID, rescanP, rescanR, base)
	if err != nil {
		blocked(resultIOError, fmt.Errorf("构造结果基线: %w", err))
		return
	}
	newBaseline.BaselineID = baselineID
	newBaseline.CreatedAt = nowStr
	rescanP.SnapshotID = a.deps.IDs("snap_")
	rescanP.CapturedAt = nowStr
	rescanR.SnapshotID = a.deps.IDs("snap_")
	rescanR.CapturedAt = nowStr
	commit := buildSyncCommit(rel, plan, commitID, baselineID, nowStr, completeness, remaining,
		rescanP.SnapshotID, rescanR.SnapshotID, buildCommitChanges(plans, snapP, snapR, rescanP, rescanR), nil)

	// object_refs：运行级恢复引用中的 CAS before 保全引用（恢复完成的运行重建
	// 引用行；引擎路径的 size 事实在恢复引用中不携带，此处按缺省 0 落库）。
	casRefs := casRefRows(run)

	err = a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		if err := repos.Snapshots.Insert(ctx, rescanP); err != nil {
			return fmt.Errorf("写入验证快照: %w", err)
		}
		if err := repos.Snapshots.Insert(ctx, rescanR); err != nil {
			return fmt.Errorf("写入验证快照: %w", err)
		}
		if err := repos.Baselines.Insert(ctx, newBaseline); err != nil {
			return fmt.Errorf("写入结果基线: %w", err)
		}
		if err := repos.Commits.Insert(ctx, commit); err != nil {
			return fmt.Errorf("写入提交: %w", err)
		}
		if err := repos.Commits.InsertObjectRefs(ctx, "commit", commitID, casRefs); err != nil {
			return fmt.Errorf("写入对象引用: %w", err)
		}
		if err := repos.Relations.UpdateHeadBaseline(ctx, rel.RelationID, baselineID); err != nil {
			return err
		}
		if err := repos.Relations.UpdateHeadCommit(ctx, rel.RelationID, commitID); err != nil {
			return err
		}
		if err := repos.Relations.UpdateHealth(ctx, rel.RelationID, model.HealthHealthy); err != nil {
			return err
		}
		if err := repos.ApplyRuns.AttachCommit(ctx, run.TaskID, commitID, nowStr); err != nil {
			return err
		}
		if err := repos.ApplyRuns.AdvanceState(ctx, run.TaskID, model.ApplyRunCommitted, nowStr); err != nil {
			return err
		}
		for _, op := range ops {
			if err := repos.Journal.AdvanceStatus(ctx, run.TaskID, op.OperationID,
				model.OperationStatusVerified, nowStr, nil); err != nil {
				return fmt.Errorf("操作 %s 标记 verified: %w", op.OperationID, err)
			}
		}
		a.consumePlanConfirmation(ctx, repos, plan.PlanID)
		return nil
	})
	if err != nil {
		// 事务回滚零残留：无 Baseline/Commit/head 推进，run 走恢复面（不做补偿——
		// 已落地写入是诚实部分完成，引擎失败面同口径）
		blocked(resultIOError, fmt.Errorf("提交事务失败: %w", err))
		return
	}

	// staging 仅在提交事务成功后清理（ADR-0004 §5）；失败保留证据可重试。
	if err := syncstage.CleanupRun(a.deps.StagingRoot, run.TaskID); err != nil {
		log.Printf("recovery: 清理暂存失败（staging_cleared 保持未清理，可重试）: %v", err)
	} else if err := a.deps.ApplyRuns.MarkStagingCleared(ctx, run.TaskID, a.nowStr()); err != nil {
		log.Printf("recovery: 记录 staging_cleared 失败: %v", err)
	}

	active.Status = model.TaskStatusSucceeded
	active.Phase = "done"
	active.MessageKey = taskProgressKey(active.Kind, "succeeded")
	active.Completed = len(ops)
	active.Total = len(ops)
	active.CommitID = commitID
	active.Outcome = completeness
	if _, err := a.runner.Update(ctx, active); err != nil {
		log.Printf("recovery: 任务 %s 成功终态落库失败: %v", active.TaskID, err)
		return
	}
	_ = a.pub.PublishRelationInvalidated(ctx, rel.RelationID)
	log.Printf("recovery: 运行 %s probe 裁决完成，已 committed（%s）", run.TaskID, commitID)
}

// blockRecoveredRun 把不可自动完成的运行收口到 recovery_required（终态）：
// run 终态推进 + 任务终态（Problem 复用 err.recovery.in_progress）+ 关系恢复门
// 补齐（幂等）。staging 证据一律保留。
func (a *App) blockRecoveredRun(ctx context.Context, active model.Task, run model.ApplyRun, code string, cause error) {
	if !applyRunTerminal(run.State) {
		if err := a.deps.ApplyRuns.AdvanceState(ctx, run.TaskID, model.ApplyRunRecoveryRequired, a.nowStr()); err != nil {
			log.Printf("recovery: 运行 %s 推进 recovery_required 失败: %v", run.TaskID, err)
		}
	}
	if err := a.deps.Relations.UpdateHealth(ctx, run.RelationID, model.HealthRecoveryRequired); err != nil {
		log.Printf("recovery: 关系 %s 标记恢复态失败: %v", run.RelationID, err)
	}
	if active.Status == model.TaskStatusQueued || active.Status == model.TaskStatusRunning {
		active.Status = model.TaskStatusRecoveryRequired
		active.MessageKey = taskProgressKey(active.Kind, "recovery_required")
		active.Problem = &model.Problem{Code: CodeRecoveryInProgress, Detail: cause.Error()}
		if _, err := a.runner.Update(ctx, active); err != nil {
			log.Printf("recovery: 任务 %s 恢复终态落库失败: %v", active.TaskID, err)
		}
	}
	log.Printf("recovery: 运行 %s 保持 recovery_required（code=%s）: %v", run.TaskID, code, cause)
}

// advanceRunToApplying 把运行沿成功链步进到 applying（prepared→staged→applying；
// 已在 applying/verifying 的运行不动——verifying 相位由恢复收口在原相位继续，
// 回推 prepared 会被状态机拒绝）。T08 harness 实跑修复：原实现把 verifying 误作
// 「started」起点沿链回退步进，verifying 相位崩溃一律 journal_advance_failed。
func (a *App) advanceRunToApplying(ctx context.Context, taskID, from string) error {
	if from == model.ApplyRunVerifying {
		return nil
	}
	chain := []string{model.ApplyRunPrepared, model.ApplyRunStaged, model.ApplyRunApplying}
	started := false
	for _, s := range chain {
		if s == from {
			started = true
			continue
		}
		if started {
			if err := a.deps.ApplyRuns.AdvanceState(ctx, taskID, s, a.nowStr()); err != nil {
				return err
			}
		}
	}
	if !started {
		return fmt.Errorf("recovery: 运行 %s 相位 %s 不在可恢复链上", taskID, from)
	}
	return nil
}

// listAllJournalOps 按 ordinal 升序取尽任务的全部操作行。
func (a *App) listAllJournalOps(ctx context.Context, taskID string) ([]model.JournalOperation, error) {
	var out []model.JournalOperation
	cursor := ""
	for {
		page, next, err := a.deps.Journal.ListByTask(ctx, taskID, ports.PageRequest{Cursor: cursor, Limit: ports.MaxPageLimit})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" || next == cursor {
			return out, nil
		}
		cursor = next
	}
}

// recoveryActions 按侧构造动作执行器（redo 的执行面）。
func (a *App) recoveryActions(stgRun *syncstage.Run, rootBySide map[model.Side]string) (map[model.Side]*syncstage.Actions, error) {
	out := map[model.Side]*syncstage.Actions{}
	for _, side := range []model.Side{model.SideProject, model.SideRuntime} {
		act, err := syncstage.NewActions(stgRun, rootBySide[side])
		if err != nil {
			return nil, err
		}
		out[side] = act
	}
	return out, nil
}
