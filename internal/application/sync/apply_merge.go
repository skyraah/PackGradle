// write_merged 的暂存期确定性重算（票 #93，ADR-0009 §8）：Apply 暂存期按计划
// 锁定的三侧内容快照重跑同一 diff3（同算法同输入同输出——计划期分类与块证据
// 用的 merge.Texts 纯函数），合并产物字节写进本运行 staging。之后 ownership
// proof、验证、提交、暂存清理与 ADR-0004 恢复协议全走既有管线零新增环节；
// 本文件只做「三侧取数复核 + 重算 + 类型校验」一件事。
package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/core/merge"
	"packgradle/internal/core/model"
)

// mergePhaseTiming 是 merge 分相计时（票 #93，-metrics 增量：diff3/校验/写盘，
// 只记录不设门槛，P4 验收规格 §6）。diff3/校验由 recomputeMergeProduct 计时，
// 写盘相由调用方对 StageContent 计时补齐。
type mergePhaseTiming struct {
	Diff3MS    int64
	ValidateMS int64
	WriteMS    int64
}

// recomputeMergeProduct 按计划锁定的三侧快照重算合并产物：
//   - Base：按计划基线该资源 project 侧表示的内容摘要从 CAS 读对象（与计划期
//     合并判定同一取数口径），sha256 复核；
//   - Project/Runtime：按资源双端同路径读端点活文件，逐侧 sha256 与计划前置
//     条件期望值比对（「双端字节与计划快照相符」的取数面二次复核——前置条件
//     磁盘复核已在 stageOneOperation 先行拦截，此处防取数窗口内的竞态）；
//   - merge.Texts 重跑同一 diff3：出现冲突块 = 计划期判定与重算不一致（算法
//     确定、输入复核相符时不可达），按错误上抛走整场失败恢复面；
//   - ValidateMerged 复跑同一类型校验（toml/json 解码，失败同样上抛）。
//
// 返回合并产物字节、产物 sha256 与分相计时。
func (a *App) recomputeMergeProduct(ctx context.Context, fp *applyFilePlan) ([]byte, string, mergePhaseTiming, error) {
	var timing mergePhaseTiming

	base, err := readVerified(func() ([]byte, error) { return a.readCASObject(ctx, fp.mergeBaseDigest) }, fp.mergeBaseDigest)
	if err != nil {
		return nil, "", timing, fmt.Errorf("基线内容 %s: %w", shortDigest(fp.mergeBaseDigest), err)
	}
	// 双端根按侧装配：合并资源双端同路径（文件资源），重算必须以 project 为
	// A 侧、runtime 为 B 侧（与计划期 diff3 同序，CONTEXT.md 域词汇口径）。
	projRoot, rtRoot := fp.root, fp.sourceRoot
	if fp.targetSide != model.SideProject {
		projRoot, rtRoot = fp.sourceRoot, fp.root
	}
	preBySide := map[string]model.Precondition{}
	for _, pre := range fp.op.Preconditions {
		preBySide[pre.Side] = pre
	}
	fetchEndpoint := func(side model.Side, root string) ([]byte, error) {
		pre, ok := preBySide[string(side)]
		if !ok || pre.Expected == nil {
			return nil, fmt.Errorf("%s 侧计划前置条件缺期望摘要", side)
		}
		return readVerified(func() ([]byte, error) {
			return os.ReadFile(filepath.Join(root, filepath.FromSlash(fp.sourceRel)))
		}, pre.Expected.Digest)
	}
	projText, err := fetchEndpoint(model.SideProject, projRoot)
	if err != nil {
		return nil, "", timing, fmt.Errorf("project 侧 %s: %w", fp.sourceRel, err)
	}
	rtText, err := fetchEndpoint(model.SideRuntime, rtRoot)
	if err != nil {
		return nil, "", timing, fmt.Errorf("runtime 侧 %s: %w", fp.sourceRel, err)
	}

	start := time.Now()
	res := merge.Texts(base, projText, rtText)
	timing.Diff3MS = time.Since(start).Milliseconds()
	if len(res.Hunks) > 0 {
		return nil, "", timing, fmt.Errorf("重算出现 %d 个冲突块（计划期判定干净，输入复核相符时不可达）", len(res.Hunks))
	}

	start = time.Now()
	verr := merge.ValidateMerged(fp.sourceRel, res.Merged)
	timing.ValidateMS = time.Since(start).Milliseconds()
	if verr != nil {
		return nil, "", timing, verr
	}
	sum := sha256.Sum256(res.Merged)
	return res.Merged, hex.EncodeToString(sum[:]), timing, nil
}

// readVerified 调用取数闭包并复核 sha256 与期望摘要一致；失败一律 error
//（执行期与计划期之间的外部写者竞态不产出错误合并产物）。
func readVerified(fetch func() ([]byte, error), wantDigest string) ([]byte, error) {
	data, err := fetch()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, wantDigest) {
		return nil, fmt.Errorf("内容摘要不符（外部写者竞态）: got=sha256:%s want=sha256:%s", got, wantDigest)
	}
	return data, nil
}

// readCASObject 读 CAS 对象全量字节（对象缺失/读取失败上抛）。
func (a *App) readCASObject(ctx context.Context, digest string) ([]byte, error) {
	rc, err := a.deps.CAS.Open(ctx, digest)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// shortDigest 摘要缩略（错误串可读性；诊断面脱敏不涉——digest 非敏感）。
func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12] + "…"
	}
	return d
}

// accumMergePhases 累计一次 write_merged 展开行的重算/写盘分相（staging worker
// 并发到达，applyTimingMu 保护；runApply 收口时并入 LastApplyTiming 供数
// pgheadless -metrics）。
func (a *App) accumMergePhases(t mergePhaseTiming, writeMS int64) {
	a.applyTimingMu.Lock()
	defer a.applyTimingMu.Unlock()
	a.mergeDiff3MS += t.Diff3MS
	a.mergeValidateMS += t.ValidateMS
	a.mergeWriteMS += writeMS
	a.mergeOps++
}

// mergePhasesSnapshot 取本次运行的 merge 分相累计并清零（runApply 的收口
// defer 内调用；recordApplyTiming 随后覆盖 LastApplyTiming）。
func (a *App) mergePhasesSnapshot() (diff3MS, validateMS, writeMS int64, ops int) {
	a.applyTimingMu.Lock()
	defer a.applyTimingMu.Unlock()
	diff3MS, validateMS, writeMS, ops = a.mergeDiff3MS, a.mergeValidateMS, a.mergeWriteMS, a.mergeOps
	a.mergeDiff3MS, a.mergeValidateMS, a.mergeWriteMS, a.mergeOps = 0, 0, 0, 0
	return diff3MS, validateMS, writeMS, ops
}
