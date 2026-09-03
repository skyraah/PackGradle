package transport_test

// SettingsService 三方法 transport 端到端测试（契约 06 §2/§3.6，票 #57）：
// 走 bootstrap.BuildWithRetention 的生产装配（设置端口接真实 appconfig，
// 授权开关接真实 SQLite 栈），覆盖 AC 四点：读默认、写合法、越界拒绝、
// 开关切换后 WorkspaceDTO 投影一致。不启动 Wails。

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/appconfig"
	"packgradle/internal/bootstrap"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
	"packgradle/internal/transport"
)

// newSettingsFixture 装配真实栈与最小工作区，返回 SettingsService 与
// SyncService（读投影对照）。
func newSettingsFixture(t *testing.T) (*transport.SettingsService, *transport.SyncService, string) {
	t.Helper()
	base := t.TempDir()
	projectRoot := filepath.Join(base, "project")
	instanceDir := filepath.Join(base, "instance", "Collapse")
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// PrepareRelation blocking 检查的最小面：pack.toml / instance.cfg / minecraft 游戏目录
	writeFile(filepath.Join(projectRoot, "pack.toml"), "name = \"Collapse\"\n")
	writeFile(filepath.Join(instanceDir, "instance.cfg"), "[General]\nname=\"Collapse\"\n")
	writeFile(filepath.Join(instanceDir, "minecraft", "mods", ".keep"), "")

	stack, err := bootstrap.BuildWithRetention(filepath.Join(base, "userdata"),
		appconfig.NewConfigManagerAt(filepath.Join(base, "config.toml")))
	if err != nil {
		t.Fatalf("装配栈失败: %v", err)
	}
	t.Cleanup(func() { stack.Close() })
	if stack.Settings == nil {
		t.Fatal("BuildWithRetention 应装配 SettingsService")
	}

	prep, err := stack.App.PrepareRelation(t.Context(), model.PrepareRelationInput{
		ProjectRoot: projectRoot, RuntimeInstanceDir: instanceDir,
	})
	if err != nil {
		t.Fatalf("PrepareRelation: %v", err)
	}
	for _, c := range prep.Checks {
		if c.Severity == "blocking" && !c.Passed {
			t.Fatalf("预检 %s 未通过: %s", c.Code, c.Detail)
		}
	}
	rel, err := stack.App.CreateRelation(t.Context(), prep.PreparationID)
	if err != nil {
		t.Fatalf("CreateRelation: %v", err)
	}
	return stack.Settings, stack.Service, rel.RelationID
}

// TestSettingsServiceGetRetentionDefaults 读默认：全新配置返回五键默认值
// （AC「读默认」）。
func TestSettingsServiceGetRetentionDefaults(t *testing.T) {
	svc, _, _ := newSettingsFixture(t)

	got, err := svc.GetRetentionSettings()
	if err != nil {
		t.Fatalf("GetRetentionSettings: %v", err)
	}
	want := model.DefaultRetention()
	if got.SchemaVersion != model.CurrentSchemaVersion {
		t.Errorf("schema_version = %d, 期望 %d", got.SchemaVersion, model.CurrentSchemaVersion)
	}
	if got.KeepCommits != want.KeepCommits || got.KeepDays != want.KeepDays ||
		got.RelationCapacityBytes != want.RelationCapacityBytes ||
		got.PreserveMaxBytes != want.PreserveMaxBytes || got.TrashDays != want.TrashDays {
		t.Errorf("默认投影不一致: got %+v want %+v", got, want)
	}
}

// TestSettingsServiceUpdateRetention 写合法：更新返回写入值，重读持久化一致
// （AC「写合法」）。
func TestSettingsServiceUpdateRetention(t *testing.T) {
	svc, _, _ := newSettingsFixture(t)

	input := transport.UpdateRetentionSettingsDTO{
		KeepCommits: 10, KeepDays: 14,
		RelationCapacityBytes: 2 << 30, PreserveMaxBytes: 0, TrashDays: 3,
	}
	got, err := svc.UpdateRetentionSettings(input)
	if err != nil {
		t.Fatalf("UpdateRetentionSettings: %v", err)
	}
	if got.KeepCommits != input.KeepCommits || got.KeepDays != input.KeepDays ||
		got.RelationCapacityBytes != input.RelationCapacityBytes ||
		got.PreserveMaxBytes != input.PreserveMaxBytes || got.TrashDays != input.TrashDays {
		t.Errorf("写后投影不一致: got %+v want %+v", got, input)
	}

	reread, err := svc.GetRetentionSettings()
	if err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if reread != got {
		t.Errorf("重读不一致: got %+v want %+v", reread, got)
	}
}

