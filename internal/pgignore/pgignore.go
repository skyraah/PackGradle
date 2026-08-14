package pgignore

import (
	"os"
	"path/filepath"

	gitignore "github.com/sabhiram/go-gitignore"
)

// .pgignore 是项目根目录下的忽略文件（gitignore 标准规则），
// 控制「一键关联」时哪些顶层条目不参与建链。
// 默认黑名单：版本控制、工具缓存与包核心文件。

// DefaultContent 是导入项目时创建的默认内容
const DefaultContent = `# PackGradle 一键关联忽略规则（gitignore 语法）
# 以下条目不参与一键建链，可按需增删

.git
.cache
index.toml
pack.toml
packgradle.toml
.pgignore
`

// Ensure 在项目根目录创建 .pgignore（已存在时不覆盖）。
// 返回创建结果（是否新建），供调用方提示。
func Ensure(projectPath string) (created bool, err error) {
	path := filepath.Join(projectPath, ".pgignore")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.WriteFile(path, []byte(DefaultContent), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// CoreExcluded 判断顶层条目是否为必须排除的项目核心文件/目录。
// 这些条目不参与一键关联，且不受 .pgignore 规则影响（即使规则文件损坏/清空也不会误建链）。
func CoreExcluded(name string) bool {
	switch name {
	case ".git", ".cache", "index.toml", "pack.toml", "packgradle.toml", ".pgignore":
		return true
	}
	return false
}

// Matcher 封装 .pgignore 规则匹配
type Matcher struct {
	gi *gitignore.GitIgnore
}

// Load 读取并解析项目 .pgignore；文件不存在时返回匹配一切为 false 的空匹配器。
// 解析失败（非法规则）时同样返回空匹配器，不阻断一键关联。
func Load(projectPath string) *Matcher {
	path := filepath.Join(projectPath, ".pgignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return &Matcher{}
	}
	gi := gitignore.CompileIgnoreLines(splitLines(string(data))...)
	return &Matcher{gi: gi}
}

// Matches 判断相对项目根的条目路径（如 "config" / "modlist.txt"）是否命中忽略规则。
// gitignore 的目录规则（如 "temp/"）按标准应命中目录本身，
// 库实现需路径带尾斜杠才匹配，这里补一次尾斜杠匹配。
func (m *Matcher) Matches(relPath string) bool {
	if m == nil || m.gi == nil {
		return false
	}
	if m.gi.MatchesPath(relPath) {
		return true
	}
	return m.gi.MatchesPath(relPath + "/")
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			line = trimCR(line)
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	return out
}

func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}
