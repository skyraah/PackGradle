package syncstage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// staging 运行目录布局（ADR-0004 §3：按 Apply 运行隔离，每个运行拥有自己的
// 目录和 root-relative 临时路径）：
//
//	<stagingRoot>/<task_id>/           一次 Apply 运行的全部暂存证据
//	  run.key                          运行密钥（32 字节 hex，所有权证明 HMAC 根）
//	  files/<target_relative_path>     暂存副本：镜像目标的 root-relative 路径
//	  proofs/<operation_id>.json       逐操作所有权证明（journal 之外的独立证据）
//
// 暂存副本按目标的 root-relative 路径镜像存放，恢复探测可直接由
// temp_relative_path 定位 staged 内容做幂等 redo。

const (
	// runKeyFile 是运行密钥文件名（run 目录直下）。
	runKeyFile = "run.key"
	// stagedDir 是暂存副本子目录名。
	stagedDir = "files"
	// proofsDir 是所有权证明子目录名。
	proofsDir = "proofs"
	// dlDirName 是下载暂存子目录名（ADR-0008 §6，票 #63）：download 行的
	// `.part` 与成品落此处，run 内续传、跨 run 不复用，随运行清理回收。
	dlDirName = "downloads"
	// proofExt 是单个证明文件扩展名。
	proofExt = ".json"
	// runKeyBytes 是运行密钥字节长度（HMAC-SHA256 密钥）。
	runKeyBytes = 32
)

// Run 是一次 Apply 运行的暂存目录句柄：持有运行密钥，签发与校验所有权证明、
// 写入/枚举暂存副本。零 SQLite 依赖；运行与 journal 的关联由 task_id 承担。
type Run struct {
	id  string // task_id
	dir string // <stagingRoot>/<task_id>
	key []byte // 运行密钥（原始 32 字节）
}

// StagedFile 是枚举出的单个暂存副本。
type StagedFile struct {
	// StagingRel 是相对运行目录的斜杠路径（journal 的 temp_relative_path）。
	StagingRel string `json:"temp_relative_path"`
	// TargetRel 是其镜像的目标 root-relative 路径（files/ 前缀之后的部分）。
	TargetRel string `json:"target_relative_path"`
	// Size 是文件字节数。
	Size int64 `json:"size"`
}

// validateID 校验 task_id / operation_id 形态：非空、仅 [A-Za-z0-9_-]、
// 不含路径分隔符（可安全用作目录名/文件名）。
func validateID(id string) error {
	if id == "" {
		return errors.New("syncstage: 标识符不能为空")
	}
	if len(id) > 128 {
		return fmt.Errorf("syncstage: 标识符过长: %d", len(id))
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("syncstage: 标识符含非法字符 %q", r)
		}
	}
	return nil
}

// OpenRun 打开（不存在则创建）task_id 的暂存运行目录，加载或生成运行密钥。
//
//   - 目录不存在：创建目录布局并生成新密钥（新运行）。
//   - 目录存在且密钥在：加载密钥（崩溃后重入同一运行）。
//   - 目录存在但密钥缺失：返回 ErrRunEvidenceIncomplete——密钥是所有权证明的
//     签名根，缺失即暂存证据不完整，不得继续签发证明（交由恢复路径裁决）。
func OpenRun(stagingRoot, taskID string) (*Run, error) {
	if stagingRoot == "" {
		return nil, errors.New("syncstage: stagingRoot 不能为空")
	}
	if err := validateID(taskID); err != nil {
		return nil, fmt.Errorf("syncstage: task_id 非法: %w", err)
	}
	dir := filepath.Join(stagingRoot, taskID)
	keyPath := filepath.Join(dir, runKeyFile)

	// 先区分「全新运行」（目录不存在）与「重入既有运行」：密钥是所有权
	// 证明的签名根，运行目录已存在而密钥缺失即暂存证据不完整，不得换钥续签。
	_, statErr := os.Lstat(dir)
	switch {
	case statErr == nil:
		key, err := readRunKey(keyPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("syncstage: %w: %s 缺失", ErrRunEvidenceIncomplete, runKeyFile)
			}
			return nil, fmt.Errorf("syncstage: 读取运行密钥失败: %w", err)
		}
		// 重入既有运行：目录布局幂等补齐。
		if err := ensureRunDirs(dir); err != nil {
			return nil, err
		}
		return &Run{id: taskID, dir: dir, key: key}, nil
	case errors.Is(statErr, fs.ErrNotExist):
		// 新运行：建布局 + 生成密钥。密钥经原子写落盘，先于任何证明签发。
		if err := ensureRunDirs(dir); err != nil {
			return nil, err
		}
		raw := make([]byte, runKeyBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("syncstage: 生成运行密钥失败: %w", err)
		}
		if err := writeFileAtomic(keyPath, strings.NewReader(hex.EncodeToString(raw))); err != nil {
			return nil, fmt.Errorf("syncstage: 写入运行密钥失败: %w", err)
		}
		return &Run{id: taskID, dir: dir, key: raw}, nil
	default:
		return nil, fmt.Errorf("syncstage: 检查运行目录失败: %w", statErr)
	}
}

