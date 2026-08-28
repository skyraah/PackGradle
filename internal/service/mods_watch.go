package service

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v3/pkg/application"

	"packgradle/internal/appconfig"
	"packgradle/internal/fsutil"
	"packgradle/internal/prism"
)

// ModsDiffEvent 是 mods 目录监听结果推送到前端的事件名。
// 前端通过 Events.On(ModsDiffEvent, cb) 订阅；事件数据为 prism.ModsWatchEvent。
const ModsDiffEvent = "packgradle:mods-diff"

// modsWatchSide 标识触发比对的一侧（写入 ModsWatchEvent.Side）
const (
	modsWatchSideProject  = "project"  // 项目 mods 目录变化
	modsWatchSideInstance = "instance" // 实例 mods/.index 目录变化
	modsWatchSideBoth     = "both"     // 防抖窗口内两侧都变化
)

// modsWatchDebounce 是文件系统事件聚合窗口：一次编辑/刷新会产生多个事件，
// 窗口内只做一次 MetaDiff 比对。
const modsWatchDebounce = 600 * time.Millisecond

func init() {
	// 注册自定义事件数据类型：Wails 生成 TS 绑定时会把
	// "packgradle:mods-diff" 的数据类型映射为 prism.ModsWatchEvent。
	application.RegisterEvent[prism.ModsWatchEvent](ModsDiffEvent)
}

// modsWatchPair 是一个已关联项目需要监听的两侧目录
type modsWatchPair struct {
	project       string
	projectMods   string // <项目>/mods
	instanceIndex string // <实例>/minecraft/mods/.index
}

// modsWatchTarget 描述一个 fsnotify 注册项：
// watchPath 是实际注册到 fsnotify 的路径（目标目录不存在时回退到最近存在的父目录），
// targetPath 是真正关心变化的目录。
type modsWatchTarget struct {
	watchPath  string
	targetPath string
	project    string
	side       string
}

// modsWatchJob 是单个项目的防抖任务：文件事件进 channel，
// 独立 goroutine 聚合后执行一次 MetaDiff。
type modsWatchJob struct {
	project string
	ch      chan string
	stop    chan struct{}
}

// modsWatcher 负责「项目 mods ↔ 实例 mods/.index」的双端监听。
// 所有项目共用一个 fsnotify.Watcher；每个项目一条防抖任务队列。
type modsWatcher struct {
	svc *PrismService
	fw  *fsnotify.Watcher

	mu    sync.Mutex
	paths map[string][]modsWatchTarget // fsnotify 实际注册路径 → 关心它的目标
	jobs  map[string]*modsWatchJob     // 项目名 → 防抖任务
	done  bool
}

// WatchMods 让 mods 目录监听覆盖当前全部已关联项目（幂等，可重复调用）：
// 监听项目 <project>/mods 与实例 <instance>/minecraft/mods/.index，
// 任一侧变化后防抖执行一次 MetaDiff，并以 "packgradle:mods-diff"
// 事件将 prism.ModsWatchEvent 发到前端。
// 返回当前已进入监听的项目名（按名称排序）。
func (s *PrismService) WatchMods() ([]string, error) {
	w, err := s.ensureModsWatcher()
	if err != nil {
		return nil, err
	}
	return w.sync()
}

// ServiceStartup 启动时自动监听所有已经建立关联的项目。
// 监听器创建失败只记日志不中断应用启动：WatchMods 可随时重试初始化。
func (s *PrismService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	w, err := s.ensureModsWatcher()
	if err != nil {
		log.Printf("mods watch 启动失败（应用继续运行）: %v", err)
		return nil
	}
	if _, err := w.sync(); err != nil {
		log.Printf("mods watch 初始同步失败: %v", err)
		return nil
	}
	go func() {
		<-ctx.Done()
		_ = w.close()
	}()
	return nil
}

// ServiceShutdown 停止所有目录监听。
func (s *PrismService) ServiceShutdown() error {
	s.watchMu.Lock()
	w := s.modsWatch
	s.watchMu.Unlock()
	if w == nil {
		return nil
	}
	return w.close()
}

