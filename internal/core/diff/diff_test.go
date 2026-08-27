package diff

import (
	"strings"
	"testing"

	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// content 构造固定 sha256 内容指纹。
func content(digest string) *model.ContentRef {
	return &model.ContentRef{Algorithm: "sha256", Digest: digest, Size: int64(len(digest))}
}

// fileObs 构造 text_file 资源观察（文件语义只取决于内容指纹）。
func fileObs(id, digest string) model.ResourceObservation {
	return model.ResourceObservation{
		ResourceID: model.ResourceID(id),
		Kind:       model.ResourceTextFile,
		Representation: model.Representation{
			RelativePath: "config/" + strings.TrimPrefix(id, "file:") + ".ini",
			Format:       "ini",
			Content:      content(digest),
		},
	}
}

// modObs 构造 mod 资源观察；id 决定身份置信度
// （mod:modrinth: / mod:curseforge: 为 high，mod:jar: / mod:path: 为 low）。
func modObs(id, version, declaredHash string) model.ResourceObservation {
	ident := normalize.IdentityFromResourceID(model.ResourceID(id))
	format := "packwiz-mod-toml"
	path := "mods/" + ident.Key + ".pw.toml"
	if ident.Provider == "jar" {
		format = "jar"
		path = ident.Key
	}
	return model.ResourceObservation{
		ResourceID: model.ResourceID(id),
		Kind:       model.ResourceMod,
		Identity:   ident,
		Representation: model.Representation{
			RelativePath: path,
			Format:       format,
			Content:      content("actual-" + declaredHash),
			Metadata: map[string]string{
				model.MetaVersion:           version,
				model.MetaSide:              "both",
				model.MetaDeclaredHashAlgo:  "sha256",
				model.MetaDeclaredHashValue: declaredHash,
			},
		},
	}
}

// snapshot 构造最小 ObservedSnapshot（乱序插入 map，验证输出仍有序）。
func snapshot(side model.Side, obs ...model.ResourceObservation) model.ObservedSnapshot {
	resources := make(map[model.ResourceID]model.ResourceObservation, len(obs))
	for _, o := range obs {
		resources[o.ResourceID] = o
	}
	return model.ObservedSnapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		SnapshotID:    "snap_" + string(side),
		RelationID:    "rel_test",
		Side:          side,
		Resources:     resources,
	}
}

// baseEntry 描述基线单资源的双侧表示。
type baseEntry struct {
	id               string
	project, runtime *model.Representation
}

// bothBase 构造双侧 present 且同指纹的基线条目。
func bothBase(id, digest string) baseEntry {
	rep := model.Representation{
		RelativePath: "config/" + strings.TrimPrefix(id, "file:") + ".ini",
		Format:       "ini",
		Content:      content(digest),
	}
	return baseEntry{id: id, project: &rep, runtime: &rep}
}

// absentBase 构造显式 absent tombstone 基线条目：资源在基线中登记，双侧均不存在。
func absentBase(id string) baseEntry {
	return baseEntry{id: id}
}

// baseline 构造最小 SyncBaseline；project/runtime 为 nil 表示该侧 absent。
func baseline(entries ...baseEntry) *model.SyncBaseline {
	resources := make(map[model.ResourceID]model.BaselineResource, len(entries))
	for _, e := range entries {
		state := "absent"
		if e.project != nil || e.runtime != nil {
			state = "present"
		}
		resources[model.ResourceID(e.id)] = model.BaselineResource{
			State:                 state,
			ProjectRepresentation: e.project,
			RuntimeRepresentation: e.runtime,
			Recoverability:        model.RecoverabilityNone,
		}
	}
	return &model.SyncBaseline{
		SchemaVersion: model.CurrentSchemaVersion,
		BaselineID:    "base_test",
		RelationID:    "rel_test",
		Resources:     resources,
	}
}

