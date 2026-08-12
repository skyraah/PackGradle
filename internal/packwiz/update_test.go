package packwiz

import "testing"

func TestParseUpdateOutput(t *testing.T) {
	output := `Loading modpack...
Reading metadata files...
A supported update system for "mcrd-cn.ksmcbrigade-1.20.1-4" cannot be found.
Checking for updates...
Updates found:
Create: create-1.20.1-6.0.8.jar -> create-1.20.1-6.0.9.jar
Mekanism: Mekanism-1.20.1-10.4.16.80.jar -> Mekanism-1.20.1-10.4.17.00.jar
Failed to check updates for Jade: unexpected API response
Update skipped for pinned mod Sodium
Do you want to update? [Y/n]: Cancelled!
`
	updates, errors := ParseUpdateOutput(output)
	if len(updates) != 2 {
		t.Fatalf("应解析到 2 个有更新的 mod，实际 %d: %+v", len(updates), updates)
	}
	create := updates[0]
	if create.Name != "Create" || !create.HasUpdate ||
		create.CurrentFile != "create-1.20.1-6.0.8.jar" || create.LatestFile != "create-1.20.1-6.0.9.jar" {
		t.Errorf("有更新条目解析不正确: %+v", create)
	}
	if updates[1].Name != "Mekanism" || updates[1].LatestFile != "Mekanism-1.20.1-10.4.17.00.jar" {
		t.Errorf("有更新条目解析不正确: %+v", updates[1])
	}

	if len(errors) != 3 {
		t.Fatalf("应解析到 3 个失败/跳过条目，实际 %d: %+v", len(errors), errors)
	}
	byName := map[string]string{}
	for _, e := range errors {
		byName[e.Name] = e.Error
	}
	if byName["mcrd-cn.ksmcbrigade-1.20.1-4"] != "err.update.no_updater" {
		t.Errorf("无更新源条目应返回错误码 err.update.no_updater: %+v", byName)
	}
	if byName["Jade"] != "unexpected API response" {
		t.Errorf("检查失败条目应透传 packwiz 原文: %+v", byName)
	}
	if byName["Sodium"] != "err.update.pinned" {
		t.Errorf("固定跳过条目应返回错误码 err.update.pinned: %+v", byName)
	}
}

// 空输出 / 无更新输出
func TestParseUpdateOutputNoUpdates(t *testing.T) {
	updates, errors := ParseUpdateOutput("Loading modpack...\nAll files are up to date!\n")
	if len(updates) != 0 || len(errors) != 0 {
		t.Errorf("无更新时不应解析出条目: updates=%+v errors=%+v", updates, errors)
	}
	updates, errors = ParseUpdateOutput("")
	if len(updates) != 0 || len(errors) != 0 {
		t.Errorf("空输出不应解析出条目: updates=%+v errors=%+v", updates, errors)
	}
}
