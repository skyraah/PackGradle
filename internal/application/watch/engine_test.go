package watch

// 引擎单测（P4 验收规格 §4.1，票 #92）：注入假事件源 + 假仓库 + 假自动链，
// 真目录真事件时序只断不变式——静默期/上限由 trigger 纯逻辑测试覆盖（假时钟），
// 本文件覆盖引擎级行为：单飞 dirty 补轮、连败暂停与手动复位、恢复期标脏、
// 父目录回退与再现重挂、监听异常有限重建与 watch_failed、非健康关系卸载。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/policy"
	"packgradle/internal/core/model"
)

// ---- 假事件源 ----

type fakeSource struct {
	mu        sync.Mutex
	events    chan ports.DirEvent
	errs      chan error
	adds      map[string]int
	removes   map[string]int
	addErrs   map[string]error
	failAll   bool
	closed    chan struct{}
	closeOnce sync.Once
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		events:  make(chan ports.DirEvent, 128),
		errs:    make(chan error, 8),
		adds:    map[string]int{},
		removes: map[string]int{},
		addErrs: map[string]error{},
		closed:  make(chan struct{}),
	}
}

func (s *fakeSource) Add(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adds[path]++
	if s.failAll {
		return errors.New("注入：注册失败")
	}
	if err := s.addErrs[path]; err != nil {
		return err
	}
	return nil
}

func (s *fakeSource) Remove(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removes[path]++
	return nil
}

func (s *fakeSource) Events() <-chan ports.DirEvent { return s.events }
func (s *fakeSource) Errors() <-chan error          { return s.errs }

func (s *fakeSource) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// emit 推入一次 OS 事件（目录监听测试注入缝）。
func (s *fakeSource) emit(t *testing.T, ev ports.DirEvent) {
	t.Helper()
	select {
	case s.events <- ev:
	case <-time.After(2 * time.Second):
		t.Fatal("事件通道拥塞")
	}
}

func (s *fakeSource) addCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adds[path]
}

// ---- 假仓库 ----

type stubRelations struct {
	mu   sync.Mutex
	rels map[string]model.Relation
}

func (r *stubRelations) Create(ctx context.Context, rel model.Relation) error { return nil }
func (r *stubRelations) Get(_ context.Context, id string) (model.Relation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rel, ok := r.rels[id]; ok {
		return rel, nil
	}
	return model.Relation{}, ports.ErrNotFound
}
func (r *stubRelations) List(_ context.Context, _ ports.PageRequest) ([]model.Relation, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.Relation, 0, len(r.rels))
	for id := range r.rels {
		out = append(out, r.rels[id])
	}
	return out, "", nil
}
func (r *stubRelations) UpdateHealth(_ context.Context, id string, health model.RelationHealth) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel := r.rels[id]
	rel.Health = health
	r.rels[id] = rel
	return nil
}
func (r *stubRelations) IncrementRevision(ctx context.Context, id string) (int, error) {
	return 1, nil
}
func (r *stubRelations) PairExists(ctx context.Context, projectID, runtimeID string) (bool, error) {
	return false, nil
}
func (r *stubRelations) UpdateHeadBaseline(ctx context.Context, id, baselineID string) error {
	return nil
}
func (r *stubRelations) UpdateHeadCommit(ctx context.Context, id, commitID string) error {
	return nil
}
func (r *stubRelations) UpdateAuthorizedApply(ctx context.Context, id string, enabled bool) error {
	return nil
}

type stubEndpoints struct {
	projects map[string]model.Project
	runtimes map[string]model.Runtime
}