// TestThreeWayWithBaselineTruthTable 逐行覆盖架构文档 §6.3 真值表。
func TestThreeWayWithBaselineTruthTable(t *testing.T) {
	const id = "file:options.ini"
	cases := []struct {
		name          string
		base          string // "absent"=显式 tombstone 条目；其余为双侧同指纹 digest
		projectDigest string // "" 表示该侧 absent
		runtimeDigest string
		want          Classification
		wantConflict  model.ConflictKind // "" 表示无冲突
	}{
		{"双未变 noop", "h1", "h1", "h1", ClassNoop, ""},
		{"P 变 R 未变", "h1", "h2", "h1", ClassProjectToRuntime, ""},
		{"P 未变 R 变", "h1", "h1", "h2", ClassRuntimeToProject, ""},
		{"P 删 R 未变", "h1", "", "h1", ClassRemoveRuntimeCandidate, ""},
		{"R 删 P 未变", "h1", "h1", "", ClassRemoveProjectCandidate, ""},
		{"双变同指纹 converged", "h1", "h2", "h2", ClassConverged, ""},
		{"双变异指纹 conflict_modify", "h1", "h2", "h3", ClassConflictModify, model.ConflictModifyModify},
		{"P 删 R 变 conflict_delete_modify", "h1", "", "h2", ClassConflictDeleteModify, model.ConflictDeleteModify},
		{"R 删 P 变 conflict_delete_modify", "h1", "h2", "", ClassConflictDeleteModify, model.ConflictDeleteModify},
		{"双删 converged", "h1", "", "", ClassConverged, ""},
		{"基线 tombstone P 新增 R 无", "absent", "h2", "", ClassProjectToRuntime, ""},
		{"基线 tombstone 双端新增同指纹 converged", "absent", "h2", "h2", ClassConverged, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var base *model.SyncBaseline
			if tc.base == "absent" {
				base = baseline(absentBase(id))
			} else {
				base = baseline(bothBase(id, tc.base))
			}
			var proj, rt []model.ResourceObservation
			if tc.projectDigest != "" {
				proj = append(proj, fileObs(id, tc.projectDigest))
			}
			if tc.runtimeDigest != "" {
				rt = append(rt, fileObs(id, tc.runtimeDigest))
			}
			res, err := ThreeWay(Input{
				RelationID: "rel_test",
				Base:       base,
				Project:    snapshot(model.SideProject, proj...),
				Runtime:    snapshot(model.SideRuntime, rt...),
			})
			if err != nil {
				t.Fatalf("ThreeWay 报错: %v", err)
			}
			if len(res.Diffs) != 1 {
				t.Fatalf("期望 1 条 diff，得到 %d", len(res.Diffs))
			}
			d := res.Diffs[0]
			if d.ResourceID != model.ResourceID(id) {
				t.Fatalf("diff 资源 = %s，期望 %s", d.ResourceID, id)
			}
			if d.Kind != model.ResourceTextFile {
				t.Errorf("Kind = %q，期望 text_file", d.Kind)
			}
			if d.Classification != tc.want {
				t.Errorf("分类 = %q，期望 %q", d.Classification, tc.want)
			}
			if tc.wantConflict == "" {
				if len(res.Conflicts) != 0 {
					t.Errorf("期望无冲突，得到 %d 条", len(res.Conflicts))
				}
			} else if len(res.Conflicts) != 1 || res.Conflicts[0].Kind != tc.wantConflict {
				t.Errorf("冲突不符合期望: %+v", res.Conflicts)
			}
			// 存在性与语义字段
			wantBasePresent := tc.base != "absent"
			if d.ProjectPresent != (tc.projectDigest != "") {
				t.Errorf("ProjectPresent = %v", d.ProjectPresent)
			}
			if d.RuntimePresent != (tc.runtimeDigest != "") {
				t.Errorf("RuntimePresent = %v", d.RuntimePresent)
			}
			if d.BaseProjectPresent != wantBasePresent {
				t.Errorf("BaseProjectPresent = %v", d.BaseProjectPresent)
			}
			if d.BaseRuntimePresent != wantBasePresent {
				t.Errorf("BaseRuntimePresent = %v", d.BaseRuntimePresent)
			}
			if (d.ProjectSemantic != "") != d.ProjectPresent {
				t.Errorf("ProjectSemantic=%q 与 ProjectPresent=%v 不一致", d.ProjectSemantic, d.ProjectPresent)
			}
			if (d.RuntimeSemantic != "") != d.RuntimePresent {
				t.Errorf("RuntimeSemantic=%q 与 RuntimePresent=%v 不一致", d.RuntimeSemantic, d.RuntimePresent)
			}
		})
	}
}

