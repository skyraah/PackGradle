// pgfixture 是 P1 性能基线的 fixture 生成器 CLI 与验收门槛评估器（验收规格 §2.1/§2.3，
// P2 §3 增量）。
//
// 生成（单命令可重放；产物不入 git，内容由 seed 确定性派生）：
//
//	pgfixture -out <目录> [-seed N] [-mods N] [-text-files N]
//
// 评估（读 pgheadless -metrics 产出的记录，校验门槛，全过退出码 0，任一超标
// 退出码 1）。两份记录评扫描三项（P1 §2.3）；三份记录加评 apply 两项
//（P2 §3：冷 apply ≤30s、Apply 峰值内存增量 <256MiB，第三份须为 -apply 产出）；
// 四/五份记录加评 restore/GC 新门槛（P3 §7：restore 冷 ≤30s、restore 内存增量
// <256MiB、GC ≤30s，票 #66）：
//
//	pgfixture -eval <cold.json>,<warm.json>[,<apply.json>[,<restore.json>[,<gc.json>]]]
//
// 假 CDN 进程模式（票 #66；验收规格 §5.1，实现见 serve.go）：
//
//	pgfixture -serve [127.0.0.1:PORT]
//
// 双侧变更注入（票 #87；P4 验收规格 §3.3）：对已生成并完成初次同步的
// fixture，同一 config/handmade.toml 注入两侧不同改动：
//
//	pgfixture -out <目录> -dual-edit merge|conflict
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"packgradle/internal/perffixture"
)

// 门槛（验收规格 §2.3 沿用 + P2 §3 + P3 §7 新增，票 #66）：冷 ≤10s、热 ≤2s、
// 热命中率 ≥95%；冷 apply ≤30s、Apply 峰值内存增量 <256MiB；restore 冷 ≤30s、
// restore 峰值内存增量 <256MiB、GC ≤30s（download 相位只记录不设门槛）。
const (
	coldBudgetMS     = 10_000
	warmBudgetMS     = 2_000
	warmHitRatioGate = 0.95
	applyBudgetMS    = 30_000
	applyPeakBytes   = uint64(256) << 20 // 256MiB
	restoreBudgetMS  = 30_000
	restorePeakBytes = uint64(256) << 20 // 256MiB
	gcBudgetMS       = 30_000
)

func main() {
	out := flag.String("out", "", "fixture 生成目标目录（-eval/-serve 未指定时必填）")
	seed := flag.Int64("seed", 20260830, "全局确定性种子")
	mods := flag.Int("mods", 0, "mod 数量（0 取生产规模默认值；acceptance:headless 用小规模）")
	textFiles := flag.Int("text-files", 0, "config/kubejs/scripts 文件数量（0 取生产规模默认值）")
	plainMods := flag.Int("plain-mods", 0, "无 CF 声明 mod 数量（票 #60 -restore 验收变体）")
	eval := flag.String("eval", "", "评估模式：逗号分隔的 cold,warm[,apply[,restore[,gc]]] 记录路径（apply 及之后可选）")
	serveAddr := flag.String("serve", "", "假 CDN 进程模式（票 #66）：监听地址（空串不启用；127.0.0.1:0 自动分配）")
	dualEdit := flag.String("dual-edit", "", "双侧变更注入（票 #87）：对 -out 指定的已生成并完成初次同步的 fixture，同一 config/handmade.toml 注入两侧不同改动；取值 merge（互不重叠→干净合并）| conflict（同段改动→真冲突）")
	flag.Parse()

	switch {
	case *serveAddr != "":
		runServe(serveOptions{addr: *serveAddr})
	case *eval != "":
		os.Exit(runEval(*eval))
	case *dualEdit != "":
		os.Exit(runDualEdit(*out, *dualEdit))
	default:
		runGenerate(*out, *mods, *textFiles, *plainMods, *seed)
	}
}

// runDualEdit 双侧变更注入入口（票 #87，验收规格 §3.3）。输出形态独立于
// 生成/评估模式（既有 stderr 断言不受影响）；失败写 stderr 并退出码 1。
func runDualEdit(out, variant string) int {
	if out == "" {
		fmt.Fprintln(os.Stderr, "-dual-edit 需要与 -out 同时指定（fixture 目标目录）")
		return 2
	}
	if err := perffixture.DualEdit(out, variant); err != nil {
		fmt.Fprintln(os.Stderr, "双侧变更注入失败:", err)
		return 1
	}
	fmt.Printf("双侧变更注入完成（variant=%s）:\n  project: config/handmade.toml\n  runtime: minecraft/config/handmade.toml\n", variant)
	return 0
}

