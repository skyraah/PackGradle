package errs

// code→locale 一致性测试（T13，票 #23）：Go 端发射的每个 err.*/msg.* 错误码
// 必须在 frontend/src/locales/zh-CN.json 有对应键——缺键即前端裸码泄漏
// （vue-i18n 缺键渲染键名本身）。反向地，locale 中的 err.* 键必须仍有 Go
// 发射点，防止审计漂移积累死键。
//
// 约定：错误码必须以字面量形式出现在非测试 Go 源中（当前无动态拼接的
// err.* 码）；msg.task.<kind>.queued 由任务运行器以 "msg.task."+kind+".queued"
// 动态构造，无法按字面量捕获，改按 model 的 TaskKind 常量矩阵校验
// （同时覆盖前端任务类型回退键 workspaces.taskKind.<kind>）。
//
// 测试只比对键存在性；插值参数（{0} 等）与 args 的对应由契约 03 §3 错误码
// 清单人工维护，不入自动化（调用点分析无法从字符串还原实参数）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// localeFile 是前端唯一语言文件（zh-CN 为基准语言）。
const localeFile = "frontend/src/locales/zh-CN.json"

// codeRe 捕获字符串字面量中的完整错误码（至少两段，排除 "msg.task." 这类
// 动态拼接前缀被截断匹配）。
var codeRe = regexp.MustCompile(`"((?:err|msg)\.[a-z0-9_]+(?:\.[a-z0-9_]+)+)"`)

// kindRe 从 model 提取任务类型常量（TaskKindScan = "scan" 等）。
var kindRe = regexp.MustCompile(`TaskKind\w+\s*=\s*"([a-z]+)"`)

// skippedDirs 是源码遍历排除项：依赖产物、构建输出与前端（前端 TS 内
// 不发射错误码，locale 文件另行直读）。
var skippedDirs = map[string]bool{
	".git": true, "node_modules": true, "frontend": true, "bin": true, "build": true,
}

// repoRoot 是仓库根（测试工作目录为 internal/errs）。
var repoRoot = filepath.Join("..", "..")

// goCodes 遍历仓库非测试 Go 源，收集全部 err.*/msg.* 码字面量。
func goCodes(t *testing.T) map[string]bool {
	t.Helper()
	codes := map[string]bool{}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] || (d.Name() == "testdata" && path != repoRoot) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range codeRe.FindAllStringSubmatch(string(src), -1) {
			codes[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 Go 源: %v", err)
	}
	return codes
}

// taskKinds 读取 model 的 TaskKind 常量；矩阵校验随新任务类型自动扩展。
func taskKinds(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot, "internal", "core", "model", "event.go"))
	if err != nil {
		t.Fatalf("读取 model/event.go: %v", err)
	}
	var kinds []string
	for _, m := range kindRe.FindAllStringSubmatch(string(src), -1) {
		kinds = append(kinds, m[1])
	}
	if len(kinds) == 0 {
		t.Fatal("model/event.go 未提取到任何 TaskKind 常量")
	}
	return kinds
}

// localeKeys 加载 zh-CN 语言文件的扁平键集。
func localeKeys(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(localeFile)))
	if err != nil {
		t.Fatalf("读取 locale: %v", err)
	}
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err != nil {
		t.Fatalf("解析 %s: %v", localeFile, err)
	}
	keys := make(map[string]bool, len(flat))
	for k := range flat {
		keys[k] = true
	}
	return keys
}

// reportMissing 排序后逐条报错，收敛各测试相同的报告形状。
func reportMissing(t *testing.T, items []string, format string) {
	t.Helper()
	slices.Sort(items)
	for _, item := range items {
		t.Errorf(format, item)
	}
}

// TestGoCodesHaveLocaleKeys 正向：Go 发射的每个错误码在 locale 有键。
func TestGoCodesHaveLocaleKeys(t *testing.T) {
	codes := goCodes(t)
	keys := localeKeys(t)
	var missing []string
	for code := range codes {
		if !keys[code] {
			missing = append(missing, code)
		}
	}
	reportMissing(t, missing, "locale 缺键: %s（Go 端发射，前端将裸显键名）")
}

// TestLocaleErrKeysHaveEmitters 反向：locale 的 err.* 键仍有 Go 发射点
// （msg.* 键含运行器动态构造，不适用字面量反向检查，由任务矩阵覆盖）。
func TestLocaleErrKeysHaveEmitters(t *testing.T) {
	codes := goCodes(t)
	var dead []string
	for key := range localeKeys(t) {
		if strings.HasPrefix(key, "err.") && !codes[key] {
			dead = append(dead, key)
		}
	}
	reportMissing(t, dead, "locale 死键: %s（无任何 Go 发射点，应删除或补发射点）")
}

// TestTaskKindMessageMatrix 任务类型矩阵：运行器动态构造
// msg.task.<kind>.queued，前端回退键 workspaces.taskKind.<kind>。
func TestTaskKindMessageMatrix(t *testing.T) {
	keys := localeKeys(t)
	for _, kind := range taskKinds(t) {
		for _, key := range []string{"msg.task." + kind + ".queued", "workspaces.taskKind." + kind} {
			if !keys[key] {
				t.Errorf("locale 缺键: %s（任务类型 %q 动态发射）", key, kind)
			}
		}
	}
}
