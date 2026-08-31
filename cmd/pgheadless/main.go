// pgheadless 是新架构 P1 只读核心的 headless 验证入口：
// 不启动 Wails，直接跑通 PrepareRelation → CreateRelation → StartScan → PrepareSync → GetPlan。
// 用法：pgheadless -project <packwiz项目根> -instance <Prism实例目录> [-data <用户数据目录>] [-resolve]
//
// -resolve（A 口径 headless 链路，验收规格 §1.1）：PrepareSync 后对 draft
// 计划执行 ResolvePlan → GetPlan，补全规格要求的完整链路。冲突选择取固定
// 策略（initialize_choice 取 project 侧优先、modify 类取 take_project）；
// 不可裁决冲突（identity_ambiguous/mapping_collision）直接失败带证据。
//
// T14 性能基线：-metrics <file> 时输出分项 JSON（扫描四相耗时 + 本次扫描
// hash cache 命中 delta），供 task acceptance:perf 冷/热两轮采集与
// pgfixture -eval 门槛评估。
//
// T09（票 #46）apply 度量增量：记录形态升为 p2-perf-run/1——-apply 链路
// 成功时追加 apply 段（staging/applying/verifying 分相 + apply 总耗时 +
// 峰值内存增量，采样口径见 apply.go applyPeakMemory），机器规格四元组沿 P1。
//
// -apply（P2 A 口径主链路，票 #40）：-resolve 链路之后 ConfirmPlan → 轮询
// GetTask 至终态 → committed/ListCommits/applied 断言链（实现在 apply.go；
// Taskfile acceptance:headless 同目录两遍跑，第二遍 noop 收口）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/core/model"
	"packgradle/internal/store"
)

func main() {
	projectRoot := flag.String("project", "", "Packwiz 项目根目录（含 pack.toml）")
	instanceDir := flag.String("instance", "", "Prism 实例目录（含 instance.cfg）")
	dataRoot := flag.String("data", "", "用户数据目录（默认系统用户数据目录下 PackGradle）")
	metricsPath := flag.String("metrics", "", "分项指标 JSON 输出路径（T14 性能基线，可选）")
	resolve := flag.Bool("resolve", false, "PrepareSync 后执行 ResolvePlan → GetPlan（A 口径 headless 链路）")
	apply := flag.Bool("apply", false, "ResolvePlan 后 ConfirmPlan → Apply → committed 断言链（P2 A 口径主链路）")
	flag.Parse()
	if *projectRoot == "" || *instanceDir == "" {
		flag.Usage()
		os.Exit(2)
	}
	runStart := time.Now()

	// 绝对化输入：库内端点登记使用 canonical 绝对路径，复用匹配与 metrics
	// 记录都必须与之一致
	projectAbs, err := filepath.Abs(*projectRoot)
	fatalOn(err, "解析项目根绝对路径")
	instanceAbs, err := filepath.Abs(*instanceDir)
	fatalOn(err, "解析实例目录绝对路径")

	root := *dataRoot
	if root == "" {
		var err error
		root, err = store.DefaultRoot()
		if err != nil {
			log.Fatalf("定位用户数据目录失败: %v", err)
		}
	}
	stack, err := bootstrap.Build(root)
	if err != nil {
		log.Fatalf("装配失败: %v", err)
	}
	defer stack.Close()

	ctx := context.Background()
	app := stack.App

	// 命中计数以本次扫描的 delta 记账（GetHashCacheStats 为进程生命周期累计）
	before, err := app.GetHashCacheStats(ctx)
	fatalOn(err, "GetHashCacheStats")

	rel := ensureRelation(ctx, app, projectAbs, instanceAbs)
	dump("Relation", rel)

	task, err := app.StartScan(ctx, rel.RelationID)
	fatalOn(err, "StartScan")
	fmt.Printf("StartScan -> task %s\n", task.TaskID)

	waitScan(ctx, app, rel.RelationID)
	after, err := app.GetHashCacheStats(ctx)
	fatalOn(err, "GetHashCacheStats")

	timing := lastScanTiming(app)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	fatalOn(err, "GetWorkspace")
	dump("GetWorkspace", ws)

	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	fatalOn(err, "PrepareSync")
	dump("PrepareSync", plan)

	got, err := app.GetPlan(ctx, plan.PlanID)
	fatalOn(err, "GetPlan")
	dump("GetPlan", got)

	var applyStats *applyChainStats
	if *apply {
		stats, err := runApplyChain(ctx, app, rel, got)
		if err != nil {
			log.Fatalf("-apply 链路失败: %v", err)
		}
		applyStats = stats
		fmt.Println("headless -apply 链路完成（ConfirmPlan → committed 断言全过）")
	} else if *resolve {
		resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{
			PlanID:      plan.PlanID,
			Resolutions: defaultResolutions(plan.Conflicts),
		})
		fatalOn(err, "ResolvePlan")
		dump("ResolvePlan", resolved)
		final, err := app.GetPlan(ctx, resolved.PlanID)
		fatalOn(err, "GetPlan(resolved)")
		dump("GetPlan(resolved)", final)
		fmt.Println("headless 全链路完成（含 ResolvePlan → GetPlan）")
	} else {
		fmt.Println("headless 链路完成（-resolve 未指定，止于 GetPlan）")
	}

	if *metricsPath != "" {
		rec := metricsRecord{
			// T09：p1-perf-run/1 → p2-perf-run/1（-apply 时追加 apply 段）
			Schema:      "p2-perf-run/1",
			CapturedAt:  time.Now().UTC().Format(time.RFC3339),
			ProjectRoot: projectAbs,
			InstanceDir: instanceAbs,
			DataRoot:    root,
			Machine: machineInfo{
				Host: hostName(), OS: runtime.GOOS, Arch: runtime.GOARCH,
				GoVersion: runtime.Version(), CPUs: runtime.NumCPU(),
			},
			ScanPhasesMS: scanPhasesMS{
				ProjectScan: timing.ProjectScanMs, RuntimeScan: timing.RuntimeScanMs,
				Normalize: timing.NormalizeMs, Persist: timing.PersistMs,
			},
			ScanTotalMS: timing.TotalMs,
			RunTotalMS:  time.Since(runStart).Milliseconds(),
			HashCache: hashDelta{
				Hits:     after.Hits - before.Hits,
				Misses:   after.Misses - before.Misses,
				HitRatio: deltaRatio(after, before),
			},
		}
		if applyStats != nil {
			rec.Apply = applyStats
		}
		writeMetrics(*metricsPath, rec)
	}
}

