package syncstage

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 所有权证明（ownership proof）：一份可用 HMAC-SHA256 独立校验的记录，
// 证明「该目标路径属于本次 Apply 运行」（redesign §6.6 步骤 3、ADR-0004 §2/§4）。
//
// 形态：运行标识 + 操作标识 + 关系标识 + 目标 root-relative 路径 +
// before/after 内容摘要 + 随机 nonce，以运行密钥（run.key，仅存在于本运行
// 暂存目录）为 HMAC-SHA256 密钥的签名式记录。
//
// 校验保证（恢复探测 T05 可独立执行，不依赖路径存在性猜测）：
//   - 伪造：无运行密钥的签名无法通过 HMAC 比对 → ErrProofInvalid；
//   - 跨运行：密钥按运行隔离，甲运行的证明在乙运行密钥下必然失配；
//     run_id 与密钥双重绑定；
//   - 篡改：任一字段（含 nonce 与 schema 版本）改动都改变规范化签名串，
//     HMAC 比对失败 → ErrProofInvalid。
//
// 序列化：JSON（schema_version 字段控制演进），落两处——journal 的
// ownership_proof_json 列（上层落库）与运行暂存目录 proofs/<op_id>.json
// （本包 SaveProof），两处副本可互验。
//
// 进程被强杀后重入同一运行：OpenRun 从暂存目录加载同一密钥，历史证明仍可
// 校验；密钥文件缺失则 ErrRunEvidenceIncomplete，交由恢复路径，不得换钥续签。

// proofSchemaVersion 是所有权证明记录的当前 schema 版本。
const proofSchemaVersion = 1

// nonceBytes 是每个证明的随机 nonce 字节数（防同字段记录的重放签名复用）。
const nonceBytes = 16

