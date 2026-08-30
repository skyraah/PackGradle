package syncstage

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ---- 运行目录生命周期 ----

func TestOpenRunCreatesLayoutAndKey(t *testing.T) {
	stagingRoot := newStagingRoot(t)
	run := openTestRun(t, stagingRoot, "task_01RUNA")

	if run.ID() != "task_01RUNA" {
		t.Errorf("ID = %q", run.ID())
	}
	if run.Dir() != filepath.Join(stagingRoot, "task_01RUNA") {
		t.Errorf("Dir = %q", run.Dir())
	}
	// 布局：run.key + files/ + proofs/
	for _, sub := range []string{runKeyFile, stagedDir, proofsDir} {
		if _, err := os.Stat(filepath.Join(run.Dir(), sub)); err != nil {
			t.Errorf("布局缺失 %s: %v", sub, err)
		}
	}
	key, err := os.ReadFile(filepath.Join(run.Dir(), runKeyFile))
	if err != nil {
		t.Fatalf("读取 run.key 失败: %v", err)
	}
	if len(key) != 64 {
		t.Errorf("run.key 应为 32 字节 hex（64 字符）, 实际 %d", len(key))
	}
	if _, err := hex.DecodeString(string(key)); err != nil {
		t.Errorf("run.key 不是合法 hex: %v", err)
	}
}

// TestOpenRunReopenSameKey 崩溃重入：同一运行目录再次打开得到同一密钥，
// 既有证明仍可校验（T05 恢复探测的前提）。
func TestOpenRunReopenSameKey(t *testing.T) {
	stagingRoot := newStagingRoot(t)
	run := openTestRun(t, stagingRoot, "task_01RUNB")
	proof := mustIssueProof(t, run, "op_0001", "rel_01R", "mods/a.jar", "", digestOf([]byte("after")))

	reopened, err := OpenRun(stagingRoot, "task_01RUNB")
	if err != nil {
		t.Fatalf("重入 OpenRun 失败: %v", err)
	}
	if !bytes.Equal(reopened.key, run.key) {
		t.Fatal("重入后的运行密钥不一致")
	}
	if err := reopened.VerifyOwnershipProof(proof); err != nil {
		t.Errorf("重入后既有证明应可校验: %v", err)
	}
}

// TestOpenRunMissingKeyFails 密钥缺失 = 暂存证据不完整，不得换钥续签。
func TestOpenRunMissingKeyFails(t *testing.T) {
	stagingRoot := newStagingRoot(t)
	run := openTestRun(t, stagingRoot, "task_01RUNC")
	if err := os.Remove(filepath.Join(run.Dir(), runKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRun(stagingRoot, "task_01RUNC"); !errors.Is(err, ErrRunEvidenceIncomplete) {
		t.Errorf("密钥缺失应返回 ErrRunEvidenceIncomplete, 实际 %v", err)
	}
}

func TestOpenRunValidation(t *testing.T) {
	stagingRoot := newStagingRoot(t)
	if _, err := OpenRun("", "task_x"); err == nil {
		t.Error("空 stagingRoot 应报错")
	}
	for _, bad := range []string{"", "..", "a/b", "task:1", "task_/key"} {
		if _, err := OpenRun(stagingRoot, bad); err == nil {
			t.Errorf("非法 task_id %q 应报错", bad)
		}
	}
}

// ---- 暂存副本写入与枚举 ----

func TestStageContentRoundtripAndEnumerate(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_01RUND")
	content := randomBytes(t, 256*1024)
	digest := digestOf(content)

	tempRel, err := run.StageContent("mods/sub/a.jar", bytes.NewReader(content), digest)
	if err != nil {
		t.Fatalf("StageContent 失败: %v", err)
	}
	if tempRel != stagedDir+"/mods/sub/a.jar" {
		t.Errorf("tempRel = %q, 期望 %q", tempRel, stagedDir+"/mods/sub/a.jar")
	}
	stagedAbs, err := run.StageAbs(tempRel)
	if err != nil {
		t.Fatalf("StageAbs 失败: %v", err)
	}
	if got := readEndpointFile(t, stagedAbs); !bytes.Equal(got, content) {
		t.Error("暂存副本二进制往返不一致")
	}

	// 枚举：含暂存副本，TargetRel 镜像目标路径
	files, err := run.ListStagedFiles()
	if err != nil {
		t.Fatalf("ListStagedFiles 失败: %v", err)
	}
	if len(files) != 1 || files[0].TargetRel != "mods/sub/a.jar" || files[0].Size != int64(len(content)) {
		t.Errorf("枚举结果 = %+v", files)
	}
}

// TestStageContentDigestMismatchNoResidue 复核失败即删，不留可疑证据。
func TestStageContentDigestMismatchNoResidue(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_01RUNE")
	content := []byte("actual content")
	wrongDigest := digestOf([]byte("claimed content"))

	if _, err := run.StageContent("mods/a.jar", bytes.NewReader(content), wrongDigest); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digest 复核失败应返回 ErrDigestMismatch, 实际 %v", err)
	}
	files, err := run.ListStagedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("复核失败后不应残留暂存副本: %+v", files)
	}
}