// ensureRunDirs 幂等创建运行目录布局（files/ 与 proofs/）。
func ensureRunDirs(dir string) error {
	for _, sub := range []string{stagedDir, proofsDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return fmt.Errorf("syncstage: 创建暂存目录 %s 失败: %w", sub, err)
		}
	}
	return nil
}

// readRunKey 读取并解码运行密钥（64 位小写 hex → 32 字节）。
func readRunKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("syncstage: 运行密钥不是合法 hex: %w", err)
	}
	if len(raw) != runKeyBytes {
		return nil, fmt.Errorf("syncstage: 运行密钥长度 %d 字节，期望 %d", len(raw), runKeyBytes)
	}
	return raw, nil
}

// ID 返回运行标识（task_id）。
func (r *Run) ID() string { return r.id }

// Dir 返回运行暂存目录绝对路径。
func (r *Run) Dir() string { return r.dir }

// DlDir 返回运行下载暂存子目录绝对路径（downloads/，票 #63）。不创建：
// 下载引擎 Fetch 首次使用时自建；目录不在暂存副本/证明枚举与恢复裁决面内，
// 崩溃后随运行目录按 ADR-0004 恢复矩阵处置。
func (r *Run) DlDir() string { return filepath.Join(r.dir, dlDirName) }

// TempRelFor 返回目标 root-relative 路径对应的暂存副本相对路径
// （files/<target_rel>，斜杠形态）；该值即 journal 的 temp_relative_path。
// 目标路径非法（逃逸）时返回 ErrPathEscape。
func (r *Run) TempRelFor(targetRel string) (string, error) {
	return StagedRel(targetRel)
}

// StagedRel 返回目标 root-relative 路径对应的暂存副本相对路径
// （files/<target_rel>，斜杠形态；TempRelFor 的无句柄形态）。供不开运行句柄的
// 调用方按同一形状计算暂存路径（restore 执行器消费计划暂存锚上的用户补全
// 字节，票 #60）；目标路径非法（逃逸）时返回 ErrPathEscape。
func StagedRel(targetRel string) (string, error) {
	clean, err := normalizeRelative(targetRel)
	if err != nil {
		return "", err
	}
	return stagedDir + "/" + clean, nil
}

// StageAbs 把暂存副本相对路径（journal 的 temp_relative_path）安全解析为
// 运行目录内绝对路径；逃逸出运行目录的路径返回 ErrPathEscape。
// 供恢复探测按 journal 引用定位 staged 内容。
func (r *Run) StageAbs(stagingRel string) (string, error) {
	clean, err := normalizeRelative(stagingRel)
	if err != nil {
		return "", err
	}
	if clean != stagedDir && !strings.HasPrefix(clean, stagedDir+"/") {
		return "", fmt.Errorf("%w: %q 不在 %s/ 下", ErrPathEscape, stagingRel, stagedDir)
	}
	abs := filepath.Join(r.dir, filepath.FromSlash(clean))
	if !withinRoot(r.dir, abs) {
		return "", fmt.Errorf("%w: %q 解析到 %s", ErrPathEscape, stagingRel, abs)
	}
	return abs, nil
}

