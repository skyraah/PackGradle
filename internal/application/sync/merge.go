// 合并判定的三侧全文读取缝装配（票 #87，ADR-0009 §1/§2）。
// diff 包是纯计算层，三侧全文由本层注入：Base 取自 CAS（基线表示的内容指纹
// 指向保全对象），Project/Runtime 取端点活文件（快照只携带指纹不携带字节）。
package sync

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/core/diff"
)

// mergeSources 装配合并判定的三侧全文读取闭包：
//   - Base：按内容摘要从 CAS 读对象；
//   - Project/Runtime：按表示相对路径读端点活文件。
//
// 路径安全做纵深防御（快照表示本经扫描器 Resolver 校验）：拒绝绝对路径与
// 「..」穿越。取数失败不上抛——diff 层按「合并面不可用」逐资源降级
// conflict_modify；字节与快照指纹的 sha256 互验同样由 diff 层执行。
func (a *App) mergeSources(ctx context.Context, projectRoot, runtimeRoot string) *diff.MergeSources {
	readEndpoint := func(root, relPath string) ([]byte, error) {
		if relPath == "" || filepath.IsAbs(relPath) {
			return nil, fmt.Errorf("merge: 非法相对路径 %q", relPath)
		}
		clean := filepath.Clean(filepath.FromSlash(relPath))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("merge: 相对路径越出端点根 %q", relPath)
		}
		return os.ReadFile(filepath.Join(root, clean))
	}
	openObject := func(digest string) ([]byte, error) {
		rc, err := a.deps.CAS.Open(ctx, digest)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return &diff.MergeSources{
		Base:    openObject,
		Project: func(relPath string) ([]byte, error) { return readEndpoint(projectRoot, relPath) },
		Runtime: func(relPath string) ([]byte, error) { return readEndpoint(runtimeRoot, relPath) },
	}
}
