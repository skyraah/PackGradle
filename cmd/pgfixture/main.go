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

// 门槛（验收规格 §2.3 沿用 + P2 §3 新增）：冷 ≤10s、热 ≤2s、热命中率 ≥95%；
// 冷 apply ≤30s、Apply 峰值内存增量 <256MiB。
const (
	coldBudgetMS     = 10_000
	warmBudgetMS     = 2_000
	warmHitRatioGate = 0.95
	applyBudgetMS    = 30_000
	applyPeakBytes   = uint64(256) << 20 // 256MiB
)

func main() {
	out := flag.String("out", "", "fixture 生成目标目录（-eval/-serve 未指定时必填）")
	seed := flag.Int64("seed", 20260830, "全局确定性种子")
	mods := flag.Int("mods", 0, "mod 数量（0 取生产规模默认值；acceptance:headless 用小规模）")
	textFiles := flag.Int("text-files", 0, "config/kubejs/scripts 文件数量（0 取生产规模默认值）")
	plainMods := flag.Int("plain-mods", 0, "无 CF 声明 mod 数量（票 #60 -restore 验收变体）")
	eval := flag.String("eval", "", "评估模式：逗号分隔的 cold,warm[,apply[,restore[,gc]]] 记录路径（apply 及之后可选）")
	serveAddr := flag.String("serve", "", "假 CDN 进程模式（票 #66）：监听地址（空串不启用；127.0.0.1:0 自动分配）")
	flag.Parse()

	switch {
	case *serveAddr != "":
		runServe(serveOptions{addr: *serveAddr})
	case *eval != "":
		os.Exit(runEval(*eval))
	default:
		runGenerate(*out, *mods, *textFiles, *plainMods, *seed)
	}
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
	if len(parts) != 2 && len(parts) != 3 {
		fmt.Fprintln(os.Stderr, "-eval 需要 cold,warm 或 cold,warm,apply 记录路径")
		return 2
	}
	cold, err := readRecord(parts[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取冷记录:", err)
		return 2
	}
	warm, err := readRecord(parts[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取热记录:", err)
		return 2
	}
	var applyRec *applyMetrics
	if len(parts) == 3 {
		rec, err := readRecord(parts[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取 apply 记录:", err)
			return 2
		}
		if rec.Apply == nil {
			fmt.Fprintf(os.Stderr, "%s: 记录无 apply 段（须为 pgheadless -apply -metrics 产出）\n", parts[2])
			return 2
		}
		applyRec = rec.Apply
	}
	return evalRecords(cold, warm, applyRec)
}

// evalRecords 门槛评估（纯函数，单测覆盖）：扫描三项恒评（P1 §2.3 沿用）；
// applyRec 非 nil 时加评 apply 两项（P2 §3）。返回退出码：0 达标 / 1 超标。
// 命中率由 hits/misses 现算，不信任记录中的存储值（防御写入端口径漂移）。
func evalRecords(cold, warm *perfRecord, applyRec *applyMetrics) int {
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
	if applyRec != nil {
		fmt.Printf("冷 apply %d ms（kind=%s 操作数=%d，门槛 ≤%d ms）\n",
			applyRec.ApplyTotalMS, applyRec.Kind, applyRec.OperationCount, applyBudgetMS)
		fmt.Printf("Apply 峰值内存增量 %.1f MiB（口径 %s，门槛 <%d MiB）\n",
			applyRec.PeakMemory.DeltaMiB, applyRec.PeakMemory.Metric,
			applyPeakBytes>>20)
		if applyRec.ApplyTotalMS > applyBudgetMS {
			fmt.Printf("FAIL: 冷 apply 超门槛\n")
			failed = true
		}
		if applyRec.PeakMemory.DeltaBytes >= applyPeakBytes {
			fmt.Printf("FAIL: Apply 峰值内存增量超门槛\n")
			failed = true
		}
	}
	if failed {
		fmt.Println("性能基线未达标（验收规格：任一超标 = 不得标完成，需记录原因分析）")
		return 1
	}
	if applyRec != nil {
		fmt.Println("性能基线达标（扫描三项 + apply 两项，五门槛全过）")
	} else {
		fmt.Println("性能基线达标（冷/热/命中率三项全过）")
	}
	return 0
}

// perfRecord 是 pgheadless -metrics 的记录形态（p1-perf-run/1；P2 起为
// p2-perf-run/1，追加 apply 段）。eval 只读门槛相关字段。
type perfRecord struct {
	Schema      string        `json:"schema"`
	ScanTotalMS int64         `json:"scan_total_ms"`
	HashCache   struct {
		Hits     int64   `json:"hits"`
		Misses   int64   `json:"misses"`
		HitRatio float64 `json:"hit_ratio"`
	} `json:"hash_cache"`
	Apply *applyMetrics `json:"apply,omitempty"`
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
	if r.Schema != "p1-perf-run/1" && r.Schema != "p2-perf-run/1" {
		return nil, fmt.Errorf("%s: 不是 p1-perf-run/1 或 p2-perf-run/1 记录", path)
	}
	return &r, nil
}
