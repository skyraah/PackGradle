package watch

// 引擎（ADR-0010 §4/§5/§6/§7，票 #92）：常驻监听全部健康 relation 的管辖目录，
// OS 事件经触发器状态机聚合后自动调 QuickUpdate 用例（#86，不复制编排、不绕
// 任务互斥）。职责分块：
//   - 挂载面：MappingPolicy 的函数（surface.go）+ 单事件源多目录 + 最近存在
//     父目录回退、目录再现重挂、递归子目录动态补挂；
//   - 触发面：静默期 1.5s + 上限 10s、inflight 单飞 + dirty 补轮（trigger.go）；
//   - 防打转：自动执行连败 2 次暂停（手动快速更新成功复位，不做定时重试）；
//   - 错误面：目录消失回退父目录；挂载持续失败/事件源异常 → 有限次重建 →
//     仍败发 watch_failed（契约 04 §2.5 预留形状），降级=回手动。
//
// 并发模型：loop goroutine（run 及其同步调用的挂载/触发路径）是挂载状态的
// 唯一写者；自动链在独立 goroutine 执行（长操作不经 mu），收口经 settleChain
// 短临界区回写；外部入口 Kick/NotifyQuickUpdateResult/WatchStatus 亦短临界区。
// rels 内可变字段一律持 mu 访问。

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/fsutil"
)

// AutoChain 是自动链入口缝：生产装配=QuickUpdate 用例（#86）包装；测试注入
// 假链。返回收口三态 outcome（no_diff|apply_started|awaiting_confirmation）
// 与 apply 任务 id（apply_started 时非空，成败事实在任务终态）。
type AutoChain func(ctx context.Context, relationID string) (outcome, applyTaskID string, err error)

// 防打转与错误面常量（ADR-0010 §6/§7）：
const (
	// autoFailPauseAt 是自动执行连败暂停阈值（2 次容得下偶发抖动，又不让
	// 持续性故障空转 IO）。
	autoFailPauseAt = 2
	// mountFailUnavailableAt 是挂载连续失败转 unavailable 的阈值（有限重建）。
	mountFailUnavailableAt = 3
	// maxSourceRebuilds 是事件源异常后的有限重建次数（仍败→unavailable）。
	maxSourceRebuilds = 3
	// defaultPollInterval 是 apply 终态轮询间隔（与 quickupdate 链内等待同型）。
	defaultPollInterval = 50 * time.Millisecond
)

// outcomeApplyStarted 是 QuickUpdate 收口三态之一（#86 用例常量的本地镜像：
// watch 包不 import sync，链缝由 bootstrap 注入时按三态字符串翻译）。
const outcomeApplyStarted = "apply_started"

// watch_status 三态（契约 07 §3.2；空串=未挂载）。
const (
	StatusActive      = "active"
	StatusUnavailable = "unavailable"
	StatusPaused      = "paused"
)

// Deps 是引擎依赖（bootstrap 装配；测试注入假实现）。
type Deps struct {
	Relations ports.RelationRepository
	Endpoints ports.EndpointRepository
	Mappings  ports.MappingRepository
	// Tasks 供 apply 终态等待（自动链成败事实源）；nil 时跳过等待。
	Tasks ports.TaskRepository
	// Source 是初始 OS 事件源；NewSource 是异常后的重建工厂（nil=不重建，
	// 直接转 unavailable）。
	Source    ports.DirEventSource
	NewSource func() (ports.DirEventSource, error)
	// Chain 是自动链缝（生产=QuickUpdate 包装）。
	Chain AutoChain
	// PublishWatchFailed 发 watch_failed（契约 04 §2.5：envelope 带
	// relation_id、payload {}；nil=不发，仅状态投影）。
	PublishWatchFailed func(ctx context.Context, relationID string) error
	// Now 是时钟缝（假时钟单测注入）。
	Now func() time.Time
	// Quiesce/MaxWait/PollInterval：0 取编译期默认（测试注入短值）。
	Quiesce      time.Duration
	MaxWait      time.Duration
	PollInterval time.Duration
}

// Engine 是监听引擎（应用运行期常驻；bootstrap 启动、Stack.Close 收敛）。
type Engine struct {
	deps Deps
	// src 是当前事件源（仅 loop goroutine 读写；重建时整体替换）。
	src ports.DirEventSource

	ctx      context.Context
	cancel   context.CancelFunc
	chainCtx context.Context // 自动链用：不随 Stop 取消（链收口完整性优先）
	kickCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	started  atomic.Bool // Go 已调用（Stop 的等待以 loop 真正启动为前提）

	// mu 保护 rels 及其全部可变字段。
	mu         sync.Mutex
	rels       map[string]*relState
	registered map[string]int // 事件源注册引用计数（多关系可回退同一父目录）
}

