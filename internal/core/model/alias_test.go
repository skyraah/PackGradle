package model

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// R1 别名路径纯函数断言（ADR-0011 §7 R1）：构造含绝对路径的端点错误 →
// 别名化结果为 <project>/… / <runtime>/… 形态、无绝对路径残留。

func TestAliasPath(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("home", "player1", "project")
	cases := []struct {
		name string
		path string
		want string
	}{
		{"root 本身", root, "<project>"},
		{"root 内文件", filepath.Join(root, "mods", "a.pw.toml"), "<project>/mods/a.pw.toml"},
		{"root 内目录", filepath.Join(root, "config"), "<project>/config"},
		{"root 外（上级）", filepath.Join(filepath.Dir(root), "other.jar"), "<project>/…"},
		// Windows 卷上大小写同义；别名后相对段保留原大小写
		{"大小写不同（Windows 同义）", strings.ToUpper(filepath.Join(root, "mods", "b.jar")), "<project>/MODS/B.JAR"},
		{"斜杠形态", filepath.ToSlash(root) + "/config/x.json", "<project>/config/x.json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AliasPath(root, AliasProject, c.path); got != c.want {
				t.Errorf("AliasPath(%q) = %q, 期望 %q", c.path, got, c.want)
			}
		})
	}
}

func TestAliasPathRuntime(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("home", "player1", "instance", "minecraft")
	got := AliasPath(root, AliasRuntime, filepath.Join(root, "mods", "x.jar"))
	if got != "<runtime>/mods/x.jar" {
		t.Errorf("运行侧别名 = %q, 期望 <runtime>/mods/x.jar", got)
	}
}

func TestAliasFor(t *testing.T) {
	if AliasFor(SideProject) != AliasProject {
		t.Errorf("project 侧应为 %s", AliasProject)
	}
	if AliasFor(SideRuntime) != AliasRuntime {
		t.Errorf("runtime 侧应为 %s", AliasRuntime)
	}
}

func TestAliasDetail(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("Users", "player1", "proj")
	sep := string(filepath.Separator)
	text := "packwiz: 解析 " + filepath.Join(root, "mods", "a.pw.toml") + ": toml 语法错误"
	got := AliasDetail(root, AliasProject, text)
	if strings.Contains(got, "player1") || strings.Contains(got, root) {
		t.Errorf("别名化后仍含绝对路径: %q", got)
	}
	// 别名前缀后保留文本原有的分隔符形态（detail 是原样透传文本）
	if !strings.Contains(got, AliasProject+sep+"mods"+sep+"a.pw.toml") {
		t.Errorf("别名形态缺失: %q", got)
	}

	// 端点根出现在错误文本中但大小写与输入不同（realpath 改写大小写）时同样替换
	got2 := AliasDetail(root, AliasProject,
		"端点根不可达: "+strings.ToUpper(root)+sep+"pack.toml: no such dir")
	if strings.Contains(got2, "player1") || strings.Contains(got2, "PLAYER1") {
		t.Errorf("大小写变体未别名化: %q", got2)
	}
	if !strings.Contains(got2, AliasProject) {
		t.Errorf("大小写变体别名缺失: %q", got2)
	}

	// 无关文本原样返回
	if got3 := AliasDetail(root, AliasProject, "无路径错误"); got3 != "无路径错误" {
		t.Errorf("无关文本被改写: %q", got3)
	}
	if got4 := AliasDetail(root, AliasProject, ""); got4 != "" {
		t.Errorf("空文本应原样: %q", got4)
	}

	// 前缀重叠不误伤：root="…/proj" 不替换 "…/project" 的出现
	proj := string(filepath.Separator) + filepath.Join("Users", "player1", "proj")
	sibling := filepath.Dir(proj) + string(filepath.Separator) + "project"
	got5 := AliasDetail(proj, AliasProject, "端点外路径 "+filepath.Join(sibling, "x.toml")+" 不可达")
	if strings.Contains(got5, AliasProject) {
		t.Errorf("前缀重叠目录被误替换: %q", got5)
	}
}

func TestAliasError(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("Users", "player1", "proj")
	err := errors.New("filesystem: 端点路径不可达: " + filepath.Join(root, "pack.toml") + ": not exist")
	got := AliasError(root, AliasProject, err)
	want := "filesystem: 端点路径不可达: " + AliasProject + string(filepath.Separator) + "pack.toml: not exist"
	if got.Error() != want {
		t.Errorf("AliasError 文本 = %q, 期望 %q", got.Error(), want)
	}
	if AliasError(root, AliasProject, nil) != nil {
		t.Error("nil 错误应返回 nil")
	}
}
