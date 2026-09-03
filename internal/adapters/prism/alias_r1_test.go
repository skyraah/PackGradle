package prism

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// R1 别名路径验收断言（ADR-0011 §7 R1；P4 验收规格 §5.2 场景 3，运行实例侧）：
// 构造含绝对路径的端点错误（哈希失败的 *fs.PathError 内嵌游戏目录绝对路径）
// → 新写 Diagnostic.Detail 为 <runtime>/… 别名、无用户名。
func TestScanHashFailedDetailAliased(t *testing.T) {
	gameDir := makeGameDir(t)
	failHash := func(ctx context.Context, abs string) (model.ContentRef, ports.FileFacts, error) {
		// 与真实 Hasher 同形的失败：os.Open 的 PathError 内嵌绝对路径
		return model.ContentRef{}, ports.FileFacts{}, &fs.PathError{Op: "open", Path: abs, Err: fs.ErrPermission}
	}
	report, err := New().Scan(context.Background(), gameDir, ports.ScanOptions{
		Policy:   modsOnlyPolicy(),
		HashFile: failHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, d := range report.Diagnostics {
		if d.Code != "diag.scan.hash_failed" {
			continue
		}
		found++
		if !strings.Contains(d.Detail, model.AliasRuntime) {
			t.Fatalf("新写 detail 应为别名路径: %q", d.Detail)
		}
		if strings.Contains(d.Detail, gameDir) {
			t.Fatalf("新写 detail 不得含端点根绝对路径（用户名）: %q", d.Detail)
		}
	}
	if found == 0 {
		t.Fatalf("应产生 hash_failed 诊断: %+v", report.Diagnostics)
	}
}

// TestScanEndpointRootUnreachableErrorAliased 构造端点根不可达错误 → 返回错误
// 别名化为 <runtime>，不含绝对路径。
func TestScanEndpointRootUnreachableErrorAliased(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	_, err := New().Scan(context.Background(), missing, ports.ScanOptions{Policy: modsOnlyPolicy()})
	if err == nil {
		t.Fatal("不可达端点应返回错误")
	}
	if !strings.Contains(err.Error(), model.AliasRuntime) {
		t.Fatalf("错误串应为别名路径: %q", err.Error())
	}
	if strings.Contains(err.Error(), missing) {
		t.Fatalf("错误串不得含绝对路径（用户名）: %q", err.Error())
	}
}
