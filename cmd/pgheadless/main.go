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

	"packgradle/internal/appconfig"
	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/cdnproc"
	"packgradle/internal/core/model"
	"packgradle/internal/download"
	"packgradle/internal/store"
)

// dnlManagedCDN 是 -download 链自动拉起的假 CDN 进程句柄（装配前拉起——引擎
// BaseURL 必须装配前确定；main defer Close，链内附着复用）。
var dnlManagedCDN *cdnproc.Serve

func main() {
	projectRoot := flag.String("project", "", "Packwiz 项目根目录（含 pack.toml）")
	instanceDir := flag.String("instance", "", "Prism 实例目录（含 instance.cfg）")
	dataRoot := flag.String("data", "", "用户数据目录（默认系统用户数据目录下 PackGradle）")
	metricsPath := flag.String("metrics", "", "分项指标 JSON 输出路径（T14 性能基线，可选）")
	resolve := flag.Bool("resolve", false, "PrepareSync 后执行 ResolvePlan → GetPlan（A 口径 headless 链路）")
	apply := flag.Bool("apply", false, "ResolvePlan 后 ConfirmPlan → Apply → committed 断言链（P2 A 口径主链路）")
	commits := flag.Int("commits", 0, "连续小 apply 造 N 个提交（票 #64 acceptance:gc 历史夹具；每轮 project 侧加一个文件）")
	gcRun := flag.Bool("gc", false, "RequestGC → 等终态 → 墓碑/存活/引用图不变式断言链（票 #64 acceptance:gc 主链）")
	revive := flag.String("revive", "", "从回收站人工复活指定 digest（票 #64 CLI 形态；解压回 objects 并置回 ready）")
	keepCommits := flag.Int("keep-commits", 0, "写入 config.toml [retention] keep_commits 后继续（0=不动配置；验收 K=3 保底用）")
	restore := flag.Bool("restore", false, "回滚五场景断言链（P3 票 #60 四场景 + 票 #88 metafile 捕获回滚：ADR-0012 出口①；需 -plain-mods 夹具；假 CDN 进程自动拉起供探测端点）")
	cdnURL := flag.String("cdn", "", "假 CDN BaseURL（票 #66 验收缝，如 http://127.0.0.1:PORT/files）：下载引擎与 CF 探测指向假 CDN 进程（pgfixture -serve），零真网；空 = 生产 CDN 前缀")
	downloadChain := flag.Bool("download", false, "假 CDN 五场景断言链（票 #66 acceptance:download：成功链/探测降标/failed 可重入/剔除语义/续传；零真网。独立 fixture 与数据目录；-cdn 为空时自动拉起 pgfixture -serve）")
	restoreTarget := flag.Bool("restore-target", false, "restore 强杀目标进程（票 #66 acceptance:recovery:restore）：建夹具历史（c1/c2）→ PrepareRestore(最老提交) → ConfirmRestorePlan → 轮询至 committed；stdout 相位标记供 pgrecovery killwindow 观察（-cdn 为空时自动拉起假 CDN 进程）")
	restoreCold := flag.Bool("restore-cold", false, "3000 fixture restore 冷链路度量（票 #66 acceptance:perf：漂移删全部受管文本 → restore c1 → committed exact；-metrics 记 restore 段，pgfixture -eval 评 restore 冷 ≤30s/内存 <256MiB）")
	setAuthorized := flag.Int("set-authorized", -1, "设置工作区授权开关后退出（票 #66 L1 数据准备：1=开启快速更新授权，0=关闭；需 -data 与 -project/-instance 指向既有关系）")
	pgfixtureBin := flag.String("pgfixture", filepath.Join("bin", "pgfixture.exe"), "pgfixture 可执行文件（-download 自动拉起假 CDN 进程用）")
	dlWork := flag.String("download-work", filepath.Join("build", "download"), "-download 链工作目录（夹具）")
	dlRecord := flag.String("record", "", "-download 记录 JSON 路径（空=自动 docs/acceptance/records/p3-download-<date>-<host>.json；\"-\"=不落盘）")
	flag.Parse()

	// -revive 只需数据根，不需 fixture 端点。
	if *revive != "" {
		runRevive(*dataRoot, *revive)
		return
	}
	// 票 #66：-set-authorized L1 数据准备面（BuildWithRetention 装配 Settings）。
	if *setAuthorized >= 0 {
		runSetAuthorized(*dataRoot, *projectRoot, *instanceDir, *setAuthorized == 1)
		return
	}
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

	// -restore 链路种子文件须在首次扫描前落位（受管 config 面入基线）。
	if *restore {
		if err := rstSeedFiles(projectAbs, filepath.Join(instanceAbs, "minecraft")); err != nil {
			log.Fatalf("-restore 种子文件写入失败: %v", err)
		}
	}

	root := *dataRoot
	if root == "" {
		var err error
		root, err = store.DefaultRoot()
		if err != nil {
			log.Fatalf("定位用户数据目录失败: %v", err)
		}
	}
	// -keep-commits 在装配前写入（config.toml [retention]，票 #64）：装配读
	// 同一文件，GC 引擎经 Retention 端口取到新值；装配后写只落另一实例内存。
	if *keepCommits > 0 {
		writeKeepCommits(root, *keepCommits)
	}
	// 统一经 BuildWithRetention 装配（票 #64）：config.toml [retention] 与产品
	// 同源（headless Build 的 nil 端口只退默认值，读不回写）。
	retentionMgr, err := appconfig.NewConfigManagerAtLoaded(filepath.Join(root, "config.toml"))
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	// -cdn 注入（票 #66 验收缝）：下载引擎/CF 探测指向假 CDN 进程（零真网），
	// 同时注入快退避测试缝（dlTestStack 先例：429/503 重试面不拖慢验收链路，
	// 4 次重试×1ms 与生产指数退避 1s→30s 的分桶语义不变）。空 = 生产行为；
	// 但 -download 链在 -cdn 未给时自动拉起假 CDN 进程（装配前置——引擎
	// BaseURL 必须在装配前确定；句柄交链内复用，进程生命周期随本进程）。
	dlOpts := download.Options{}
	if *cdnURL != "" {
		dlOpts.BaseURL = *cdnURL
		dlOpts.Backoff = func(int) time.Duration { return time.Millisecond }
		dlOpts.Sleep = func(context.Context, time.Duration) error { return nil }
		fmt.Printf("== -cdn == 下载引擎/CF 探测 → %s（快退避验收缝）\n", *cdnURL)
	} else if *downloadChain || *restoreTarget || *restore {
		s, err := cdnproc.StartServe(*pgfixtureBin, "127.0.0.1:0")
		fatalOn(err, "拉起假 CDN 进程（验收链自动管理）")
		defer s.Close()
		dnlManagedCDN = s
		*cdnURL = s.URL()
		dlOpts.BaseURL = *cdnURL
		dlOpts.Backoff = func(int) time.Duration { return time.Millisecond }
		dlOpts.Sleep = func(context.Context, time.Duration) error { return nil }
		fmt.Printf("== -cdn == 下载引擎/CF 探测 → %s（自动拉起假 CDN 进程，快退避验收缝）\n", *cdnURL)
	}
	stack, err := bootstrap.BuildWithDownloadOptions(root, retentionMgr, dlOpts)
	if err != nil {
		log.Fatalf("装配失败: %v", err)
	}
	defer stack.Close()

	ctx := context.Background()
	app := stack.App

	// 票 #64：-commits（历史夹具）与 -gc（验收链）独立成模，不走单计划链路。
	if *commits > 0 {
		rel0 := ensureRelation(ctx, app, projectAbs, instanceAbs)
		seedCommits(ctx, app, rel0, projectAbs, *commits)
		fmt.Printf("== -commits == 已造 %d 个提交\n", *commits)
		return
	}
	if *gcRun {
		rel0 := ensureRelation(ctx, app, projectAbs, instanceAbs)
		gcStats := &gcChainStats{Kind: "gc", Probes: os.Getenv("PGHEADLESS_GC_PROBES") == "1"}
		gcStart := time.Now()
		if err := runGCChain(ctx, stack, app, rel0, projectAbs, gcStats); err != nil {
			log.Fatalf("-gc 链路失败: %v", err)
		}
		gcStats.GCTotalMS = time.Since(gcStart).Milliseconds()
		fmt.Printf("== -gc 计时 == %d ms（门槛 ≤30s，验收规格 §7）\n", gcStats.GCTotalMS)
		if *metricsPath != "" {
			writeMetrics(*metricsPath, metricsRecord{
				Schema: "p3-perf-run/1", CapturedAt: time.Now().UTC().Format(time.RFC3339),
				ProjectRoot: projectAbs, InstanceDir: instanceAbs, DataRoot: root,
				Machine: newMachineInfo(), GC: gcStats,
			})
		}
		return
	}
	// 票 #66：-download 五场景链（独立模，不走主链路的 plan 断言面）。
	// 夹具由链内生成（先夹具后登记关系）；假 CDN 进程已在装配前拉起
	//（dnlManagedCDN），链内附着复用。
	if *downloadChain {
		if err := runDownloadChain(dnlChainEnv{
			app: app, projectRoot: projectAbs, instanceDir: instanceAbs, cdnFlag: *cdnURL,
			managed:      dnlManagedCDN,
			pgfixtureBin: *pgfixtureBin, work: *dlWork, recordPath: *dlRecord,
		}); err != nil {
			log.Fatalf("-download 链路失败: %v", err)
		}
		return
	}

	// 票 #66：-restore-target restore 强杀目标链（pgrecovery -mode restore 的
	// 子进程面；夹具骨架由链内生成——先夹具后登记关系）。
	if *restoreTarget {
		cdn := dnlManagedCDN
		if cdn == nil && *cdnURL != "" {
			cdn = cdnproc.Attach(*cdnURL) // 外部假 CDN（pgrecovery harness 供给）
		}
		if err := runRestoreTarget(ctx, app, projectAbs, instanceAbs, cdn); err != nil {
			log.Fatalf("-restore-target 链路失败: %v", err)
		}
		return
	}

	// 票 #66：-restore-cold perf 链（独立模；漂移 + restore c1 + 度量）。
	if *restoreCold {
		rel0 := ensureRelation(ctx, app, projectAbs, instanceAbs)
		stats, err := runRestoreCold(ctx, app, rel0, instanceAbs)
		if err != nil {
			log.Fatalf("-restore-cold 链路失败: %v", err)
		}
		if *metricsPath != "" {
			writeMetrics(*metricsPath, metricsRecord{
				Schema: "p3-perf-run/1", CapturedAt: time.Now().UTC().Format(time.RFC3339),
				ProjectRoot: projectAbs, InstanceDir: instanceAbs, DataRoot: root,
				Machine: newMachineInfo(), Restore: stats,
			})
		}
		return
	}

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
	} else if *restore {
		// 场景⑤（票 #88）的 redownload 候选行探测需要确定性 CDN 端点：自动拉起
		// 的假 CDN 优先；外部 -cdn 指定时附着同一进程面。
		cdn := dnlManagedCDN
		if cdn == nil && *cdnURL != "" {
			cdn = cdnproc.Attach(*cdnURL)
		}
		if err := runRestoreChain(ctx, stack, cdn, app, rel, projectAbs, instanceAbs, root); err != nil {
			log.Fatalf("-restore 链路失败: %v", err)
		}
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
			Schema:      "p3-perf-run/1",
			CapturedAt:  time.Now().UTC().Format(time.RFC3339),
			ProjectRoot: projectAbs,
			InstanceDir: instanceAbs,
			DataRoot:    root,
			Machine: machineInfo{
				OS: runtime.GOOS, Arch: runtime.GOARCH,
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

// machineInfo 是机器规格（R2 脱敏，ADR-0011 §7：不再采集 os.Hostname——
// 性能记录不暴露设备身份；OS/Arch/GoVersion/CPUs 属通用环境信息保留）。
type machineInfo struct {
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
	Schema       string             `json:"schema"`
	CapturedAt   string             `json:"captured_at"`
	ProjectRoot  string             `json:"project_root"`
	InstanceDir  string             `json:"instance_dir"`
	DataRoot     string             `json:"data_root"`
	Machine      machineInfo        `json:"machine"`
	ScanPhasesMS scanPhasesMS       `json:"scan_phases_ms"`
	ScanTotalMS  int64              `json:"scan_total_ms"`
	RunTotalMS   int64              `json:"run_total_ms"`
	HashCache    hashDelta          `json:"hash_cache"`
	Apply        *applyChainStats   `json:"apply,omitempty"`   // 仅 -apply 链路成功时非空
	Restore      *restoreChainStats `json:"restore,omitempty"` // 仅 -restore-cold 链路（票 #66）
	GC           *gcChainStats      `json:"gc,omitempty"`      // 仅 -gc 链路（票 #66）
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

// runSetAuthorized 设置工作区授权开关（票 #66 L1 数据准备：授权模式开态）。
// 走 SettingsService（BuildWithRetention 装配）——与前端同一条 wire 面。
func runSetAuthorized(dataRoot, projectRoot, instanceDir string, enabled bool) {
	root := dataRoot
	if root == "" {
		var err error
		root, err = store.DefaultRoot()
		if err != nil {
			log.Fatalf("定位用户数据目录失败: %v", err)
		}
	}
	retentionMgr, err := appconfig.NewConfigManagerAtLoaded(filepath.Join(root, "config.toml"))
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	stack, err := bootstrap.BuildWithDownloadOptions(root, retentionMgr, download.Options{})
	if err != nil {
		log.Fatalf("装配失败: %v", err)
	}
	defer stack.Close()
	if stack.Settings == nil {
		log.Fatalf("Settings 未装配（数据根缺 config.toml？）")
	}
	ctx := context.Background()
	rel0 := ensureRelation(ctx, stack.App, mustAbs(projectRoot), mustAbs(instanceDir))
	ws, err := stack.Settings.SetWorkspaceAuthorized(rel0.RelationID, enabled)
	if err != nil {
		log.Fatalf("SetWorkspaceAuthorized 失败: %v", err)
	}
	fmt.Printf("== -set-authorized == 关系 %s authorized=%v（投影 authorized_apply=%v）\n",
		rel0.RelationID, enabled, ws.AuthorizedApply)
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		log.Fatalf("解析绝对路径 %s: %v", p, err)
	}
	return a
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
