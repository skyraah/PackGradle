package sync

// gc.go 实现 CAS 保留与垃圾回收引擎（票 #64，ADR-0007 §3–§6）：
//
//   - 任务化：kind=gc 走既有任务面（创建/进度/事件/终态全可观测）。触发三通道
//     ①启动后异步（bootstrap Stack.StartGC）②提交收口后廉价检查超 C 才建
//     （runApply 收口接线）③CLI 手动（pgheadless -gc）——三通道一视同仁经
//     RequestGC 入队，全局单飞（同一时刻至多一个 gc 任务排队/执行）。
//   - 安全窗口＝无活跃 Apply/Restore run ∧ 无任何 relation 处于 recovery_required
//     （ADR-0007 §3）。窗口不开任务停 pending（排队文案「等待空闲时段（安全
//     窗口未开 · 自动续排）」），窗口打开（任务终态/恢复处置 kick 或轮询兜底）
//     自动继续执行——排队不拒绝。
//   - 两层模型（§1/§2）：逐关系 core/gc.PlanPruning 连续前缀修剪 → ApplyPrune
//     单事务级联删除（先提交后基线）；对象回收判定始终全局。
//   - 删除协议（§5）：单事务 ready→quarantined（Has() 只认 ready 即刻不可见）
//     → zstd 压缩移入回收站（mtime 即 trash_days 时钟）→ 超期物理清除随删
//     隔离行；Put 幂等复活由 CAS.Put 的 UPSERT 天然承担；GC 全程可重入。
//   - 孤儿三向清扫（§6，GC 末位）：file-without-row 入回收站走时钟 / .tmp-* 直删 /
//     row-without-file 删行对账（被引用行保留——Has() 已不可见，restore 走降级）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/gc"
	"packgradle/internal/core/model"
)

// GC 任务相位推进的消息键（契约 06 §5：排队态文案点名「自动续排」）。
const (
	msgGCWaiting    = "msg.task.gc.waiting"    // 安全窗口未开的排队态
	msgGCPruning    = "msg.task.gc.pruning"    // 修剪历史提交
	msgGCCollecting = "msg.task.gc.collecting" // 隔离无引用对象
	msgGCSweeping   = "msg.task.gc.sweeping"   // 回收站老化 + 孤儿清扫
	msgGCSucceeded  = "msg.task.gc.succeeded"
)

// gcWindowPoll 是安全窗口等待的轮询兜底间隔（kick 通道加速，轮询保证
// 无 kick 源（如手工处置恢复后直接改库）也最终续排）。
const gcWindowPoll = 2 * time.Second

// RequestGC 建 GC 任务并启动引擎（触发通道①③；通道②在收口后另经廉价
// 检查进入本方法）。全局单飞：已有活跃（queued/running）gc 任务时幂等返回
// 既有任务——ConfirmPlan 的活跃重入口径。GC 面未装配（GC/GCTrash 依赖缺失）
// 返回错误：调用方是启动/收口/CLI，不是 transport 面，无需 errs 错误码。
func (a *App) RequestGC(ctx context.Context) (view.TaskView, error) {
	if a.deps.GC == nil || a.deps.GCTrash == nil {
		return view.TaskView{}, fmt.Errorf("gc: 引擎依赖未装配（GCRepository/GCTrash）")
	}
	a.gcMu.Lock()
	defer a.gcMu.Unlock()
	if t, found, err := a.deps.Tasks.FindActiveByKind(ctx, model.TaskKindGC); err == nil && found {
		return TaskView(t), nil // 复用活跃任务（全局单飞，ADR-0007 §3）
	} else if err != nil {
		return view.TaskView{}, err
	}
	// GC 是全局任务：relation_id 置空（tasks.relation_id 可空，NULL 即全局）。
	t, err := a.runner.Create(ctx, "", model.TaskKindGC, true)
	if err != nil {
		return view.TaskView{}, err
	}
	a.startGC(t)
	return TaskView(t), nil
}

