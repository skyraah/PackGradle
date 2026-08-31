// Package syncstage 实现 Phase 2 Apply 的纯文件层基元（ADR-0004 §3、redesign §6.6）：
//
//   - 按运行隔离的暂存（staging）布局与运行目录原语（创建/枚举/清理，run.go）；
//   - 所有权证明（ownership proof）的生成/序列化/校验，可被恢复探测（T05）
//     独立校验「目标路径属于本次 Apply 运行」（proof.go）；
//   - before-content CAS 保全：旧内容经 CAS Put 流式落盘 + hash 复核后才返回
//     引用（preserve.go）；
//   - 文件动作原语 applyCreate/applyModify/applyDelete（copy 物化，幂等语义，
//     actions.go）；
//   - digest 复核原语（本文件 HashFile/VerifyFileDigest）。
//
// 零 SQLite/ports 依赖：operation journal 落库由上层（T04 引擎 / T05 恢复探测）
// 完成，本包只负责可独立校验的文件事实——暂存证据、所有权证明、digest 复核。
// 路径防线不依赖 adapters/filesystem（避免传递引入 ports）：root-relative 路径
// 经 core/normalize 校验，并对既有路径组件逐级拒绝 symlink/junction/reparse。
package syncstage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/core/model"
	"packgradle/internal/core/normalize"
)

// 哨兵错误。调用方（T04/T05）按 errors.Is 映射为业务错误码；
// 本包不发射 err.* 错误码（保持纯文件层，错误文案不外泄给前端）。
var (
	// ErrPathEscape 表示 root-relative 路径非法或解析后逃逸出 root
	// （`..`、绝对路径、盘符、symlink/junction/reparse 组件）。
	ErrPathEscape = errors.New("syncstage: 路径逃逸或非法")
	// ErrTargetModified 表示目标现存内容与期望（before/after digest）不符，
	// 疑似被外部修改；动作拒绝执行，交由恢复探测裁决。
	ErrTargetModified = errors.New("syncstage: 目标内容与期望不符")
	// ErrDigestMismatch 表示内容落盘后的 digest 复核与期望不一致。
	ErrDigestMismatch = errors.New("syncstage: digest 复核失败")
	// ErrProofInvalid 表示所有权证明缺失、伪造、跨运行或字段被篡改。
	ErrProofInvalid = errors.New("syncstage: 所有权证明无效")
	// ErrTargetNotFile 表示目标存在但不是普通文件（目录/symlink/junction）。
	ErrTargetNotFile = errors.New("syncstage: 目标不是普通文件")
	// ErrRunEvidenceIncomplete 表示运行目录存在但缺少运行密钥等暂存证据，
	// 不得在该运行下继续签发/校验证明（交由恢复路径处理）。
	ErrRunEvidenceIncomplete = errors.New("syncstage: 运行暂存证据不完整")
)

// sha256HexLen 是 sha256 十六进制摘要长度。
const sha256HexLen = 64