func (e *stubEndpoints) CreateProject(ctx context.Context, p model.Project) error { return nil }
func (e *stubEndpoints) GetProject(_ context.Context, id string) (model.Project, error) {
	if p, ok := e.projects[id]; ok {
		return p, nil
	}
	return model.Project{}, ports.ErrNotFound
}
func (e *stubEndpoints) FindProjectByRoot(ctx context.Context, fingerprint string) (model.Project, bool, error) {
	return model.Project{}, false, nil
}
func (e *stubEndpoints) ListProjects(ctx context.Context) ([]model.Project, error) {
	return nil, nil
}
func (e *stubEndpoints) CreateRuntime(ctx context.Context, r model.Runtime) error { return nil }
func (e *stubEndpoints) GetRuntime(_ context.Context, id string) (model.Runtime, error) {
	if r, ok := e.runtimes[id]; ok {
		return r, nil
	}
	return model.Runtime{}, ports.ErrNotFound
}
func (e *stubEndpoints) FindRuntimeByIdentity(ctx context.Context, adapter, identity string) (model.Runtime, bool, error) {
	return model.Runtime{}, false, nil
}
func (e *stubEndpoints) ListRuntimes(ctx context.Context) ([]model.Runtime, error) {
	return nil, nil
}
func (e *stubEndpoints) UpdateProject(ctx context.Context, p model.Project) error { return nil }
func (e *stubEndpoints) UpdateRuntime(ctx context.Context, r model.Runtime) error { return nil }

type stubMappings struct{ policies map[string]model.MappingPolicy }

func (m *stubMappings) GetPolicy(_ context.Context, relationID string) (model.MappingPolicy, error) {
	if p, ok := m.policies[relationID]; ok {
		return p, nil
	}
	return model.MappingPolicy{}, ports.ErrNotFound
}
func (m *stubMappings) CreatePolicy(ctx context.Context, relationID string, p model.MappingPolicy) error {
	return nil
}
func (m *stubMappings) SavePolicy(ctx context.Context, relationID string, p model.MappingPolicy) error {
	return nil
}

// ---- 假自动链 ----

type chainRecorder struct {
	mu      sync.Mutex
	calls   []string
	block   chan struct{} // 非 nil：每次链调用阻塞直至释放
	outcome string
	taskID  string
	err     error
}

func (c *chainRecorder) run(ctx context.Context, relationID string) (string, string, error) {
	// 调用先记账再阻塞（等待中的链调用也计入计数）
	c.mu.Lock()
	c.calls = append(c.calls, relationID)
	block := c.block
	c.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outcome, c.taskID, c.err
}

func (c *chainRecorder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// ---- 装配助手 ----

// testStack 是引擎测试装配：单关系 + default-v1 监听面 + 假链。
type testStack struct {
	root       string // 项目根（pack.toml 语境）
	rtRoot     string // 运行侧 minecraft/
	eng        *Engine
	src        *fakeSource
	chain      *chainRecorder
	rels       *stubRelations
	watchFails []string
}

// newTestStack 装配引擎（短时序：静默期 30ms/上限 120ms/轮询 1ms）；
// mods 项目侧目录预建。引擎已 Go()，测试结束 Stop。
func newTestStack(t *testing.T, health model.RelationHealth) *testStack {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "project")
	rtRoot := filepath.Join(base, "instance", "minecraft")
	if err := os.MkdirAll(filepath.Join(root, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rtRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	rels := &stubRelations{rels: map[string]model.Relation{
		"rel_x": {
			RelationID: "rel_x", ProjectID: "prj_x", RuntimeID: "run_x",
			Health: health, Revision: 1,
		},
	}}
	endpoints := &stubEndpoints{
		projects: map[string]model.Project{"prj_x": {ProjectID: "prj_x", RootPath: root}},
		runtimes: map[string]model.Runtime{"run_x": {RuntimeID: "run_x", RootPath: rtRoot}},
	}
	pol, err := policy.MergeSuggestions(policy.DefaultV1(), []string{"config"})
	if err != nil {
		t.Fatal(err)
	}
	mappings := &stubMappings{policies: map[string]model.MappingPolicy{"rel_x": pol}}
	chain := &chainRecorder{outcome: "no_diff"}

	ts := &testStack{root: root, rtRoot: rtRoot, src: newFakeSource(), chain: chain, rels: rels}
	eng, err := New(Deps{
		Relations: rels,
		Endpoints: endpoints,
		Mappings:  mappings,
		Source:    ts.src,
		Chain:     chain.run,
		PublishWatchFailed: func(_ context.Context, relationID string) error {
			ts.watchFails = append(ts.watchFails, relationID)
			return nil
		},
		Now:          time.Now,
		Quiesce:      30 * time.Millisecond,
		MaxWait:      120 * time.Millisecond,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts.eng = eng
	eng.Go()
	t.Cleanup(eng.Stop)
	if health == model.HealthHealthy || health == model.HealthRecoveryRequired {
		ts.waitActive(t)
	} else {
		// 非健康关系不常驻监听：等未挂载态稳定
		waitFor(t, 2*time.Second, func() bool { return eng.WatchStatus("rel_x") == "" })
	}
	return ts
}

// waitActive 等待关系进入挂载 active 态。
func (ts *testStack) waitActive(t *testing.T) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool { return ts.eng.WatchStatus("rel_x") == StatusActive })
}

// waitFor 轮询直至 cond 为真或超时。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("轮询超时")
}

// ---- 引擎行为测试 ----

// TestEngineTriggerAndConverge 触发与收敛：管辖目录写文件 → 静默期后自动链
// 触发 → no_diff 收敛（apply 写盘自触发经收敛扫描自然终止的引擎侧不变式：
// 无新事件不再触发）。
func TestEngineTriggerAndConverge(t *testing.T) {
	ts := newTestStack(t, model.HealthHealthy)

	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "a.jar"), Op: ports.DirWrite})
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() >= 1 })
	time.Sleep(150 * time.Millisecond) // 静默期 ×2：收敛后不应再触发
	if n := ts.chain.count(); n != 1 {
		t.Fatalf("自动链调用次数 = %d, 期望 1（no_diff 收敛终止）", n)
	}
}

