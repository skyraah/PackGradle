package syncstage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// 文件动作原语（copy Materializer）：applyCreate/applyModify/applyDelete。
//
// 每个动作都要求有效的所有权证明（先 Verify，后执行）——无证明不动作；
// 动作开始前把证明副本原子写入暂存目录（crash 后恢复探测可凭暂存证据
// 独立复核，不依赖 SQLite 是否赶得上落库）。
//
// 幂等铁律（ADR-0004 §2，redesign §6.6）：对已达成 after digest 的目标重放
// 逐字节不变——已就绪的目标直接返回 already_applied，不重写、不触碰 mtime。
// 现存内容与期望不符（疑似外部修改）一律 ErrTargetModified 拒绝，交由恢复
// 探测裁决，绝不凭「看起来相同」猜测。
//
// 写入路径：内容先经 StageContent 原子落暂存并复核 digest（crash 后幂等
// redo 的证据），再从暂存副本经 writeFileAtomic（临时文件 + fsync + 原子
// rename）落到目标，最后对目标复核 digest。SQLite journal 的逐操作状态推进
// 由上层负责；本层只保证文件事实。

// Outcome 是单次动作的结果类别。
type Outcome string

const (
	// OutcomeApplied 表示动作本次实际执行（或确认完成删除）。
	OutcomeApplied Outcome = "applied"
	// OutcomeAlreadyApplied 表示目标已达成期望状态，重放无操作（幂等）。
	OutcomeAlreadyApplied Outcome = "already_applied"
)

// ApplyResult 是单次文件动作的结果。TempRel/Digest 供上层写 journal 的
// temp_relative_path 与 after_digest；delete 的两者为空。
type ApplyResult struct {
	Outcome   Outcome `json:"outcome"`
	TargetRel string  `json:"target_relative_path"`
	TempRel   string  `json:"temp_relative_path,omitempty"` // 暂存副本相对运行目录路径（斜杠）
	Digest    string  `json:"digest,omitempty"`             // 复核通过的 after digest
}

// Actions 绑定一个 Apply 运行与一个端点 root，提供文件动作原语。
type Actions struct {
	run  *Run
	root string // 端点 canonical root（绝对路径）
}

// NewActions 构造动作执行器。endpointRoot 必须是已存在的目录
// （canonical real root 由上层经端点解析管线解析后传入）。
func NewActions(run *Run, endpointRoot string) (*Actions, error) {
	if run == nil {
		return nil, errors.New("syncstage: 运行句柄不能为空")
	}
	if endpointRoot == "" {
		return nil, errors.New("syncstage: 端点 root 不能为空")
	}
	abs, err := filepath.Abs(endpointRoot)
	if err != nil {
		return nil, fmt.Errorf("syncstage: 端点 root 非法: %w", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("syncstage: 端点 root 不可达: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("syncstage: 端点 root %s 不是目录", abs)
	}
	return &Actions{run: run, root: filepath.Clean(abs)}, nil
}

// Run 返回绑定的运行句柄。
func (a *Actions) Run() *Run { return a.run }

// Root 返回绑定的端点 root。
func (a *Actions) Root() string { return a.root }

// prepare 校验证明属于本运行且动作类别匹配，返回规范化目标路径与绝对路径。
func (a *Actions) prepare(p OwnershipProof, wantCreate, wantDelete bool) (string, string, error) {
	if err := a.run.VerifyOwnershipProof(p); err != nil {
		return "", "", err
	}
	switch {
	case wantCreate && (p.BeforeDigest != "" || p.AfterDigest == ""):
		return "", "", fmt.Errorf("%w: create 证明必须无 before、有 after digest", ErrProofInvalid)
	case wantDelete && p.AfterDigest != "":
		return "", "", fmt.Errorf("%w: delete 证明必须无 after digest", ErrProofInvalid)
	}
	cleanRel, err := normalizeRelative(p.TargetPath)
	if err != nil {
		return "", "", err
	}
	abs, _, err := resolveTarget(a.root, p.TargetPath)
	if err != nil {
		return "", "", err
	}
	return cleanRel, abs, nil
}

// recordProof 把证明副本写入暂存证据（动作执行前落盘；已存在则原子覆盖为
// 同一内容，无副作用）。
func (a *Actions) recordProof(p OwnershipProof) error {
	if err := a.run.SaveProof(p); err != nil {
		return fmt.Errorf("syncstage: 落盘所有权证明失败: %w", err)
	}
	return nil
}

// applyCreate/applyModify 共用的落地管线：暂存 → 原子落地 → 目标复核。
func (a *Actions) materialize(p OwnershipProof, cleanRel, abs string, content io.Reader) (ApplyResult, error) {
	tempRel, err := a.run.StageContent(p.TargetPath, content, p.AfterDigest)
	if err != nil {
		return ApplyResult{}, err
	}
	stagedAbs, err := a.run.StageAbs(tempRel)
	if err != nil {
		return ApplyResult{}, err
	}
	sf, err := os.Open(stagedAbs)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("syncstage: 打开暂存副本失败: %w", err)
	}
	defer sf.Close()
	if err := writeFileAtomic(abs, sf); err != nil {
		return ApplyResult{}, err
	}
	if err := VerifyFileDigest(abs, p.AfterDigest); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		Outcome:   OutcomeApplied,
		TargetRel: cleanRel,
		TempRel:   tempRel,
		Digest:    p.AfterDigest,
	}, nil
}