// ensureRelation 返回给定端点对的 Relation：数据目录里已有同一端点对
//（热扫描重跑、重复执行）→ 直接复用；没有匹配项 → 走 PrepareRelation
//（带 §2.1 受管范围建议）→ CreateRelation。绝不盲取无关工作区——
// -metrics 记录的端点路径必须与 -project/-instance 指向一致。
func ensureRelation(ctx context.Context, app syncapp.Application, projectRoot, instanceDir string) view.RelationView {
	page, err := app.ListWorkspaces(ctx, ports.PageRequest{Limit: 10})
	fatalOn(err, "ListWorkspaces")
	if page.NextCursor != "" {
		log.Fatalf("工作区超过一页，pgheadless 仅支持单 Relation fixture 场景")
	}
	gameDir := filepath.Join(filepath.Clean(instanceDir), "minecraft")
	for _, ws := range page.Items {
		if sameDir(ws.Relation.Project.RootPath, projectRoot) &&
			sameDir(ws.Relation.Runtime.RootPath, gameDir) {
			return ws.Relation
		}
	}

	prep, err := app.PrepareRelation(ctx, model.PrepareRelationInput{
		ProjectRoot:        projectRoot,
		RuntimeInstanceDir: instanceDir,
		// 验收规格 §2.1：config/kubejs/scripts 受管文件纳入扫描范围
		Suggestions: []string{"config", "kubejs", "scripts"},
	})
	fatalOn(err, "PrepareRelation")
	dump("PrepareRelation", prep)

	rel, err := app.CreateRelation(ctx, prep.PreparationID)
	fatalOn(err, "CreateRelation")
	return rel
}

// sameDir 容忍盘符大小写与路径分隔符差异的目录相等比较（Windows）。
func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b)))
}