// maybeScheduleGCAfterCommit 是触发通道②（ADR-0007 §3）：提交收口后廉价
// 检查本关系占用，超容量锚点 C 才建 GC 任务——非超量场景零 GC 任务开销
// （一次 SUM 查询 + 一次活跃任务查询）。异步建任务不阻塞 apply 收口路径。
func (a *App) maybeScheduleGCAfterCommit(ctx context.Context, relationID string) {
	if a.deps.GC == nil {
		return
	}
	ret := a.retentionSettings()
	if ret.RelationCapacityBytes <= 0 {
		return
	}
	usage, err := a.deps.GC.RelationUsageBytes(ctx, relationID)
	if err != nil {
		log.Printf("gc: 收口后占用检查失败（跳过本轮触发）: %v", err)
		return
	}
	if usage <= ret.RelationCapacityBytes {
		return
	}
	if _, found, err := a.deps.Tasks.FindActiveByKind(ctx, model.TaskKindGC); err != nil || found {
		return // 已有活跃任务（或查询失败保守不建），单飞不重复排队
	}
	go func() {
		if _, err := a.RequestGC(ctxWithoutCancel(ctx)); err != nil {
			log.Printf("gc: 容量超限触发建任务失败: %v", err)
		}
	}()
}

// StartGC 是触发通道①（ADR-0007 §3）：启动后异步建 GC 任务。bootstrap 装配
// 后显式调用（Stack.StartGC），产品入口与验收链共享；测试装配不自动触发。
// 幂等：已有活跃任务（含上轮未完成的排队任务）时 RequestGC 直接复用。
func (a *App) StartGC() {
	go func() {
		if _, err := a.RequestGC(ctxWithoutCancel(context.Background())); err != nil {
			log.Printf("gc: 启动触发失败: %v", err)
		}
	}()
}

// kickGC 唤醒安全窗口等待中的 GC 引擎（任务终态/恢复处置等开窗事件触发
// 复查，ADR-0007 §3；带缓冲单槽，无等待者时丢弃——轮询兜底）。
func (a *App) kickGC() {
	select {
	case a.gcKick <- struct{}{}:
	default:
	}
}

// startGC 在任务创建成功后启动引擎协程（StartScan/ConfirmPlan 的 startApply
// 先例：ctx WithoutCancel 派生，取消句柄注册进 runner 供 CancelTask 触发）。
func (a *App) startGC(t model.Task) {
	gcCtx, cancel := context.WithCancel(ctxWithoutCancel(context.Background()))
	a.runner.RegisterCancel(t.TaskID, cancel)
	go func() {
		defer a.runner.UnregisterCancel(t.TaskID)
		defer a.kickGC() // 引擎退出（终态/取消）也是一次窗口复查事件
		a.runGC(gcCtx, t)
	}()
}

