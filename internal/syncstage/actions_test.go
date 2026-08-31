package syncstage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"packgradle/internal/junction"
)

// ---- ApplyCreate ----

// TestApplyCreateRoundtripAndIdempotentReplay 创建落地 + 幂等重放逐字节不变。
func TestApplyCreateRoundtripAndIdempotentReplay(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNA")
	content := randomBytes(t, 768*1024) // 二进制、越过缓冲边界
	after := digestOf(content)
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "mods/sodium-0.6.5.jar", "", after)

	res, err := a.ApplyCreate(proof, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("ApplyCreate 失败: %v", err)
	}
	if res.Outcome != OutcomeApplied || res.Digest != after || res.TargetRel != "mods/sodium-0.6.5.jar" {
		t.Fatalf("结果 = %+v", res)
	}
	target := filepath.Join(root, "mods", "sodium-0.6.5.jar")
	if got := readEndpointFile(t, target); !bytes.Equal(got, content) {
		t.Error("目标内容与输入不一致（二进制往返）")
	}
	// 暂存副本真实存在且逐字节一致（幂等 redo 的证据）
	stagedAbs, err := run.StageAbs(res.TempRel)
	if err != nil {
		t.Fatal(err)
	}
	if got := readEndpointFile(t, stagedAbs); !bytes.Equal(got, content) {
		t.Error("暂存副本与输入不一致")
	}
	// 证明副本已落暂存
	saved, err := run.LoadProof("op_0001")
	if err != nil || saved != proof {
		t.Errorf("证明未落暂存: (%+v, %v)", saved, err)
	}

	// 幂等重放：already_applied 且逐字节不变（无重写、mtime 不动）
	st1, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // 越过 mtime 分辨率，任何重写都会显形
	replay, err := a.ApplyCreate(proof, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("幂等重放失败: %v", err)
	}
	if replay.Outcome != OutcomeAlreadyApplied || replay.Digest != after {
		t.Errorf("重放结果 = %+v, 期望 already_applied", replay)
	}
	st2, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("重放改写了目标文件（mtime 变化），违反幂等铁律")
	}
	if got := readEndpointFile(t, target); !bytes.Equal(got, content) {
		t.Error("重放后内容变化")
	}
}

// TestApplyCreateForeignContentRefused create 目标已存在且内容不符 → 含糊态拒绝。
func TestApplyCreateForeignContentRefused(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNB")
	foreign := []byte("user modified this file externally")
	path := writeEndpointFile(t, root, "cfg/opts.ini", foreign)
	after := digestOf([]byte("planned content"))
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "cfg/opts.ini", "", after)

	if _, err := a.ApplyCreate(proof, bytes.NewReader([]byte("planned content"))); !errors.Is(err, ErrTargetModified) {
		t.Fatalf("应返回 ErrTargetModified, 实际 %v", err)
	}
	if got := readEndpointFile(t, path); !bytes.Equal(got, foreign) {
		t.Error("拒绝后目标内容不得被改动")
	}
}

// TestApplyCreateProofKindMismatch 动作与证明类别不匹配一律拒绝。
func TestApplyCreateProofKindMismatch(t *testing.T) {
	a, run, _ := newActions(t, "task_03RUNC")
	del := mustIssueProof(t, run, "op_0001", "", "a.txt", digestOf([]byte("x")), "")
	if _, err := a.ApplyCreate(del, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("delete 证明不可用于 create, 实际 %v", err)
	}
	mod := mustIssueProof(t, run, "op_0002", "", "a.txt", digestOf([]byte("x")), digestOf([]byte("y")))
	if _, err := a.ApplyCreate(mod, bytes.NewReader([]byte("y"))); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("modify 证明不可用于 create, 实际 %v", err)
	}
}

// TestApplyCreateStagedDigestMismatch 内容与证明的 after digest 不符 → 拒绝落地。
func TestApplyCreateStagedDigestMismatch(t *testing.T) {
	a, run, root := newActions(t, "task_03RUND")
	after := digestOf([]byte("claimed"))
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "mods/a.jar", "", after)

	actual := []byte("actual bytes")
	if _, err := a.ApplyCreate(proof, bytes.NewReader(actual)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("暂存复核失败应返回 ErrDigestMismatch, 实际 %v", err)
	}
	// 目标未落地、暂存无残留
	if _, err := os.Stat(filepath.Join(root, "mods", "a.jar")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("复核失败不得落地目标: %v", err)
	}
	files, err := run.ListStagedFiles()
	if err != nil || len(files) != 0 {
		t.Errorf("复核失败不得残留暂存副本: %+v, %v", files, err)
	}
}