// StageContent 把 after 内容流式写入暂存副本（原子写）并复核 digest：
// 落盘后重算 sha256，与 wantDigest 不符即删除副本并返回 ErrDigestMismatch，
// 不留半成品证据。返回 journal 可引用的暂存相对路径。
//
// 复用短路（P2-T14）：暂存副本已存在且 digest 复核相符时直接返回既有路径，
// 不重写（applying 期动作原语对 staging 期已暂存的同一内容重放本方法——重写
// 是逐文件 fsync 的纯浪费；幂等重放语义逐字节不变）。digest 复核照常执行：
// 短路省的是写，不是校验。
func (r *Run) StageContent(targetRel string, content io.Reader, wantDigest string) (string, error) {
	if content == nil {
		return "", errors.New("syncstage: 暂存内容不能为空")
	}
	if err := validateDigestHex(wantDigest); err != nil {
		return "", fmt.Errorf("syncstage: wantDigest 非法: %w", err)
	}
	stagingRel, err := r.TempRelFor(targetRel)
	if err != nil {
		return "", err
	}
	abs, err := r.StageAbs(stagingRel)
	if err != nil {
		return "", err
	}
	// 复用短路：既有副本复核相符即复用（原子 rename 保证存在的副本必然完整）。
	if existing, statErr := os.Lstat(abs); statErr == nil && existing.Mode().IsRegular() {
		if got, _, hashErr := hashFilePath(abs); hashErr == nil && got == wantDigest {
			return stagingRel, nil
		}
	}
	if err := writeFileAtomic(abs, content); err != nil {
		return "", err
	}
	got, _, err := hashFilePath(abs)
	if err != nil {
		return "", err
	}
	if got != wantDigest {
		os.Remove(abs) // 复核失败即删，不留可疑证据
		return "", fmt.Errorf("%w: staged %s got=%s want=%s", ErrDigestMismatch, stagingRel, got, wantDigest)
	}
	return stagingRel, nil
}

// hashFilePath 计算路径内容的 sha256（hex）。
func hashFilePath(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("syncstage: 打开 %s: %w", path, err)
	}
	defer f.Close()
	return hashReader(f)
}

// RunExists 报告 task_id 的暂存运行目录是否已存在（只读探测，不创建布局、
// 不生成密钥）。restore 计划的就绪面投影（票 #59）据此避免读路径产生
// staging 目录副作用；写路径仍走 OpenRun。
func RunExists(stagingRoot, taskID string) bool {
	if stagingRoot == "" || validateID(taskID) != nil {
		return false
	}
	_, err := os.Lstat(filepath.Join(stagingRoot, taskID))
	return err == nil
}

// StagedDigest 返回目标路径暂存副本的 sha256（hex）。副本不存在返回 ok=false
//（正常情况：尚未补全）；路径逃逸返回 ErrPathEscape。只读，不写任何状态——
// restore 计划的 staged 就绪面投影（票 #59）按此实时推导，不改计划行。
func (r *Run) StagedDigest(targetRel string) (digest string, ok bool, err error) {
	stagingRel, err := r.TempRelFor(targetRel)
	if err != nil {
		return "", false, err
	}
	abs, err := r.StageAbs(stagingRel)
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Lstat(abs); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("syncstage: 检查暂存副本失败: %w", statErr)
	}
	got, _, err := hashFilePath(abs)
	if err != nil {
		return "", false, err
	}
	return got, true, nil
}

// ListStagedFiles 枚举运行目录下全部暂存副本（按 StagingRel 排序），
// 供恢复探测盘点 staging 完整性与提交后清理。
func (r *Run) ListStagedFiles() ([]StagedFile, error) {
	root := filepath.Join(r.dir, stagedDir)
	var out []StagedFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(r.dir, path)
		if relErr != nil {
			return relErr
		}
		st, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		stagingRel := filepath.ToSlash(rel)
		out = append(out, StagedFile{
			StagingRel: stagingRel,
			TargetRel:  strings.TrimPrefix(stagingRel, stagedDir+"/"),
			Size:       st.Size(),
		})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("syncstage: 枚举暂存副本失败: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StagingRel < out[j].StagingRel })
	return out, nil
}

// CleanupRun 清理 task_id 的整个运行暂存目录（ADR-0004 §5：提交事务成功后
// 按所有权证明执行且可重试）。目录不存在视为已清理（幂等成功）。
// 仅删除 <stagingRoot>/<task_id> 子树，不触碰其他运行。
func CleanupRun(stagingRoot, taskID string) error {
	if stagingRoot == "" {
		return errors.New("syncstage: stagingRoot 不能为空")
	}
	if err := validateID(taskID); err != nil {
		return fmt.Errorf("syncstage: task_id 非法: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(stagingRoot, taskID)); err != nil {
		return fmt.Errorf("syncstage: 清理运行暂存失败: %w", err)
	}
	return nil
}

// Remove 清理本运行的暂存目录（CleanupRun 的句柄形态，语义相同）。
func (r *Run) Remove() error {
	if err := os.RemoveAll(r.dir); err != nil {
		return fmt.Errorf("syncstage: 清理运行暂存失败: %w", err)
	}
	return nil
}