// OwnershipProof 是单操作的所有权证明记录（JSON 序列化形态，字段顺序固定）。
type OwnershipProof struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`                  // 所属 Apply 运行（task_id）
	OperationID   string `json:"operation_id"`            // 操作标识（op_xxxx）
	RelationID    string `json:"relation_id,omitempty"`   // 关系标识（跨运行混淆的额外绑定）
	TargetPath    string `json:"target_relative_path"`    // 目标 root-relative 路径（斜杠）
	BeforeDigest  string `json:"before_digest,omitempty"` // 旧内容 sha256（create 为空）
	AfterDigest   string `json:"after_digest,omitempty"`  // 新内容 sha256（delete 为空）
	Nonce         string `json:"nonce"`                   // 随机 nonce（hex）
	Signature     string `json:"signature"`               // HMAC-SHA256 签名（hex）
}

// Kind 推导证明对应的动作类别：create（无 before）/ delete（无 after）/
// modify（前后都有）。
func (p OwnershipProof) Kind() string {
	switch {
	case p.BeforeDigest == "" && p.AfterDigest != "":
		return "create"
	case p.BeforeDigest != "" && p.AfterDigest == "":
		return "delete"
	default:
		return "modify"
	}
}

// IssueProof 为一次操作签发所有权证明。目标路径必须能通过 root-relative
// 校验、digest 必须是合法 sha256 形态，否则拒绝签发（不签发指向逃逸路径的
// 证明）。签发即签名：证明一旦离开本包，任何字段改动都无法通过校验。
func (r *Run) IssueProof(relationID, operationID, targetRel, beforeDigest, afterDigest string) (OwnershipProof, error) {
	if err := validateID(operationID); err != nil {
		return OwnershipProof{}, fmt.Errorf("syncstage: operation_id 非法: %w", err)
	}
	cleanRel, err := normalizeRelative(targetRel)
	if err != nil {
		return OwnershipProof{}, err
	}
	if err := validateDigestHex(beforeDigest); err != nil {
		return OwnershipProof{}, fmt.Errorf("syncstage: before_digest 非法: %w", err)
	}
	if err := validateDigestHex(afterDigest); err != nil {
		return OwnershipProof{}, fmt.Errorf("syncstage: after_digest 非法: %w", err)
	}
	if beforeDigest == "" && afterDigest == "" {
		return OwnershipProof{}, errors.New("syncstage: before/after digest 不可同时为空")
	}
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return OwnershipProof{}, fmt.Errorf("syncstage: 生成 nonce 失败: %w", err)
	}
	p := OwnershipProof{
		SchemaVersion: proofSchemaVersion,
		RunID:         r.id,
		OperationID:   operationID,
		RelationID:    relationID,
		TargetPath:    cleanRel,
		BeforeDigest:  beforeDigest,
		AfterDigest:   afterDigest,
		Nonce:         hex.EncodeToString(nonce),
	}
	p.Signature = r.signProof(p)
	return p, nil
}

// proofSigningPayload 返回规范化签名串：固定字段顺序、换行分隔、不含签名自身。
// 任何字段（含 schema 版本）的改动都会改变该串。
func proofSigningPayload(p OwnershipProof) string {
	return strings.Join([]string{
		strconv.Itoa(p.SchemaVersion),
		p.RunID,
		p.OperationID,
		p.RelationID,
		p.TargetPath,
		p.BeforeDigest,
		p.AfterDigest,
		p.Nonce,
	}, "\n")
}

// signProof 以运行密钥计算证明的 HMAC-SHA256 签名（hex）。
func (r *Run) signProof(p OwnershipProof) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(proofSigningPayload(p)))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyOwnershipProof 校验证明属于本运行且未被篡改/伪造：
// schema 版本、run_id 绑定、路径形态与 HMAC 签名全数通过才放行；
// 任一失败返回 ErrProofInvalid。不依赖目标路径是否存在。
func (r *Run) VerifyOwnershipProof(p OwnershipProof) error {
	if p.SchemaVersion != proofSchemaVersion {
		return fmt.Errorf("%w: schema_version %d 不受支持", ErrProofInvalid, p.SchemaVersion)
	}
	if p.RunID != r.id {
		return fmt.Errorf("%w: 证明属于运行 %q，当前运行 %q", ErrProofInvalid, p.RunID, r.id)
	}
	if err := validateID(p.OperationID); err != nil {
		return fmt.Errorf("%w: operation_id 非法: %v", ErrProofInvalid, err)
	}
	if _, err := normalizeRelative(p.TargetPath); err != nil {
		return err
	}
	if err := validateDigestHex(p.BeforeDigest); err != nil {
		return fmt.Errorf("%w: before_digest 非法: %v", ErrProofInvalid, err)
	}
	if err := validateDigestHex(p.AfterDigest); err != nil {
		return fmt.Errorf("%w: after_digest 非法: %v", ErrProofInvalid, err)
	}
	if p.BeforeDigest == "" && p.AfterDigest == "" {
		return fmt.Errorf("%w: before/after digest 不可同时为空", ErrProofInvalid)
	}
	want := r.signProof(p)
	if !hmac.Equal([]byte(want), []byte(p.Signature)) {
		return fmt.Errorf("%w: 签名不匹配（伪造或字段被篡改）", ErrProofInvalid)
	}
	return nil
}

// SaveProof 把证明序列化存入运行暂存目录（proofs/<operation_id>.json，原子写）。
// 只保存能通过本校验的证明——暂存目录里不出现无效证据。与 journal 的
// ownership_proof_json 副本可互验。
func (r *Run) SaveProof(p OwnershipProof) error {
	if err := r.VerifyOwnershipProof(p); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("syncstage: 序列化证明失败: %w", err)
	}
	path := filepath.Join(r.dir, proofsDir, p.OperationID+proofExt)
	if err := writeFileAtomic(path, strings.NewReader(string(data))); err != nil {
		return err
	}
	return nil
}

// LoadProof 从运行暂存目录读取指定操作的证明（原始记录，不校验）；
// 校验由调用方执行 VerifyOwnershipProof（恢复探测必须独立复核，不信任落盘内容）。
func (r *Run) LoadProof(operationID string) (OwnershipProof, error) {
	if err := validateID(operationID); err != nil {
		return OwnershipProof{}, fmt.Errorf("syncstage: operation_id 非法: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(r.dir, proofsDir, operationID+proofExt))
	if err != nil {
		return OwnershipProof{}, fmt.Errorf("syncstage: 读取证明失败: %w", err)
	}
	var p OwnershipProof
	if err := json.Unmarshal(data, &p); err != nil {
		return OwnershipProof{}, fmt.Errorf("syncstage: 解析证明失败: %w", err)
	}
	return p, nil
}

// ListProofs 枚举运行暂存目录中的全部证明（按 OperationID 排序，原始记录
// 不校验），供恢复探测盘点暂存证据。
func (r *Run) ListProofs() ([]OwnershipProof, error) {
	entries, err := os.ReadDir(filepath.Join(r.dir, proofsDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("syncstage: 枚举证明失败: %w", err)
	}
	var out []OwnershipProof
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), proofExt) {
			continue
		}
		p, err := r.LoadProof(strings.TrimSuffix(e.Name(), proofExt))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OperationID < out[j].OperationID })
	return out, nil
}