// TestEngineQuiescenceBatchesStorm 静默期聚合（引擎级）：事件风暴只触发一次链
//（与受控重查同型的 inflight 单飞由 QuickUpdate 用例承担，引擎侧聚合断言）。
func TestEngineQuiescenceBatchesStorm(t *testing.T) {
	ts := newTestStack(t, model.HealthHealthy)

	for i := 0; i < 20; i++ {
		ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", fmt.Sprintf("m%d.jar", i)), Op: ports.DirWrite})
		time.Sleep(2 * time.Millisecond)
	}
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() >= 1 })
	time.Sleep(100 * time.Millisecond)
	if n := ts.chain.count(); n != 1 {
		t.Fatalf("风暴聚合后链调用 = %d, 期望 1", n)
	}
}

// TestEngineInflightDirtySupplement 单飞 dirty 补轮：链进行中（阻塞）新失效只标
// dirty；本轮结束补一轮至干净——风暴两轮收敛。
func TestEngineInflightDirtySupplement(t *testing.T) {
	ts := newTestStack(t, model.HealthHealthy)
	block := make(chan struct{})
	ts.chain.mu.Lock()
	ts.chain.block = block
	ts.chain.mu.Unlock()

	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "a.jar"), Op: ports.DirWrite})
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() == 1 })

	// 链进行中新失效：只标 dirty
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "b.jar"), Op: ports.DirWrite})
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "pack.toml"), Op: ports.DirWrite})
	time.Sleep(50 * time.Millisecond)
	if n := ts.chain.count(); n != 1 {
		t.Fatalf("inflight 期间不应并发第二链: %d", n)
	}

	// 本轮收口 → 补一轮
	close(block)
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() == 2 })
	time.Sleep(150 * time.Millisecond)
	if n := ts.chain.count(); n != 2 {
		t.Fatalf("补轮后链调用 = %d, 期望 2（风暴 ≤2 轮）", n)
	}
}