func runGenerate(out string, mods, textFiles, plainMods int, seed int64) {
	if out == "" {
		flag.Usage()
		os.Exit(2)
	}
	res, err := perffixture.Generate(context.Background(), perffixture.Options{
		OutDir: out, Seed: seed, Mods: mods, TextFiles: textFiles, PlainMods: plainMods,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成失败:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Printf("fixture 生成完成（seed=%d）:\n%s\n", seed, b)
}

func runEval(spec string) int {
	parts := strings.Split(spec, ",")
	if len(parts) < 2 || len(parts) > 5 {
		fmt.Fprintln(os.Stderr, "-eval 需要 cold,warm 或 cold,warm,apply[,restore[,gc]] 记录路径")
		return 2
	}
	recs := make([]*perfRecord, len(parts))
	for i, p := range parts {
		r, err := readRecord(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取记录:", err)
			return 2
		}
		recs[i] = r
	}
	if len(parts) >= 3 && recs[2].Apply == nil {
		fmt.Fprintf(os.Stderr, "%s: 记录无 apply 段（须为 pgheadless -apply -metrics 产出）\n", parts[2])
		return 2
	}
	if len(parts) >= 4 && recs[3].Restore == nil {
		fmt.Fprintf(os.Stderr, "%s: 记录无 restore 段（须为 pgheadless -restore-cold -metrics 产出）\n", parts[3])
		return 2
	}
	if len(parts) >= 5 && recs[4].GC == nil {
		fmt.Fprintf(os.Stderr, "%s: 记录无 gc 段（须为 pgheadless -gc -metrics 产出）\n", parts[4])
		return 2
	}
	return evalRecords(recs[0], recs[1], recs[2].Apply, getRestore(recs), getGC(recs))
}

func getRestore(recs []*perfRecord) *restoreMetrics {
	for _, r := range recs {
		if r.Restore != nil {
			return r.Restore
		}
	}
	return nil
}

func getGC(recs []*perfRecord) *gcMetrics {
	for _, r := range recs {
		if r.GC != nil {
			return r.GC
		}
	}
	return nil
}

// evalRecords 门槛评估（纯函数，单测覆盖）：扫描三项恒评（P1 §2.3 沿用）；
// applyRec/restoreRec/gcRec 非 nil 时加评对应门槛（P2 §3 / P3 §7，票 #66）。
// 返回退出码：0 达标 / 1 超标。命中率由 hits/misses 现算，不信任记录中的
// 存储值（防御写入端口径漂移）。
func evalRecords(cold, warm *perfRecord, applyRec *applyMetrics, restoreRec *restoreMetrics, gcRec *gcMetrics) int {
	ratio := 0.0
	if total := warm.HashCache.Hits + warm.HashCache.Misses; total > 0 {
		ratio = float64(warm.HashCache.Hits) / float64(total)
	}
	fmt.Printf("冷扫描 %d ms（门槛 ≤%d ms）\n", cold.ScanTotalMS, coldBudgetMS)
	fmt.Printf("热扫描 %d ms（门槛 ≤%d ms）\n", warm.ScanTotalMS, warmBudgetMS)
	fmt.Printf("热命中率 %.2f%%（门槛 ≥%.2f%%，hits=%d misses=%d）\n",
		ratio*100, warmHitRatioGate*100, warm.HashCache.Hits, warm.HashCache.Misses)

	failed := false
	if cold.ScanTotalMS > coldBudgetMS {
		fmt.Printf("FAIL: 冷扫描超门槛\n")
		failed = true
	}
	if warm.ScanTotalMS > warmBudgetMS {
		fmt.Printf("FAIL: 热扫描超门槛\n")
		failed = true
	}
	if ratio < warmHitRatioGate {
		fmt.Printf("FAIL: 热命中率低于门槛\n")
		failed = true
	}
	gates := 3
	if applyRec != nil {
		gates += 2
		fmt.Printf("冷 apply %d ms（kind=%s 操作数=%d，门槛 ≤%d ms）\n",
			applyRec.ApplyTotalMS, applyRec.Kind, applyRec.OperationCount, applyBudgetMS)
		fmt.Printf("Apply 峰值内存增量 %.1f MiB（口径 %s，门槛 <%d MiB）\n",
			applyRec.PeakMemory.DeltaMiB, applyRec.PeakMemory.Metric, applyPeakBytes>>20)
		if applyRec.ApplyTotalMS > applyBudgetMS {
			fmt.Printf("FAIL: 冷 apply 超门槛\n")
			failed = true
		}
		if applyRec.PeakMemory.DeltaBytes >= applyPeakBytes {
			fmt.Printf("FAIL: Apply 峰值内存增量超门槛\n")
			failed = true
		}
	}
	if restoreRec != nil {
		gates += 2
		fmt.Printf("restore 冷全链路 %d ms（prepare=%d staging=%d[下载 %d] applying=%d verifying=%d，操作数=%d，门槛 ≤%d ms）\n",
			restoreRec.RestoreTotalMS, restoreRec.PrepareMS, restoreRec.PhasesMS.Staging,
			restoreRec.StagingDownloadMS, restoreRec.PhasesMS.Applying, restoreRec.PhasesMS.Verifying,
			restoreRec.OperationCount, restoreBudgetMS)
		fmt.Printf("restore 峰值内存增量 %.1f MiB（口径 %s，门槛 <%d MiB）\n",
			restoreRec.PeakMemory.DeltaMiB, restoreRec.PeakMemory.Metric, restorePeakBytes>>20)
		if restoreRec.RestoreTotalMS > restoreBudgetMS {
			fmt.Printf("FAIL: restore 冷链路超门槛\n")
			failed = true
		}
		if restoreRec.PeakMemory.DeltaBytes >= restorePeakBytes {
			fmt.Printf("FAIL: restore 峰值内存增量超门槛\n")
			failed = true
		}
	}
	if gcRec != nil {
		gates++
		fmt.Printf("GC %d ms（墓碑=%d 存活提交=%d 对账违例=%d，门槛 ≤%d ms）\n",
			gcRec.GCTotalMS, gcRec.Tombstones, gcRec.AliveCommits, gcRec.AuditViolations, gcBudgetMS)
		if gcRec.GCTotalMS > gcBudgetMS {
			fmt.Printf("FAIL: GC 超门槛\n")
			failed = true
		}
		if gcRec.AuditViolations != 0 {
			fmt.Printf("FAIL: 引用图对账存在违例\n")
			failed = true
		}
	}
	if failed {
		fmt.Println("性能基线未达标（验收规格：任一超标 = 不得标完成，需记录原因分析）")
		return 1
	}
	fmt.Printf("性能基线达标（%d 项门槛全过）\n", gates)
	return 0
}

// perfRecord 是 pgheadless -metrics 的记录形态（p1-perf-run/1；P2 起为
// p2-perf-run/1 追加 apply 段；P3 起为 p3-perf-run/1 追加 restore/gc 段，票
// #66）。eval 只读门槛相关字段。
type perfRecord struct {
	Schema      string `json:"schema"`
	ScanTotalMS int64  `json:"scan_total_ms"`
	HashCache   struct {
		Hits     int64   `json:"hits"`
		Misses   int64   `json:"misses"`
		HitRatio float64 `json:"hit_ratio"`
	} `json:"hash_cache"`
	Apply   *applyMetrics   `json:"apply,omitempty"`
	Restore *restoreMetrics `json:"restore,omitempty"`
	GC      *gcMetrics      `json:"gc,omitempty"`
}

// applyMetrics 是记录的 apply 段（p2-perf-run/1；形态定义见 cmd/pgheadless/apply.go）。
type applyMetrics struct {
	Kind           string `json:"kind"`
	OperationCount int    `json:"operation_count"`
	ApplyTotalMS   int64  `json:"apply_total_ms"`
	PeakMemory     struct {
		Metric     string  `json:"metric"`
		DeltaBytes uint64  `json:"delta_bytes"`
		DeltaMiB   float64 `json:"delta_mib"`
	} `json:"peak_memory_delta"`
}

// restoreMetrics / gcMetrics 是记录的 restore/gc 段（p3-perf-run/1；形态定义
// 见 cmd/pgheadless/restorecold.go 与 gc.go，票 #66）。
type restoreMetrics struct {
	Kind           string `json:"kind"`
	OperationCount int    `json:"operation_count"`
	PrepareMS      int64  `json:"prepare_ms"`
	PhasesMS       struct {
		Staging   int64 `json:"staging"`
		Applying  int64 `json:"applying"`
		Verifying int64 `json:"verifying"`
	} `json:"phases_ms"`
	StagingDownloadMS int64 `json:"staging_download_ms"`
	RestoreTotalMS    int64 `json:"restore_total_ms"`
	PeakMemory        struct {
		Metric     string  `json:"metric"`
		DeltaBytes uint64  `json:"delta_bytes"`
		DeltaMiB   float64 `json:"delta_mib"`
	} `json:"peak_memory_delta"`
}

type gcMetrics struct {
	Kind                  string `json:"kind"`
	GCTotalMS             int64  `json:"gc_total_ms"`
	Tombstones            int    `json:"tombstones"`
	AliveCommits          int    `json:"alive_commits"`
	AuditViolations       int    `json:"audit_violations"`
	OldestVerifiedObjects int    `json:"oldest_verified_objects"`
}

// readRecord 读记录；schema 兼容 P1（p1-perf-run/1）与 P2（p2-perf-run/1）。
func readRecord(path string) (*perfRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r perfRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Schema != "p1-perf-run/1" && r.Schema != "p2-perf-run/1" && r.Schema != "p3-perf-run/1" {
		return nil, fmt.Errorf("%s: 不是 p1/p2/p3-perf-run/1 记录", path)
	}
	return &r, nil
}
