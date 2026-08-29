package sqlite

// RunInTx 单事务边界测试（ADR-0003）：闭包内多仓库写入随事务提交或回滚；
// 事务域仓库的内部事务方法（SavePolicy / Snapshot Insert 等自开事务的路径）
// 必须加入外层事务而不是嵌套开新事务。

import (
	"context"
	"errors"
	"testing"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/core/model"
)

func TestRunInTxCommitAndRollback(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uow := NewUnitOfWork(db)
	now := time.Now().UTC().Format(time.RFC3339)

	newProject := func(id string) model.Project {
		return model.Project{
			SchemaVersion: model.CurrentSchemaVersion, ProjectID: id,
			Adapter: "packwiz", DisplayName: "P " + id,
			RootPath: "D:/packs/" + id, BindingFingerprint: "sha256:" + id, CreatedAt: now,
		}
	}
	prep := model.RelationPreparation{
		SchemaVersion: model.CurrentSchemaVersion, PreparationID: "prep_tx",
		CreatedAt: now, ExpiresAt: "2999-01-01T00:00:00Z",
		Input: model.PrepareRelationInput{ProjectRoot: "D:/packs/x", RuntimeInstanceDir: "D:/inst/x"},
	}
	if err := NewPreparationRepository(db).Insert(ctx, prep); err != nil {
		t.Fatal(err)
	}

	commitFn := func(repos ports.Repos) error {
		if err := repos.Endpoints.CreateProject(ctx, newProject("prj_tx")); err != nil {
			return err
		}
		if err := repos.Preparations.MarkConsumed(ctx, "prep_tx"); err != nil {
			return err
		}
		return nil
	}
	if err := uow.RunInTx(ctx, commitFn); err != nil {
		t.Fatalf("RunInTx 提交失败: %v", err)
	}
	if _, err := NewEndpointRepository(db).GetProject(ctx, "prj_tx"); err != nil {
		t.Fatalf("提交后应可读到事务内写入: %v", err)
	}

	// 失败闭包：先写入再返回错误 → 全部回滚（含已存在的 prep_tx 消费态不受影响）
	rollbackFn := func(repos ports.Repos) error {
		if err := repos.Endpoints.CreateProject(ctx, newProject("prj_rollback")); err != nil {
			return err
		}
		return errors.New("注入失败")
	}
	if err := uow.RunInTx(ctx, rollbackFn); err == nil {
		t.Fatal("期望闭包错误外抛")
	}
	if _, err := NewEndpointRepository(db).GetProject(ctx, "prj_rollback"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("闭包失败后写入应回滚, got %v", err)
	}
}

// TestTxBoundReposJoinOuterTx 验证事务域仓库的内部事务方法加入外层事务：
// SavePolicy / SnapshotRepository.Insert 在闭包内的写入随外层回滚而消失。
func TestTxBoundReposJoinOuterTx(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uow := NewUnitOfWork(db)
	relationID := fixtureRelation(t, db, "tx")

	snap := model.ObservedSnapshot{
		SchemaVersion: model.CurrentSchemaVersion, SnapshotID: "snap_tx",
		RelationID: relationID, Side: model.SideProject,
		Scanner: model.ScannerInfo{Name: "t", Version: "0"}, CapturedAt: "2026-08-29T00:00:00Z",
		SnapshotDigest: "sha256:snap", NormalizationVersion: model.CurrentSchemaVersion,
	}
	pol := model.MappingPolicy{
		SchemaVersion: model.CurrentSchemaVersion, PolicyID: "pol_tx", Revision: 1,
		Rules: []model.MappingRule{{ID: "mods", ResourceKind: "mod", Direction: "bidirectional",
			Materialization: "copy", MergePolicy: "packwiz", RuntimeLocalPolicy: "exclude"}},
	}
	inner := func(repos ports.Repos) error {
		if err := repos.Mappings.SavePolicy(ctx, relationID, pol); err != nil {
			return err
		}
		return repos.Snapshots.Insert(ctx, snap)
	}
	if err := uow.RunInTx(ctx, func(repos ports.Repos) error {
		if err := inner(repos); err != nil {
			return err
		}
		return errors.New("注入失败")
	}); err == nil {
		t.Fatal("期望闭包错误外抛")
	}
	if _, err := NewMappingRepository(db).GetPolicy(ctx, relationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SavePolicy 应回滚（未落任何策略行）, got %v", err)
	}
	if _, err := NewSnapshotRepository(db).Get(ctx, "snap_tx"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Snapshot Insert 应回滚, got %v", err)
	}
	// SavePolicy 的联动修订同样回滚：fixture 出生 revision 仍为原值
	rel, err := NewRelationRepository(db).Get(ctx, relationID)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Revision != 1 {
		t.Fatalf("联动修订应回滚, revision = %d", rel.Revision)
	}
}
