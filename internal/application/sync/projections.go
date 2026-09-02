package sync

// Apply 运行与历史读投影（契约 05 §2/§3.2/§3.3/§3.5；票 #39）。
// 只读用例：预置 store 行即可测，不依赖 Apply 引擎（T05+）。
// 白名单纪律（契约 05 §0 硬约束 4 / ADR-0004 §4）：普通用户视图不透出
// 临时路径（temp_relative_path）与 ownership proof（ownership_proof_json）。

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// 错误码（契约 05 §6；票 #39）。文案由前端 locale 提供。
const (
	// CodeApplyNoRun 是 GetApplyRun 于该关系无任何 Apply 运行记录（args {0}=relation_id）。
	CodeApplyNoRun = "err.apply.no_run"
	// CodeApplyRunNotFound 是 ListApplyOperations 的 task 不存在或跨关系（args {0}=task_id）。
	CodeApplyRunNotFound = "err.apply.run_not_found"
	// CodeCommitNotFound 是 GetCommit 的记录不存在或跨关系（args {0}=commit_id）。
	CodeCommitNotFound = "err.commit.not_found"
)

// GetApplyRun 返回该关系当前/最近一次 Apply 运行头（created_at 最新，task_id 决胜；
// 契约 05 §3.2）。消费方：恢复详情页、工作区横幅、plans 页进度区。
func (a *App) GetApplyRun(ctx context.Context, relationID string) (view.ApplyRunView, error) {
	if _, err := a.deps.Relations.Get(ctx, relationID); err != nil {
		return view.ApplyRunView{}, errs.New(CodeRelationNotFound, relationID)
	}
	run, ok, err := a.deps.ApplyRuns.LatestByRelation(ctx, relationID)
	if err != nil {
		return view.ApplyRunView{}, err
	}
	if !ok {
		return view.ApplyRunView{}, errs.New(CodeApplyNoRun, relationID)
	}
	return applyRunView(run), nil
}

// ListApplyOperations 按 ordinal 升序分页返回一次运行的逐操作白名单清单
// （契约 05 §3.3；恢复详情页逐资源证据）。cursor 协议与 GetChanges 一致：
// cursor 为上一页末条 operation_id，筛选条件跨页不变；task 不存在或跨关系
// → err.apply.run_not_found。
func (a *App) ListApplyOperations(ctx context.Context, input view.ListApplyOperationsInput) (view.ApplyOperationPage, error) {
	if _, err := a.deps.Relations.Get(ctx, input.RelationID); err != nil {
		return view.ApplyOperationPage{}, errs.New(CodeRelationNotFound, input.RelationID)
	}
	run, err := a.deps.ApplyRuns.Get(ctx, input.TaskID)
	if err != nil || run.RelationID != input.RelationID {
		// 不存在与跨关系同一口径（契约 05 §3.3），不泄露其他关系的运行存在性。
		return view.ApplyOperationPage{}, errs.New(CodeApplyRunNotFound, input.TaskID)
	}
	// 仓储按 ordinal 分页：先把 operation_id cursor 译为 ordinal 再取页；
	// cursor 操作不存在（陈旧/未知）→ 从首页开始，与 GetChanges 的宽容口径一致。
	ordinalCursor := ""
	if input.Cursor != "" {
		if op, gerr := a.deps.Journal.GetOperation(ctx, input.TaskID, input.Cursor); gerr == nil {
			ordinalCursor = strconv.Itoa(op.Ordinal)
		}
	}
	ops, nextOrdinal, err := a.deps.Journal.ListByTask(ctx, input.TaskID, ports.PageRequest{Cursor: ordinalCursor, Limit: input.Limit})
	if err != nil {
		return view.ApplyOperationPage{}, err
	}
	page := view.ApplyOperationPage{
		SchemaVersion: model.CurrentSchemaVersion,
		Items:         make([]view.ApplyOperationView, 0, len(ops)),
	}
	for _, op := range ops {
		page.Items = append(page.Items, applyOperationView(op))
	}
	if nextOrdinal != "" && len(ops) > 0 {
		page.NextCursor = ops[len(ops)-1].OperationID
	}
	return page, nil
}

