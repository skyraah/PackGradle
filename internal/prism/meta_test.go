package prism

import (
	"strings"
	"testing"
)

const samplePW = `name = "Example Mod"
filename = "example.jar"
side = "both"

[download]
url = "https://example.com/example.jar"
hash-format = "sha256"
hash = "abc123"

[update.modrinth]
mod-id = "abc"
version = "1.2.3"
`

// 推送：side 条目后插入四个 x-prismlauncher-* 字段，其余内容原样保留
func TestToPrismFormat(t *testing.T) {
	out, err := ToPrismFormat([]byte(samplePW), PrismMeta{
		Loaders:     "forge:47.4.10",
		MCVersions:  "1.20.1",
		ReleaseType: "release",
		Version:     "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// side 行后紧跟四个字段
	if !strings.Contains(s, "side = \"both\"\nx-prismlauncher-loaders = \"forge:47.4.10\"\nx-prismlauncher-mc-versions = \"1.20.1\"\nx-prismlauncher-release-type = \"release\"\nx-prismlauncher-version-number = \"1.2.3\"") {
		t.Errorf("应在 side 后插入四个字段:\n%s", s)
	}
	// 其余内容保留
	if !strings.Contains(s, "name = \"Example Mod\"") || !strings.Contains(s, "[download]") || !strings.Contains(s, "url = \"https://example.com/example.jar\"") {
		t.Errorf("原有内容应保留:\n%s", s)
	}
	// 字段不重复
	if strings.Count(s, "x-prismlauncher-loaders") != 1 {
		t.Error("字段不应重复")
	}
}

// 推送：无 side 条目时插入文件开头
func TestToPrismFormatNoSide(t *testing.T) {
	out, err := ToPrismFormat([]byte("name = \"X\"\nfilename = \"x.jar\"\n"), PrismMeta{Loaders: "fabric:0.15"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "x-prismlauncher-loaders = \"fabric:0.15\"\nx-prismlauncher-mc-versions = \"\"\n") {
		t.Errorf("无 side 应插到开头:\n%s", s)
	}
}

// 拉取：删除 x-prismlauncher-* 与 [download].url，其余保留（其他表 url 保留）
func TestFromPrismFormat(t *testing.T) {
	prism := `name = "Example Mod"
filename = "example.jar"
side = "both"
x-prismlauncher-loaders = "forge:47.4.10"
x-prismlauncher-mc-versions = "1.20.1"
x-prismlauncher-release-type = "release"
x-prismlauncher-version-number = "1.2.3"

[download]
url = "https://example.com/example.jar"
hash-format = "sha256"
hash = "abc123"

[update.modrinth]
mod-id = "abc"
version = "1.2.3"
url = "https://example.com/modrinth-page"
`
	out, err := FromPrismFormat([]byte(prism))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, f := range prismlauncherFields {
		if strings.Contains(s, f) {
			t.Errorf("应删除 %s:\n%s", f, s)
		}
	}
	if strings.Contains(s, "\nurl = \"https://example.com/example.jar\"") || strings.Contains(s, "[download]\nurl") {
		t.Errorf("[download] 的 url 应删除:\n%s", s)
	}
	// 其他表 url 保留
	if !strings.Contains(s, "url = \"https://example.com/modrinth-page\"") {
		t.Errorf("非 download 表的 url 应保留:\n%s", s)
	}
	if !strings.Contains(s, "hash-format = \"sha256\"") || !strings.Contains(s, "side = \"both\"") {
		t.Errorf("其余内容应保留:\n%s", s)
	}
}

// 拉取：无扩展字段/无 url 时原样返回
func TestFromPrismFormatPlain(t *testing.T) {
	plain := `name = "Example Mod"
filename = "example.jar"
side = "both"

[update.modrinth]
mod-id = "abc"
version = "1.2.3"
`
	out, err := FromPrismFormat([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != plain {
		t.Errorf("无 Prism 字段时应原样保留:\n%s", string(out))
	}
}

// 往返：To → From 恢复 packwiz 原格式（x-prismlauncher 字段与 download.url 都不应残留）
func TestMetaRoundTrip(t *testing.T) {
	out, err := ToPrismFormat([]byte(samplePW), PrismMeta{
		Loaders:     "forge:47.4.10",
		MCVersions:  "1.20.1",
		ReleaseType: "release",
		Version:     "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	back, err := FromPrismFormat(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(back)
	for _, f := range prismlauncherFields {
		if strings.Contains(s, f) {
			t.Errorf("往返后 %s 应被删除", f)
		}
	}
	if strings.Contains(s, "url = \"https://example.com/example.jar\"") {
		t.Error("往返后 [download].url 应被删除（Prism 侧格式含 url，拉回 packwiz 需剔除）")
	}
	if !strings.Contains(s, "name = \"Example Mod\"") || !strings.Contains(s, "side = \"both\"") {
		t.Error("往返后主体内容应保留")
	}
}
