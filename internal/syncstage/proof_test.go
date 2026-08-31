package syncstage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- 生成 / 序列化 / 校验三件套 ----

func TestIssueVerifySerializeRoundtrip(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_02RUNA")
	before := digestOf([]byte("old"))
	after := digestOf([]byte("new"))

	p := mustIssueProof(t, run, "op_0001", "rel_02R", "cfg/opts.ini", before, after)
	if p.Kind() != "modify" {
		t.Errorf("Kind = %q, 期望 modify", p.Kind())
	}
	if p.SchemaVersion != proofSchemaVersion || p.RunID != run.ID() || p.TargetPath != "cfg/opts.ini" {
		t.Errorf("字段绑定不符: %+v", p)
	}
	if len(p.Nonce) != nonceBytes*2 {
		t.Errorf("nonce 应为 %d 字节 hex, 实际 %d", nonceBytes, len(p.Nonce))
	}
	if err := run.VerifyOwnershipProof(p); err != nil {
		t.Fatalf("合法证明被拒绝: %v", err)
	}

	// JSON 序列化往返（journal ownership_proof_json / 暂存证据的形态）
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back OwnershipProof
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back != p {
		t.Errorf("JSON 往返不一致: %+v vs %+v", back, p)
	}
	if err := run.VerifyOwnershipProof(back); err != nil {
		t.Errorf("序列化往返后证明应可校验: %v", err)
	}
}

func TestProofKindMapping(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_02RUNB")
	create := mustIssueProof(t, run, "op_0001", "", "a.txt", "", digestOf([]byte("x")))
	if create.Kind() != "create" {
		t.Errorf("create Kind = %q", create.Kind())
	}
	del := mustIssueProof(t, run, "op_0002", "", "a.txt", digestOf([]byte("x")), "")
	if del.Kind() != "delete" {
		t.Errorf("delete Kind = %q", del.Kind())
	}
}

// TestProofIssueValidation 签发侧防线：逃逸路径与非法 digest 拒绝签发。
func TestProofIssueValidation(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_02RUNC")
	upperDigest := strings.ToUpper(digestOf([]byte("x")))
	cases := []struct {
		name                     string
		opID, rel, before, after string
		wantErr                  error
	}{
		{"逃逸目标", "op_0001", "../evil.txt", "", digestOf([]byte("x")), ErrPathEscape},
		{"空操作 ID", "", "a.txt", "", digestOf([]byte("x")), nil},
		{"非 hex digest", "op_0003", "a.txt", "nothexnothexnothexnothexnothexnothexnothex", "", nil},
		{"大写 digest", "op_0004", "a.txt", "", upperDigest, nil},
		{"前后摘要同空", "op_0005", "a.txt", "", "", nil},
	}
	for _, c := range cases {
		_, err := run.IssueProof("rel_02R", c.opID, c.rel, c.before, c.after)
		if err == nil {
			t.Errorf("%s: 应拒绝签发", c.name)
			continue
		}
		if c.wantErr != nil && !errors.Is(err, c.wantErr) {
			t.Errorf("%s: 期望 %v, 实际 %v", c.name, c.wantErr, err)
		}
	}
	// 合法形态（反斜杠目标归一）仍可签发
	p, err := run.IssueProof("rel_02R", "op_0006", `mods\a.jar`, "", digestOf([]byte("x")))
	if err != nil {
		t.Fatalf("反斜杠目标应可签发: %v", err)
	}
	if p.TargetPath != "mods/a.jar" {
		t.Errorf("目标应归一为斜杠形态, 实际 %q", p.TargetPath)
	}
}

// TestProofTamperMatrix 篡改任一字段都不可通过校验（签名绑定全部字段）。
func TestProofTamperMatrix(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_02RUND")
	p := mustIssueProof(t, run, "op_0001", "rel_02R", "mods/a.jar", digestOf([]byte("old")), digestOf([]byte("new")))

	mutations := map[string]func(*OwnershipProof){
		"schema_version": func(x *OwnershipProof) { x.SchemaVersion = 99 },
		"run_id":         func(x *OwnershipProof) { x.RunID = "task_02OTHER" },
		"operation_id":   func(x *OwnershipProof) { x.OperationID = "op_9999" },
		"relation_id":    func(x *OwnershipProof) { x.RelationID = "rel_02X" },
		"target_path":    func(x *OwnershipProof) { x.TargetPath = "mods/b.jar" },
		"before_digest":  func(x *OwnershipProof) { x.BeforeDigest = digestOf([]byte("attacker-old")) },
		"after_digest":   func(x *OwnershipProof) { x.AfterDigest = digestOf([]byte("attacker-new")) },
		"nonce":          func(x *OwnershipProof) { x.Nonce = "0000" },
		"signature":      func(x *OwnershipProof) { x.Signature = hex.EncodeToString(make([]byte, 32)) },
	}
	for name, mutate := range mutations {
		tampered := p
		mutate(&tampered)
		if err := run.VerifyOwnershipProof(tampered); !errors.Is(err, ErrProofInvalid) {
			t.Errorf("篡改 %s 后应返回 ErrProofInvalid, 实际 %v", name, err)
		}
	}
}