// ensureModsWatcher 懒创建共享监听器（仅创建一次）
func (s *PrismService) ensureModsWatcher() (*modsWatcher, error) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.modsWatch != nil {
		return s.modsWatch, nil
	}
	w, err := newModsWatcher(s)
	if err != nil {
		return nil, err
	}
	s.modsWatch = w
	return w, nil
}

// refreshModsWatch 在关联/实例路径发生变化后重同步监听集合。
// 监听器尚未创建（如单元测试或应用未启动）时为 no-op。
func (s *PrismService) refreshModsWatch() {
	s.watchMu.Lock()
	w := s.modsWatch
	s.watchMu.Unlock()
	if w == nil {
		return
	}
	if _, err := w.sync(); err != nil {
		log.Printf("mods watch 重同步失败: %v", err)
	}
}

// emitModsDiff 将差异数据包发给前端；测试可注入 emitModsEvent 捕获。
func (s *PrismService) emitModsDiff(event prism.ModsWatchEvent) {
	if s.emitModsEvent != nil {
		s.emitModsEvent(event)
		return
	}
	if app := application.Get(); app != nil && app.Event != nil {
		app.Event.Emit(ModsDiffEvent, event)
	}
}

func newModsWatcher(svc *PrismService) (*modsWatcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &modsWatcher{
		svc:   svc,
		fw:    fw,
		paths: map[string][]modsWatchTarget{},
		jobs:  map[string]*modsWatchJob{},
	}
	go w.run()
	return w, nil
}

// currentModsWatchPairs 扫描当前全部已关联且两侧目录可定位的项目
func (s *PrismService) currentModsWatchPairs() []modsWatchPair {
	instances := s.scanInstancesSafe()
	var out []modsWatchPair
	for _, e := range s.config.Get().Projects {
		if !fsutil.IsDir(e.Path) {
			continue // 项目目录已不存在：无可监听
		}
		pc, err := appconfig.LoadProjectConfig(e.Path)
		if err != nil || pc.Instance == "" {
			continue
		}
		inst, ok := instances[pc.Instance]
		if !ok || inst.Error != "" || !fsutil.IsDir(inst.Path) {
			continue
		}
		out = append(out, modsWatchPair{
			project:       e.Name,
			projectMods:   filepath.Join(e.Path, "mods"),
			instanceIndex: filepath.Join(inst.GameDir, "mods", ".index"),
		})
	}
	return out
}

// sync 把 fsnotify 注册项与防抖任务对齐到当前已关联项目集合。
// 返回按名称排序的监听项目列表；单条路径注册失败只记日志不中断其余项目
// （目录可能正在被删除/重建，下一次 WatchMods 或变化后的 refresh 会重试）。
func (w *modsWatcher) sync() ([]string, error) {
	pairs := w.svc.currentModsWatchPairs()
	desiredPaths := map[string][]modsWatchTarget{}
	desiredProjects := map[string]bool{}
	projects := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if !desiredProjects[p.project] {
			desiredProjects[p.project] = true
			projects = append(projects, p.project)
		}
		for _, t := range watchTargetsFor(p) {
			desiredPaths[t.watchPath] = append(desiredPaths[t.watchPath], t)
		}
	}
	sort.Strings(projects)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return projects, nil
	}

	// 移除不再关心的路径
	for watchPath := range w.paths {
		if _, ok := desiredPaths[watchPath]; ok {
			continue
		}
		_ = w.fw.Remove(watchPath)
		delete(w.paths, watchPath)
	}

	// 添加新路径 / 更新同一路径上的目标
	for watchPath, targets := range desiredPaths {
		if _, ok := w.paths[watchPath]; !ok {
			if err := w.fw.Add(watchPath); err != nil {
				log.Printf("mods watch: 注册 %s 失败: %v", watchPath, err)
				continue
			}
		}
		w.paths[watchPath] = targets
	}

	// 对齐每个项目的防抖任务
	for _, project := range projects {
		w.ensureJobLocked(project)
	}
	for project, job := range w.jobs {
		if !desiredProjects[project] {
			close(job.stop)
			delete(w.jobs, project)
		}
	}
	return projects, nil
}

// watchTargetsFor 返回一个关联项目需要注册的两条监听目标。
// 目录不存在时回退到最近存在的父目录，父目录上的事件会按 targetPath 过滤。
func watchTargetsFor(p modsWatchPair) []modsWatchTarget {
	return []modsWatchTarget{
		newModsWatchTarget(p.projectMods, p.project, modsWatchSideProject),
		newModsWatchTarget(p.instanceIndex, p.project, modsWatchSideInstance),
	}
}