// defaultResolutions 为 draft 计划的全部冲突生成固定策略选择：
// initialize_choice 优先 project 侧（packwiz 是事实源）、modify 类取
// take_project。identity_ambiguous/mapping_collision 不可经 Resolution
// 裁决（plan.validChoice 拒绝），出现即 fatal 带证据。
func defaultResolutions(conflicts []model.Conflict) []model.Resolution {
	res := make([]model.Resolution, 0, len(conflicts))
	for _, c := range conflicts {
		switch c.Kind {
		case model.ConflictInitialize:
			choice := model.ChoiceInitializeFromRuntime
			if c.Project != nil {
				choice = model.ChoiceInitializeFromProject
			}
			res = append(res, model.Resolution{ResourceID: c.ResourceID, Choice: choice})
		case model.ConflictModifyModify, model.ConflictDeleteModify:
			res = append(res, model.Resolution{ResourceID: c.ResourceID, Choice: model.ChoiceTakeProject})
		default:
			log.Fatalf("不可裁决冲突 %s（kind=%s, detail=%s）", c.ResourceID, c.Kind, c.Detail)
		}
	}
	return res
}

// lastScanTiming 经非导出能力读取扫描分相耗时（不入 transport 契约）。
func lastScanTiming(app syncapp.Application) view.ScanTimingView {
	type timingSource interface{ LastScanTiming() view.ScanTimingView }
	ts, ok := app.(timingSource)
	if !ok {
		return view.ScanTimingView{}
	}
	return ts.LastScanTiming()
}

func deltaRatio(after, before view.HashCacheStatsView) float64 {
	hits := after.Hits - before.Hits
	total := hits + (after.Misses - before.Misses)
	if total <= 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// ---- metrics 记录形态（p2-perf-run/1；pgfixture -eval 读取 ScanTotalMS/
// HashCache 与 apply 段。apply 段形态定义在 apply.go，-apply 链路产出）----

type machineInfo struct {
	Host      string `json:"host"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
	CPUs      int    `json:"cpus"`
}

type scanPhasesMS struct {
	ProjectScan int64 `json:"project_scan"`
	RuntimeScan int64 `json:"runtime_scan"`
	Normalize   int64 `json:"normalize"`
	Persist     int64 `json:"persist"`
}

type hashDelta struct {
	Hits     int64   `json:"hits"`
	Misses   int64   `json:"misses"`
	HitRatio float64 `json:"hit_ratio"`
}

type metricsRecord struct {
	Schema       string           `json:"schema"`
	CapturedAt   string           `json:"captured_at"`
	ProjectRoot  string           `json:"project_root"`
	InstanceDir  string           `json:"instance_dir"`
	DataRoot     string           `json:"data_root"`
	Machine      machineInfo      `json:"machine"`
	ScanPhasesMS scanPhasesMS     `json:"scan_phases_ms"`
	ScanTotalMS  int64            `json:"scan_total_ms"`
	RunTotalMS   int64            `json:"run_total_ms"`
	HashCache    hashDelta        `json:"hash_cache"`
	Apply        *applyChainStats `json:"apply,omitempty"` // 仅 -apply 链路成功时非空
}

func writeMetrics(path string, rec metricsRecord) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatalf("创建 metrics 目录失败: %v", err)
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		log.Fatalf("metrics 序列化失败: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Fatalf("写入 metrics 失败: %v", err)
	}
	fmt.Printf("== metrics ==\n%s\n", b)
}

func hostName() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// waitScan 轮询直到无活动任务（事件不是事实源，以查询 API 为准）。
func waitScan(ctx context.Context, app syncapp.Application, relationID string) {
	for i := 0; i < 300; i++ {
		page, err := app.ListTasks(ctx, relationID, true, ports.PageRequest{Limit: 5})
		if err == nil && len(page.Items) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Fatalf("扫描任务超时未结束（relation=%s）", relationID)
}

func dump(stage string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("%s 序列化失败: %v", stage, err)
	}
	fmt.Printf("== %s ==\n%s\n", stage, b)
}

func fatalOn(err error, stage string) {
	if err != nil {
		log.Fatalf("%s 失败: %v", stage, err)
	}
}
