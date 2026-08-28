package packwiz

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"packgradle/internal/application/ports"
	apppolicy "packgradle/internal/application/policy"
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

// hasDiag 报告诊断列表中是否存在指定 code 的诊断。
func hasDiag(diags []model.Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestScanEscapedMetafileSkipped(t *testing.T) {
	dir := makeProject(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "evil.pw.toml"), []byte("机密"), 0o644); err != nil {
		t.Fatal(err)
	}
	requireDirLink(t, filepath.Join(dir, "linked"), outside)
	idx := indexTOML + "\n[[files]]\nfile = \"linked/evil.pw.toml\"\nhash = \"6\"\nmetafile = true\n"
	if err := os.WriteFile(filepath.Join(dir, "index.toml"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range report.Observations {
		if o.ResourceID == "mod:path:linked/evil.pw.toml" {
			t.Fatalf("越界条目不应产出观察: %+v", o)
		}
	}
	if !hasDiag(report.Diagnostics, "diag.scan.path_escape") {
		t.Fatalf("越界条目应产生 path_escape 诊断: %+v", report.Diagnostics)
	}
}

func TestScanPolicyPrefixEscapeSkipped(t *testing.T) {
	dir := makeProject(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.cfg"), []byte("机密"), 0o644); err != nil {
		t.Fatal(err)
	}
	requireDirLink(t, filepath.Join(dir, "linked"), outside)
	policy := modsOnlyPolicy()
	policy.Rules = append(policy.Rules, model.MappingRule{
		ID: "escape", ResourceKind: "text_file",
		ProjectPrefix: "linked", RuntimePrefix: "linked",
		Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual",
		RuntimeLocalPolicy: "exclude",
	})
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: policy})
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

func TestScanPolicyPrefixDotDotRejectedAtCompile(t *testing.T) {
	// `..` 前缀是非法规则，编译期即拒绝（结构化 *RuleError），不再落到运行时诊断
	dir := makeProject(t)
	policy := modsOnlyPolicy()
	policy.Rules = append(policy.Rules, model.MappingRule{
		ID: "escape", ResourceKind: "text_file",
		ProjectPrefix: "../outside", RuntimePrefix: "../outside",
		Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual",
		RuntimeLocalPolicy: "exclude",
	})
	_, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: policy})
	var re *apppolicy.RuleError
	if !errors.As(errors.Unwrap(err), &re) && !errors.As(err, &re) {
		t.Fatalf("`..` 前缀应在编译期被拒绝: %v", err)
	}
	if re != nil && re.Field != "project_prefix" {
		t.Errorf("RuleError.Field = %q, want project_prefix", re.Field)
	}
}

func TestScanPolicyPrefixLinkInsideRootFollowed(t *testing.T) {
	// root 内合法链接：解析到 realpath 后正常产出观察（相对路径按真实路径报告）
	dir := makeProject(t)
	if err := os.MkdirAll(filepath.Join(dir, "configReal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configReal", "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	requireDirLink(t, filepath.Join(dir, "configLink"), filepath.Join(dir, "configReal"))
	policy := modsOnlyPolicy()
	policy.Rules = append(policy.Rules, model.MappingRule{
		ID: "config", ResourceKind: "text_file",
		ProjectPrefix: "configLink", RuntimePrefix: "configLink",
		Direction: "bidirectional", Materialization: "copy", MergePolicy: "manual",
		RuntimeLocalPolicy: "exclude",
	})
	hashed := 0
	fakeHash := func(ctx context.Context, abs string) (model.ContentRef, ports.FileFacts, error) {
		hashed++
		return model.ContentRef{Algorithm: "sha256", Digest: "d1", Size: 5}, ports.FileFacts{SizeBytes: 5}, nil
	}
	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: policy, HashFile: fakeHash})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range report.Observations {
		if o.Kind == model.ResourceTextFile {
			found = true
			if o.Representation.RelativePath != "configReal/notes.txt" {
				t.Fatalf("root 内链接应按 realpath 报告: %+v", o.Representation.RelativePath)
			}
		}
	}
	if !found || hashed != 1 {
		t.Fatalf("root 内链接应产出观察并哈希: found=%v hashed=%d", found, hashed)
	}
}
