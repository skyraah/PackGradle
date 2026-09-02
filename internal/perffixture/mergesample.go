// 合并验收样本与双侧变更注入（票 #87，P4 验收规格 §3.1/§3.3）：
// Generate 落盘含手工注释的 toml 样本与二进制资源样本（双侧同字节），
// DualEdit 对已生成且完成初次同步的 fixture 注入两侧不同改动——
// 同一 toml 两侧互不重叠改动（merge 变体）或同段冲突改动（conflict 变体）。
package perffixture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 合并样本的端点根相对路径（slash 形态）。
const (
	// HandmadeTomlRel 是手工注释 toml 样本路径：config 前缀受默认建议规则
	// 管辖（text_file），注释/键序/空行/缩进样本用于字节级保真断言
	//（ADR-0009 §2 验收口径）。
	HandmadeTomlRel = "config/handmade.toml"
	// BinarySampleRel 是二进制资源样本路径（黑名单真值表的 fixture 侧来源；
	// 需 binary_file 文件规则才会进观察，验收链自建策略）。
	BinarySampleRel = "resources/brand.dat"
)

// binarySampleSize 是二进制资源样本的字节数（4KB，含全值域伪随机字节）。
const binarySampleSize = 4 << 10

// HandmadeToml 是手工注释 toml 样本全文：含手工注释、键序、空行与缩进样本；
// project_anchor/runtime_anchor 两个互不重叠的锚点段供 -dual-edit 注入。
const HandmadeToml = `# 手工注释样本：玩家手写风格的配置文件。
# 第二行注释：验证合并后注释原样保留。

[graphics]
fancy_graphics = false
  render_distance = 12   # 行内注释 + 缩进键序样本


[audio]
master_volume = 0.8

# 双侧变更注入锚点（pgfixture -dual-edit）：两个互不重叠的锚点段。
[project_anchor]
project_marker = "untouched"

[runtime_anchor]
runtime_marker = "untouched"
`

// writeBinarySample 落盘二进制资源样本（raw PRNG 字节，含 NUL——真二进制而非
// 文本；writeRandomFile 的文本/二进制分流按 seed 取模判定，不满足此用途）。
func writeBinarySample(ctx context.Context, path string, seed uint64) error {
	_, err := writeFileFunc(ctx, path, func(w io.Writer) error {
		rng := prng{state: seed}
		buf := make([]byte, binarySampleSize)
		rng.fill(buf)
		_, err := w.Write(buf)
		return err
	})
	return err
}

// DualEdit 对已生成并完成初次同步的 fixture 注入双侧变更（票 #87）：
// 同一 config/handmade.toml 两侧注入不同改动。初次同步前 project 侧无
// config 副本，双侧文件必须都已存在，缺失即报错。
//
// variant=merge：project 侧改 [project_anchor]、runtime 侧改 [runtime_anchor]
// （互不重叠 → 三方合并干净，merged_clean 路径）；
// variant=conflict：双侧同改 [project_anchor]（真冲突 → conflict_modify 块证据）。
func DualEdit(outDir, variant string) error {
	projectFile := filepath.Join(outDir, "project", filepath.FromSlash(HandmadeTomlRel))
	runtimeFile := filepath.Join(outDir, "instance", "minecraft", filepath.FromSlash(HandmadeTomlRel))

	var projEdit, rtEdit func(string) string
	switch variant {
	case "merge":
		projEdit = func(s string) string {
			return strings.Replace(s, `project_marker = "untouched"`, `project_marker = "edited-by-project"`, 1)
		}
		rtEdit = func(s string) string {
			return strings.Replace(s, `runtime_marker = "untouched"`, `runtime_marker = "edited-by-runtime"`, 1)
		}
	case "conflict":
		projEdit = func(s string) string {
			return strings.Replace(s, `project_marker = "untouched"`, `project_marker = "project-side"`, 1)
		}
		rtEdit = func(s string) string {
			return strings.Replace(s, `project_marker = "untouched"`, `project_marker = "runtime-side"`, 1)
		}
	default:
		return fmt.Errorf("dual-edit: 未知变体 %q（合法值 merge|conflict）", variant)
	}

	// 逐侧改写：替换必须恰好命中 1 处（样本形态被改动即报错，防静默错编）。
	apply := func(path string, edit func(string) string) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("dual-edit: %w（初次同步前 project 侧无该文件，须先完成同步）", err)
		}
		edited := edit(string(raw))
		if bytes.Equal(raw, []byte(edited)) {
			return fmt.Errorf("dual-edit: %s 未发生任何改动（样本形态不符）", path)
		}
		return os.WriteFile(path, []byte(edited), 0o644)
	}
	if err := apply(projectFile, projEdit); err != nil {
		return err
	}
	return apply(runtimeFile, rtEdit)
}
