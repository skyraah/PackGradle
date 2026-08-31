package syncstage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"packgradle/internal/core/model"
)

// before-content CAS 保全（ADR-0004 §3）：进入 staged 前，所有将被覆盖或
// 删除、且 recoverability policy 要求保留的旧内容，必须已写入 CAS 并完成
// hash 复核；对象只有在完整落盘并校验后，上层才能在 SQLite 中建立引用
// （journal 的 recovery_ref_json / object_refs）。SQLite 事务不进入本层。

// ContentStore 是 before-content 保全所需的最小 CAS 能力接口
// （由 internal/store/objectstore.CAS 满足）。本包不触碰 SQLite——
// Put 的 objects 行登记属于 objectstore 内部事务，引用落库由上层完成。
type ContentStore interface {
	// Put 流式写入内容，返回内容引用（含 sha256 与字节数）。
	Put(ctx context.Context, r io.Reader) (model.ContentRef, error)
	// Open 按 digest 打开已落盘对象内容流（独立复核用）。
	Open(ctx context.Context, digest string) (io.ReadCloser, error)
}

// RequiresCASBackup 报告 recoverability policy 是否要求在覆盖/删除前把旧内容
// 保全进 CAS（model.Recoverability 先例，ADR-0004 §3）：
//
//   - cas：恢复路径就是 CAS，必须保全；
//   - user_object：用户自有对象，不可再生成，必须保全；
//   - unrecoverable：无任何恢复途径，必须保全（否则回滚即丢失）；
//   - none：策略显式声明无恢复概念（如派生内容），不保全；
//   - redownload：可从提供方重新下载，不保全（CAS 空间留给不可再生内容）；
//   - 未知值（含空值）：按需要保全处理（fail-safe，宁可多存不留缺口的恢复）。
func RequiresCASBackup(rec model.Recoverability) bool {
	switch rec {
	case model.RecoverabilityNone, model.RecoverabilityRedownload:
		return false
	default:
		return true
	}
}

// PreserveBeforeContent 把 absPath 的旧内容流式保全进 CAS 并独立复核：
// Put 落盘（临时文件 + fsync + 原子 rename）后，重新 Open 对象文件流式重算
// sha256，digest 与 size 全部吻合才返回引用；任一环节失败即返回错误且
// 不返回引用——调用方不得据此建立 journal/object_refs 引用（复核失败不留引用）。
//
// 返回 preserved=false 表示本次无需保全或无可保全内容：
//   - recoverability policy 不要求（RequiresCASBackup=false）；
//   - 目标不存在（覆盖前已缺失，属幂等重放场景，不是错误）。
//
// 目标存在但不是普通文件（目录/symlink/junction）时返回 ErrTargetNotFile。
func PreserveBeforeContent(ctx context.Context, store ContentStore, absPath string, rec model.Recoverability) (model.ContentRef, bool, error) {
	if !RequiresCASBackup(rec) {
		return model.ContentRef{}, false, nil
	}
	st, err := os.Lstat(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return model.ContentRef{}, false, nil
		}
		return model.ContentRef{}, false, fmt.Errorf("syncstage: stat %s 失败: %w", absPath, err)
	}
	if !st.Mode().IsRegular() || st.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return model.ContentRef{}, false, fmt.Errorf("syncstage: %s: %w", absPath, ErrTargetNotFile)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return model.ContentRef{}, false, fmt.Errorf("syncstage: 打开 %s 失败: %w", absPath, err)
	}
	defer f.Close()
	ref, err := store.Put(ctx, f)
	if err != nil {
		return model.ContentRef{}, false, fmt.Errorf("syncstage: CAS 保全 %s 失败: %w", absPath, err)
	}

	// 独立复核：重新打开已落盘对象，重算 sha256 与字节数。
	// 复核失败即错、不返回引用（ Put 返回的 digest 只是写入时流的宣称值）。
	rc, err := store.Open(ctx, ref.Digest)
	if err != nil {
		return model.ContentRef{}, false, fmt.Errorf("syncstage: 复核 CAS 对象 %s 失败: %w", ref.Digest, err)
	}
	defer rc.Close()
	gotDigest, gotSize, err := hashReader(rc)
	if err != nil {
		return model.ContentRef{}, false, err
	}
	if gotDigest != ref.Digest || gotSize != ref.Size {
		return model.ContentRef{}, false, fmt.Errorf(
			"%w: CAS 对象落盘复核不符 got=(%s,%d) want=(%s,%d)",
			ErrDigestMismatch, gotDigest, gotSize, ref.Digest, ref.Size)
	}
	return ref, true, nil
}