// writeFileAtomic 以「临时文件 + fsync + 原子 rename」写入 dest（与
// adapters/filesystem.WriteFileAtomic 同语义的本包实现，避免 ports 传递依赖）：
// 临时文件建在 dest 同目录（同卷保证 rename 原子），任何失败都清理临时文件；
// os.Rename 在 Windows 上按 MOVEFILE_REPLACE_EXISTING 语义覆盖已存在文件。
func writeFileAtomic(dest string, r io.Reader) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("syncstage: 创建父目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pgtmp-*")
	if err != nil {
		return fmt.Errorf("syncstage: 创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := io.Copy(tmp, r); err != nil {
		cleanup()
		return fmt.Errorf("syncstage: 写入内容: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("syncstage: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("syncstage: 关闭临时文件: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("syncstage: 原子替换: %w", err)
	}
	return nil
}

// WriteFileAtomic 以「临时文件 + fsync + 原子 rename」写入 dest
// （writeFileAtomic 的导出形态）：恢复补偿以 CAS before 保全回写旧内容时
// 必须保持与动作原语相同的原子落盘语义（T05 恢复探测消费）。
func WriteFileAtomic(dest string, r io.Reader) error {
	return writeFileAtomic(dest, r)
}

// withinRoot 报告 target 是否落在 root 之内（含 root 本身）；
// 大小写不敏感（Windows 卷），统一斜杠后前缀比较。
func withinRoot(root, target string) bool {
	rootSlash := strings.ToLower(filepath.ToSlash(filepath.Clean(root))) + "/"
	targetSlash := strings.ToLower(filepath.ToSlash(filepath.Clean(target)))
	if targetSlash+"/" == rootSlash {
		return true
	}
	return strings.HasPrefix(targetSlash, rootSlash)
}

// resolveTarget 把 root-relative 路径解析为 root 内绝对路径，并做两级防御：
// rel 先经 normalize.NormalizeRelativePath 拒绝 `..`/绝对路径/盘符/冒号；
// 再对 root 到目标的既有路径组件逐级 Lstat，凡 symlink/junction/reparse
// 组件一律拒绝（防御经由链接绕道 root 外的写删链路）。目标自身或其祖先
// 尚不存在时返回 exists=false（abs 仍可用于创建）。
func resolveTarget(root, rel string) (abs string, exists bool, err error) {
	cleanRel, err := normalizeRelative(rel)
	if err != nil {
		return "", false, err
	}
	abs = filepath.Join(root, filepath.FromSlash(cleanRel))
	if !withinRoot(root, abs) {
		return "", false, fmt.Errorf("%w: %q 解析到 %s", ErrPathEscape, rel, abs)
	}
	current := filepath.Clean(root)
	for _, part := range strings.Split(cleanRel, "/") {
		current = filepath.Join(current, part)
		st, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				// 既有链路到此为止，剩余尾部不含 `..`（normalize 已保证）。
				return abs, false, nil
			}
			return "", false, fmt.Errorf("syncstage: 检查路径组件 %s 失败: %w", current, statErr)
		}
		// Windows 上 junction/reparse 在 Go ≥1.25 呈 ModeIrregular，symlink 呈
		// ModeSymlink；两者一并拒绝（realpath 全解析由上层 resolver 负责，
		// 本层取保守拒绝口径）。
		if st.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", false, fmt.Errorf("%w: %q 组件 %q 是链接/junction", ErrPathEscape, rel, part)
		}
	}
	return abs, true, nil
}

// normalizeRelative 校验并归一 root-relative 路径（斜杠形态、无 `..`/盘符）。
func normalizeRelative(rel string) (string, error) {
	clean, err := normalize.NormalizeRelativePath(rel, false)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	return clean, nil
}

// isPlainRegularFile 报告 path 是否为普通文件且不经过任何重解析点。
func isPlainRegularFile(path string) (bool, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().IsRegular() && st.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0, nil
}

// hashReader 流式计算 sha256 与字节数（64KB 缓冲，不整读内存）。
func hashReader(r io.Reader) (digest string, size int64, err error) {
	h := sha256.New()
	buf := make([]byte, 64*1024)
	n, err := io.CopyBuffer(h, r, buf)
	if err != nil {
		return "", 0, fmt.Errorf("syncstage: 计算摘要: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// HashFile 计算路径内容的 sha256 指纹（model.ContentRef 形态，MVP 固定
// sha256）。目录或重解析点返回错误。
func HashFile(path string) (model.ContentRef, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.ContentRef{}, fmt.Errorf("syncstage: 打开 %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return model.ContentRef{}, fmt.Errorf("syncstage: stat %s: %w", path, err)
	}
	if !st.Mode().IsRegular() {
		return model.ContentRef{}, fmt.Errorf("syncstage: %s: %w", path, ErrTargetNotFile)
	}
	digest, _, err := hashReader(f)
	if err != nil {
		return model.ContentRef{}, err
	}
	return model.ContentRef{Algorithm: "sha256", Digest: digest, Size: st.Size()}, nil
}

// VerifyFileDigest 复核路径内容 sha256 与期望一致；不一致返回
// ErrDigestMismatch（detail 携带双方 digest），目标保持原样。
func VerifyFileDigest(path, wantDigest string) error {
	ref, err := HashFile(path)
	if err != nil {
		return err
	}
	if ref.Digest != wantDigest {
		return fmt.Errorf("%w: %s got=%s want=%s", ErrDigestMismatch, path, ref.Digest, wantDigest)
	}
	return nil
}

// validateDigestHex 校验 sha256 摘要形态：空串（该侧无内容）或 64 位小写十六进制。
func validateDigestHex(digest string) error {
	if digest == "" {
		return nil
	}
	if len(digest) != sha256HexLen {
		return fmt.Errorf("syncstage: 非法 sha256 digest 长度 %d", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("syncstage: 非法 sha256 digest: %w", err)
	}
	if strings.ToLower(digest) != digest {
		return fmt.Errorf("syncstage: sha256 digest 必须为小写: %s", digest)
	}
	return nil
}