// runGC 执行 GC 四阶段编排：安全窗口等待 → 修剪 → 回收 → 清扫。
func (a *App) runGC(ctx context.Context, queued model.Task) {
	// 接管检查：仅 queued 任务可被引擎拾起（queued 窗口被取消的任务按取消面
	// 收口，引擎不推进）。
	t, err := a.deps.Tasks.Get(ctx, queued.TaskID)
	if err != nil {
		log.Printf("gc: 读取任务 %s 失败，放弃接管: %v", queued.TaskID, err)
		return
	}
	if t.Status != model.TaskStatusQueued {
		return
	}

	// ---- 阶段 0：安全窗口等待（pending 排队，开窗自动续排）----
	if !a.gcWindowOpen(ctx) {
		t.Phase = "waiting"
		t.MessageKey = msgGCWaiting
		// 不能用 :=：块内 shadow 丢掉推进后的 sequence 快照，开窗续排的
		// 首个 Update 必然乐观锁冲突。
		updated, err := a.runner.Update(ctx, t)
		if err != nil {
			log.Printf("gc: 任务 %s 排队文案落库失败: %v", t.TaskID, err)
			return
		}
		t = updated
		if !a.waitGCWindow(ctx) {
			// ctx 取消（CancelTask 或进程退出）：终态由本协程落库。
			a.runner.MarkCancelled(ctx, t)
			return
		}
	}

	// ---- 阶段 1：修剪历史提交（逐关系，ADR-0007 §1/§2）----
	t.Phase = "pruning"
	t.MessageKey = msgGCPruning
	t, err = a.runner.Update(ctx, t)
	if err != nil {
		log.Printf("gc: 任务 %s 推进失败: %v", t.TaskID, err)
		return
	}
	retention := a.retentionSettings()
	rels, _, err := a.deps.Relations.List(ctx, ports.PageRequest{Limit: ports.MaxPageLimit})
	if err != nil {
		a.runner.MarkFailed(ctx, t, "", fmt.Sprintf("gc: 列关系失败: %v", err))
		return
	}
	relationIDs := make([]string, 0, len(rels))
	for _, rel := range rels {
		relationIDs = append(relationIDs, rel.RelationID)
	}
	for _, relID := range relationIDs {
		if ctx.Err() != nil {
			a.runner.MarkCancelled(ctx, t)
			return
		}
		if err := a.pruneRelation(ctx, relID, retention); err != nil {
			a.runner.MarkFailed(ctx, t, "", fmt.Sprintf("gc: 修剪关系 %s 失败: %v", relID, err))
			return
		}
	}

	// ---- 阶段 2：对象回收（全局判定 + 删除协议，ADR-0007 §4/§5）----
	t.Phase = "collecting"
	t.MessageKey = msgGCCollecting
	t, err = a.runner.Update(ctx, t)
	if err != nil {
		log.Printf("gc: 任务 %s 推进失败: %v", t.TaskID, err)
		return
	}
	if err := a.collectObjects(ctx, relationIDs); err != nil {
		a.runner.MarkFailed(ctx, t, "", fmt.Sprintf("gc: 回收对象失败: %v", err))
		return
	}

	// ---- 阶段 3：回收站老化 + 孤儿三向清扫（GC 末位，ADR-0007 §5 步骤 3/§6）----
	t.Phase = "sweeping"
	t.MessageKey = msgGCSweeping
	t, err = a.runner.Update(ctx, t)
	if err != nil {
		log.Printf("gc: 任务 %s 推进失败: %v", t.TaskID, err)
		return
	}
	if err := a.sweepOrphans(ctx, retention); err != nil {
		a.runner.MarkFailed(ctx, t, "", fmt.Sprintf("gc: 清扫失败: %v", err))
		return
	}

	t.Status = model.TaskStatusSucceeded
	t.Phase = "done"
	t.MessageKey = msgGCSucceeded
	if _, err := a.runner.Update(ctx, t); err != nil {
		log.Printf("gc: 任务 %s 成功终态落库失败: %v", t.TaskID, err)
	}
}