// relState 是单关系的监听状态。
type relState struct {
	id string
	// status 是 watch_status 投影（""|active|unavailable|paused）。
	status string
	// failStreak 是自动物化连败计数（成功清零；达阈值暂停）。
	failStreak int
	// mountFails 是挂载连续失败计数（有注册成功即清零；达阈值 unavailable）。
	mountFails int
	// failSent 是 unavailable 下 watch_failed 已发标记（恢复后复位可再发）。
	failSent bool
	trig     trigger
	// targets 是语义监听面（policy 的函数输出；policy 修改后重算）。
	targets []SurfaceTarget
	// watchPaths 是实际注册路径 → 其服务的管辖根目录（回退注册点与展开
	// 补挂的子目录都归属语义管辖根，重挂按根整体迁移）。
	watchPaths map[string]string
	// projRoot/rtRoot 是日志脱敏的别名锚（ADR-0011 §7 R1）。
	projRoot, rtRoot string
}

// New 构造引擎（Go 启动）。
func New(deps Deps) (*Engine, error) {
	if deps.Relations == nil || deps.Endpoints == nil || deps.Mappings == nil ||
		deps.Source == nil || deps.Chain == nil || deps.Now == nil {
		return nil, errWatchDeps{}
	}
	if deps.Quiesce <= 0 {
		deps.Quiesce = QuiescePeriod
	}
	if deps.MaxWait <= 0 {
		deps.MaxWait = MaxWaitPeriod
	}
	if deps.PollInterval <= 0 {
		deps.PollInterval = defaultPollInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		deps:       deps,
		src:        deps.Source,
		ctx:        ctx,
		cancel:     cancel,
		chainCtx:   context.WithoutCancel(ctx),
		kickCh:     make(chan struct{}, 1),
		done:       make(chan struct{}),
		rels:       map[string]*relState{},
		registered: map[string]int{},
	}, nil
}

type errWatchDeps struct{}

func (errWatchDeps) Error() string {
	return "watch: 缺少依赖（Relations/Endpoints/Mappings/Source/Chain/Now）"
}

// Go 启动常驻 goroutine（bootstrap 专用，调用一次）。
func (e *Engine) Go() {
	e.started.Store(true)
	go e.run()
}

// Stop 停止引擎并释放事件源（幂等；loop 已启动时阻塞到其退出——栈装配了
// 引擎但未 StartWatcher 时（headless 工具）直接释放事件源，零等待）。
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		e.cancel()
		if e.started.Load() {
			<-e.done
		}
		_ = e.src.Close()
	})
}

// Kick 异步请求全量重挂（relation 建立/重绑/删除、policy 修改、任务终态健康
// 漂移、链收口自愈都归这条通道；带缓冲单槽，无等待者时丢弃——下轮续挂）。
func (e *Engine) Kick() {
	select {
	case e.kickCh <- struct{}{}:
	default:
	}
}

// WatchStatus 投影 watch_status（契约 07 §3.2：active|unavailable|paused，
// 空串=未挂载）。GetWorkspace 经 sync.App.AttachWatch 读取。
func (e *Engine) WatchStatus(relationID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	rel := e.rels[relationID]
	if rel == nil {
		return ""
	}
	return rel.status
}

// NotifyQuickUpdateResult 订阅手动快速更新收口（契约 07 §3.2：paused 由手动
// 快速更新成功复位）：成功 → 连败清零、paused 复位 active、待决失效清空
//（链刚做过全量扫描，挂起的失效事实已随消化）；失败不动自动面（连败只计
// 自动执行）。transport SyncService.QuickUpdate 收口后调用。
func (e *Engine) NotifyQuickUpdateResult(relationID string, chainErr error) {
	if chainErr != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rel := e.rels[relationID]
	if rel == nil {
		return
	}
	rel.failStreak = 0
	if rel.status == StatusPaused {
		rel.status = StatusActive
		slog.Info("watch: 手动快速更新成功，自动面复位 active", "relation", relationID)
	}
	rel.trig.clear()
}

// ---- 常驻 loop ----

