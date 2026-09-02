package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 真网冒烟（B 口径人工段，验收规格「必做必录但非门槛」）：默认跳过，
// PACKGRADLE_REALNET_SMOKE=1 时执行。验证 CDN 对直链形状的持续认可
// （ADR-0008 §2 黄金向量，实测出处=研究笔记 §1.2）与引擎真网全链路
// （流式下载→.part→声明 sha1 校验→成品 rename）。失败不判 A/B 口径失败，
// 但必须判因记录进 docs/acceptance/reports/p3-acceptance-*.md §6 并注记
// 代理状态；403 形状变化 → 触发 ADR-0008 C 方案（curseforge_api_key）回票。
// 首选直连（env -u HTTPS_PROXY）；代理痕迹（fake-ip 198.18.0.0/15 段）如实注记。
func TestRealNetSmokeManual(t *testing.T) {
	if os.Getenv("PACKGRADLE_REALNET_SMOKE") != "1" {
		t.Skip("manual real-net smoke; set PACKGRADLE_REALNET_SMOKE=1 to run")
	}
	// 黄金向量「7位常规例（实测206向量）」；sha1 为 2026-09-02 冒烟实拉全量字节的
	// 实测摘要（1,409,495 B，与 HEAD Content-Length 一致），声明格式沿现役
	// packwiz CF metafile 惯例 sha1（ADR-0008 §2 查证）。
	req := Request{
		FileID:     7270446,
		Filename:   "jei-1.20.1-forge-15.20.0.127.jar",
		HashFormat: "sha1",
		Hash:       "6c7684a0e9356f6d60d7b12a14caf6c644ee11ef",
	}
	engine, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	res, err := engine.Fetch(ctx, dir, req)
	if err != nil {
		t.Fatalf("Fetch(真网): %v", err)
	}
	if res.Size != 1409495 {
		t.Errorf("字节数漂移：got %d, want 1409495（与声明 sha1 的采样基线不一致，判因记录）", res.Size)
	}
	if _, err := os.Stat(filepath.Join(dir, req.Filename)); err != nil {
		t.Errorf("成品文件缺失: %v", err)
	}
	t.Logf("真网冒烟通过：%d B，耗时 %s", res.Size, time.Since(start).Round(time.Millisecond))
}