// pruneRelation 对单关系计算并执行一次连续前缀修剪（决策归 core/gc 纯函数，
// 执行归 GCRepository.ApplyPrune 单事务；ADR-0007 §1 两层模型的第一层）。
func (a *App) pruneRelation(ctx context.Context, relationID string, retention model.RetentionSettings) error {
	chain, err := a.deps.GC.RelationCommitsChain(ctx, relationID)
	if err != nil {
		return err
	}
	if len(chain) == 0 {
		return nil
	}
	refs, err := a.deps.GC.RelationObjectRefs(ctx, relationID)
	if err != nil {
		return err
	}
	protected, err := a.deps.GC.ProtectedBaselineIDs(ctx, relationID)
	if err != nil {
		return err
	}
	protectedSet := make(map[string]bool, len(protected))
	for _, id := range protected {
		protectedSet[id] = true
	}
	nodes := make([]gc.CommitNode, len(chain))
	for i, c := range chain {
		createdAt, err := time.Parse(time.RFC3339, c.CreatedAt)
		if err != nil {
			return fmt.Errorf("gc: 提交 %s 时间 %s 解析失败: %w", c.CommitID, c.CreatedAt, err)
		}
		nodes[i] = gc.CommitNode{
			CommitID:         c.CommitID,
			ParentID:         c.ParentCommitID,
			ResultBaselineID: c.ResultBaselineID,
			CreatedAt:        createdAt,
		}
	}
	refsIn := make([]gc.ObjectRef, len(refs))
	for i, r := range refs {
		refsIn[i] = gc.ObjectRef{CommitID: r.OwnerID, Digest: r.Digest, Size: r.Size}
	}
	decision := gc.PlanPruning(gc.PruneInput{
		Now:                a.deps.Now(),
		Retention:          retention,
		Commits:            nodes,
		Refs:               refsIn,
		ProtectedBaselines: protectedSet,
	})
	if len(decision.Pruned) == 0 {
		return nil
	}
	return a.deps.GC.ApplyPrune(ctx, relationID, decision.Pruned, decision.DroppedBaselines,
		decision.ReconnectCommitID, decision.ReconnectBaselineID)
}