// run 是常驻主循环：事件源、重挂 kick、到点触发、退出收敛四路 select。
func (e *Engine) run() {
	defer close(e.done)
	e.resyncAll() // 启动初挂
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	var timerC <-chan time.Time
	for {
		timerC = e.armTimer(timer, timerC)
		select {
		case ev, ok := <-e.src.Events():
			if !ok {
				e.handleSourceLost() // 事件通道关闭=事件源死亡：有限重建
				continue
			}
			e.handleEvent(ev)
		case err, ok := <-e.src.Errors():
			if ok && err != nil {
				slog.Warn("watch: 事件源错误（尝试重建）", "err", err)
				e.handleSourceLost()
			}
		case <-timerC:
			timerC = nil
			e.fireDue()
		case <-e.kickCh:
			e.resyncAll()
		case <-e.ctx.Done():
			e.unmountAll()
			return
		}
	}
}

// armTimer 停掉旧装载后按最近触发 deadline 重装；无待决返回 nil（空转）。
func (e *Engine) armTimer(timer *time.Timer, cur <-chan time.Time) <-chan time.Time {
	if cur != nil {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	now := e.deps.Now()
	var earliest time.Time
	e.mu.Lock()
	for _, rel := range e.rels {
		if dl, ok := rel.trig.deadline(); ok && (earliest.IsZero() || dl.Before(earliest)) {
			earliest = dl
		}
	}
	e.mu.Unlock()
	if earliest.IsZero() {
		return nil
	}
	d := earliest.Sub(now)
	if d < 0 {
		d = 0
	}
	timer.Reset(d)
	return timer.C
}

// fireDue 发射全部到点关系（守卫：暂停/不可用/恢复期不发射，失效保持待决——
// 监听保持、标脏，ADR-0010 §4/§6）。
func (e *Engine) fireDue() {
	now := e.deps.Now()
	var toFire []string
	e.mu.Lock()
	for id, rel := range e.rels {
		if rel.status == StatusActive && rel.trig.fireable(now) {
			toFire = append(toFire, id)
		}
	}
	e.mu.Unlock()

	for _, id := range toFire {
		if !e.chainGate(id) {
			continue // 恢复期/关系漂移：保持待决不物化
		}
		e.mu.Lock()
		rel := e.rels[id]
		if rel == nil || rel.status != StatusActive || !rel.trig.fireable(e.deps.Now()) {
			e.mu.Unlock()
			continue
		}
		rel.trig.start()
		e.mu.Unlock()
		go e.runChain(id)
	}
}

// chainGate 是发射前的关系健康守卫：恢复期触发只标脏不自动物化（ADR-0010
// §4，挂载保持）；关系已消失/漂移不发射。返回 false 时待决失效保持。
func (e *Engine) chainGate(relationID string) bool {
	ctx, cancel := context.WithTimeout(e.chainCtx, 5*time.Second)
	defer cancel()
	rel, err := e.deps.Relations.Get(ctx, relationID)
	if err != nil {
		return false
	}
	return rel.Health == model.HealthHealthy
}

// runChain 执行一次自动链：调 QuickUpdate 同一用例（不复制编排、不绕任务
// 互斥）；apply_started 时等 apply 任务终态——自动物化的成败事实在任务终态，
// 且终态前不发射下轮（避免自撞任务互斥把 already_running 误计成连败）。
func (e *Engine) runChain(relationID string) {
	outcome, applyTaskID, err := e.deps.Chain(e.chainCtx, relationID)
	if err == nil && outcome == outcomeApplyStarted && applyTaskID != "" && e.deps.Tasks != nil {
		if werr := e.waitApplyTerminal(applyTaskID); werr != nil {
			err = werr
		}
	}
	if err != nil {
		slog.Warn("watch: 自动链失败（计入连败）", "relation", relationID, "err", err)
	} else {
		slog.Info("watch: 自动链收口", "relation", relationID, "outcome", outcome)
	}
	e.settleChain(relationID, err == nil)
	e.Kick() // 链后重挂（健康/policy 漂移自愈）
}

// settleChain 链收口回写：连败计数/暂停（ADR-0010 §6）+ dirty 补轮（风暴
// ≤2 轮，与受控重查同型）。
func (e *Engine) settleChain(relationID string, success bool) {
	now := e.deps.Now()
	e.mu.Lock()
	rel := e.rels[relationID]
	if rel == nil {
		e.mu.Unlock()
		return
	}
	if success {
		rel.failStreak = 0
	} else {
		rel.failStreak++
		if rel.failStreak >= autoFailPauseAt && rel.status != StatusPaused {
			rel.status = StatusPaused
			slog.Warn("watch: 自动物化连败暂停（监听保持、标脏；手动快速更新成功即复位）",
				"relation", relationID, "fail_streak", rel.failStreak)
		}
	}
	rel.trig.settle(now)
	e.mu.Unlock()
}

// waitApplyTerminal 轮询 apply 任务到终态（任务持久化状态是事实源）。
func (e *Engine) waitApplyTerminal(taskID string) error {
	ctx := e.chainCtx
	for {
		t, err := e.deps.Tasks.Get(ctx, taskID)
		if err != nil {
			return err
		}
		switch t.Status {
		case model.TaskStatusSucceeded:
			return nil
		case model.TaskStatusFailed, model.TaskStatusRecoveryRequired, model.TaskStatusCancelled:
			if t.Problem != nil {
				return &chainTaskError{status: t.Status, msg: t.Problem.Detail}
			}
			return &chainTaskError{status: t.Status}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.deps.PollInterval):
		}
	}
}

