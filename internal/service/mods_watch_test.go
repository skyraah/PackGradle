package service

import (
	"path/filepath"
	"testing"
	"time"

	"packgradle/internal/prism"
)

// 目录不存在时注册点回退到最近存在的父目录，事件过滤仍按目标目录匹配
func TestModsWatchTargetFallbackAndMatch(t *testing.T) {
	root := t.TempDir()
	mods := filepath.Join(root, "mods")
	index := filepath.Join(root, "minecraft", "mods", ".index")

	target := newModsWatchTarget(mods, "proj", modsWatchSideProject)
	if target.watchPath != root {
		t.Errorf("mods 不存在时应回退到父目录 %q，实际 %q", root, target.watchPath)
	}
	if target.targetPath != mods {
		t.Errorf("targetPath 错误: %q", target.targetPath)
	}
	// 父目录上的无关事件不匹配
	if modsWatchPathMatches(filepath.Join(root, "pack.toml"), target.targetPath) {
		t.Error("父目录中的无关文件不应匹配 mods 目标")
	}
	// 目标目录本身或目标目录内文件匹配
	if !modsWatchPathMatches(mods, target.targetPath) {
		t.Error("目标目录本身应匹配")
	}
	if !modsWatchPathMatches(filepath.Join(mods, "a.pw.toml"), target.targetPath) {
		t.Error("目标目录内文件应匹配")
	}

	// 多层缺失时逐级向上回退
	indexTarget := newModsWatchTarget(index, "proj", modsWatchSideInstance)
	if indexTarget.watchPath != root {
		t.Errorf("多层缺失时应回退到根目录 %q，实际 %q", root, indexTarget.watchPath)
	}
	// 目标缺失时，中间目录（如 minecraft/mods）创建应触发注册点迁移
	if !modsWatchPathIsAncestorOf(filepath.Join(root, "minecraft", "mods"), indexTarget.targetPath) {
		t.Error("minecraft/mods 创建应视为 .index 目标的祖先迁移点")
	}
	if modsWatchPathIsAncestorOf(filepath.Join(root, "pack.toml"), indexTarget.targetPath) {
		t.Error("无关文件不应视为目标祖先")
	}
}

// 只有已经关联且实例可定位的项目才进入监听集合
func TestCurrentModsWatchPairs(t *testing.T) {
	_, instancesDir := makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)

	linked, _ := makeLinkProject(t, cm, "Linked")
	if err := svc.LinkProject(linked, "Collapse"); err != nil {
		t.Fatal(err)
	}
	_, _ = makeLinkProject(t, cm, "Unlinked")

	pairs := svc.currentModsWatchPairs()
	if len(pairs) != 1 || pairs[0].project != linked {
		t.Fatalf("应只包含已关联项目 Linked，实际 %+v", pairs)
	}
	entry, _ := cm.FindProject(linked)
	if pairs[0].projectMods != filepath.Join(entry.Path, "mods") {
		t.Errorf("项目 mods 路径错误: %q", pairs[0].projectMods)
	}
	if pairs[0].instanceIndex != filepath.Join(instancesDir, "Collapse", "minecraft", "mods", ".index") {
		t.Errorf("实例 .index 路径错误: %q", pairs[0].instanceIndex)
	}
}

// WatchMods 幂等返回当前已监听项目列表
func TestWatchModsListsLinkedProjects(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	proj, _ := makeLinkProject(t, cm, "Collapse")
	if err := svc.LinkProject(proj, "Collapse"); err != nil {
		t.Fatal(err)
	}

	got, err := svc.WatchMods()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != proj {
		t.Errorf("应返回正在监听的项目列表，实际 %v", got)
	}
	// 幂等：再次调用返回相同结果
	got2, err := svc.WatchMods()
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || got2[0] != proj {
		t.Errorf("重复调用结果不一致: %v", got2)
	}
}

// 端到端：项目 mods 目录变化 → 防抖后做一次 MetaDiff → 事件发包（测试注入捕获）
func TestWatchModsEmitsDiffOnProjectChange(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	proj, _ := makeMetaProject(t, svc, cm, "Collapse")

	events := make(chan prism.ModsWatchEvent, 4)
	svc.emitModsEvent = func(e prism.ModsWatchEvent) { events <- e }

	if _, err := svc.WatchMods(); err != nil {
		t.Fatal(err)
	}

	// 修改项目侧 mods 中的 pw.toml
	entry, _ := cm.FindProject(proj)
	mustWriteFile(t, filepath.Join(entry.Path, "mods", "moda.pw.toml"),
		"name = \"Mod A\"\nfilename = \"moda.jar\"\nside = \"both\"\nversion = \"9.9.9\"\n")

	select {
	case event := <-events:
		if event.Project != proj {
			t.Errorf("事件项目名错误: %+v", event)
		}
		if event.Side != modsWatchSideProject {
			t.Errorf("应由项目侧触发，实际 %q", event.Side)
		}
		if event.Error != "" {
			t.Fatalf("比对失败: %s", event.Error)
		}
		// 项目 moda 版本改为 9.9.9，实例侧 2.0.0：应产生版本差异
		found := false
		for _, v := range event.Diff.VersionDiff {
			if v.ID == "moda" && v.ProjectVersion == "9.9.9" && v.InstanceVersion == "2.0.0" {
				found = true
			}
		}
		if !found {
			t.Errorf("版本差异未随文件变化更新: %+v", event.Diff.VersionDiff)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待项目 mods 变化事件超时")
	}
}

// 端到端：实例 .index 目录变化 → 事件标记 instance 侧并带上实例独有差异
func TestWatchModsEmitsDiffOnInstanceChange(t *testing.T) {
	_, _ = makePrismFixture(t)
	cm := newTestConfig(t)
	svc := newPrismServiceWithMemory(cm)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	proj, indexDir := makeMetaProject(t, svc, cm, "Collapse")

	events := make(chan prism.ModsWatchEvent, 4)
	svc.emitModsEvent = func(e prism.ModsWatchEvent) { events <- e }

	if _, err := svc.WatchMods(); err != nil {
		t.Fatal(err)
	}

	mustWriteFile(t, filepath.Join(indexDir, "mode.pw.toml"),
		"name = \"Mod E\"\nfilename = \"mode.jar\"\nside = \"both\"\n")

	select {
	case event := <-events:
		if event.Project != proj {
			t.Errorf("事件项目名错误: %+v", event)
		}
		if event.Side != modsWatchSideInstance {
			t.Errorf("应由实例侧触发，实际 %q", event.Side)
		}
		if event.Error != "" {
			t.Fatalf("比对失败: %s", event.Error)
		}
		found := false
		for _, id := range event.Diff.InstanceOnly {
			if id == "mode" {
				found = true
			}
		}
		if !found {
			t.Errorf("实例独有差异应包含 mode: %+v", event.Diff.InstanceOnly)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待实例 .index 变化事件超时")
	}
}

// 无关联项目时 WatchMods 不创建监听路径，返回空列表
func TestWatchModsEmptyWhenNoLinks(t *testing.T) {
	svc := newPrismServiceWithMemory(newTestConfig(t))
	t.Cleanup(func() { _ = svc.ServiceShutdown() })

	got, err := svc.WatchMods()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("无关联项目应返回空列表，实际 %v", got)
	}
	if svc.modsWatch == nil {
		t.Error("监听器应已创建（供后续关联时复用）")
	}
}
