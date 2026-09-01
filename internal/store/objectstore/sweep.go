package objectstore

// sweep.go 为孤儿三向清扫供盘上事实（ADR-0007 §6，票 #64）：
//   - file-without-row：objects/sha256/** 下的对象文件无 DB 行（Put 后事务失败的
//     盘上残留）→ 引擎令其入回收站走时钟；
//   - .tmp-* 写中断残渣（objectsRoot 根下）→ 引擎直接删，无账目可挂。
// DB 侧事实（row-without-file）由 GC 引擎经仓储查询，两侧在引擎处对账。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"packgradle/internal/application/ports"
)

// ListObjectFiles 遍历 <objects>/sha256/** 全部对象文件（digest 从路径文件名
// 复原；非法 digest 文件名跳过——外部残留不进对账，也绝不被清扫误删）。
// 满足 ports.GCTrash。
func (c *CAS) ListObjectFiles() ([]ports.GCObjectFile, error) {
	root := filepath.Join(c.objectsRoot, algorithm)
	var out []ports.GCObjectFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		digest := strings.ToLower(d.Name())
		if err := validateDigest(digest); err != nil {
			return nil
		}
		out = append(out, ports.GCObjectFile{Digest: digest, Path: path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: 遍历对象文件失败: %w", err)
	}
	return out, nil
}

// ListTmpFiles 返回 objectsRoot 根下的 .tmp-* 写中断残渣（Put 的临时文件以
// 该前缀创建；表前缀常量 tmpPrefix 的复制源于包内同包引用，直接用）。
// 满足 ports.GCTrash。
func (c *CAS) ListTmpFiles() ([]string, error) {
	entries, err := os.ReadDir(c.objectsRoot)
	if err != nil {
		return nil, fmt.Errorf("objectstore: 读取对象根目录失败: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), tmpPrefix) {
			out = append(out, filepath.Join(c.objectsRoot, e.Name()))
		}
	}
	return out, nil
}

// DeleteFile 删除盘上文件（.tmp 残渣直删用；不存在视为成功，幂等）。
func (c *CAS) DeleteFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("objectstore: 删除文件 %s 失败: %w", path, err)
	}
	return nil
}