// chainTaskError 是 apply 任务终态失败包装（detail 只进日志，不外露新错误码）。
type chainTaskError struct{ status, msg string }

func (err *chainTaskError) Error() string {
	if err.msg != "" {
		return "apply 任务终态 " + err.status + ": " + err.msg
	}
	return "apply 任务终态 " + err.status
}

// ---- 事件处理 ----

// handleEvent 处理一次 OS 事件：语义匹配触发失效 + 挂载面维护（回退/重挂/
// 补挂）。仅 chmod 不构成变化（legacy 同判）。
func (e *Engine) handleEvent(ev ports.DirEvent) {
	if ev.Path == "" || ev.Op == ports.DirChmod {
		return
	}
	now := e.deps.Now()

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rel := range e.rels {
		// ① 触发面：事件落在语义管辖范围 → 失效（暂停/不可用期照常积压，
		// 触发被 fireDue 的 status 守卫拦截——监听保持、标脏）。
		for _, t := range rel.targets {
			if eventMatchesTarget(ev.Path, t) {
				rel.trig.invalidate(now)
				break
			}
		}
		// ② 维护面：注册点自身消失 → 其管辖根重挂（回退上移）。
		for watchPath, servedDir := range rel.watchPaths {
			if isGoneEvent(ev.Op) && fsutil.SamePath(ev.Path, watchPath) {
				e.remountLocked(rel, servedDir)
			}
		}
		// ③ 维护面：目录创建/改名 → 注册点向下迁移；管辖子树内新目录 → 递归补挂。
		if !isDirEvent(ev.Op) {
			continue
		}
		for _, t := range rel.targets {
			switch {
			case fsutil.SamePath(ev.Path, t.Dir) || isStrictAncestor(ev.Path, t.Dir):
				e.remountLocked(rel, t.Dir)
			case t.File == "" && isStrictUnder(ev.Path, t.Dir) && fsutil.IsDir(ev.Path):
				e.expandLocked(rel, t.Dir, ev.Path)
			}
		}
	}
}

// remountLocked 把管辖根 dir 的注册点重算到最近存在父目录（目录再现由父
// 监听捕获向下迁移，ADR-0010 §7；调用方持 mu）。
func (e *Engine) remountLocked(rel *relState, dir string) {
	for watchPath, served := range rel.watchPaths {
		if fsutil.SamePath(served, dir) {
			e.releaseWatchLocked(rel, watchPath)
		}
	}
	e.addWatchLocked(rel, nearestExistingDir(dir), dir)
}

// expandLocked 递归补挂管辖根 root 下新出现的子树（排除集过滤；from 是事件
// 到达的新目录；调用方持 mu）。
func (e *Engine) expandLocked(rel *relState, root, from string) {
	if ExcludedDirName(filepath.Base(from)) {
		return // mods/.index 等排除集目录不监听、不下探
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return
	}
	e.addWatchLocked(rel, from, root)
	for _, ent := range entries {
		if !ent.IsDir() || ExcludedDirName(ent.Name()) {
			continue
		}
		e.expandLocked(rel, root, filepath.Join(from, ent.Name()))
	}
}

