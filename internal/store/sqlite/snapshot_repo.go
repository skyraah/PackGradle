package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// SnapshotRepository 是 ports.SnapshotRepository 的 SQLite 实现
// （observed_snapshots + resource_representations 两表）。
//
// 权威数据流：写入时把每条 ResourceObservation 序列化为 semantic_json 存入
// resource_representations；读取时以 semantic_json 反序列化为准，头表中的
// 冗余列（kind/identity/path/format 等）只用于 SQL 查询与诊断。
type SnapshotRepository struct {
	db *sql.DB
}

var _ ports.SnapshotRepository = (*SnapshotRepository)(nil)

// NewSnapshotRepository 创建共享 *sql.DB 的快照仓库。
func NewSnapshotRepository(db *sql.DB) *SnapshotRepository {
	return &SnapshotRepository{db: db}
}

// Insert 在同一事务写 observed_snapshots 头表与全部 resource_representations 行。
// relation 不存在时由外键约束报错，转换为 ErrRelationNotFound。
func (r *SnapshotRepository) Insert(ctx context.Context, s model.ObservedSnapshot) error {
	diagnosticsJSON, err := json.Marshal(s.Diagnostics)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化快照 %s 诊断: %w", s.SnapshotID, err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: 写入快照 %s 开启事务: %w", s.SnapshotID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO observed_snapshots(id, relation_id, side, binding_fingerprint, scanner_name, scanner_version,
	captured_at, snapshot_digest, normalization_version, policy_digest, resource_count, diagnostics_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.SnapshotID, s.RelationID, s.Side, s.BindingFingerprint,
		s.Scanner.Name, s.Scanner.Version, s.CapturedAt, s.SnapshotDigest,
		s.NormalizationVersion, s.PolicyDigest, len(s.Resources), string(diagnosticsJSON)); err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("sqlite: 写入快照 %s: %w", s.SnapshotID, ErrRelationNotFound)
		}
		return fmt.Errorf("sqlite: 写入快照 %s: %w", s.SnapshotID, err)
	}

	insertRes, err := tx.PrepareContext(ctx, `
INSERT INTO resource_representations(snapshot_id, resource_id, resource_kind,
	identity_provider, identity_key, identity_confidence, policy_id,
	relative_path, format, content_algorithm, content_digest, size, semantic_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("sqlite: 写入快照 %s 资源: %w", s.SnapshotID, err)
	}
	defer insertRes.Close()

	for id, res := range s.Resources {
		semantic, err := json.Marshal(res)
		if err != nil {
			return fmt.Errorf("sqlite: 序列化快照 %s 资源 %s: %w", s.SnapshotID, id, err)
		}
		var contentAlgorithm, contentDigest any
		var size any
		if res.Representation.Content != nil {
			contentAlgorithm = res.Representation.Content.Algorithm
			contentDigest = res.Representation.Content.Digest
			size = res.Representation.Content.Size
		}
		if _, err := insertRes.ExecContext(ctx,
			s.SnapshotID, string(id), string(res.Kind),
			res.Identity.Provider, res.Identity.Key, res.Identity.Confidence,
			res.PolicyID, res.Representation.RelativePath, res.Representation.Format,
			contentAlgorithm, contentDigest, size, string(semantic)); err != nil {
			return fmt.Errorf("sqlite: 写入快照 %s 资源 %s: %w", s.SnapshotID, id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: 写入快照 %s 提交: %w", s.SnapshotID, err)
	}
	return nil
}

// Get 按 id 读取完整快照（含 resources map 与诊断）；不存在返回 ErrNotFound。
func (r *SnapshotRepository) Get(ctx context.Context, id string) (model.ObservedSnapshot, error) {
	return r.queryOne(ctx, "SELECT id FROM observed_snapshots WHERE id=?", id)
}

// GetForRelation 校验快照属于指定 Relation 且 side 匹配；任一不匹配返回 ErrNotFound。
func (r *SnapshotRepository) GetForRelation(ctx context.Context, id, relationID string, side model.Side) (model.ObservedSnapshot, error) {
	return r.queryOne(ctx,
		"SELECT id FROM observed_snapshots WHERE id=? AND relation_id=? AND side=?", id, relationID, side)
}

// LatestByRelationSide 返回指定关系与端的最新快照（captured_at DESC, id DESC 取 1）。
func (r *SnapshotRepository) LatestByRelationSide(ctx context.Context, relationID string, side model.Side) (model.ObservedSnapshot, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `
SELECT id FROM observed_snapshots WHERE relation_id=? AND side=?
ORDER BY captured_at DESC, id DESC LIMIT 1`, relationID, side).Scan(&id)
	if err == sql.ErrNoRows {
		return model.ObservedSnapshot{}, false, nil
	}
	if err != nil {
		return model.ObservedSnapshot{}, false, fmt.Errorf("sqlite: 查询关系 %s %s 侧最新快照: %w", relationID, side, err)
	}
	snap, err := r.queryOne(ctx, "SELECT id FROM observed_snapshots WHERE id=?", id)
	if err != nil {
		return model.ObservedSnapshot{}, false, err
	}
	return snap, true, nil
}

// queryOne 执行定位查询（返回单行 id），再加载头表与全部资源行。
func (r *SnapshotRepository) queryOne(ctx context.Context, locateQuery string, args ...any) (model.ObservedSnapshot, error) {
	var s model.ObservedSnapshot
	err := r.db.QueryRowContext(ctx, locateQuery, args...).Scan(&s.SnapshotID)
	if err == sql.ErrNoRows {
		return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 读取快照: %w", ErrNotFound)
	}
	if err != nil {
		return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 读取快照: %w", err)
	}

	var diagnosticsJSON string
	err = r.db.QueryRowContext(ctx, `
SELECT id, relation_id, side, binding_fingerprint, scanner_name, scanner_version,
	captured_at, snapshot_digest, normalization_version, policy_digest, diagnostics_json
FROM observed_snapshots WHERE id=?`, s.SnapshotID).
		Scan(&s.SnapshotID, &s.RelationID, &s.Side, &s.BindingFingerprint,
			&s.Scanner.Name, &s.Scanner.Version, &s.CapturedAt, &s.SnapshotDigest,
			&s.NormalizationVersion, &s.PolicyDigest, &diagnosticsJSON)
	if err == sql.ErrNoRows {
		return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 读取快照 %s: %w", s.SnapshotID, ErrNotFound)
	}
	if err != nil {
		return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 读取快照 %s: %w", s.SnapshotID, err)
	}
	s.SchemaVersion = model.CurrentSchemaVersion
	if err := json.Unmarshal([]byte(diagnosticsJSON), &s.Diagnostics); err != nil {
		return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 解析快照 %s 诊断: %w", s.SnapshotID, err)
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT semantic_json FROM resource_representations WHERE snapshot_id=? ORDER BY resource_id`, s.SnapshotID)
	if err != nil {
		return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 读取快照 %s 资源: %w", s.SnapshotID, err)
	}
	defer rows.Close()

	s.Resources = make(map[model.ResourceID]model.ResourceObservation)
	for rows.Next() {
		var semantic string
		if err := rows.Scan(&semantic); err != nil {
			return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 读取快照 %s 资源: %w", s.SnapshotID, err)
		}
		var res model.ResourceObservation
		if err := json.Unmarshal([]byte(semantic), &res); err != nil {
			return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 解析快照 %s 资源: %w", s.SnapshotID, err)
		}
		s.Resources[res.ResourceID] = res
	}
	if err := rows.Err(); err != nil {
		return model.ObservedSnapshot{}, fmt.Errorf("sqlite: 读取快照 %s 资源: %w", s.SnapshotID, err)
	}
	return s, nil
}
