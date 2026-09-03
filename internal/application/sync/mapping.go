package sync

import (
	"context"

	"packgradle/internal/application/policy"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// 错误码（契约 03 §3；票 #20）。文案由前端 locale 提供。
const (
	// CodeMappingNotFound 是关系无策略（理论上不可达：CreateRelation 事务内直写初始 policy）。
	CodeMappingNotFound = "err.mapping.not_found"
	// CodeMappingStaleRevision 是乐观锁冲突（args {0}=expected, {1}=actual）。
	CodeMappingStaleRevision = "err.mapping.stale_revision"
)

// policyView 汇集读投影组装（rules 归一空切片；RelationRevision 为乐观锁取值来源）。
func policyView(p model.MappingPolicy, relationRevision int) view.PolicyView {
	rules := p.Rules
	if rules == nil {
		rules = []model.MappingRule{}
	}
	return view.PolicyView{
		SchemaVersion:    model.CurrentSchemaVersion,
		PolicyID:         p.PolicyID,
		PolicyRevision:   p.Revision,
		Rules:            rules,
		RelationRevision: relationRevision,
	}
}

// GetMappingPolicy 读取关系的当前映射策略（契约 03 §2.3）。
// RelationRevision 随视图返回，供 mappings 页作为乐观锁 expected_revision 回传。
func (a *App) GetMappingPolicy(ctx context.Context, relationID string) (view.PolicyView, error) {
	rel, err := a.deps.Relations.Get(ctx, relationID)
	if err != nil {
		return view.PolicyView{}, errs.New(CodeRelationNotFound, relationID)
	}
	p, err := a.deps.Mappings.GetPolicy(ctx, relationID)
	if err != nil {
		return view.PolicyView{}, errs.New(CodeMappingNotFound, relationID)
	}
	return policyView(p, rel.Revision), nil
}

// UpdateMappingPolicy 保存映射策略修改（契约 03 §2.3，票 #20）：编译校验、
// 乐观锁校验与 SavePolicy（UPSERT + 同事务递增 relations.revision，ADR-0002
// 决议 2）收进单个 SQLite 事务——中途失败整体回滚，修订号只在成功保存后前进，
// 旧 Plan 随修订递增立即 stale（§8.3）。Rules 整体替换；策略集身份
// （PolicyID/模板 Revision）保持当前策略不变（ADR-0002 决议 5）。
func (a *App) UpdateMappingPolicy(ctx context.Context, input view.UpdateMappingPolicyInput) (view.PolicyView, error) {
	var out view.PolicyView
	err := a.deps.Tx.RunInTx(ctx, func(repos ports.Repos) error {
		rel, err := repos.Relations.Get(ctx, input.RelationID)
		if err != nil {
			return errs.New(CodeRelationNotFound, input.RelationID)
		}
		cur, err := repos.Mappings.GetPolicy(ctx, input.RelationID)
		if err != nil {
			return errs.New(CodeMappingNotFound, input.RelationID)
		}
		next := model.MappingPolicy{
			SchemaVersion: model.CurrentSchemaVersion,
			PolicyID:      cur.PolicyID,
			Revision:      cur.Revision,
			Rules:         input.Rules,
		}
		if next.Rules == nil {
			next.Rules = []model.MappingRule{}
		}
		// 编译校验（契约 03 §2.3：T04 编译器守门）先于修订检查——纯函数，
		// 失败即回滚，修订号不前进；违规规则与原因进 err.mapping.compile_failed
		if cerr := policy.Validate(next); cerr != nil {
			return policyCompileError(cerr)
		}
		// 乐观锁：ExpectedRevision 必须等于当前关系修订（存储层权威值）
		if rel.Revision != input.ExpectedRevision {
			return errs.New(CodeMappingStaleRevision, input.ExpectedRevision, rel.Revision)
		}
		if err := repos.Mappings.SavePolicy(ctx, input.RelationID, next); err != nil {
			return err
		}
		saved, err := repos.Relations.Get(ctx, input.RelationID)
		if err != nil {
			return err
		}
		out = policyView(next, saved.Revision)
		return nil
	})
	if err != nil {
		return view.PolicyView{}, err
	}
	// 监听面是 policy 的函数（ADR-0010 §3，票 #92）：policy 修改 → 重挂监听。
	a.kickWatch()
	return out, nil
}
