package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
	"packgradle/internal/errs"
)

// scanPhases 是扫描任务的阶段总数（进度 total）。
const scanPhases = 4

// StartScan 启动（或复用）Relation 扫描任务。
// 同一 Relation 同时最多一个 Scan Task；重复 StartScan 返回活动任务。
func (a *App) StartScan(ctx context.Context, relationID string) (view.TaskView, error) {
	gate := a.relationGate(relationID)
	gate.Lock()
	defer gate.Unlock()

	rel, err := a.deps.Relations.Get(ctx, relationID)
	if err != nil {
		return view.TaskView{}, errs.New(CodeRelationNotFound, relationID)
	}
	if t, found, err := a.deps.Tasks.FindActiveByRelationAndKind(ctx, relationID, model.TaskKindScan); err == nil && found {
		return TaskView(t), nil // 复用活动任务（架构 §9.1）
	} else if err != nil {
		return view.TaskView{}, err
	}

	proj, err := a.deps.Endpoints.GetProject(ctx, rel.ProjectID)
	if err != nil {
		_ = a.deps.Relations.UpdateHealth(ctx, relationID, model.HealthEndpointMissing)
		return view.TaskView{}, errs.New(CodeScanEndpointMissing, relationID)
	}
	rt, err := a.deps.Endpoints.GetRuntime(ctx, rel.RuntimeID)
	if err != nil {
		_ = a.deps.Relations.UpdateHealth(ctx, relationID, model.HealthEndpointMissing)
		return view.TaskView{}, errs.New(CodeScanEndpointMissing, relationID)
	}

	// 端点绑定验证：指纹不匹配 → rebind_required，不把新目录当作旧端点
	fpProj, err := a.deps.Fingerprinter.Fingerprint(proj.RootPath)
	if err != nil {
		_ = a.deps.Relations.UpdateHealth(ctx, relationID, model.HealthEndpointMissing)
		return view.TaskView{}, errs.NewDetail(CodeScanEndpointMissing, "项目端点不可达: "+err.Error(), relationID)
	}
	fpRt, err := a.deps.Fingerprinter.Fingerprint(rt.RootPath)
	if err != nil {
		_ = a.deps.Relations.UpdateHealth(ctx, relationID, model.HealthEndpointMissing)
		return view.TaskView{}, errs.NewDetail(CodeScanEndpointMissing, "运行时端点不可达: "+err.Error(), relationID)
	}
	if fpProj != proj.BindingFingerprint || fpRt != rt.BindingFingerprint {
		_ = a.deps.Relations.UpdateHealth(ctx, relationID, model.HealthRebindRequired)
		return view.TaskView{}, errs.New(CodeRelationRebindRequired, relationID)
	}

	t, err := a.runner.Create(ctx, relationID, model.TaskKindScan, true)
	if err != nil {
		return view.TaskView{}, err
	}
	scanCtx, cancel := context.WithCancel(ctxWithoutCancel(ctx))
	a.runner.RegisterCancel(t.TaskID, cancel)
	go func() {
		defer a.runner.UnregisterCancel(t.TaskID)
		a.runScan(scanCtx, t, rel, proj, rt, fpProj, fpRt)
	}()
	return TaskView(t), nil
}

// runScan 执行扫描四阶段：project_scan → runtime_scan → normalize → persist。
// 终态（cancelled/failed/succeeded）一律经 commitCtx 落库：
// 用户取消后 ctx 已失效，若用它写终态会因 context.Canceled 写库失败，
// 留下永远 running 的僵尸任务并锁死该 Relation 的后续扫描。
func (a *App) runScan(ctx context.Context, t model.Task, rel model.Relation, proj model.Project, rt model.Runtime, fpProj, fpRt string) {
	commitCtx := context.WithoutCancel(ctx)
	fail := func(code string, err error) {
		if errors.Is(ctx.Err(), context.Canceled) {
			a.runner.MarkCancelled(commitCtx, t)
			return
		}
		a.runner.MarkFailed(commitCtx, t, code, err.Error(), rel.RelationID)
	}
	// advance 推进阶段；更新失败时用 commitCtx 补记终态，绝不静默返回。
	advance := func(tt model.Task) (model.Task, bool) {
		next, err := a.runner.Update(ctx, tt)
		if err == nil {
			return next, true
		}
		fail(CodeScanAdapterFailed, fmt.Errorf("任务状态更新失败: %w", err))
		return next, false
	}
	t.Status = model.TaskStatusRunning
	t.Total = scanPhases
	t.Phase = "scan_project"
	t.MessageKey = "msg.task.scan.scanning_project"
	t.Completed = 0
	var ok bool
	if t, ok = advance(t); !ok {
		return
	}

	pol, err := a.deps.Mappings.GetPolicy(ctx, rel.RelationID)
	if err != nil {
		fail(CodeScanAdapterFailed, fmt.Errorf("加载 MappingPolicy: %w", err))
		return
	}
	polDigest, err := normalize.PolicyDigest(pol)
	if err != nil {
		fail(CodeScanAdapterFailed, err)
		return
	}

	reportP, err := a.deps.ProjectScan.Scan(ctx, proj.RootPath, ports.ScanOptions{
		Policy:   pol,
		HashFile: a.cachedHash(proj.BindingFingerprint, proj.RootPath),
	})
	if err != nil {
		fail(CodeScanAdapterFailed, err)
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		a.runner.MarkCancelled(commitCtx, t)
		return
	}

	t.Phase = "scan_runtime"
	t.MessageKey = "msg.task.scan.scanning_runtime"
	t.Completed = 1
	if t, ok = advance(t); !ok {
		return
	}
	reportR, err := a.deps.RuntimeScan.Scan(ctx, rt.RootPath, ports.ScanOptions{
		Policy:   pol,
		Hint:     buildScanHint(reportP),
		HashFile: a.cachedHash(rt.BindingFingerprint, rt.RootPath),
	})
	if err != nil {
		fail(CodeScanAdapterFailed, err)
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		a.runner.MarkCancelled(commitCtx, t)
		return
	}

	t.Phase = "normalize"
	t.MessageKey = "msg.task.scan.normalizing"
	t.Completed = 2
	if t, ok = advance(t); !ok {
		return
	}
	snapP, err := assembleSnapshot(rel.RelationID, model.SideProject, fpProj, polDigest, a.deps.ProjectScan, reportP)
	if err != nil {
		fail(CodeScanAdapterFailed, err)
		return
	}
	snapP.SnapshotID = a.deps.IDs("snap_")
	snapR, err := assembleSnapshot(rel.RelationID, model.SideRuntime, fpRt, polDigest, a.deps.RuntimeScan, reportR)
	if err != nil {
		fail(CodeScanAdapterFailed, err)
		return
	}
	snapR.SnapshotID = a.deps.IDs("snap_")

	t.Phase = "persist"
	t.MessageKey = "msg.task.scan.persisting"
	t.Completed = 3
	if t, ok = advance(t); !ok {
		return
	}
	if err := a.deps.Snapshots.Insert(ctx, snapP); err != nil {
		fail(CodeScanAdapterFailed, fmt.Errorf("持久化项目快照: %w", err))
		return
	}
	if err := a.deps.Snapshots.Insert(ctx, snapR); err != nil {
		fail(CodeScanAdapterFailed, fmt.Errorf("持久化运行时快照: %w", err))
		return
	}

	t.Phase = "done"
	t.Status = model.TaskStatusSucceeded
	t.MessageKey = "msg.task.scan.succeeded"
	t.Completed = scanPhases
	if _, err := a.runner.Update(commitCtx, t); err != nil {
		log.Printf("scan: 任务 %s 成功终态落库失败: %v", t.TaskID, err)
		return
	}
	_ = a.pub.PublishRelationInvalidated(commitCtx, rel.RelationID)
}

