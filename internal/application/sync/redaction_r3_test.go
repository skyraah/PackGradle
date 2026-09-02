package sync_test

// R3 凭据零泄漏注入断言（红线⑥；ADR-0011 §7 R3/§9；P4 验收规格 §5.4）：
// config.toml 注入 curseforge_api_key 并装载进生产栈，随后构造三类失败——
// 端点不可达（错误 detail）、坏 metafile 扫描（诊断输出）、预检不可达
//（检查 detail）——断言日志 / 错误 detail / 诊断输出三条通道均零泄漏。
// 配套复核结论见票 #90 报告（归验收收口票 T13 报告归档）。

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"packgradle/internal/appconfig"
	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/core/model"
)

const r3CanaryKey = "CFKEY-pgr3-canary-7f3d9a2b4c6e8f10"

// leakIn 报告 secret 是否出现在任一文本片段中。
func r3LeakIn(secret string, texts ...string) bool {
	for _, s := range texts {
		if s != "" && strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

func TestR3CredentialNoLeakOnFailure(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()

	// ① 注入凭据：config.toml 携带 curseforge_api_key 并经生产加载层装载
	configPath := filepath.Join(base, "config.toml")
	if err := os.WriteFile(configPath, []byte(
		"curseforge_api_key = \""+r3CanaryKey+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr, err := appconfig.NewConfigManagerAtLoaded(configPath)
	if err != nil {
		t.Fatalf("加载注入凭据的配置失败: %v", err)
	}
	if got := mgr.Get().CurseforgeApiKey; got != r3CanaryKey {
		t.Fatalf("前置条件不成立：凭据未在场 %q", got)
	}

	// ② 生产装配（config 端口接真实 appconfig；凭据在 config 快照内在场）
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	projectRoot := filepath.Join(base, "project")
	instanceDir := filepath.Join(base, "instance", "Collapse")
	writeFile(filepath.Join(projectRoot, "pack.toml"), "name = \"Collapse\"\n")
	writeFile(filepath.Join(projectRoot, "index.toml"),
		"index = { file = \"index.toml\", hash-format = \"sha256\", hash = \"0\" }\n"+
			"[[files]]\nfile = \"mods/broken.pw.toml\"\nhash = \"1\"\nmetafile = true\n")
	writeFile(filepath.Join(projectRoot, "mods", "broken.pw.toml"), "this is [ not toml")
	writeFile(filepath.Join(instanceDir, "instance.cfg"), "[General]\nname=\"Collapse\"\n")
	writeFile(filepath.Join(instanceDir, "minecraft", "mods", ".keep"), "")

	stack, err := bootstrap.BuildWithRetention(filepath.Join(base, "userdata"), mgr)
	if err != nil {
		t.Fatalf("装配栈失败: %v", err)
	}
	t.Cleanup(func() { stack.Close() })

	prep, err := stack.App.PrepareRelation(ctx, model.PrepareRelationInput{
		ProjectRoot: projectRoot, RuntimeInstanceDir: instanceDir,
	})
	if err != nil {
		t.Fatalf("PrepareRelation: %v", err)
	}
	rel, err := stack.App.CreateRelation(ctx, prep.PreparationID)
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}

	// ③ 日志通道捕获（stdlib log 当前出口；#91 slog 迁移后随施工复核复跑）
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	// ④ 失败 A：端点不可达 → 错误 detail（指纹采集失败内嵌端点绝对路径）。
	// 整目录改名：保证步骤 ⑤ 还原后绑定指纹逐字节一致（不触发 rebind_required）。
	hidden := filepath.Join(base, "project-hidden")
	if err := os.Rename(projectRoot, hidden); err != nil {
		t.Fatal(err)
	}
	_, endpointErr := stack.App.StartScan(ctx, rel.RelationID)
	if endpointErr == nil {
		t.Fatal("端点删除后扫描应失败")
	}
	if err := os.Rename(hidden, projectRoot); err != nil {
		t.Fatal(err)
	}

	// ⑤ 失败 B：诊断输出——坏 metafile 扫描产生 modmeta_unreadable 诊断
	if _, err := stack.App.StartScan(ctx, rel.RelationID); err != nil {
		t.Fatalf("带坏 metafile 的扫描不应失败: %v", err)
	}
	waitScanSettled(t, stack.App, rel.RelationID)
	ws, err := stack.App.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.LatestProjectSnapshot == nil {
		t.Fatal("扫描后应有项目侧快照")
	}
	diags, err := stack.App.GetSnapshotDiagnostics(ctx, rel.RelationID, ws.LatestProjectSnapshot.SnapshotID)
	if err != nil {
		t.Fatalf("GetSnapshotDiagnostics: %v", err)
	}
	var diagTexts []string
	aliased := false
	for _, d := range diags {
		diagTexts = append(diagTexts, d.Detail)
		diagTexts = append(diagTexts, d.Args...)
		if d.Code == "diag.scan.modmeta_unreadable" && strings.Contains(d.Detail, model.AliasProject) {
			aliased = true
		}
	}
	if !aliased {
		t.Fatalf("新写诊断 detail 应为别名路径（R1）: %+v", diags)
	}

	// ⑥ 失败 C：预检不可达 → 检查 detail
	badPrep, err := stack.App.PrepareRelation(ctx, model.PrepareRelationInput{
		ProjectRoot:        filepath.Join(base, "missing-project"),
		RuntimeInstanceDir: instanceDir,
	})
	if err != nil {
		t.Fatalf("不可达预检应返回结果而非错误: %v", err)
	}
	var checkTexts []string
	for _, c := range badPrep.Checks {
		checkTexts = append(checkTexts, c.Detail)
	}

	// ⑦ 三通道零泄漏断言（红线⑥）
	leakSources := map[string][]string{
		"错误 detail": {endpointErr.Error()},
		"诊断输出":   diagTexts,
		"检查 detail": checkTexts,
		"日志":       {logBuf.String()},
	}
	for channel, texts := range leakSources {
		if r3LeakIn(r3CanaryKey, texts...) {
			t.Errorf("凭据经 %s 通道泄漏（红线⑥违规）", channel)
		}
	}
}

// waitScanSettled 轮询直到该关系无活跃任务（StartScan 异步跑，事件不是事实源）。
func waitScanSettled(t *testing.T, app interface {
	ListTasks(ctx context.Context, relationID string, active bool, page ports.PageRequest) (view.TaskPage, error)
}, relationID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		page, err := app.ListTasks(context.Background(), relationID, true, ports.PageRequest{Limit: 5})
		if err == nil && len(page.Items) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("扫描任务 30s 未收口")
}
