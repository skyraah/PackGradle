package appconfig

// config.toml [retention] 加载层测试（ADR-0007 §8；契约 06 §3.6，票 #57）：
// 五键默认值、每键边界（越界整体拒绝带字段名）、preserve_max_bytes=0（不限）、
// 持久化往返与手编 TOML 的加载层校验。

import (
	"os"
	"path/filepath"
	"testing"

	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// TestRetentionDefaults 全新配置（无 [retention] 段）读出五键默认值。
func TestRetentionDefaults(t *testing.T) {
	m := newTestConfig(t)
	got, err := m.Retention()
	if err != nil {
		t.Fatalf("Retention: %v", err)
	}
	if got != model.DefaultRetention() {
		t.Errorf("默认值不一致: got %+v want %+v", got, model.DefaultRetention())
	}
}

// TestRetentionBoundariesRejected 每键越界（下界-1 / 上界+1）整体拒绝：
// 错误码 err.settings.retention_invalid、{0}=字段名、配置不落任何键。
func TestRetentionBoundariesRejected(t *testing.T) {
	cases := []struct {
		name  string
		field string
		mut   func(*model.RetentionSettings)
	}{
		{"keep_commits 低于下界", "keep_commits", func(r *model.RetentionSettings) { r.KeepCommits = model.KeepCommitsMin - 1 }},
		{"keep_commits 高于上界", "keep_commits", func(r *model.RetentionSettings) { r.KeepCommits = model.KeepCommitsMax + 1 }},
		{"keep_days 低于下界", "keep_days", func(r *model.RetentionSettings) { r.KeepDays = model.KeepDaysMin - 1 }},
		{"keep_days 高于上界", "keep_days", func(r *model.RetentionSettings) { r.KeepDays = model.KeepDaysMax + 1 }},
		{"relation_capacity_bytes 低于下界", "relation_capacity_bytes", func(r *model.RetentionSettings) { r.RelationCapacityBytes = model.RelationCapacityMin - 1 }},
		{"relation_capacity_bytes 高于上界", "relation_capacity_bytes", func(r *model.RetentionSettings) { r.RelationCapacityBytes = model.RelationCapacityMax + 1 }},
		{"preserve_max_bytes 低于非零下界", "preserve_max_bytes", func(r *model.RetentionSettings) { r.PreserveMaxBytes = model.PreserveMaxMin - 1 }},
		{"preserve_max_bytes 高于上界", "preserve_max_bytes", func(r *model.RetentionSettings) { r.PreserveMaxBytes = model.PreserveMaxMax + 1 }},
		{"trash_days 低于下界", "trash_days", func(r *model.RetentionSettings) { r.TrashDays = model.TrashDaysMin - 1 }},
		{"trash_days 高于上界", "trash_days", func(r *model.RetentionSettings) { r.TrashDays = model.TrashDaysMax + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestConfig(t)
			base := model.DefaultRetention()
			// 预写一组合法值，验证越界请求整体拒绝后原值保留
			legal := base
			legal.KeepCommits = 10
			if _, err := m.SetRetention(legal); err != nil {
				t.Fatalf("预写合法值失败: %v", err)
			}

			bad := base
			tc.mut(&bad)
			_, err := m.SetRetention(bad)
			if errs.CodeOf(err) != CodeSettingsRetentionInvalid {
				t.Fatalf("越界应返回 %s, got %v", CodeSettingsRetentionInvalid, err)
			}
			appErr := err.(*errs.AppError)
			if len(appErr.Args) != 1 || appErr.Args[0] != tc.field {
				t.Errorf("args 应为 [字段名 %s], got %v", tc.field, appErr.Args)
			}

			// 整体拒绝：配置保留预写值，未落入任何越界键
			got, err := m.Retention()
			if err != nil {
				t.Fatalf("拒绝后读取失败: %v", err)
			}
			if got != legal {
				t.Errorf("整体拒绝后配置被部分改写: got %+v want %+v", got, legal)
			}
		})
	}
}

// TestRetentionBoundariesAccepted 每键边界值（min/max）与合法中间值放行；
// preserve_max_bytes=0（不限）为合法显式取值。
func TestRetentionBoundariesAccepted(t *testing.T) {
	cases := []struct {
		name string
		set  model.RetentionSettings
	}{
		{"全部下界", model.RetentionSettings{
			KeepCommits: model.KeepCommitsMin, KeepDays: model.KeepDaysMin,
			RelationCapacityBytes: model.RelationCapacityMin,
			PreserveMaxBytes:      model.PreserveMaxMin, TrashDays: model.TrashDaysMin}},
		{"全部上界", model.RetentionSettings{
			KeepCommits: model.KeepCommitsMax, KeepDays: model.KeepDaysMax,
			RelationCapacityBytes: model.RelationCapacityMax,
			PreserveMaxBytes:      model.PreserveMaxMax, TrashDays: model.TrashDaysMax}},
		{"preserve_max_bytes=0 不限", model.RetentionSettings{
			KeepCommits: model.KeepCommitsDefault, KeepDays: model.KeepDaysDefault,
			RelationCapacityBytes: model.RelationCapacityDefault,
			PreserveMaxBytes:      model.PreserveMaxUnlimited, TrashDays: model.TrashDaysDefault}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestConfig(t)
			got, err := m.SetRetention(tc.set)
			if err != nil {
				t.Fatalf("边界值应放行: %v", err)
			}
			if got != tc.set {
				t.Errorf("返回值不一致: got %+v want %+v", got, tc.set)
			}
		})
	}
}