// ListCommits 按 created_at DESC 分页返回该关系的提交历史（契约 05 §3.5）；
// cursor 为上一页末条 commit_id（GetChanges 同协议）。仓储按 id 升序分页读
// 提交头，这里内部游标链收集后按 (created_at, id) 降序全序切片——提交是
// 不可变事实，行数有限。
func (a *App) ListCommits(ctx context.Context, relationID string, page ports.PageRequest) (view.CommitPage, error) {
	if _, err := a.deps.Relations.Get(ctx, relationID); err != nil {
		return view.CommitPage{}, errs.New(CodeRelationNotFound, relationID)
	}
	var all []model.SyncCommit
	cursor := ""
	for {
		items, next, err := a.deps.Commits.ListByRelation(ctx, relationID, ports.PageRequest{Cursor: cursor, Limit: ports.MaxPageLimit})
		if err != nil {
			return view.CommitPage{}, err
		}
		all = append(all, items...)
		if next == "" {
			break
		}
		cursor = next
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt != all[j].CreatedAt {
			return all[i].CreatedAt > all[j].CreatedAt
		}
		return all[i].CommitID > all[j].CommitID
	})
	start := 0
	if page.Cursor != "" {
		for i, c := range all {
			if c.CommitID == page.Cursor {
				start = i + 1
				break
			}
		}
		// cursor 不在集合中（陈旧/未知）→ 从首页开始，与 GetChanges 的宽容口径一致。
	}
	limit := page.NormalizeLimit()
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	out := view.CommitPage{
		SchemaVersion: model.CurrentSchemaVersion,
		Items:         make([]view.CommitSummaryView, 0, end-start),
	}
	for _, c := range all[start:end] {
		out.Items = append(out.Items, commitSummaryView(c))
	}
	if end < len(all) {
		out.NextCursor = all[end-1].CommitID
	}
	// 墓碑计数（契约 06 §3.8，票 #64）：按保留策略已清理的提交数。GC 面未装配
	// （既有测试装配）或读失败时退 0——墓碑是增强投影，不阻断历史主链路。
	if a.deps.GC != nil {
		n, err := a.deps.GC.PrunedBeforeCount(ctx, relationID)
		if err != nil {
			log.Printf("gc: 墓碑计数读取失败（按 0 投影）: %v", err)
		} else {
			out.PrunedBeforeCount = n
		}
	}
	return out, nil
}

// GetCommit 返回单提交详情，changes 全量（单 commit 资源数有限，不分页；
// 契约 05 §3.5）。表示摘要经资源身份联取由仓储完成（GetForRelation 联
// resource_representations）；commit 不存在或跨关系 → err.commit.not_found。
func (a *App) GetCommit(ctx context.Context, relationID, commitID string) (view.CommitView, error) {
	if _, err := a.deps.Relations.Get(ctx, relationID); err != nil {
		return view.CommitView{}, errs.New(CodeRelationNotFound, relationID)
	}
	c, err := a.deps.Commits.GetForRelation(ctx, commitID, relationID)
	if err != nil {
		return view.CommitView{}, errs.New(CodeCommitNotFound, commitID)
	}
	changes := make([]view.CommitChangeView, 0, len(c.Changes))
	for _, ch := range c.Changes {
		changes = append(changes, view.CommitChangeView{
			ResourceID:    string(ch.ResourceID),
			ChangeKind:    ch.ChangeKind,
			ProjectBefore: representationSummary(ch.ProjectBefore),
			ProjectAfter:  representationSummary(ch.ProjectAfter),
			RuntimeBefore: representationSummary(ch.RuntimeBefore),
			RuntimeAfter:  representationSummary(ch.RuntimeAfter),
		})
	}
	return view.CommitView{
		SchemaVersion: model.CurrentSchemaVersion,
		Summary:       commitSummaryView(c),
		PlanID:        c.PlanID,
		Changes:       changes,
		Skipped:       commitSkippedView(c.Summary),
	}, nil
}

