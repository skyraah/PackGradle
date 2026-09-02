package packwiz

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// R1 别名路径验收断言（ADR-0011 §7 R1；P4 验收规格 §5.2 场景 3）：
// 构造含绝对路径的端点错误 → 新写 Diagnostic.Detail 为别名路径、无用户名；
// 历史行不追溯（本断言只看新写入的诊断）。

// TestScanModMetaUnreadableDetailAliased 构造坏 metafile（解析错误内嵌端点内
// 绝对路径）→ modmeta_unreadable 诊断 detail 新写即别名（<project>/…），不含
// 项目根绝对路径（用户名随之消失）。
func TestScanModMetaUnreadableDetailAliased(t *testing.T) {
	dir := makeProject(t)
	if err := os.WriteFile(filepath.Join(dir, "mods", "broken.pw.toml"), []byte("this is [ not toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := os.ReadFile(filepath.Join(dir, "index.toml"))
	if err != nil {
		t.Fatal(err)
	}
	idx = append(idx, "\n[[files]]\nfile = \"mods/broken.pw.toml\"\nhash = \"9\"\nmetafile = true\n"...)
	if err := os.WriteFile(filepath.Join(dir, "index.toml"), idx, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := New().Scan(context.Background(), dir, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range report.Diagnostics {
		if d.Code != "diag.scan.modmeta_unreadable" {
			continue
		}
		if !strings.Contains(d.Detail, model.AliasProject) {
			t.Fatalf("新写 detail 应为别名路径: %q", d.Detail)
		}
		if strings.Contains(d.Detail, dir) {
			t.Fatalf("新写 detail 不得含端点根绝对路径（用户名）: %q", d.Detail)
		}
		return
	}
	t.Fatalf("应产生 modmeta_unreadable 诊断: %+v", report.Diagnostics)
}

// TestScanEndpointRootUnreachableErrorAliased 构造端点根不可达错误（错误串内嵌
// 用户输入的绝对路径）→ 返回错误别名化为 <project>，不含绝对路径。
func TestScanEndpointRootUnreachableErrorAliased(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	_, err := New().Scan(context.Background(), missing, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err == nil {
		t.Fatal("不可达端点应返回错误")
	}
	if !strings.Contains(err.Error(), model.AliasProject) {
		t.Fatalf("错误串应为别名路径: %q", err.Error())
	}
	if strings.Contains(err.Error(), missing) {
		t.Fatalf("错误串不得含绝对路径（用户名）: %q", err.Error())
	}
}
