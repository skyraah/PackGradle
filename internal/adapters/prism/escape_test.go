package prism

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// requireDirLink 在 link 处创建指向 target 的目录链接（Windows junction /
// 其他平台 symlink）；创建失败（权限/平台限制）跳过测试。
func requireDirLink(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if err != nil {
			t.Skipf("本机无法创建 junction: %v: %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("本机无法创建 symlink: %v", err)
	}
}

func hasDiag(diags []model.Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestScanModsJunctionEscape(t *testing.T) {
	gameDir := makeGameDir(t)
	outside := t.TempDir()
	mustWrite(t, outside, "evil-1.0.jar", "越界 jar")
	if err := os.RemoveAll(filepath.Join(gameDir, "mods")); err != nil {
		t.Fatal(err)
	}
	requireDirLink(t, filepath.Join(gameDir, "mods"), outside)
	report, err := New().Scan(context.Background(), gameDir, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Observations) != 0 {
		t.Fatalf("mods 越界不应产出观察: %+v", report.Observations)
	}
	if !hasDiag(report.Diagnostics, "diag.scan.path_escape") {
		t.Fatalf("mods 越界应产生 path_escape 诊断: %+v", report.Diagnostics)
	}
}

func TestScanIndexMetaJunctionEscape(t *testing.T) {
	gameDir := makeGameDir(t)
	outside := t.TempDir()
	mustWrite(t, outside, "sodium-0.6.5.jar.pw.toml", "name = \"越界元数据\"\nversion = \"9.9.9\"\n")
	if err := os.RemoveAll(filepath.Join(gameDir, "mods", ".index")); err != nil {
		t.Fatal(err)
	}
	requireDirLink(t, filepath.Join(gameDir, "mods", ".index"), outside)
	report, err := New().Scan(context.Background(), gameDir, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range report.Observations {
		if o.ResourceID == "mod:jar:sodium-0.6.5.jar" {
			if v, ok := o.Representation.Metadata[model.MetaVersion]; ok && v == "9.9.9" {
				t.Fatalf("越界 .index 元数据不应被读取: %+v", o.Representation.Metadata)
			}
		}
	}
	if !hasDiag(report.Diagnostics, "diag.scan.path_escape") {
		t.Fatalf("越界 .index 应产生 path_escape 诊断: %+v", report.Diagnostics)
	}
}

func TestScanPolicyPrefixEscapeSkipped(t *testing.T) {
	gameDir := makeGameDir(t)
	outside := t.TempDir()
	mustWrite(t, outside, "secret.cfg", "机密")
	requireDirLink(t, filepath.Join(gameDir, "linked"), outside)
	policy := modsOnlyPolicy()
	policy.Rules = append(policy.Rules, model.MappingRule{
		ID: "escape", ResourceKind: "text_file",
		ProjectPrefix: "linked", RuntimePrefix: "linked",
		Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual",
	})
	report, err := New().Scan(context.Background(), gameDir, ports.ScanOptions{Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range report.Observations {
		if o.Kind != model.ResourceMod {
			t.Fatalf("越界前缀不应产出文件观察: %+v", o)
		}
	}
	if !hasDiag(report.Diagnostics, "diag.scan.path_escape") {
		t.Fatalf("越界前缀应产生 path_escape 诊断: %+v", report.Diagnostics)
	}
}