// TestEngineAutoFailPauseAndManualReset 连败防打转（ADR-0010 §6）：自动执行连败
// 2 次暂停（watch_status=paused）；监听保持、标脏、无第三次自动执行；手动快速
// 更新成功复位 active。
func TestEngineAutoFailPauseAndManualReset(t *testing.T) {
	ts := newTestStack(t, model.HealthHealthy)
	ts.chain.mu.Lock()
	ts.chain.err = errors.New("注入：自动执行失败")
	ts.chain.mu.Unlock()

	// 第 1 次自动执行失败：failStreak=1，仍 active
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "a.jar"), Op: ports.DirWrite})
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() >= 1 })
	// 第 2 次自动执行失败（重扫 → 差异仍在 → 重触发）：暂停
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "b.jar"), Op: ports.DirWrite})
	waitFor(t, 2*time.Second, func() bool {
		return ts.chain.count() >= 2 && ts.eng.WatchStatus("rel_x") == StatusPaused
	})

	// 暂停后事件照常到达（监听保持、标脏）但无第三次自动执行
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "c.jar"), Op: ports.DirWrite})
	time.Sleep(200 * time.Millisecond)
	if n := ts.chain.count(); n != 2 {
		t.Fatalf("暂停后链调用 = %d, 期望 2（不做定时重试）", n)
	}
	if got := ts.eng.WatchStatus("rel_x"); got != StatusPaused {
		t.Fatalf("watch_status = %q, 期望 paused", got)
	}

	// 手动快速更新成功复位 active（transport SyncService.QuickUpdate 收口订阅）
	ts.eng.NotifyQuickUpdateResult("rel_x", nil)
	if got := ts.eng.WatchStatus("rel_x"); got != StatusActive {
		t.Fatalf("手动成功后 watch_status = %q, 期望 active", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := ts.chain.count(); n != 2 {
		t.Fatalf("复位不应立即触发链（待决失效已随手动链消化）: %d", n)
	}

	// 手动失败不动自动面：先置 paused 再 NotifyQuickUpdateResult(err)
	ts.eng.NotifyQuickUpdateResult("rel_x", errors.New("手动失败"))
	if got := ts.eng.WatchStatus("rel_x"); got != StatusActive {
		t.Fatalf("手动失败不应改变状态: %q", got)
	}

	// 复位后新事件照常自动触发
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "d.jar"), Op: ports.DirWrite})
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() >= 3 })
}

// TestEngineRecoveryMarksDirtyOnly 恢复期挂载保持、触发只标脏不物化（ADR-0010
// §4；验收场景 5）：事件积压不自动执行；恢复收口（healthy）后待决失效自然消化。
func TestEngineRecoveryMarksDirtyOnly(t *testing.T) {
	ts := newTestStack(t, model.HealthRecoveryRequired)

	// 恢复期：挂载保持（active），事件积压不发射
	ts.waitActive(t)
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "mods", "a.jar"), Op: ports.DirWrite})
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(ts.root, "pack.toml"), Op: ports.DirWrite})
	time.Sleep(250 * time.Millisecond)
	if n := ts.chain.count(); n != 0 {
		t.Fatalf("恢复期不应自动物化: %d", n)
	}
	if got := ts.eng.WatchStatus("rel_x"); got != StatusActive {
		t.Fatalf("恢复期挂载保持, watch_status = %q", got)
	}

	// 恢复收口：关系复位 healthy → 待决失效自然发射（不再等新事件）
	if err := ts.rels.UpdateHealth(context.Background(), "rel_x", model.HealthHealthy); err != nil {
		t.Fatal(err)
	}
	ts.eng.Kick()
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() >= 1 })
}

// TestEngineParentFallbackAndRemount 父目录回退（ADR-0010 §7）：管辖目录不存在
// 时回退注册最近存在父目录；目录再现由创建事件捕获重挂（向下迁移），其后子树
// 内事件照常触发。
func TestEngineParentFallbackAndRemount(t *testing.T) {
	ts := newTestStack(t, model.HealthHealthy)
	configDir := filepath.Join(ts.root, "config")

	// 装配时 config 尚不存在：注册点回退到项目根
	if ts.src.addCount(configDir) != 0 {
		t.Fatal("未创建目录不应被直接注册")
	}
	if ts.src.addCount(ts.root) == 0 {
		t.Fatal("config 缺失时应回退注册项目根")
	}

	// config 目录再现：Create 事件 → 注册点向下迁移
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts.src.emit(t, ports.DirEvent{Path: configDir, Op: ports.DirCreate})
	waitFor(t, 2*time.Second, func() bool { return ts.src.addCount(configDir) > 0 })

	// 迁移后管辖子树内事件照常触发
	ts.src.emit(t, ports.DirEvent{Path: filepath.Join(configDir, "x.toml"), Op: ports.DirWrite})
	waitFor(t, 2*time.Second, func() bool { return ts.chain.count() >= 1 })
}