// TestSettingsServiceUpdateRetentionInvalid 越界拒绝：单键越界整体拒绝，
// err.settings.retention_invalid {0}=字段名，既有设置不变（AC「越界拒绝」）。
func TestSettingsServiceUpdateRetentionInvalid(t *testing.T) {
	svc, _, _ := newSettingsFixture(t)

	// 预写一组合法值
	legal := transport.UpdateRetentionSettingsDTO{
		KeepCommits: 8, KeepDays: model.KeepDaysDefault,
		RelationCapacityBytes: model.RelationCapacityDefault,
		PreserveMaxBytes:      model.PreserveMaxDefault, TrashDays: model.TrashDaysDefault,
	}
	if _, err := svc.UpdateRetentionSettings(legal); err != nil {
		t.Fatalf("预写合法值失败: %v", err)
	}

	// keep_days 越界（同请求内其余键合法）→ 整体拒绝
	bad := legal
	bad.KeepDays = model.KeepDaysMax + 1
	got, err := svc.UpdateRetentionSettings(bad)
	if errs.CodeOf(err) != "err.settings.retention_invalid" {
		t.Fatalf("越界应返回 err.settings.retention_invalid, got %v", err)
	}
	appErr := err.(*errs.AppError)
	if len(appErr.Args) != 1 || appErr.Args[0] != "keep_days" {
		t.Errorf("args 应为 [keep_days], got %v", appErr.Args)
	}
	if got != (transport.RetentionSettingsDTO{}) {
		t.Errorf("拒绝时不应返回设置: %+v", got)
	}

	// 整体拒绝：既有设置保持预写值
	reread, err := svc.GetRetentionSettings()
	if err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if reread.KeepDays != legal.KeepDays || reread.KeepCommits != legal.KeepCommits {
		t.Errorf("整体拒绝后设置被部分改写: %+v", reread)
	}
}

// TestSettingsServiceRequestGC 立即回收空间上 wire（契约 06 §9，票 #65）：
// 建 kind=gc 任务（relation_id 空=全局任务）且全局单飞幂等——连续两次请求
// 复用同一活跃任务（第二次请求先于引擎收口到达，与票 #64 引擎单飞测试同
// 稳定性依据）。
func TestSettingsServiceRequestGC(t *testing.T) {
	svc, _, _ := newSettingsFixture(t)

	first, err := svc.RequestGC()
	if err != nil {
		t.Fatalf("RequestGC: %v", err)
	}
	if first.Kind != "gc" {
		t.Errorf("任务 kind = %q, 期望 gc", first.Kind)
	}
	if first.RelationID != "" {
		t.Errorf("GC 任务应为全局（relation_id 空）, got %q", first.RelationID)
	}

	second, err := svc.RequestGC()
	if err != nil {
		t.Fatalf("RequestGC 二次: %v", err)
	}
	if first.TaskID != second.TaskID {
		t.Errorf("单飞破坏：首次 %s 二次 %s", first.TaskID, second.TaskID)
	}
}