// ApplyCreate 落地一个创建动作（目标此前不存在）。
//   - 目标已是 after 内容：already_applied，逐字节不变（幂等重放）；
//   - 目标存在但内容不符：ErrTargetModified（create 目标不应已存在，含糊态拒绝）；
//   - 目标是目录/链接：ErrTargetNotFile；
//   - 正常路径：暂存复核 → 原子落地 → 目标复核，returned TempRel 可写 journal。
func (a *Actions) ApplyCreate(p OwnershipProof, content io.Reader) (ApplyResult, error) {
	cleanRel, abs, err := a.prepare(p, true, false)
	if err != nil {
		return ApplyResult{}, err
	}
	if content == nil {
		return ApplyResult{}, errors.New("syncstage: create 内容不能为空")
	}
	if err := a.recordProof(p); err != nil {
		return ApplyResult{}, err
	}
	if _, statErr := os.Lstat(abs); statErr == nil {
		plain, err := isPlainRegularFile(abs)
		if err != nil {
			return ApplyResult{}, err
		}
		if !plain {
			return ApplyResult{}, fmt.Errorf("syncstage: %s: %w", abs, ErrTargetNotFile)
		}
		ref, err := HashFile(abs)
		if err != nil {
			return ApplyResult{}, err
		}
		if ref.Digest == p.AfterDigest {
			return ApplyResult{Outcome: OutcomeAlreadyApplied, TargetRel: cleanRel, Digest: p.AfterDigest}, nil
		}
		return ApplyResult{}, fmt.Errorf("%w: create 目标已存在且内容不符 got=%s", ErrTargetModified, ref.Digest)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ApplyResult{}, fmt.Errorf("syncstage: 检查目标失败: %w", statErr)
	}
	return a.materialize(p, cleanRel, abs, content)
}

// ApplyModify 落地一个覆盖动作（before → after），content 提供 after 内容流
// （CAS 对象流/下载流由上层决定）。
//   - 目标已是 after 内容：already_applied，逐字节不变（幂等重放）；
//   - 目标是 before 内容：暂存复核 → 原子替换 → 目标复核；
//   - 目标内容既非 before 也非 after：ErrTargetModified（外部修改嫌疑，拒绝）；
//   - 目标缺失或不是普通文件：ErrTargetModified / ErrTargetNotFile。
func (a *Actions) ApplyModify(p OwnershipProof, content io.Reader) (ApplyResult, error) {
	cleanRel, abs, err := a.prepare(p, false, false)
	if err != nil {
		return ApplyResult{}, err
	}
	if content == nil {
		return ApplyResult{}, errors.New("syncstage: modify 内容不能为空")
	}
	if err := a.recordProof(p); err != nil {
		return ApplyResult{}, err
	}
	st, statErr := os.Lstat(abs)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return ApplyResult{}, fmt.Errorf("%w: modify 目标缺失（before 声称存在）", ErrTargetModified)
		}
		return ApplyResult{}, fmt.Errorf("syncstage: 检查目标失败: %w", statErr)
	}
	plain := st.Mode().IsRegular() && st.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
	if !plain {
		return ApplyResult{}, fmt.Errorf("syncstage: %s: %w", abs, ErrTargetNotFile)
	}
	ref, err := HashFile(abs)
	if err != nil {
		return ApplyResult{}, err
	}
	switch ref.Digest {
	case p.AfterDigest:
		return ApplyResult{Outcome: OutcomeAlreadyApplied, TargetRel: cleanRel, Digest: p.AfterDigest}, nil
	case p.BeforeDigest:
		return a.materialize(p, cleanRel, abs, content)
	default:
		return ApplyResult{}, fmt.Errorf("%w: modify 目标既非 before 也非 after got=%s", ErrTargetModified, ref.Digest)
	}
}

// ApplyDelete 落地一个删除动作。
//   - 目标已不存在：already_applied（幂等重放：重复恢复不二次删除）；
//   - 目标是普通文件且（证明带 before digest 时）内容与 before 一致：删除；
//   - 目标内容与 before 不符：ErrTargetModified（外部修改嫌疑，拒绝删除）；
//   - 目标是目录/链接：ErrTargetNotFile（不删目录、不经链接删除）。
//
// 旧内容的 CAS 保全由上层在调用前经 PreserveBeforeContent 完成（ADR-0004
// §3：引用前完成落盘与复核）；本原语不负责保全。
func (a *Actions) ApplyDelete(p OwnershipProof) (ApplyResult, error) {
	cleanRel, abs, err := a.prepare(p, false, true)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := a.recordProof(p); err != nil {
		return ApplyResult{}, err
	}
	st, statErr := os.Lstat(abs)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return ApplyResult{Outcome: OutcomeAlreadyApplied, TargetRel: cleanRel}, nil
		}
		return ApplyResult{}, fmt.Errorf("syncstage: 检查目标失败: %w", statErr)
	}
	plain := st.Mode().IsRegular() && st.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
	if !plain {
		return ApplyResult{}, fmt.Errorf("syncstage: %s: %w", abs, ErrTargetNotFile)
	}
	if p.BeforeDigest != "" {
		ref, err := HashFile(abs)
		if err != nil {
			return ApplyResult{}, err
		}
		if ref.Digest != p.BeforeDigest {
			return ApplyResult{}, fmt.Errorf("%w: delete 目标内容不符 got=%s want=%s", ErrTargetModified, ref.Digest, p.BeforeDigest)
		}
	}
	if err := os.Remove(abs); err != nil {
		return ApplyResult{}, fmt.Errorf("syncstage: 删除 %s 失败: %w", abs, err)
	}
	if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return ApplyResult{}, fmt.Errorf("syncstage: 复核删除失败: %w", err)
		}
		return ApplyResult{}, fmt.Errorf("%w: 删除后目标仍存在 %s", ErrDigestMismatch, abs)
	}
	return ApplyResult{Outcome: OutcomeApplied, TargetRel: cleanRel}, nil
}