// TestEngineDynamicSubdirExpansion 递归补挂：管辖子树内新建目录由目录创建事件
// 动态补挂（排除集过滤 mods/.index 等）。
func TestEngineDynamicSubdirExpansion(t *testing.T) {
	ts := newTestStack(t, model.HealthHealthy)
	sub := filepath.Join(ts.root, "mods", "newpack")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ts.src.emit(t, ports.DirEvent{Path: sub, Op: ports.DirCreate})
	waitFor(t, 2*time.Second, func() bool { return ts.src.addCount(sub) > 0 })

	// 排除集：mods/.index 不补挂
	idx := filepath.Join(ts.root, "mods", ".index")
	if err := os.MkdirAll(idx, 0o755); err != nil {
		t.Fatal(err)
	}
	ts.src.emit(t, ports.DirEvent{Path: idx, Op: ports.DirCreate})
	time.Sleep(100 * time.Millisecond)
	if ts.src.addCount(idx) != 0 {
		t.Fatal("mods/.index 不应被补挂")
	}
}

// TestEngineMountFailuresWatchFailed 监听异常（ADR-0010 §7）：注册持续失败 →
// 有限次数后 watch_status=unavailable + 发 watch_failed（一次性）；注册恢复 →
// 监听自愈回 active。
func TestEngineMountFailuresWatchFailed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(root, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	rels := &stubRelations{rels: map[string]model.Relation{
		"rel_x": {RelationID: "rel_x", ProjectID: "prj_x", RuntimeID: "run_x", Health: model.HealthHealthy},
	}}
	endpoints := &stubEndpoints{
		projects: map[string]model.Project{"prj_x": {ProjectID: "prj_x", RootPath: root}},
		runtimes: map[string]model.Runtime{"run_x": {RuntimeID: "run_x", RootPath: filepath.Join(base, "mc")}},
	}
	mappings := &stubMappings{policies: map[string]model.MappingPolicy{"rel_x": policy.DefaultV1()}}
	chain := &chainRecorder{outcome: "no_diff"}

	// 注册恒败（权限/句柄耗尽注入）：初始挂载即全部失败
	src := newFakeSource()
	src.mu.Lock()
	src.failAll = true
	src.mu.Unlock()
	var watchFails []string
	eng, err := New(Deps{
		Relations: rels, Endpoints: endpoints, Mappings: mappings,
		Source: src,
		Chain:  chain.run,
		PublishWatchFailed: func(_ context.Context, relationID string) error {
			watchFails = append(watchFails, relationID)
			return nil
		},
		Now:   time.Now,
		Quiesce: 30 * time.Millisecond, MaxWait: 120 * time.Millisecond,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.Go()
	t.Cleanup(eng.Stop)

	// 有限次数（default-v1 三个注册点全败 ≥ 阈值 3）→ unavailable + watch_failed
	waitFor(t, 2*time.Second, func() bool { return eng.WatchStatus("rel_x") == StatusUnavailable })
	if len(watchFails) != 1 || watchFails[0] != "rel_x" {
		t.Fatalf("watch_failed 收件 = %v, 期望 [rel_x] 一次", watchFails)
	}

	// unavailable 一次性：重挂仍败不重复发
	eng.Kick()
	time.Sleep(100 * time.Millisecond)
	if len(watchFails) != 1 {
		t.Fatalf("watch_failed 应一次性: %v", watchFails)
	}

	// 注册恢复：监听自愈回 active（重挂成功清连败、复位 failSent）
	src.mu.Lock()
	src.failAll = false
	src.mu.Unlock()
	eng.Kick()
	waitFor(t, 2*time.Second, func() bool { return eng.WatchStatus("rel_x") == StatusActive })
	if src.addCount(root) == 0 {
		t.Fatal("恢复后应完成注册")
	}
}

// TestEngineSourceRebuildAfterLoss 事件源死亡 → 有限次自动重建（换新源全量
// 重挂），监听面照常工作。
func TestEngineSourceRebuildAfterLoss(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(root, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	rels := &stubRelations{rels: map[string]model.Relation{
		"rel_x": {RelationID: "rel_x", ProjectID: "prj_x", RuntimeID: "run_x", Health: model.HealthHealthy},
	}}
	endpoints := &stubEndpoints{
		projects: map[string]model.Project{"prj_x": {ProjectID: "prj_x", RootPath: root}},
		runtimes: map[string]model.Runtime{"run_x": {RuntimeID: "run_x", RootPath: filepath.Join(base, "mc")}},
	}
	mappings := &stubMappings{policies: map[string]model.MappingPolicy{"rel_x": policy.DefaultV1()}}
	chain := &chainRecorder{outcome: "no_diff"}

	src0 := newFakeSource()
	rebuilt := make(chan *fakeSource, 4)
	eng, err := New(Deps{
		Relations: rels, Endpoints: endpoints, Mappings: mappings,
		Source: src0,
		NewSource: func() (ports.DirEventSource, error) {
			ns := newFakeSource()
			rebuilt <- ns
			return ns, nil
		},
		Chain: chain.run,
		Now:   time.Now,
		Quiesce: 30 * time.Millisecond, MaxWait: 120 * time.Millisecond,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.Go()
	t.Cleanup(eng.Stop)
	waitFor(t, 2*time.Second, func() bool { return eng.WatchStatus("rel_x") == StatusActive })
	if src0.addCount(root) == 0 {
		t.Fatal("初始源未注册项目根")
	}

	// 事件源死亡（Close → Events 通道关闭语义由真 fsnotify 承担；测试直接
	// 模拟引擎侧 handleSourceLost 触发路径：关闭源并推送通道关闭）
	close(src0.events)
	var ns *fakeSource
	waitFor(t, 2*time.Second, func() bool {
		select {
		case ns = <-rebuilt:
			return true
		default:
			return false
		}
	})
	// 新源全量重挂
	waitFor(t, 2*time.Second, func() bool { return ns.addCount(root) > 0 })
	if got := eng.WatchStatus("rel_x"); got != StatusActive {
		t.Fatalf("重建后 watch_status = %q, 期望 active", got)
	}

	// 新源事件照常触发自动链
	ns.emit(t, ports.DirEvent{Path: filepath.Join(root, "mods", "a.jar"), Op: ports.DirWrite})
	waitFor(t, 2*time.Second, func() bool { return chain.count() >= 1 })
}

// TestEngineSourceRebuildExhausted 重建仍败 → 全部关系 unavailable + watch_failed
//（降级=回手动，快速更新可用性不受影响由用例侧保证）。
func TestEngineSourceRebuildExhausted(t *testing.T) {
	ts := newTestStack(t, model.HealthHealthy)
	ts.eng.deps.NewSource = nil // 不重建：直接转 unavailable
	close(ts.src.events)

	waitFor(t, 2*time.Second, func() bool { return ts.eng.WatchStatus("rel_x") == StatusUnavailable })
	if len(ts.watchFails) != 1 || ts.watchFails[0] != "rel_x" {
		t.Fatalf("watch_failed 收件 = %v", ts.watchFails)
	}
}

// TestEngineNonHealthyNotMounted 非健康关系不常驻监听（契约 07 §3.2 空值语义）；
// 健康复位后动态挂载。
func TestEngineNonHealthyNotMounted(t *testing.T) {
	ts := newTestStack(t, model.HealthEndpointMissing)
	time.Sleep(100 * time.Millisecond)
	if got := ts.eng.WatchStatus("rel_x"); got != "" {
		t.Fatalf("非健康关系 watch_status = %q, 期望空", got)
	}
	if ts.src.addCount(ts.root) != 0 {
		t.Fatal("非健康关系不应注册监听")
	}

	_ = ts.rels.UpdateHealth(context.Background(), "rel_x", model.HealthHealthy)
	ts.eng.Kick()
	ts.waitActive(t)
}