// addWatchLocked 注册一条监听（引用计数；注册失败计入关系挂载连败）。
// served 是该注册点服务的管辖根目录。调用方持 mu。
func (e *Engine) addWatchLocked(rel *relState, watchPath, served string) {
	if cur, ok := rel.watchPaths[watchPath]; ok && fsutil.SamePath(cur, served) {
		return
	}
	rel.watchPaths[watchPath] = served
	if n := e.registered[watchPath]; n > 0 {
		e.registered[watchPath] = n + 1
		return
	}
	if err := e.src.Add(watchPath); err != nil {
		slog.Warn("watch: 注册监听失败", "path", rel.alias(watchPath), "err", err)
		delete(rel.watchPaths, watchPath)
		rel.mountFails++
		e.evaluateMountHealthLocked(rel)
		return
	}
	e.registered[watchPath] = 1
}

// releaseWatchLocked 摘除一条注册（引用计数到 0 才真正 Remove）。调用方持 mu。
func (e *Engine) releaseWatchLocked(rel *relState, watchPath string) {
	if _, ok := rel.watchPaths[watchPath]; !ok {
		return
	}
	delete(rel.watchPaths, watchPath)
	n := e.registered[watchPath] - 1
	if n > 0 {
		e.registered[watchPath] = n
		return
	}
	delete(e.registered, watchPath)
	_ = e.src.Remove(watchPath)
}

// evaluateMountHealthLocked 挂载连败 → 有限重建仍败转 unavailable + watch_failed
//（一次性；恢复后复位可再发）。调用方持 mu。
func (e *Engine) evaluateMountHealthLocked(rel *relState) {
	if rel.mountFails < mountFailUnavailableAt || rel.status == StatusUnavailable {
		return
	}
	e.markUnavailableLocked(rel)
}

// markUnavailableLocked 转 unavailable 并发 watch_failed（契约 04 §2.5 预留
// 形状原样启用：envelope 带 relation_id、payload {}，前端按 invalidation 处理
// + 一次性「监听不可用」提示）。调用方持 mu。
func (e *Engine) markUnavailableLocked(rel *relState) {
	rel.status = StatusUnavailable
	rel.mountFails = 0
	if rel.failSent {
		return
	}
	rel.failSent = true
	slog.Warn("watch: 监听不可用，降级回手动（快速更新不受影响）", "relation", rel.id)
	if e.deps.PublishWatchFailed == nil {
		return
	}
	if err := e.deps.PublishWatchFailed(context.WithoutCancel(e.ctx), rel.id); err != nil {
		slog.Warn("watch: 发布 watch_failed 失败", "relation", rel.id, "err", err)
	}
}

// ---- 重挂（resync）----

// resyncAll 把监听面对齐当前 repository 事实：健康 relation 全量常驻（healthy
// + 恢复期保持，ADR-0010 §4/契约 07 §3.2），policy/端点路径漂移重挂，非健康/
// 消失关系卸载。
func (e *Engine) resyncAll() {
	ctx, cancel := context.WithTimeout(e.chainCtx, 30*time.Second)
	defer cancel()
	rels, _, err := e.deps.Relations.List(ctx, ports.PageRequest{Limit: ports.MaxPageLimit})
	if err != nil {
		slog.Warn("watch: 列举关系失败（保留既有挂载）", "err", err)
		return
	}
	keep := map[string]bool{}
	for i := range rels {
		switch rels[i].Health {
		case model.HealthHealthy, model.HealthRecoveryRequired:
			keep[rels[i].RelationID] = true
		default:
			// endpoint_missing / rebind_required：非健康关系不常驻监听
		}
	}

	e.mu.Lock()
	for id, rel := range e.rels {
		if !keep[id] {
			for watchPath := range rel.watchPaths {
				e.releaseWatchLocked(rel, watchPath)
			}
			delete(e.rels, id)
		}
	}
	e.mu.Unlock()

	for id := range keep {
		e.resyncRelation(ctx, id)
	}
}

