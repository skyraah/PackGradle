package sync

// content_ingest.go 实现 ADR-0012 §2/§3 的对象摄取通道（执行规格 §F2 裁定：
// 对象摄取时点＝提交收口期，扫描期不落 CAS 对象）：
//
//   - result baseline 持久化前，对基线项目侧 mod 表示按 Content digest 统一
//     从项目工作树读字节入 CAS（sync/restore/recovery 恢复收口与将来的 merge
//     提交同款通道；Put 幂等，跨提交按内容寻址去重）；
//   - 对象经提交 object_refs（purpose=baseline_content）被 sync_commits 传递
//     引用，自动落 ADR-0006 §10.1 保护根并进入既有容量记账——GC 决策与容量
//     口径零新参数（ADR-0012 §3）；
//   - verify→收口之间的外部写者竞态（读取摘要与表示不符）→ 该表示退无
//     Content（回滚面自然落降级语义）+ 记诊断 + 不失败提交。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// objectRefPurposeBaselineContent 是基线内容摄取引用的 purpose（object_refs
// 行；区别于 before_preservation——前者是结果基线的传递引用通道）。
const objectRefPurposeBaselineContent = "baseline_content"

// ingestBaselineProjectContent 在提交收口期把结果基线项目侧 mod 表示的
// Content 引用对象统一从工作树读字节入 CAS。语义：
//
//   - 读取摘要与表示 Content 不符（外部写者竞态）、文件缺失或读取/哈希失败：
//     该表示退无 Content + 记诊断，不失败提交（降级语义的落点：restore 判定
//     面按无 Content 表示走既有降标通道）；
//   - CAS.Put/实存查询失败：Content 保留（digest 是扫描实测的事实，对象暂缺
//     由 restore 的 CAS 实存判定自然降级），记诊断不失败提交；此时不产引用行
//     （object_refs 只指向 ready 对象，悬挂引用被外键拒绝）；
//   - 对象已实存（上轮提交摄取过、跨提交内容寻址去重）→ 跳过 Put 零成本；
//   - 返回落 object_refs 的引用行（purpose=baseline_content）与降级诊断；
//     引用行由调用方与提交同一事务写入。
//
// 表示指针变异只发生在 baseline 自身的表示副本上（buildVerifiedBaseline/
// buildRestoreResultBaseline 的复扫表示是值拷贝），输入快照的观察事实不受影响。
func (a *App) ingestBaselineProjectContent(ctx context.Context, projectRoot string, baseline *model.SyncBaseline) ([]ports.ObjectRefRow, []model.Diagnostic) {
	if a.deps.CAS == nil || baseline == nil {
		return nil, nil
	}
	// 确定性序：按 ResourceID 字节序遍历（诊断与引用行顺序可复现）。
	ids := make([]model.ResourceID, 0, len(baseline.Resources))
	for id := range baseline.Resources {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var refs []ports.ObjectRefRow
	var diags []model.Diagnostic
	ingested := map[string]bool{} // digest → 对象已确认 ready 实存（本轮内去重）
	for _, id := range ids {
		res := baseline.Resources[id]
		rep := res.ProjectRepresentation
		if rep == nil || rep.Content == nil {
			continue
		}
		if normalize.KindOfResourceID(id) != model.ResourceMod {
			continue // 捕获面仅项目侧 mod 清单（ADR-0012 §2）；文件资源字节经 before 保全入 CAS
		}
		digest := strings.ToLower(rep.Content.Digest)
		if !ingested[digest] {
			ok, err := a.deps.CAS.Has(ctx, digest)
			switch {
			case err != nil:
				diags = append(diags, diagIngest(rep, id, "diag.commit.cas_lookup_failed",
					"CAS 实存查询失败，本表示保持 Content（对象缺失由回滚判定面降级）: "+err.Error()))
			case ok:
				ingested[digest] = true
			default:
				keep, derr := a.ingestProjectFile(ctx, projectRoot, rep, id)
				if derr != nil {
					diags = append(diags, *derr)
				}
				if keep {
					ingested[digest] = true
				} else {
					rep.Content = nil // 摘要与快照不符/文件缺失：表示退无 Content（竞态降级落点）
				}
			}
		}
		if rep.Content != nil && ingested[digest] {
			refs = append(refs, ports.ObjectRefRow{
				Algorithm: "sha256", Digest: digest,
				Purpose: objectRefPurposeBaselineContent, Size: rep.Content.Size,
			})
		}
	}
	for _, d := range diags {
		log.Printf("commit: 基线内容摄取降级 %s（%s）: %s", d.RelativePath, d.Code, d.Detail)
	}
	return refs, diags
}

// ingestProjectFile 从项目工作树读回 metafile 字节、按表示 Content 复核摘要，
// 命中即 Put 入 CAS。返回 keep=true 表示对象已落库（引用行可建立）；
// keep=false 分两种：竞态/不可读（调用方把该表示退无 Content）与 Put 失败
// （调用方保留 Content，对象暂缺由回滚判定面降级）——两种都附诊断。
func (a *App) ingestProjectFile(ctx context.Context, projectRoot string, rep *model.Representation, id model.ResourceID) (keep bool, derr *model.Diagnostic) {
	degrade := func(code, detail string) *model.Diagnostic {
		return &model.Diagnostic{
			Severity: "warning", Code: code,
			Args: []string{rep.RelativePath}, RelativePath: rep.RelativePath,
			ResourceID: id, Detail: detail,
		}
	}
	abs := filepath.Join(projectRoot, filepath.FromSlash(rep.RelativePath))
	// 防御性复核：表示路径来自本端点扫描（root 相对），越出根即数据受损。
	if rel, err := filepath.Rel(projectRoot, abs); err != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, degrade("diag.commit.path_escape", "表示路径越出项目根，拒绝读取")
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return false, degrade("diag.commit.content_unreadable",
			"工作树读取失败，该表示退无 Content: "+err.Error())
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != strings.ToLower(rep.Content.Digest) || int64(len(raw)) != rep.Content.Size {
		return false, degrade("diag.commit.content_mismatch",
			fmt.Sprintf("读取摘要与快照不符（外部写者竞态），该表示退无 Content: got=(sha256:%s,%d) want=(sha256:%s,%d)",
				got, len(raw), strings.ToLower(rep.Content.Digest), rep.Content.Size))
	}
	ref, err := a.deps.CAS.Put(ctx, bytes.NewReader(raw))
	if err != nil {
		return false, degrade("diag.commit.cas_put_failed",
			"CAS 摄取失败，本表示保持 Content（对象缺失由回滚判定面降级）: "+err.Error())
	}
	if !strings.EqualFold(ref.Digest, rep.Content.Digest) {
		return false, degrade("diag.commit.cas_put_mismatch",
			fmt.Sprintf("CAS 落盘复核摘要不符: got=%s want=%s", ref.Digest, rep.Content.Digest))
	}
	return true, nil
}

// diagIngest 构造摄取面的降级诊断（证据形态与扫描诊断同形，随提交摘要落账）。
func diagIngest(rep *model.Representation, id model.ResourceID, code, detail string) model.Diagnostic {
	return model.Diagnostic{
		Severity: "warning", Code: code,
		Args: []string{rep.RelativePath}, RelativePath: rep.RelativePath,
		ResourceID: id, Detail: detail,
	}
}

// ingestDiagEntry 是提交摘要 content_ingest 键的单条降级记录（summary_json
// 内部账目，DTO 投影不读取）。
type ingestDiagEntry struct {
	ResourceID string `json:"resource_id"`
	Code       string `json:"code"`
	Detail     string `json:"detail,omitempty"`
}
