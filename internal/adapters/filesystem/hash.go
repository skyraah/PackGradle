// Package filesystem 实现 ports 定义的文件系统能力：
// 流式哈希、原子写、root-relative 路径安全解析与端点绑定指纹。
package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// Hasher 实现 ports.FileHasher：流式 SHA-256，不整读内存。
type Hasher struct{}

// NewHasher 构造流式哈希器。
func NewHasher() *Hasher { return &Hasher{} }

// HashFile 返回文件内容指纹与观察事实（size/mtime；FileKey P1 不采集）。
func (h *Hasher) HashFile(ctx context.Context, absPath string) (model.ContentRef, ports.FileFacts, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return model.ContentRef{}, ports.FileFacts{}, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return model.ContentRef{}, ports.FileFacts{}, err
	}
	if st.IsDir() {
		return model.ContentRef{}, ports.FileFacts{}, &os.PathError{Op: "hash", Path: absPath, Err: errIsDir}
	}

	digest, err := sha256File(f)
	if err != nil {
		return model.ContentRef{}, ports.FileFacts{}, err
	}
	return model.ContentRef{
			Algorithm: "sha256",
			Digest:    digest,
			Size:      st.Size(),
		}, ports.FileFacts{
			SizeBytes:          st.Size(),
			ModifiedAtUnixNano: st.ModTime().UnixNano(),
		}, nil
}

func sha256File(r io.Reader) (string, error) {
	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(h, r, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashReader 也供 CAS/物化复用的流式摘要工具。
func hashReader(r io.Reader) (hash.Hash, error) {
	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(h, r, buf); err != nil {
		return nil, err
	}
	return h, nil
}