func newModsWatchTarget(targetPath, project, side string) modsWatchTarget {
	return modsWatchTarget{
		watchPath:  nearestExistingDir(targetPath),
		targetPath: filepath.Clean(targetPath),
		project:    project,
		side:       side,
	}
}

// nearestExistingDir 向上找到最近的存在目录（用于监听尚未创建的目录）
func nearestExistingDir(path string) string {
	p := filepath.Clean(path)
	for {
		if fsutil.IsDir(p) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p // 文件系统根目录（Add 失败会记日志）
		}
		p = parent
	}
}

// refreshProject 刷新单个项目的注册路径，返回该项目是否仍在监听集合中。
// 由防抖任务在比对后调用：目标目录被删除/新建后，fsnotify 注册需要跟随迁移。
func (w *modsWatcher) refreshProject(project string) bool {
	var desired []modsWatchTarget
	for _, p := range w.svc.currentModsWatchPairs() {
		if p.project == project {
			desired = append(desired, watchTargetsFor(p)...)
			break
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return false
	}

	// 移除该项目旧注册
	for watchPath, targets := range w.paths {
		kept := targets[:0]
		for _, t := range targets {
			if t.project != project {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			_ = w.fw.Remove(watchPath)
			delete(w.paths, watchPath)
		} else {
			w.paths[watchPath] = kept
		}
	}

	// 注册新目标
	for _, t := range desired {
		if _, ok := w.paths[t.watchPath]; !ok {
			if err := w.fw.Add(t.watchPath); err != nil {
				log.Printf("mods watch: 注册 %s 失败: %v", t.watchPath, err)
				continue
			}
		}
		w.paths[t.watchPath] = append(w.paths[t.watchPath], t)
	}
	return len(desired) > 0
}

func (w *modsWatcher) ensureJobLocked(project string) {
	if _, ok := w.jobs[project]; ok {
		return
	}
	job := &modsWatchJob{project: project, ch: make(chan string, 2), stop: make(chan struct{})}
	w.jobs[project] = job
	go w.runJob(job)
}

// run 消费 fsnotify 事件并转发到对应项目的防抖任务
func (w *modsWatcher) run() {
	for {
		select {
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			if err != nil {
				log.Printf("mods watch: fsnotify 错误: %v", err)
			}
		}
	}
}

func (w *modsWatcher) handleEvent(ev fsnotify.Event) {
	if ev.Name == "" || ev.Op&^fsnotify.Chmod == 0 {
		return // 仅 chmod 不构成元数据变化
	}

	w.mu.Lock()
	notifications := map[string]map[string]bool{}
	refreshNeeded := map[string]bool{}
	refreshProjects := map[string]bool{}
	for watchPath, targets := range w.paths {
		if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && fsutil.SamePath(ev.Name, watchPath) {
			// 注册路径本身被删除/改名：本注册失效，交给 refreshProject 迁到最近父目录
			refreshNeeded[watchPath] = true
		}
		for _, t := range targets {
			if modsWatchPathMatches(ev.Name, t.targetPath) {
				sides := notifications[t.project]
				if sides == nil {
					sides = map[string]bool{}
					notifications[t.project] = sides
				}
				sides[t.side] = true
				continue
			}
			// 目标目录尚未创建、正在监听其祖先目录时，中间目录（如实例 mods/）
			// 被新建也会让注册点有机会向目标迁移，否则后续 .index 事件可能漏听。
			if ev.Op&(fsnotify.Create|fsnotify.Rename) != 0 && modsWatchPathIsAncestorOf(ev.Name, t.targetPath) {
				refreshProjects[t.project] = true
			}
		}
	}
	w.mu.Unlock()

	for project, sides := range notifications {
		for side := range sides {
			w.notify(project, side)
		}
	}
	if len(refreshNeeded) > 0 {
		go w.refreshRegisteredPaths(refreshNeeded)
	}
	for project := range refreshProjects {
		go w.refreshProject(project)
	}
}

// refreshRegisteredPaths 在注册路径自身消失时立即重定位（不等待比对）
func (w *modsWatcher) refreshRegisteredPaths(watchPaths map[string]bool) {
	w.mu.Lock()
	projects := map[string]bool{}
	for watchPath := range watchPaths {
		if _, ok := w.paths[watchPath]; !ok {
			continue
		}
		for _, t := range w.paths[watchPath] {
			projects[t.project] = true
		}
		_ = w.fw.Remove(watchPath)
		delete(w.paths, watchPath)
	}
	w.mu.Unlock()

	for project := range projects {
		w.refreshProject(project)
	}
}

// modsWatchPathMatches 判断 fsnotify 事件路径是否属于目标目录（含目标目录本身）
func modsWatchPathMatches(eventPath, targetPath string) bool {
	if fsutil.SamePath(eventPath, targetPath) {
		return true
	}
	eventPath = strings.ToLower(filepath.Clean(eventPath))
	targetPath = strings.ToLower(filepath.Clean(targetPath))
	return strings.HasPrefix(eventPath, targetPath+string(os.PathSeparator))
}

// modsWatchPathIsAncestorOf 判断事件路径是否为目标的严格祖先目录。
// 用于目标目录尚不存在、监听点在其上方时，随中间目录创建向下迁移注册点。
func modsWatchPathIsAncestorOf(eventPath, targetPath string) bool {
	if fsutil.SamePath(eventPath, targetPath) {
		return false
	}
	eventPath = strings.ToLower(filepath.Clean(eventPath))
	targetPath = strings.ToLower(filepath.Clean(targetPath))
	return strings.HasPrefix(targetPath, eventPath+string(os.PathSeparator))
}

func (w *modsWatcher) notify(project, side string) {
	w.mu.Lock()
	job := w.jobs[project]
	w.mu.Unlock()
	if job == nil {
		return
	}
	select {
	case job.ch <- side:
	default: // channel 已有待处理事件：下一次防抖会一并覆盖
	}
}

// runJob 单项目防抖：聚合短暂窗口内的文件事件，执行一次双端比对并发包。
// 每次比对后调用 refreshProject，让监听跟随目录的删除/重建。
func (w *modsWatcher) runJob(job *modsWatchJob) {
	pending := map[string]bool{}
	timer := time.NewTimer(modsWatchDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		select {
		case side := <-job.ch:
			pending[side] = true
			timer.Reset(modsWatchDebounce)
		case <-timer.C:
			side := joinModsWatchSides(pending)
			pending = map[string]bool{}
			if side == "" {
				continue
			}
			w.compareAndEmit(job.project, side)
			if !w.refreshProject(job.project) {
				w.removeJob(job)
				return
			}
		case <-job.stop:
			return
		}
	}
}

// compareAndEmit 执行一次 MetaDiff 并以自定义事件发包至前端
func (w *modsWatcher) compareAndEmit(project, side string) {
	event := prism.ModsWatchEvent{Project: project, Side: side}
	diff, err := w.svc.MetaDiff(project)
	if err != nil {
		event.Error = err.Error()
		log.Printf("mods watch: 项目 %s 差异比对失败: %v", project, err)
	} else {
		event.Diff = diff
	}
	w.svc.emitModsDiff(event)
}

func joinModsWatchSides(sides map[string]bool) string {
	hasProject := sides[modsWatchSideProject]
	hasInstance := sides[modsWatchSideInstance]
	switch {
	case hasProject && hasInstance:
		return modsWatchSideBoth
	case hasProject:
		return modsWatchSideProject
	case hasInstance:
		return modsWatchSideInstance
	default:
		return ""
	}
}

// removeJob 在项目退出监听集合后移除自身任务
func (w *modsWatcher) removeJob(job *modsWatchJob) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if current, ok := w.jobs[job.project]; ok && current == job {
		delete(w.jobs, job.project)
	}
}

// close 幂等关闭监听器：停掉所有防抖任务并关闭 fsnotify
func (w *modsWatcher) close() error {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return nil
	}
	w.done = true
	jobs := make([]*modsWatchJob, 0, len(w.jobs))
	for _, job := range w.jobs {
		jobs = append(jobs, job)
	}
	w.jobs = map[string]*modsWatchJob{}
	w.mu.Unlock()

	for _, job := range jobs {
		close(job.stop)
	}
	return w.fw.Close()
}