// collectObjects 执行全局对象回收（ADR-0007 §4 存活集 → §5 删除协议步骤 1/2）。
// 回收判定始终全局（CAS 跨关系去重），锚点只管记账不管回收范围。
func (a *App) collectObjects(ctx context.Context, relationIDs []string) error {
	reach := map[string]bool{}
	for _, relID := range relationIDs {
		refs, err := a.deps.GC.RelationObjectRefs(ctx, relID)
		if err != nil {
			return err
		}
		for _, r := range refs {
			reach[strings.ToLower(r.Digest)] = true
		}
	}
	// 保护根集 1 的基线通道：存活基线 logical_digest 命中 objects 的部分
	//（ADR-0007 §4——基线是 GC 的引用根，留基线不留提交对象永受保护）。
	baselineHits, err := a.deps.GC.BaselineDigestHits(ctx, relationIDs)
	if err != nil {
		return err
	}
	for _, d := range baselineHits {
		reach[strings.ToLower(d)] = true
	}
	// 计划引用通道的对象面：活跃计划 base 基线命中的对象（屏障的第二道
	// 防线——基线行随提交被裁时，其对象在计划活跃期间仍受保护）。
	planHits, err := a.deps.GC.PlanBaseDigestHits(ctx, relationIDs)
	if err != nil {
		return err
	}
	for _, d := range planHits {
		reach[strings.ToLower(d)] = true
	}
	// 保护根集 2/3 的恢复引用通道：活跃/未处置 run 的 run 级与 journal 级
	// recovery_refs（kind=cas 条目）——「进行中 run 的 staging 绑定」即此处
	// 的 cas 引用（staged 内容已进 CAS；ADR-0006 §10 硬约束的 GC 侧口径）。
	runRefs, err := a.deps.GC.UnresolvedRunRefs(ctx)
	if err != nil {
		return err
	}
	journalRefs, err := a.deps.GC.JournalCASRefs(ctx)
	if err != nil {
		return err
	}
	for _, raw := range append(append([][]byte{}, runRefs...), journalRefs...) {
		for _, d := range parseCASRefDigests(raw) {
			reach[strings.ToLower(d)] = true
		}
	}

	// 候选集 = ready 底册 − 存活集；单事务批量隔离（WHERE state='ready' 保
	// 可重入，ADR-0007 §5 步骤 1：Has() 只认 ready，标记完成即不可见）。
	ready, err := a.deps.GC.ReadyDigests(ctx)
	if err != nil {
		return err
	}
	candidates := make([]string, 0, len(ready))
	for _, d := range ready {
		if !reach[strings.ToLower(d)] {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) > 0 {
		if _, err := a.deps.GC.QuarantineObjects(ctx, candidates); err != nil {
			return err
		}
	}

	// 入回收站（步骤 2）：逐个压缩搬运，幂等可重入——
	//   - 对象文件缺失（row-without-file）：改走删行对账（ADR-0007 §6 第三向）；
	//   - trash 副本已在（上轮崩溃残留）：MoveToTrash 只清原文件自然续上；
	//   - Put 幂等复活（行已回 ready）：搬前不重查也不会误删——MoveToTrash
	//     只动对象文件与 trash 副本，复活重物化的文件在行置 ready 后由下一轮
	//     以存活集重算（最坏晚清一轮，绝不误删活引用的账目面）。
	quarantined, err := a.deps.GC.ListQuarantined(ctx)
	if err != nil {
		return err
	}
	for _, q := range quarantined {
		err := a.deps.GCTrash.MoveToTrash(q.Digest)
		if err == nil {
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			// 行在账、文件不在盘：直接删行对账（零引用兜底在仓储 WHERE 内）。
			if err := a.deps.GC.PurgeQuarantinedRows(ctx, []string{q.Digest}); err != nil {
				return err
			}
			continue
		}
		return err
	}
	return nil
}

// sweepOrphans 执行回收站老化清除与孤儿三向清扫（ADR-0007 §5 步骤 3/§6，
// GC 流程末位、存活集计算之后）。
func (a *App) sweepOrphans(ctx context.Context, retention model.RetentionSettings) error {
	// ---- 回收站老化：超 trash_days 的条目删文件随删隔离行（先文件后行，
	// 崩溃残留孤行由下方 row-without-file 对账兜底）。复活过的 digest 其
	// trash 副本到期同样清除；PurgeQuarantinedRows 的零引用 WHERE 保证
	// 已被新提交引用的复活对象行绝不误删。 ----
	entries, err := a.deps.GCTrash.ListTrash()
	if err != nil {
		return err
	}
	expired := gc.ExpiredTrash(a.deps.Now(), trashEntriesOf(entries), retention.TrashDays)
	for _, e := range expired {
		if err := a.deps.GCTrash.DeleteTrashEntry(gcTrashEntryOf(e)); err != nil {
			return err
		}
		if err := a.deps.GC.PurgeQuarantinedRows(ctx, []string{e.Digest}); err != nil {
			return err
		}
	}

	// ---- 三向清扫 ----
	// 账目侧：ready 底册 + 隔离行（行集合）。
	ready, err := a.deps.GC.ReadyDigests(ctx)
	if err != nil {
		return err
	}
	quarantined, err := a.deps.GC.ListQuarantined(ctx)
	if err != nil {
		return err
	}
	rows := map[string]bool{}
	for _, d := range ready {
		rows[strings.ToLower(d)] = true
	}
	for _, q := range quarantined {
		rows[strings.ToLower(q.Digest)] = true
	}

	// 第一向 file-without-row：盘上有文件、账上无行（Put 后事务失败的残留）
	// → 入回收站走 trash_days 时钟（与正常回收同通道，不直接删）。
	files, err := a.deps.GCTrash.ListObjectFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if rows[strings.ToLower(f.Digest)] {
			continue
		}
		if err := a.deps.GCTrash.MoveToTrash(f.Digest); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	// 第二向 .tmp-*：写中断残渣直接删，无账目可挂。
	tmps, err := a.deps.GCTrash.ListTmpFiles()
	if err != nil {
		return err
	}
	for _, p := range tmps {
		if err := a.deps.GCTrash.DeleteFile(p); err != nil {
			return err
		}
	}

	// 第三向 row-without-file：账上有行、盘上无文件 → 被引用行保留（Has()
	// 已按文件缺失不可见，restore 走既有降级分支）；零引用行删行对账。
	onDisk := map[string]bool{}
	for _, f := range files {
		onDisk[strings.ToLower(f.Digest)] = true
	}
	var missing []string
	for d := range rows {
		if !onDisk[d] {
			missing = append(missing, d)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	referenced, err := a.deps.GC.ReferencedMissingRows(ctx, missing)
	if err != nil {
		return err
	}
	refSet := make(map[string]bool, len(referenced))
	for _, d := range referenced {
		refSet[strings.ToLower(d)] = true
	}
	var orphans []string
	for _, d := range missing {
		if !refSet[d] {
			orphans = append(orphans, d)
		}
	}
	return a.deps.GC.PurgeQuarantinedRows(ctx, orphans)
}

// gcWindowOpen 判定安全窗口（ADR-0007 §3）：无活跃/未处置 run ∧ 无任何
// relation 处于 recovery_required。存储读失败保守按窗口关闭处理（下轮重查）。
func (a *App) gcWindowOpen(ctx context.Context) bool {
	unresolved, err := a.deps.GC.HasUnresolvedRuns(ctx)
	if err != nil {
		log.Printf("gc: 查未收口运行失败（保守视为窗口未开）: %v", err)
		return false
	}
	if unresolved {
		return false
	}
	rels, _, err := a.deps.Relations.List(ctx, ports.PageRequest{Limit: ports.MaxPageLimit})
	if err != nil {
		log.Printf("gc: 列关系失败（保守视为窗口未开）: %v", err)
		return false
	}
	for _, rel := range rels {
		if rel.Health == model.HealthRecoveryRequired {
			return false
		}
	}
	return true
}

// waitGCWindow 阻塞等待安全窗口打开：kick（任务终态/恢复处置）加速 + 轮询
// 兜底。返回 false = ctx 取消（进程退出/任务取消），调用方按取消收口。
func (a *App) waitGCWindow(ctx context.Context) bool {
	ticker := time.NewTicker(gcWindowPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-a.gcKick:
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return false
		}
		if a.gcWindowOpen(ctx) {
			return true
		}
	}
}

// ReviveObject 人工复活（ADR-0007 §5「GC 误收的最后一道保险」，CLI 形态
// pgheadless -revive）：回收站副本解压回 objects + 隔离行置回 ready。
// 两步各自幂等，任意时点崩溃重跑续上。
func (a *App) ReviveObject(ctx context.Context, digest string) error {
	if a.deps.GC == nil || a.deps.GCTrash == nil {
		return fmt.Errorf("gc: 引擎依赖未装配（GCRepository/GCTrash）")
	}
	if err := a.deps.GCTrash.RestoreFromTrash(digest); err != nil {
		return err
	}
	return a.deps.GC.RestoreObject(ctx, digest)
}

// parseCASRefDigests 从恢复引用 JSON（引擎定义形状：[{"kind":"cas",
// "algorithm":...,"digest":...}, ...]，apply.go staged 收集同源）解析全部
// kind=cas 条目的 digest；解析失败返回空（引用原文非引擎写入时不可解，
// 不阻塞 GC——恢复引用另有 journal 原文与 run 级冗余）。
func parseCASRefDigests(raw []byte) []string {
	var refs []map[string]string
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil
	}
	var out []string
	for _, r := range refs {
		if r["kind"] == "cas" && r["digest"] != "" {
			out = append(out, r["digest"])
		}
	}
	return out
}

// trashEntriesOf / gcTrashEntryOf 在 ports 与 core/gc 的条目结构间转换
//（同形异型：决策包不依赖 application 端口）。
func trashEntriesOf(in []ports.GCTrashEntry) []gc.TrashEntry {
	out := make([]gc.TrashEntry, len(in))
	for i, e := range in {
		out[i] = gc.TrashEntry{Digest: e.Digest, ModifiedAt: e.ModifiedAt}
	}
	return out
}

func gcTrashEntryOf(e gc.TrashEntry) ports.GCTrashEntry {
	return ports.GCTrashEntry{Digest: e.Digest, ModifiedAt: e.ModifiedAt}
}
