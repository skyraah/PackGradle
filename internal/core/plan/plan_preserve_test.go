package plan

import (
	"testing"

	"packgradle/internal/core/model"
)

// preserve_skip 计划行判定单测（ADR-0007 §7，票 #64；契约 06 §3.7）：
// 非 mod 覆盖/删除行超阈值固化 PreserveSkip；mod 行与 create 行不标；
// 阈值 0（不限）全不标。

// bigObs 构造指定 digest 与内容字节数的文件资源观察。
func bigObs(id, digest string, sizeBytes int64) model.ResourceObservation {
	obs := fileObs(id, digest, "")
	obs.Representation.Content = &model.ContentRef{Algorithm: "sha256", Digest: digest, Size: sizeBytes}
	return obs
}

const miB = int64(1) << 20

// TestBuildDraftPreserveSkip 表驱动验证计划行 preserve_skip 固化。
func TestBuildDraftPreserveSkip(t *testing.T) {
	// 项目侧小 config 新内容 + 大 mod jar；运行端放旧版本——big.ini 旧版本
	// 40 MiB（超阈值，被覆盖即不留存）、small.ini 旧版本 1 MiB（不超）；
	// mod jar 运行端缺 → create 行（无旧版本，顺带验证 create 行不标）。
	const bigFile = "file:big.ini"
	const smallFile = "file:small.ini"
	const bigMod = "mod:jar:big.jar"
	project := snapshot(model.SideProject,
		bigObs(bigFile, "big-new", 1*miB), bigObs(smallFile, "small-new", 1*miB), bigObs(bigMod, "mod-new", 40*miB))
	oldRuntime := snapshot(model.SideRuntime,
		bigObs(bigFile, "big-old", 40*miB), bigObs(smallFile, "small-old", 1*miB))

	tests := []struct {
		name       string
		threshold  int64
		check      func(t *testing.T, plan model.SyncPlan)
	}{
		{
			name:      "覆盖行超阈值固化标记",
			threshold: 32 * miB,
			check: func(t *testing.T, p model.SyncPlan) {
				op, ok := findOp(p.Operations, bigFile)
				if !ok {
					t.Fatal("大文件操作行缺失")
				}
				if !op.PreserveSkip {
					t.Fatal("旧版本 40MiB 非 mod 覆盖行应 preserve_skip=true")
				}
			},
		},
		{
			name:      "小文件不标",
			threshold: 32 * miB,
			check: func(t *testing.T, p model.SyncPlan) {
				op, ok := findOp(p.Operations, smallFile)
				if !ok {
					t.Fatal("小文件操作行缺失")
				}
				if op.PreserveSkip {
					t.Fatal("1MiB 行不应标记")
				}
			},
		},
		{
			name:      "mod 行不标",
			threshold: 32 * miB,
			check: func(t *testing.T, p model.SyncPlan) {
				op, ok := findOp(p.Operations, bigMod)
				if !ok {
					t.Fatal("mod 操作行缺失")
				}
				if op.PreserveSkip {
					t.Fatal("mod 行恒不标（重取通道）")
				}
			},
		},
		{
			name:      "阈值 0=不限全不标",
			threshold: 0,
			check: func(t *testing.T, p model.SyncPlan) {
				for _, op := range p.Operations {
					if op.PreserveSkip {
						t.Fatalf("阈值 0 不应出现标记: %s", op.ResourceID)
					}
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := buildInput(baseline(bothBase(bigFile, "big-old"), bothBase(smallFile, "small-old")),
				project, oldRuntime, rule("r1", "bidirectional"))
			in.PreserveMaxBytes = tc.threshold
			p, err := BuildDraft(in)
			if err != nil {
				t.Fatalf("BuildDraft: %v", err)
			}
			// 双侧修改产生 modify_modify 冲突，take_project 决议后才有操作行。
			res := make([]model.Resolution, 0, len(p.Conflicts))
			for _, c := range p.Conflicts {
				res = append(res, model.Resolution{ResourceID: c.ResourceID, Choice: model.ChoiceTakeProject})
			}
			resolved, err := Resolve(p, project, oldRuntime, res, in.PreserveMaxBytes)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			tc.check(t, resolved)
		})
	}
}
