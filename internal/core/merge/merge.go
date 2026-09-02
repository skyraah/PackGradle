// Package merge 实现三方文本合并的纯函数 adapter（ADR-0009 §1–§3/§5，票 #87）。
// 以基线为公共祖先对双侧同改的文本资源做 diff3 合并（epiclabs-io/diff3，纯算法
// 库，P4 唯一新顶层依赖；core 禁平台依赖的边界不涉，fsnotify 禁令照旧）：
//   - 零冲突块 → 合并产物（未冲突区域字节级不变：手工注释、键序、空行、缩进
//     天然保真，ADR-0009 §2）；
//   - 含冲突块 → 结构化冲突块证据（域词汇 project/base/runtime + 各侧起始行号）。
//
// 本包只做纯计算：无 I/O、无副作用；三侧全文由调用方（diff 分类层）取自
// CAS 与端点活文件。
package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/epiclabs-io/diff3"

	"packgradle/internal/core/model"
)

// HunkSide 是冲突块在单侧的行片段与起始行号（1 起始，契约 07 §3.3）。
type HunkSide struct {
	Start int      `json:"start"`
	Lines []string `json:"lines"`
}

// Hunk 是单个冲突块的三侧证据（域词汇 project/base/runtime，非 diff3 的 A/O/B，
// CONTEXT.md「冲突块」词条口径）。
type Hunk struct {
	Project HunkSide `json:"project"`
	Base    HunkSide `json:"base"`
	Runtime HunkSide `json:"runtime"`
}

// Result 是三方文本合并的结论：零冲突块时 Merged 非空、Hunks 为空；
// 含冲突块时 Hunks 非空、Merged 为 nil（不产出带冲突标记的产物，
// 语法残缺或带标记的文件绝不会被当干净合并写出）。
type Result struct {
	Merged []byte
	Hunks  []Hunk
}

// Texts 对三侧全文做 diff3 合并（A=project、O=base、B=runtime）。
// 行切分保留原字节（按 \n 切分、\n 还原，CRLF 的 \r 留在行内容里），
// 使「切分→拼接」为字节恒等变换，未冲突区域因此字节级不变；
// 双侧同改相同内容（假冲突）按已决议处理，不进 Hunks。
func Texts(base, project, runtime []byte) Result {
	baseLines := splitLines(base)
	projLines := splitLines(project)
	rtLines := splitLines(runtime)

	// excludeFalseConflicts=true：双侧相同改动不是冲突。
	items := diff3.Diff3Merge[string](projLines, baseLines, rtLines, true)

	res := Result{}
	var merged []string
	for _, item := range items {
		if item.Conflict == nil {
			merged = append(merged, item.Ok...)
			continue
		}
		res.Hunks = append(res.Hunks, Hunk{
			Project: hunkSide(item.Conflict.A, item.Conflict.AIndex),
			Base:    hunkSide(item.Conflict.O, item.Conflict.OIndex),
			Runtime: hunkSide(item.Conflict.B, item.Conflict.BIndex),
		})
	}
	if len(res.Hunks) == 0 {
		res.Merged = joinLines(merged)
	}
	return res
}

// DetailJSON 把一文件的全部冲突块定形为 Conflict.Detail 的 hunk JSON
// （契约 07 §3.3）：{"hunks":[{"project":{...},"base":{...},"runtime":{...}}]}。
// SQL 层零 schema 变更（conflicts.detail 为 TEXT，ADR-0009 §3）。
func DetailJSON(hunks []Hunk) (string, error) {
	b, err := json.Marshal(struct {
		Hunks []Hunk `json:"hunks"`
	}{Hunks: hunks})
	if err != nil {
		return "", fmt.Errorf("merge: 冲突块 JSON 序列化失败: %w", err)
	}
	return string(b), nil
}

// Mergeable 报告资源是否允许进入合并判定（永不合并黑名单，ADR-0009 §5）：
// 二进制资源（按行合并无意义）与 `.index` 元数据（只读不写面，ADR-0005 §5）
// 永不合并；其余文本资源默认可合并。
func Mergeable(kind model.ResourceKind, relPath string) bool {
	if kind == model.ResourceBinaryFile {
		return false
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".index" {
			return false
		}
	}
	return true
}

// ValidateMerged 按资源类型分派对合并产物做合法性校验（ADR-0009 §5）：
// toml → BurntSushi 解码、json → 标准库解码、其余纯文本不校验。
// 校验失败 = 合并提议不成立，由调用方降级 conflict_modify（非错误）。
func ValidateMerged(relPath string, merged []byte) error {
	lower := strings.ToLower(relPath)
	switch {
	case strings.HasSuffix(lower, ".toml"):
		var v map[string]any
		if _, err := toml.Decode(string(merged), &v); err != nil {
			return fmt.Errorf("merge: 合并产物 toml 解码失败: %w", err)
		}
	case strings.HasSuffix(lower, ".json"):
		var v any
		if err := json.Unmarshal(merged, &v); err != nil {
			return fmt.Errorf("merge: 合并产物 json 解码失败: %w", err)
		}
	}
	return nil
}

// splitLines 按字节切分行：以 \n 分隔、保留行内全部字节（含 CRLF 的 \r 与
// 末尾空元素），split(join(x)) == x 恒成立，保真合并的前提。
func splitLines(data []byte) []string {
	parts := bytes.Split(data, []byte{'\n'})
	lines := make([]string, len(parts))
	for i, p := range parts {
		lines[i] = string(p)
	}
	return lines
}

// joinLines 以 \n 还原行序列（splitLines 的逆变换，字节恒等）。
func joinLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n"))
}

// hunkSide 构造单侧证据：起始行号转 1 起始；行片段去尾部 \r
// （CRLF 行的展示净化，不影响合并产物字节）。
func hunkSide(lines []string, index int) HunkSide {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimSuffix(l, "\r")
	}
	return HunkSide{Start: index + 1, Lines: out}
}
