package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

// EndpointRepository 是 ports.EndpointRepository 的 SQLite 实现（projects / runtimes 表）。
type EndpointRepository struct {
	db *sql.DB
}

// 编译期接口断言。
var _ ports.EndpointRepository = (*EndpointRepository)(nil)

// NewEndpointRepository 创建共享 *sql.DB 的端点仓库。
func NewEndpointRepository(db *sql.DB) *EndpointRepository {
	return &EndpointRepository{db: db}
}

// CreateProject 登记项目端点；重复登记（同 id 或同 UNIQUE(adapter, root_path)）返回 ErrDuplicate。
func (r *EndpointRepository) CreateProject(ctx context.Context, p model.Project) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO projects(id, adapter, display_name, root_path, binding_fingerprint, created_at)
VALUES(?,?,?,?,?,?)`,
		p.ProjectID, p.Adapter, p.DisplayName, p.RootPath, p.BindingFingerprint, p.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("sqlite: 创建 Project %s: %w", p.ProjectID, ErrDuplicate)
		}
		return fmt.Errorf("sqlite: 创建 Project %s: %w", p.ProjectID, err)
	}
	return nil
}

// GetProject 按 id 读取项目；不存在返回 ErrNotFound。
func (r *EndpointRepository) GetProject(ctx context.Context, id string) (model.Project, error) {
	var p model.Project
	err := r.db.QueryRowContext(ctx, `
SELECT id, adapter, display_name, root_path, binding_fingerprint, created_at
FROM projects WHERE id=?`, id).
		Scan(&p.ProjectID, &p.Adapter, &p.DisplayName, &p.RootPath, &p.BindingFingerprint, &p.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return model.Project{}, fmt.Errorf("sqlite: 读取 Project %s: %w", id, ErrNotFound)
		}
		return model.Project{}, fmt.Errorf("sqlite: 读取 Project %s: %w", id, err)
	}
	p.SchemaVersion = model.CurrentSchemaVersion
	return p, nil
}

// FindProjectByRoot 按 binding_fingerprint 精确匹配查找项目（幂等重登记场景）。
func (r *EndpointRepository) FindProjectByRoot(ctx context.Context, fingerprint string) (model.Project, bool, error) {
	var p model.Project
	err := r.db.QueryRowContext(ctx, `
SELECT id, adapter, display_name, root_path, binding_fingerprint, created_at
FROM projects WHERE binding_fingerprint=?`, fingerprint).
		Scan(&p.ProjectID, &p.Adapter, &p.DisplayName, &p.RootPath, &p.BindingFingerprint, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return model.Project{}, false, nil
	}
	if err != nil {
		return model.Project{}, false, fmt.Errorf("sqlite: 按 fingerprint 查找 Project: %w", err)
	}
	p.SchemaVersion = model.CurrentSchemaVersion
	return p, true, nil
}

// CreateRuntime 登记运行时端点；重复登记返回 ErrDuplicate。
func (r *EndpointRepository) CreateRuntime(ctx context.Context, rt model.Runtime) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO runtimes(id, adapter, display_name, root_path, adapter_identity, binding_fingerprint, created_at)
VALUES(?,?,?,?,?,?,?)`,
		rt.RuntimeID, rt.Adapter, rt.DisplayName, rt.RootPath, rt.AdapterIdentity, rt.BindingFingerprint, rt.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("sqlite: 创建 Runtime %s: %w", rt.RuntimeID, ErrDuplicate)
		}
		return fmt.Errorf("sqlite: 创建 Runtime %s: %w", rt.RuntimeID, err)
	}
	return nil
}

// GetRuntime 按 id 读取运行时；不存在返回 ErrNotFound。
func (r *EndpointRepository) GetRuntime(ctx context.Context, id string) (model.Runtime, error) {
	var rt model.Runtime
	err := r.db.QueryRowContext(ctx, `
SELECT id, adapter, display_name, root_path, adapter_identity, binding_fingerprint, created_at
FROM runtimes WHERE id=?`, id).
		Scan(&rt.RuntimeID, &rt.Adapter, &rt.DisplayName, &rt.RootPath, &rt.AdapterIdentity, &rt.BindingFingerprint, &rt.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return model.Runtime{}, fmt.Errorf("sqlite: 读取 Runtime %s: %w", id, ErrNotFound)
		}
		return model.Runtime{}, fmt.Errorf("sqlite: 读取 Runtime %s: %w", id, err)
	}
	rt.SchemaVersion = model.CurrentSchemaVersion
	return rt, nil
}

// FindRuntimeByIdentity 按 adapter + adapter_identity（如 Prism 实例目录名）查找运行时。
func (r *EndpointRepository) FindRuntimeByIdentity(ctx context.Context, adapter, adapterIdentity string) (model.Runtime, bool, error) {
	var rt model.Runtime
	err := r.db.QueryRowContext(ctx, `
SELECT id, adapter, display_name, root_path, adapter_identity, binding_fingerprint, created_at
FROM runtimes WHERE adapter=? AND adapter_identity=?`, adapter, adapterIdentity).
		Scan(&rt.RuntimeID, &rt.Adapter, &rt.DisplayName, &rt.RootPath, &rt.AdapterIdentity, &rt.BindingFingerprint, &rt.CreatedAt)
	if err == sql.ErrNoRows {
		return model.Runtime{}, false, nil
	}
	if err != nil {
		return model.Runtime{}, false, fmt.Errorf("sqlite: 按 identity 查找 Runtime: %w", err)
	}
	rt.SchemaVersion = model.CurrentSchemaVersion
	return rt, true, nil
}