// buildScanHint 从项目扫描结果构造跨侧身份提示（唯一的跨侧匹配通道）。
func buildScanHint(reportP model.ScanReport) ports.ScanHint {
	hint := ports.ScanHint{FilenameToResourceID: map[string]string{}}
	for _, obs := range reportP.Observations {
		if obs.Kind != model.ResourceMod {
			continue
		}
		if filename, ok := obs.Representation.Metadata[model.MetaFilename]; ok && filename != "" {
			hint.FilenameToResourceID[strings.ToLower(filename)] = string(obs.ResourceID)
		}
	}
	return hint
}

// assembleSnapshot 把扫描报告组装为不可变 ObservedSnapshot（含 digest）。
func assembleSnapshot(relationID string, side model.Side, fingerprint, polDigest string, scanner interface {
	Name() string
	Version() string
}, report model.ScanReport) (model.ObservedSnapshot, error) {
	resources := make(map[model.ResourceID]model.ResourceObservation, len(report.Observations))
	for _, obs := range report.Observations {
		if _, dup := resources[obs.ResourceID]; dup {
			return model.ObservedSnapshot{}, fmt.Errorf("scan: 资源 %s 重复出现（identity 不唯一）", obs.ResourceID)
		}
		resources[obs.ResourceID] = obs
	}
	snap := model.ObservedSnapshot{
		SchemaVersion:        model.CurrentSchemaVersion,
		SnapshotID:           "", // 由调用方分配
		RelationID:           relationID,
		Side:                 side,
		CapturedAt:           time.Now().UTC().Format(time.RFC3339),
		BindingFingerprint:   fingerprint,
		NormalizationVersion: normalize.NormalizationVersion,
		PolicyDigest:         polDigest,
		Scanner:              model.ScannerInfo{Name: scanner.Name(), Version: scanner.Version()},
		Resources:            resources,
		Diagnostics:          report.Diagnostics,
	}
	digest, err := normalize.SnapshotDigest(snap)
	if err != nil {
		return model.ObservedSnapshot{}, err
	}
	snap.SnapshotDigest = digest
	return snap, nil
}

// cachedHash 返回带 hash cache 闭包的哈希函数：
// (fingerprint, path, size, mtime) 全部一致时复用；缓存只是性能优化，不是事实来源。
func (a *App) cachedHash(rootFingerprint, root string) func(ctx context.Context, absPath string) (model.ContentRef, ports.FileFacts, error) {
	return func(ctx context.Context, absPath string) (model.ContentRef, ports.FileFacts, error) {
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return a.deps.Hasher.HashFile(ctx, absPath)
		}
		relLower := strings.ToLower(filepath.ToSlash(rel))
		st, statErr := os.Stat(absPath)
		if statErr != nil {
			return model.ContentRef{}, ports.FileFacts{}, statErr
		}
		key := ports.HashCacheKey{
			RootFingerprint: rootFingerprint,
			RelativePath:    relLower,
			SizeBytes:       st.Size(),
			MtimeUnixNano:   st.ModTime().UnixNano(),
		}
		if digest, found, err := a.deps.HashCache.Lookup(ctx, key); err == nil && found {
			return model.ContentRef{Algorithm: "sha256", Digest: digest, Size: st.Size()}, ports.FileFacts{
				SizeBytes:          st.Size(),
				ModifiedAtUnixNano: st.ModTime().UnixNano(),
			}, nil
		}
		ref, facts, err := a.deps.Hasher.HashFile(ctx, absPath)
		if err != nil {
			return ref, facts, err
		}
		_ = a.deps.HashCache.Save(ctx, []ports.HashCacheEntry{{Key: key, Digest: ref.Digest}})
		return ref, facts, nil
	}
}