// TestSettingsServiceSetWorkspaceAuthorized 开关切换：WorkspaceDTO 投影一致，
// 与 SyncService.GetWorkspace 读投影同源同值（AC「开关切换后 WorkspaceDTO 投影
// 一致」）；不存在关系返回 err.relation.not_found。
func TestSettingsServiceSetWorkspaceAuthorized(t *testing.T) {
	svc, sync, relationID := newSettingsFixture(t)

	// 默认关闭
	def, err := sync.GetWorkspace(relationID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if def.AuthorizedApply {
		t.Error("新建工作区 authorized_apply 应默认 false")
	}

	w, err := svc.SetWorkspaceAuthorized(relationID, true)
	if err != nil {
		t.Fatalf("SetWorkspaceAuthorized(true): %v", err)
	}
	if !w.AuthorizedApply {
		t.Error("开启后 WorkspaceDTO.authorized_apply 应为 true")
	}

	// 切换返回值与 GetWorkspace 读投影一致
	reread, err := sync.GetWorkspace(relationID)
	if err != nil {
		t.Fatalf("读投影失败: %v", err)
	}
	if !reread.AuthorizedApply {
		t.Error("读投影 authorized_apply 应为 true")
	}

	// 关闭
	off, err := svc.SetWorkspaceAuthorized(relationID, false)
	if err != nil {
		t.Fatalf("SetWorkspaceAuthorized(false): %v", err)
	}
	if off.AuthorizedApply {
		t.Error("关闭后 WorkspaceDTO.authorized_apply 应为 false")
	}

	// 不存在关系
	if _, err := svc.SetWorkspaceAuthorized("rel_none", true); errs.CodeOf(err) != "err.relation.not_found" {
		t.Errorf("不存在关系应返回 err.relation.not_found, got %v", err)
	}
}

// TestSettingsServiceGetStorageStats 存储占用概览上 wire（契约 06 §2 第 5 方法，
// ADR-0011 §8 勘误兑现，票 #90）：空栈零账面（对象 0/残留 0）、DB 体积与卷剩余
// 为真值；CAS 落一个对象 + 造一个 .tmp-* 残留后计数/字节/残留如实反映。
func TestSettingsServiceGetStorageStats(t *testing.T) {
	base := t.TempDir()
	projectRoot := filepath.Join(base, "project")
	instanceDir := filepath.Join(base, "instance", "Collapse")
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(projectRoot, "pack.toml"), "name = \"Collapse\"\n")
	writeFile(filepath.Join(instanceDir, "instance.cfg"), "[General]\nname=\"Collapse\"\n")
	writeFile(filepath.Join(instanceDir, "minecraft", "mods", ".keep"), "")

	stack, err := bootstrap.BuildWithRetention(filepath.Join(base, "userdata"),
		appconfig.NewConfigManagerAt(filepath.Join(base, "config.toml")))
	if err != nil {
		t.Fatalf("装配栈失败: %v", err)
	}
	t.Cleanup(func() { stack.Close() })
	svc := stack.Settings

	// 空栈基线：CAS 账面为零，DB/卷容量为真值，schema_version 在位
	fresh, err := svc.GetStorageStats()
	if err != nil {
		t.Fatalf("GetStorageStats: %v", err)
	}
	if fresh.CasObjectCount != 0 || fresh.CasTotalBytes != 0 || fresh.CasTmpLeftovers != 0 {
		t.Fatalf("空栈 CAS 账面应为零: %+v", fresh)
	}
	if fresh.DBSizeBytes <= 0 {
		t.Errorf("db_size_bytes 应为真值: %d", fresh.DBSizeBytes)
	}
	if fresh.FreeDiskBytes <= 0 {
		t.Errorf("free_disk_bytes 应为真值: %d", fresh.FreeDiskBytes)
	}
	if fresh.SchemaVersion != model.CurrentSchemaVersion {
		t.Errorf("schema_version = %d, 期望 %d", fresh.SchemaVersion, model.CurrentSchemaVersion)
	}

	// CAS 落一个对象 + 写中断残留一份 → 计数/字节/残留如实反映
	content := []byte("packgradle storage stats fixture object bytes")
	ref, err := stack.CAS.Put(t.Context(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("CAS.Put: %v", err)
	}
	tmpPath := filepath.Join(stack.Layout.ObjectsDir, ".tmp-leftover")
	if err := os.WriteFile(tmpPath, []byte("half-written"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetStorageStats()
	if err != nil {
		t.Fatalf("GetStorageStats(2): %v", err)
	}
	if got.CasObjectCount != 1 {
		t.Errorf("cas_object_count = %d, 期望 1", got.CasObjectCount)
	}
	if got.CasTotalBytes != ref.Size || ref.Size != int64(len(content)) {
		t.Errorf("cas_total_bytes = %d, 期望 %d", got.CasTotalBytes, ref.Size)
	}
	if got.CasTmpLeftovers != 1 {
		t.Errorf("cas_tmp_leftovers = %d, 期望 1", got.CasTmpLeftovers)
	}
	if got.DBSizeBytes < fresh.DBSizeBytes {
		t.Errorf("db_size_bytes 不应缩小: %d < %d", got.DBSizeBytes, fresh.DBSizeBytes)
	}
	if got.FreeDiskBytes <= 0 {
		t.Errorf("free_disk_bytes 应为真值: %d", got.FreeDiskBytes)
	}
}