// TestRetentionPersisted 合法写入落盘，重开配置管理器读回一致；
// nil 指针键不落盘（未写键重读仍取默认）。
func TestRetentionPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	m := NewConfigManagerAt(path)
	set := model.RetentionSettings{
		KeepCommits: 12, KeepDays: 30,
		RelationCapacityBytes: 2 << 30, PreserveMaxBytes: 0, TrashDays: 14,
	}
	if _, err := m.SetRetention(set); err != nil {
		t.Fatalf("SetRetention: %v", err)
	}

	// 重开配置管理器（模拟重启：显式读盘）读回一致
	m2 := &ConfigManager{path: path}
	if err := ReadToml(m2.path, &m2.cfg); err != nil {
		t.Fatalf("重开读取配置失败: %v", err)
	}
	got, err := m2.Retention()
	if err != nil {
		t.Fatalf("重开读取失败: %v", err)
	}
	if got != set {
		t.Errorf("持久化往返不一致: got %+v want %+v", got, set)
	}
}

// TestRetentionLoadLayerValidation 手编 TOML 的加载层校验（ADR-0007 §8）：
// 越界键整体拒绝带字段名；部分键缺省时缺省键取默认；显式 0（不限）直读放行。
func TestRetentionLoadLayerValidation(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantErr string // 期望越界字段名；空串期望放行
		want    model.RetentionSettings
	}{
		{
			name:    "手编越界键拒绝",
			toml:    "[retention]\nkeep_commits = 3\nkeep_days = 30\ntrash_days = 14\n",
			wantErr: "keep_commits",
		},
		{
			name: "缺省键取默认",
			toml: "[retention]\nkeep_days = 30\n",
			want: model.RetentionSettings{KeepCommits: model.KeepCommitsDefault, KeepDays: 30,
				RelationCapacityBytes: model.RelationCapacityDefault, PreserveMaxBytes: model.PreserveMaxDefault,
				TrashDays: model.TrashDaysDefault},
		},
		{
			name: "显式 0 不限放行",
			toml: "[retention]\npreserve_max_bytes = 0\n",
			want: model.DefaultRetention(),
		},
	}
	// 显式 0 不限时 PreserveMaxBytes 应为 0 而非默认 32 MiB
	cases[2].want.PreserveMaxBytes = model.PreserveMaxUnlimited

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			// 模拟启动加载路径：显式读盘（NewConfigManagerAt 不读磁盘，测试注入用）
			m := &ConfigManager{path: path}
			if err := ReadToml(m.path, &m.cfg); err != nil {
				t.Fatalf("读取手编 TOML 失败: %v", err)
			}
			got, err := m.Retention()
			if tc.wantErr != "" {
				if errs.CodeOf(err) != CodeSettingsRetentionInvalid {
					t.Fatalf("应返回 %s, got %v", CodeSettingsRetentionInvalid, err)
				}
				appErr := err.(*errs.AppError)
				if len(appErr.Args) != 1 || appErr.Args[0] != tc.wantErr {
					t.Errorf("args 应为 [%s], got %v", tc.wantErr, appErr.Args)
				}
				return
			}
			if err != nil {
				t.Fatalf("加载层应放行: %v", err)
			}
			if got != tc.want {
				t.Errorf("归一结果不一致: got %+v want %+v", got, tc.want)
			}
		})
	}
}
