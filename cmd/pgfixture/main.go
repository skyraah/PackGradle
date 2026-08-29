// pgfixture 是 P1 性能基线的 fixture 生成器 CLI 与验收门槛评估器（验收规格 §2.1/§2.3）。
//
// 生成（单命令可重放；产物不入 git，内容由 seed 确定性派生）：
//
//	pgfixture -out <目录> [-seed N]
//
// 评估（读 pgheadless -metrics 产出的两份记录，校验冷/热/命中率门槛，
// 全过退出码 0，任一超标退出码 1）：
//
//	pgfixture -eval <cold.json>,<warm.json>
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

// 门槛（验收规格 §2.3）：冷 ≤10s、热 ≤2s、热命中率 ≥95%。
const (
	coldBudgetMS     = 10_000
	warmBudgetMS     = 2_000
	warmHitRatioGate = 0.95
)

func main() {
	out := flag.String("out", "", "fixture 生成目标目录（-eval 未指定时必填）")
	seed := flag.Int64("seed", 20260830, "全局确定性种子")
	eval := flag.String("eval", "", "评估模式：逗号分隔的 cold,warm 两份 metrics JSON 路径")
	flag.Parse()

	switch {
	case *eval != "":
		os.Exit(runEval(*eval))
	default:
		runGenerate(*out, *seed)
	}
}

func runGenerate(out string, seed int64) {
	if out == "" {
		flag.Usage()
		os.Exit(2)
	}
	res, err := perffixture.Generate(context.Background(), perffixture.Options{OutDir: out, Seed: seed})
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成失败:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Printf("fixture 生成完成（seed=%d）:\n%s\n", seed, b)
}

func runEval(spec string) int {
	parts := strings.Split(spec, ",")
	if len(parts) != 2 {
		fmt.Fprintln(os.Stderr, "-eval 需要 cold,warm 两份记录路径")
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

	// 命中率由 hits/misses 现算，不信任记录中的存储值（防御写入端口径漂移）
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
	if failed {
		fmt.Println("性能基线未达标（验收规格 §2.3：任一超标 = P1 不得标完成，需记录原因分析）")
		return 1
	}
	fmt.Println("性能基线达标（冷/热/命中率三项全过）")
	return 0
}

// perfRecord 是 pgheadless -metrics 的记录形态（p1-perf-run/1）。
type perfRecord struct {
	Schema      string `json:"schema"`
	ScanTotalMS int64  `json:"scan_total_ms"`
	HashCache   struct {
		Hits     int64   `json:"hits"`
		Misses   int64   `json:"misses"`
		HitRatio float64 `json:"hit_ratio"`
	} `json:"hash_cache"`
}

func readRecord(path string) (*perfRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r perfRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Schema != "p1-perf-run/1" {
		return nil, fmt.Errorf("%s: 不是 p1-perf-run/1 记录", path)
	}
	return &r, nil
}