// ---- ApplyModify ----

func TestApplyModifyFlowAndIdempotentReplay(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNE")
	beforeContent := []byte("v1 config content")
	afterContent := []byte("v2 config content with longer body")
	path := writeEndpointFile(t, root, "cfg/opts.ini", beforeContent)
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "cfg/opts.ini", digestOf(beforeContent), digestOf(afterContent))

	res, err := a.ApplyModify(proof, bytes.NewReader(afterContent))
	if err != nil {
		t.Fatalf("ApplyModify 失败: %v", err)
	}
	if res.Outcome != OutcomeApplied || res.Digest != digestOf(afterContent) {
		t.Fatalf("结果 = %+v", res)
	}
	if got := readEndpointFile(t, path); !bytes.Equal(got, afterContent) {
		t.Error("覆盖后内容不一致")
	}

	// 幂等重放
	st1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	replay, err := a.ApplyModify(proof, bytes.NewReader(afterContent))
	if err != nil {
		t.Fatalf("幂等重放失败: %v", err)
	}
	if replay.Outcome != OutcomeAlreadyApplied {
		t.Errorf("重放结果 = %+v", replay)
	}
	st2, _ := os.Stat(path)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("重放改写了目标文件")
	}

	// 外部修改后重放：目标既非 before 也非 after → 拒绝
	if err := os.WriteFile(path, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyModify(proof, bytes.NewReader(afterContent)); !errors.Is(err, ErrTargetModified) {
		t.Errorf("外部修改后应返回 ErrTargetModified, 实际 %v", err)
	}
}

// TestApplyModifyGuards 缺失目标与非 before 内容拒绝覆盖。
func TestApplyModifyGuards(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNF")
	before, after := digestOf([]byte("old")), digestOf([]byte("new"))
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "cfg/missing.ini", before, after)

	// 目标缺失（before 声称存在）
	if _, err := a.ApplyModify(proof, bytes.NewReader([]byte("new"))); !errors.Is(err, ErrTargetModified) {
		t.Errorf("缺失目标应返回 ErrTargetModified, 实际 %v", err)
	}

	// 目标内容是第三方值
	path := writeEndpointFile(t, root, "cfg/missing.ini", []byte("neither old nor new"))
	if _, err := a.ApplyModify(proof, bytes.NewReader([]byte("new"))); !errors.Is(err, ErrTargetModified) {
		t.Errorf("非 before 内容应返回 ErrTargetModified, 实际 %v", err)
	}
	_ = path

	// 目标是目录：目录下的缺失文件走「目标缺失」分支，目录本身走
	// ErrTargetNotFile 分支
	dirProof := mustIssueProof(t, run, "op_0002", "rel_03R", "adir/file.txt", before, after)
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyModify(dirProof, bytes.NewReader([]byte("new"))); !errors.Is(err, ErrTargetModified) {
		t.Errorf("缺失文件目标应返回 ErrTargetModified, 实际 %v", err)
	}
	// 目标直接是目录
	dirTargetProof := mustIssueProof(t, run, "op_0003", "rel_03R", "adir", before, after)
	if _, err := a.ApplyModify(dirTargetProof, bytes.NewReader([]byte("new"))); !errors.Is(err, ErrTargetNotFile) {
		t.Errorf("目录目标应返回 ErrTargetNotFile, 实际 %v", err)
	}
}

// ---- ApplyDelete ----

func TestApplyDeleteFlowAndIdempotentReplay(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNG")
	content := []byte("obsolete mod jar")
	path := writeEndpointFile(t, root, "mods/old.jar", content)
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "mods/old.jar", digestOf(content), "")

	res, err := a.ApplyDelete(proof)
	if err != nil {
		t.Fatalf("ApplyDelete 失败: %v", err)
	}
	if res.Outcome != OutcomeApplied || res.Digest != "" || res.TempRel != "" {
		t.Fatalf("结果 = %+v", res)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("目标应已删除: %v", err)
	}

	// 幂等重放：目标已不存在 → already_applied（重复恢复不二次删除）
	replay, err := a.ApplyDelete(proof)
	if err != nil {
		t.Fatalf("删除重放失败: %v", err)
	}
	if replay.Outcome != OutcomeAlreadyApplied {
		t.Errorf("删除重放结果 = %+v", replay)
	}
}