func TestStageAbsRejectsEscape(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_01RUNF")
	for _, bad := range []string{"../../evil", "proofs/op_0001.json", "run.key", "files/../../outside"} {
		if _, err := run.StageAbs(bad); !errors.Is(err, ErrPathEscape) {
			t.Errorf("StageAbs(%q) 应返回 ErrPathEscape, 实际 %v", bad, err)
		}
	}
}

// ---- 运行隔离 ----

// TestRunIsolation 同一 stagingRoot 下两个运行互不可见、互不可校验。
func TestRunIsolation(t *testing.T) {
	stagingRoot := newStagingRoot(t)
	runA := openTestRun(t, stagingRoot, "task_01RUN1")
	runB := openTestRun(t, stagingRoot, "task_01RUN2")

	if bytes.Equal(runA.key, runB.key) {
		t.Fatal("不同运行的密钥不应相同")
	}

	contentA := []byte("run a content")
	tempRelA, err := runA.StageContent("mods/a.txt", bytes.NewReader(contentA), digestOf(contentA))
	if err != nil {
		t.Fatal(err)
	}
	proofA := mustIssueProof(t, runA, "op_0001", "rel_01R", "mods/a.txt", "", digestOf(contentA))

	// 甲运行的证明在乙运行下不可校验（跨运行拒绝）
	if err := runB.VerifyOwnershipProof(proofA); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("跨运行证明应拒绝, 实际 %v", err)
	}
	// 甲运行的暂存副本对乙运行不可见
	filesB, err := runB.ListStagedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(filesB) != 0 {
		t.Errorf("乙运行不应看到甲运行暂存副本: %+v", filesB)
	}
	stagedAbsB, err := runB.StageAbs(tempRelA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stagedAbsB); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("乙运行同名暂存路径不应有内容: %v", err)
	}
	// 证明文件同样隔离
	if _, err := runB.LoadProof("op_0001"); err == nil {
		t.Error("乙运行不应读到甲运行的证明文件")
	}
}

// TestCleanupRunIsolatedAndIdempotent 清理只影响目标运行，且可重试。
func TestCleanupRunIsolatedAndIdempotent(t *testing.T) {
	stagingRoot := newStagingRoot(t)
	runA := openTestRun(t, stagingRoot, "task_01RUN3")
	runB := openTestRun(t, stagingRoot, "task_01RUN4")

	if err := CleanupRun(stagingRoot, "task_01RUN3"); err != nil {
		t.Fatalf("CleanupRun 失败: %v", err)
	}
	if _, err := os.Stat(runA.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("甲运行目录应已删除: %v", err)
	}
	if _, err := os.Stat(runB.Dir()); err != nil {
		t.Errorf("乙运行目录不应受影响: %v", err)
	}
	// 幂等：重复清理与清理不存在的运行都成功
	if err := CleanupRun(stagingRoot, "task_01RUN3"); err != nil {
		t.Errorf("重复清理应幂等成功: %v", err)
	}
	if err := CleanupRun(stagingRoot, "task_01NOPE"); err != nil {
		t.Errorf("清理不存在的运行应幂等成功: %v", err)
	}
	// 句柄形态等价
	if err := runB.Remove(); err != nil {
		t.Errorf("Remove 失败: %v", err)
	}
	if err := runB.Remove(); err != nil {
		t.Errorf("Remove 应幂等: %v", err)
	}
}

// ---- TempRelFor ----

func TestTempRelFor(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_01RUN5")
	rel, err := run.TempRelFor(`mods\a.toml`)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "files/mods/a.toml" {
		t.Errorf("TempRelFor = %q", rel)
	}
	if _, err := run.TempRelFor("../evil"); !errors.Is(err, ErrPathEscape) {
		t.Errorf("逃逸目标应拒绝, 实际 %v", err)
	}
}
