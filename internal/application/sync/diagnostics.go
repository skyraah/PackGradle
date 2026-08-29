package sync

import (
	"context"
	"errors"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
	"packgradle/internal/errs"
)

// GetSnapshotDiagnostics 返回快照持久化的诊断列表（diag.mapping.collision、
// diag.scan.* 等；诊断是证据性数据，不参与 SnapshotDigest）。
// relationID 用于跨 Relation 防护：快照不属该关系时按 not found 处理，
// 不泄漏其它关系的快照存在性。
func (a *App) GetSnapshotDiagnostics(ctx context.Context, relationID, snapshotID string) ([]model.Diagnostic, error) {
	snap, err := a.deps.Snapshots.Get(ctx, snapshotID)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return nil, errs.New(CodeSyncSnapshotNotFound, snapshotID)
		}
		return nil, err
	}
	if snap.RelationID != relationID {
		return nil, errs.New(CodeSyncSnapshotNotFound, snapshotID)
	}
	if snap.Diagnostics == nil {
		return []model.Diagnostic{}, nil
	}
	return snap.Diagnostics, nil
}