// resyncRelation 重挂单关系：重算监听面（policy 的函数）并 diff 注册集。
func (e *Engine) resyncRelation(ctx context.Context, relationID string) {
	relModel, err := e.deps.Relations.Get(ctx, relationID)
	if err != nil {
		e.dropRelation(relationID)
		return
	}
	proj, err := e.deps.Endpoints.GetProject(ctx, relModel.ProjectID)
	if err != nil || proj.RootPath == "" {
		return // 端点漂移暂不挂载（重绑/修复后 resync 恢复）
	}
	rt, err := e.deps.Endpoints.GetRuntime(ctx, relModel.RuntimeID)
	if err != nil || rt.RootPath == "" {
		return
	}
	pol, err := e.deps.Mappings.GetPolicy(ctx, relationID)
	if err != nil {
		return
	}
	targets := SurfaceFor(pol, proj.RootPath, rt.RootPath, relationID)

	e.mu.Lock()
	defer e.mu.Unlock()
	rel := e.rels[relationID]
	if rel == nil {
		rel = &relState{
			id:         relationID,
			trig:       newTrigger(e.deps.Quiesce, e.deps.MaxWait),
			watchPaths: map[string]string{},
		}
		e.rels[relationID] = rel
	}
	rel.targets = targets
	rel.projRoot, rel.rtRoot = proj.RootPath, rt.RootPath

	// 注册集 diff：desired = 各管辖根的最近存在父目录（最小重挂）
	desired := map[string]string{}
	for _, t := range targets {
		desired[nearestExistingDir(t.Dir)] = t.Dir
	}
	for watchPath := range rel.watchPaths {
		if _, ok := desired[watchPath]; !ok {
			e.releaseWatchLocked(rel, watchPath)
		}
	}
	for watchPath, served := range desired {
		e.addWatchLocked(rel, watchPath, served)
	}

	// 挂载健康收口：有注册即部分成功（清连败、复位 unavailable=监听自愈）；
	// 全部失败已在 addWatchLocked 计连败（达阈值转 unavailable）。
	switch {
	case len(rel.watchPaths) > 0:
		rel.mountFails = 0
		switch rel.status {
		case StatusUnavailable:
			rel.status = StatusActive
			rel.failSent = false // 恢复后 watch_failed 可再发
			slog.Info("watch: 监听恢复", "relation", relationID)
		case "":
			rel.status = StatusActive
		}
	case len(desired) == 0:
		rel.status = "" // 无可挂目标（端点根缺失等）：未挂载态
	default:
		e.evaluateMountHealthLocked(rel)
	}
}

// dropRelation 卸载关系（关系消失/不可读）。
func (e *Engine) dropRelation(relationID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rel := e.rels[relationID]
	if rel == nil {
		return
	}
	for watchPath := range rel.watchPaths {
		e.releaseWatchLocked(rel, watchPath)
	}
	delete(e.rels, relationID)
}

// unmountAll 退出收敛：摘除全部注册（事件源 Close 在 Stop）。
func (e *Engine) unmountAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, rel := range e.rels {
		for watchPath := range rel.watchPaths {
			e.releaseWatchLocked(rel, watchPath)
		}
		delete(e.rels, id)
	}
}

// handleSourceLost 事件源死亡/异常：有限次重建，仍败全部关系转 unavailable
//（各自发一次 watch_failed）。
func (e *Engine) handleSourceLost() {
	if e.deps.NewSource == nil {
		e.markAllUnavailable()
		return
	}
	for attempt := 1; attempt <= maxSourceRebuilds; attempt++ {
		ns, err := e.deps.NewSource()
		if err != nil {
			slog.Warn("watch: 重建事件源失败", "attempt", attempt, "err", err)
			continue
		}
		old := e.src
		e.mu.Lock()
		e.src = ns
		e.registered = map[string]int{}
		for _, rel := range e.rels {
			rel.watchPaths = map[string]string{}
			rel.mountFails = 0
		}
		e.mu.Unlock()
		_ = old.Close()
		e.Kick() // 全量重挂到新源
		return
	}
	slog.Warn("watch: 事件源重建仍败，监听降级回手动")
	e.markAllUnavailable()
}

// markAllUnavailable 全部已挂载关系转 unavailable（各自发一次 watch_failed）。
func (e *Engine) markAllUnavailable() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rel := range e.rels {
		if rel.status != "" {
			e.markUnavailableLocked(rel)
		}
	}
}

// alias 日志路径脱敏（ADR-0011 §7 R1：按归属端点根别名化）。
func (rel *relState) alias(path string) string {
	if rel.projRoot != "" && matchUnder(path, rel.projRoot) {
		return model.AliasPath(rel.projRoot, model.AliasProject, path)
	}
	if rel.rtRoot != "" && matchUnder(path, rel.rtRoot) {
		return model.AliasPath(rel.rtRoot, model.AliasRuntime, path)
	}
	return path
}

// nearestExistingDir 向上找到最近的存在目录（目录尚未创建时回退父监听，
// legacy 验证过的手段，ADR-0010 §7）。
func nearestExistingDir(path string) string {
	p := filepath.Clean(path)
	for {
		if fsutil.IsDir(p) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p // 文件系统根目录（注册失败走挂载连败面）
		}
		p = parent
	}
}