// TestApplyDeleteGuards before 不符拒绝删除；目录目标拒绝。
func TestApplyDeleteGuards(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNH")
	content := []byte("precious user data")
	path := writeEndpointFile(t, root, "saves/world.dat", content)

	// before digest 不符（外部修改嫌疑）→ 拒绝删除
	wrongBefore := mustIssueProof(t, run, "op_0001", "rel_03R", "saves/world.dat", digestOf([]byte("stale")), "")
	if _, err := a.ApplyDelete(wrongBefore); !errors.Is(err, ErrTargetModified) {
		t.Fatalf("before 不符应返回 ErrTargetModified, 实际 %v", err)
	}
	if got := readEndpointFile(t, path); !bytes.Equal(got, content) {
		t.Error("拒绝删除后目标必须完好")
	}

	// 目录目标拒绝
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirProof := mustIssueProof(t, run, "op_0002", "rel_03R", "adir", digestOf([]byte("x")), "")
	if _, err := a.ApplyDelete(dirProof); !errors.Is(err, ErrTargetNotFile) {
		t.Errorf("目录目标应返回 ErrTargetNotFile, 实际 %v", err)
	}
}

// ---- 路径逃逸防线 ----

// TestApplyLinkEscapeRejected 经链接/junction 组件绕道 root 外的写删链路一律拒绝。
func TestApplyLinkEscapeRejected(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNI")
	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")

	link := filepath.Join(root, "link")
	switch runtime.GOOS {
	case "windows":
		if err := junction.NewWindowsManager().Create(link, outside); err != nil {
			t.Skipf("创建 junction 失败（可能非 NTFS 卷）: %v", err)
		}
		defer os.Remove(link)
	default:
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("创建 symlink 失败: %v", err)
		}
		defer os.Remove(link)
	}

	after := digestOf([]byte("evil content"))
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "link/evil.txt", "", after)
	if _, err := a.ApplyCreate(proof, bytes.NewReader([]byte("evil content"))); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("经链接组件的写入应返回 ErrPathEscape, 实际 %v", err)
	}
	if _, err := os.Stat(evil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root 外不得产生文件: %v", err)
	}

	// 删除同样拒绝（不经链接删除目标内容）
	delProof := mustIssueProof(t, run, "op_0002", "rel_03R", "link/anything", digestOf([]byte("x")), "")
	if _, err := a.ApplyDelete(delProof); !errors.Is(err, ErrPathEscape) {
		t.Errorf("经链接组件的删除应返回 ErrPathEscape, 实际 %v", err)
	}
}

// TestApplyDeepMissingAncestorsCreated 正常创建允许深层不存在前缀。
func TestApplyDeepMissingAncestorsCreated(t *testing.T) {
	a, run, root := newActions(t, "task_03RUNJ")
	content := []byte("nested")
	proof := mustIssueProof(t, run, "op_0001", "rel_03R", "mods/.index/deep/new.toml", "", digestOf(content))
	if _, err := a.ApplyCreate(proof, bytes.NewReader(content)); err != nil {
		t.Fatalf("深层创建失败: %v", err)
	}
	if got := readEndpointFile(t, filepath.Join(root, "mods", ".index", "deep", "new.toml")); !bytes.Equal(got, content) {
		t.Error("深层创建内容不一致")
	}
}

// ---- NewActions 校验 ----

func TestNewActionsValidation(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_03RUNK")
	if _, err := NewActions(nil, t.TempDir()); err == nil {
		t.Error("nil 运行句柄应报错")
	}
	if _, err := NewActions(run, ""); err == nil {
		t.Error("空 root 应报错")
	}
	if _, err := NewActions(run, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("不可达 root 应报错")
	}
	fileRoot := writeEndpointFile(t, newEndpointRoot(t), "file.txt", []byte("x"))
	if _, err := NewActions(run, fileRoot); err == nil {
		t.Error("文件 root 应报错")
	}
	root := newEndpointRoot(t)
	a, err := NewActions(run, root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Root() != filepath.Clean(root) {
		t.Errorf("Root 应为清理后的绝对路径: %q", a.Root())
	}
	if a.Run() != run {
		t.Error("Run() 应返回绑定句柄")
	}
}