// commitSkippedView 从提交头 summary JSON 解析跳过清单（票 #63：sync 剔除语义
// 的透出面；引擎定义形状原样保存，解析失败/旧行无该记录返回空切片——摘要
// 是诊断性数据，解析缺陷不阻断提交详情读取）。
func commitSkippedView(summary json.RawMessage) []view.CommitSkippedView {
	if len(summary) == 0 {
		return []view.CommitSkippedView{}
	}
	var parsed struct {
		Skipped []struct {
			ResourceID string   `json:"resource_id"`
			ReasonCode string   `json:"reason_code"`
			ReasonArgs []string `json:"reason_args"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(summary, &parsed); err != nil {
		return []view.CommitSkippedView{}
	}
	out := make([]view.CommitSkippedView, 0, len(parsed.Skipped))
	for _, s := range parsed.Skipped {
		args := s.ReasonArgs
		if args == nil {
			args = []string{}
		}
		out = append(out, view.CommitSkippedView{
			ResourceID: s.ResourceID, ReasonCode: s.ReasonCode, ReasonArgs: args,
		})
	}
	return out
}

// applyRunView 把运行头投影为视图（字段直映，无临时路径/proof 通道）。
func applyRunView(run model.ApplyRun) view.ApplyRunView {
	return view.ApplyRunView{
		SchemaVersion:  model.CurrentSchemaVersion,
		TaskID:         run.TaskID,
		RelationID:     run.RelationID,
		PlanID:         run.PlanID,
		PlanDigest:     run.PlanDigest,
		State:          run.State,
		OperationCount: run.OperationCount,
		StagingCleared: run.StagingCleared,
		AcknowledgedAt: run.AcknowledgedAt,
		CommitID:       run.CommitID,
		CreatedAt:      run.CreatedAt,
		UpdatedAt:      run.UpdatedAt,
	}
}

// applyOperationView 把 journal 当前行投影为白名单清单行（契约 05 §0 硬约束 4）：
// 只取 root-relative 目标路径与计划操作身份，绝不携带 temp_relative_path 与
// ownership_proof_json（二者留在 journal 行内，仅恢复器消费）。ResourceID 与
// ChangeKind 读 operation_json（计划操作 model.PlannedOperation 的持久化形状）；
// ResultCode 读 result_json 顶层 code（引擎定义形状，缺省/解析失败为空）。
func applyOperationView(op model.JournalOperation) view.ApplyOperationView {
	v := view.ApplyOperationView{
		OperationID:  op.OperationID,
		Ordinal:      op.Ordinal,
		Status:       op.Status,
		RelativePath: op.TargetRelativePath,
	}
	if len(op.Operation) > 0 {
		var planned struct {
			Kind       string `json:"kind"`
			ResourceID string `json:"resource_id"`
		}
		if json.Unmarshal(op.Operation, &planned) == nil {
			v.ChangeKind = planned.Kind
			v.ResourceID = planned.ResourceID
		}
	}
	if len(op.Result) > 0 {
		var result struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(op.Result, &result) == nil {
			v.ResultCode = result.Code
		}
	}
	return v
}

// commitSummaryView 把提交头投影为历史列表行。
func commitSummaryView(c model.SyncCommit) view.CommitSummaryView {
	return view.CommitSummaryView{
		CommitID:           c.CommitID,
		Kind:               c.CommitKind,
		Completeness:       c.Completeness,
		RemainingChangeCnt: c.RemainingChangeCount,
		CreatedAt:          c.CreatedAt,
	}
}

// representationSummary 把表示压成单行展示摘要（契约 05 §3.5「表示摘要」）：
// "<relative_path> <format>[ <algorithm>:<digest>]"；nil 表示 → nil
// （DTO omitempty 序列化为 null 缺省）。
func representationSummary(rep *model.Representation) *string {
	if rep == nil {
		return nil
	}
	s := strings.TrimSpace(rep.RelativePath + " " + rep.Format)
	if rep.Content != nil {
		s += " " + rep.Content.Algorithm + ":" + rep.Content.Digest
	}
	return &s
}