// TestProofForge 攻击者无运行密钥：即便伪造出「自洽」的签名也无法通过校验。
func TestProofForge(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_02RUNE")
	forgeKey := []byte("attacker-controlled-key-0123456789abcdef")

	forge := OwnershipProof{
		SchemaVersion: proofSchemaVersion,
		RunID:         run.ID(),
		OperationID:   "op_0001",
		RelationID:    "rel_02R",
		TargetPath:    "saves/world.dat",
		AfterDigest:   digestOf([]byte("evil content")),
		Nonce:         "aabbccdd",
	}
	mac := hmac.New(sha256.New, forgeKey)
	mac.Write([]byte(proofSigningPayload(forge)))
	forge.Signature = hex.EncodeToString(mac.Sum(nil))

	if err := run.VerifyOwnershipProof(forge); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("伪造证明应拒绝, 实际 %v", err)
	}
}

// TestProofCrossRun 跨运行拒绝的两个面：
//  1. run_id 不同直接拒绝；
//  2. 同名运行但密钥不同（暂存被重建/另一机器）签名必然失配——
//     journal 里的历史证明不能在新密钥下洗白。
func TestProofCrossRun(t *testing.T) {
	stagingRoot := newStagingRoot(t)
	runA := openTestRun(t, stagingRoot, "task_02RUN1")
	runB := openTestRun(t, stagingRoot, "task_02RUN2")
	proofA := mustIssueProof(t, runA, "op_0001", "rel_02R", "mods/a.jar", "", digestOf([]byte("x")))

	if err := runB.VerifyOwnershipProof(proofA); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("跨运行证明应拒绝, 实际 %v", err)
	}

	// 同 run_id、不同密钥（另一 staging 根下的同名运行）
	otherRoot := newStagingRoot(t)
	runAClone := openTestRun(t, otherRoot, "task_02RUN1")
	if err := runAClone.VerifyOwnershipProof(proofA); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("同 run_id 不同密钥的证明应拒绝, 实际 %v", err)
	}
}

// ---- 暂存证据：SaveProof / LoadProof / ListProofs ----

func TestProofSaveLoadList(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_02RUN3")
	p1 := mustIssueProof(t, run, "op_0002", "rel_02R", "mods/b.jar", digestOf([]byte("o")), digestOf([]byte("n")))
	p0 := mustIssueProof(t, run, "op_0001", "rel_02R", "mods/a.jar", "", digestOf([]byte("n")))

	for _, p := range []OwnershipProof{p1, p0} {
		if err := run.SaveProof(p); err != nil {
			t.Fatalf("SaveProof(%s) 失败: %v", p.OperationID, err)
		}
	}
	// 落盘位置 proofs/<op_id>.json
	if _, err := os.Stat(filepath.Join(run.Dir(), proofsDir, "op_0001.json")); err != nil {
		t.Errorf("证明文件缺失: %v", err)
	}

	loaded, err := run.LoadProof("op_0001")
	if err != nil {
		t.Fatalf("LoadProof 失败: %v", err)
	}
	if loaded != p0 {
		t.Errorf("落盘证明往返不一致: %+v vs %+v", loaded, p0)
	}

	list, err := run.ListProofs()
	if err != nil {
		t.Fatalf("ListProofs 失败: %v", err)
	}
	if len(list) != 2 || list[0].OperationID != "op_0001" || list[1].OperationID != "op_0002" {
		t.Errorf("枚举结果应按 OperationID 排序: %+v", list)
	}

	// 只保存能通过校验的证明
	tampered := p1
	tampered.TargetPath = "mods/hacked.jar"
	if err := run.SaveProof(tampered); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("无效证明不应落盘, 实际 %v", err)
	}
}

// TestProofOnDiskTamperDetected 落盘证据被篡改时恢复探测（先 Load 后 Verify）
// 必须拒绝——暂存证据不被信任，只被复核。
func TestProofOnDiskTamperDetected(t *testing.T) {
	run := openTestRun(t, newStagingRoot(t), "task_02RUN4")
	p := mustIssueProof(t, run, "op_0001", "rel_02R", "saves/w.dat", digestOf([]byte("o")), "")
	if err := run.SaveProof(p); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(run.Dir(), proofsDir, "op_0001.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	forged := strings.Replace(string(raw), "saves/w.dat", "saves/innocent.dat", 1)
	if forged == string(raw) {
		t.Fatal("夹具替换失败")
	}
	if err := os.WriteFile(path, []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := run.LoadProof("op_0001")
	if err != nil {
		t.Fatalf("读取被篡改文件本身应成功（校验是独立步骤）: %v", err)
	}
	if err := run.VerifyOwnershipProof(loaded); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("落盘篡改必须被校验拒绝, 实际 %v", err)
	}
}