// TestConflictEvidence 验证冲突证据包含 Base/Project/Runtime 表示副本，
// absent 侧为 nil。
func TestConflictEvidence(t *testing.T) {
	const id = "file:options.ini"

	// modify_modify：三方证据均非 nil
	res, err := ThreeWay(Input{
		Base:    baseline(bothBase(id, "h1")),
		Project: snapshot(model.SideProject, fileObs(id, "h2")),
		Runtime: snapshot(model.SideRuntime, fileObs(id, "h3")),
	})
	if err != nil {
		t.Fatalf("ThreeWay 报错: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("期望 1 条冲突，得到 %d", len(res.Conflicts))
	}
	c := res.Conflicts[0]
	if c.Kind != model.ConflictModifyModify {
		t.Fatalf("冲突类型 = %q", c.Kind)
	}
	for name, rep := range map[string]*model.Representation{
		"Base": c.Base, "Project": c.Project, "Runtime": c.Runtime,
	} {
		if rep == nil {
			t.Errorf("modify_modify 冲突的 %s 证据不得为 nil", name)
		}
	}
	if got := c.Project.Content.Digest; got != "h2" {
		t.Errorf("Project 证据指纹 = %q，期望 h2", got)
	}
	if got := c.Runtime.Content.Digest; got != "h3" {
		t.Errorf("Runtime 证据指纹 = %q，期望 h3", got)
	}

	// delete_modify（project 侧删除）：Project 为 nil，Base/Runtime 非 nil
	res2, err := ThreeWay(Input{
		Base:    baseline(bothBase(id, "h1")),
		Project: snapshot(model.SideProject),
		Runtime: snapshot(model.SideRuntime, fileObs(id, "h2")),
	})
	if err != nil {
		t.Fatalf("ThreeWay 报错: %v", err)
	}
	if len(res2.Conflicts) != 1 {
		t.Fatalf("期望 1 条冲突，得到 %d", len(res2.Conflicts))
	}
	c2 := res2.Conflicts[0]
	if c2.Kind != model.ConflictDeleteModify {
		t.Fatalf("冲突类型 = %q", c2.Kind)
	}
	if c2.Project != nil {
		t.Errorf("已删除侧 Project 证据应为 nil，得到 %+v", c2.Project)
	}
	if c2.Runtime == nil || c2.Base == nil {
		t.Errorf("Runtime/Base 证据不得为 nil: %+v", c2)
	}
}

// TestThreeWayInitialize 覆盖无 baseline 的初始化判定。
func TestThreeWayInitialize(t *testing.T) {
	const modID = "mod:modrinth:AANobbMI"
	const jarID = "mod:jar:sodium-0.6.5.jar"

	t.Run("高置信度 mod 双端 declared hash 相同 adopt_equal", func(t *testing.T) {
		projObs := modObs(modID, "0.6.5", "dh1")
		rtObs := modObs(modID, "0.6.5", "dh1")
		// 两侧路径与格式不同：高置信度语义摘要不含文件名，仍应相等
		rtObs.Representation.RelativePath = "mods/sodium-0.6.5.jar"
		rtObs.Representation.Format = "jar"
		res, err := ThreeWay(Input{
			Project: snapshot(model.SideProject, projObs),
			Runtime: snapshot(model.SideRuntime, rtObs),
		})
		if err != nil {
			t.Fatalf("ThreeWay 报错: %v", err)
		}
		d := res.Diffs[0]
		if d.Classification != ClassAdoptEqual {
			t.Errorf("分类 = %q，期望 adopt_equal", d.Classification)
		}
		if d.ProjectSemantic != d.RuntimeSemantic {
			t.Errorf("两侧语义应相同: %q vs %q", d.ProjectSemantic, d.RuntimeSemantic)
		}
		if len(res.Conflicts) != 0 {
			t.Errorf("adopt_equal 不应产生冲突，得到 %d 条", len(res.Conflicts))
		}
	})

	t.Run("低置信度 mod 双端同语义仍 init_choice", func(t *testing.T) {
		projObs := modObs(jarID, "1.0.0", "dj")
		rtObs := modObs(jarID, "1.0.0", "dj")
		// 构造相同 ContentRef 与相同文件名的 jar representation：
		// 两侧语义摘要确实相同，但低置信度身份绝不参与跨侧等价判定。
		res, err := ThreeWay(Input{
			Project: snapshot(model.SideProject, projObs),
			Runtime: snapshot(model.SideRuntime, rtObs),
		})
		if err != nil {
			t.Fatalf("ThreeWay 报错: %v", err)
		}
		d := res.Diffs[0]
		if d.ProjectSemantic == "" || d.ProjectSemantic != d.RuntimeSemantic {
			t.Fatalf("前置失败：两侧语义应非空且相同，得到 %q vs %q", d.ProjectSemantic, d.RuntimeSemantic)
		}
		if d.Classification != ClassInitChoice {
			t.Errorf("低置信度身份必须 init_choice，得到 %q", d.Classification)
		}
		if len(res.Conflicts) != 1 || res.Conflicts[0].Kind != model.ConflictInitialize {
			t.Fatalf("期望 initialize_choice 冲突: %+v", res.Conflicts)
		}
	})

	t.Run("双端不同 init_choice", func(t *testing.T) {
		res, err := ThreeWay(Input{
			Project: snapshot(model.SideProject, modObs(modID, "0.6.5", "dh1")),
			Runtime: snapshot(model.SideRuntime, modObs(modID, "0.6.6", "dh1")),
		})
		if err != nil {
			t.Fatalf("ThreeWay 报错: %v", err)
		}
		if res.Diffs[0].Classification != ClassInitChoice {
			t.Errorf("分类 = %q，期望 init_choice", res.Diffs[0].Classification)
		}
		if len(res.Conflicts) != 1 || res.Conflicts[0].Kind != model.ConflictInitialize {
			t.Fatalf("期望 initialize_choice 冲突: %+v", res.Conflicts)
		}
	})

	t.Run("仅 project 存在 init_choice", func(t *testing.T) {
		res, err := ThreeWay(Input{
			Project: snapshot(model.SideProject, fileObs("file:only.ini", "hp")),
			Runtime: snapshot(model.SideRuntime),
		})
		if err != nil {
			t.Fatalf("ThreeWay 报错: %v", err)
		}
		d := res.Diffs[0]
		if d.Classification != ClassInitChoice {
			t.Errorf("分类 = %q，期望 init_choice", d.Classification)
		}
		c := res.Conflicts[0]
		if c.Kind != model.ConflictInitialize || c.Project == nil || c.Runtime != nil || c.Base != nil {
			t.Errorf("初始化冲突证据不符合: %+v", c)
		}
	})

	t.Run("仅 runtime 存在 init_choice", func(t *testing.T) {
		res, err := ThreeWay(Input{
			Project: snapshot(model.SideProject),
			Runtime: snapshot(model.SideRuntime, fileObs("file:only.ini", "hr")),
		})
		if err != nil {
			t.Fatalf("ThreeWay 报错: %v", err)
		}
		if res.Diffs[0].Classification != ClassInitChoice {
			t.Errorf("分类 = %q，期望 init_choice", res.Diffs[0].Classification)
		}
		c := res.Conflicts[0]
		if c.Kind != model.ConflictInitialize || c.Runtime == nil || c.Project != nil {
			t.Errorf("初始化冲突证据不符合: %+v", c)
		}
	})

	t.Run("双端相同文件 adopt_equal", func(t *testing.T) {
		res, err := ThreeWay(Input{
			Project: snapshot(model.SideProject, fileObs("file:same.ini", "h1")),
			Runtime: snapshot(model.SideRuntime, fileObs("file:same.ini", "h1")),
		})
		if err != nil {
			t.Fatalf("ThreeWay 报错: %v", err)
		}
		if res.Diffs[0].Classification != ClassAdoptEqual {
			t.Errorf("分类 = %q，期望 adopt_equal", res.Diffs[0].Classification)
		}
	})
}

// TestResultOrdering 验证 Diffs/Conflicts 均按 ResourceID 字节序排序。
func TestResultOrdering(t *testing.T) {
	// 乱序构造 snapshot.Resources
	proj := snapshot(model.SideProject,
		fileObs("file:c.ini", "hc"),
		fileObs("file:a.ini", "ha"),
		modObs("mod:modrinth:zzz", "1.0", "hz"),
		fileObs("file:b.ini", "hb"),
		modObs("mod:jar:aaa", "1.0", "hj"),
	)
	res, err := ThreeWay(Input{Project: proj, Runtime: snapshot(model.SideRuntime)})
	if err != nil {
		t.Fatalf("ThreeWay 报错: %v", err)
	}

	wantOrder := []string{"file:a.ini", "file:b.ini", "file:c.ini", "mod:jar:aaa", "mod:modrinth:zzz"}
	if len(res.Diffs) != len(wantOrder) {
		t.Fatalf("期望 %d 条 diff，得到 %d", len(wantOrder), len(res.Diffs))
	}
	for i, want := range wantOrder {
		if got := string(res.Diffs[i].ResourceID); got != want {
			t.Errorf("Diffs[%d] = %s，期望 %s", i, got, want)
		}
		if got := string(res.Conflicts[i].ResourceID); got != want {
			t.Errorf("Conflicts[%d] = %s，期望 %s", i, got, want)
		}
	}
}

// TestThreeWayInvalidObservation 验证语义计算错误向上传播为 error。
func TestThreeWayInvalidObservation(t *testing.T) {
	obs := fileObs("file:bad.ini", "h1")
	obs.Representation.Content = nil // 文件资源缺少内容指纹
	if _, err := ThreeWay(Input{Project: snapshot(model.SideProject, obs)}); err == nil {
		t.Fatal("期望 SemanticDigest 错误以 error 返回，而不是 panic 或忽略")
	}
}
