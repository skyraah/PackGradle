package model

import "encoding/json"

// Apply 执行的领域类型（ADR-0004：apply_runs 运行头、operation_journal 逐操作行、
// operation_journal_events 追加历史三层事实模型；schema v5 落列，票 #34）。

// ApplyRunState 是 Apply 运行的六阶段（ADR-0004 §1/§5）。
const (
	ApplyRunPrepared         = "prepared"
	ApplyRunStaged           = "staged"
	ApplyRunApplying         = "applying"
	ApplyRunVerifying        = "verifying"
	ApplyRunCommitted        = "committed"
	ApplyRunRecoveryRequired = "recovery_required"
)

// ApplyRunTransitions 是运行六阶段的合法迁移表：成功路径固定
// prepared→staged→applying→verifying→committed（ADR-0004 §5）；任一阶段失败
// 进入 recovery_required；committed 与 recovery_required 为终态（人工确认只收口
// 恢复语义，不再推进运行）。
var ApplyRunTransitions = map[string][]string{
	ApplyRunPrepared:         {ApplyRunStaged, ApplyRunRecoveryRequired},
	ApplyRunStaged:           {ApplyRunApplying, ApplyRunRecoveryRequired},
	ApplyRunApplying:         {ApplyRunVerifying, ApplyRunRecoveryRequired},
	ApplyRunVerifying:        {ApplyRunCommitted, ApplyRunRecoveryRequired},
	ApplyRunCommitted:        {},
	ApplyRunRecoveryRequired: {},
}

// 逐操作状态（ADR-0004 §2 单调路径）。
const (
	OperationStatusPending     = "pending"
	OperationStatusRunning     = "running"
	OperationStatusApplied     = "applied"
	OperationStatusVerified    = "verified"
	OperationStatusFailed      = "failed"
	OperationStatusCompensated = "compensated"
)

// OperationStatusTransitions 是逐操作状态的合法迁移表：
// pending→running→applied→verified 单调主路径；失败分支 failed；
// 补偿完成分支 compensated；verified/compensated 为终态
// （已达成 verified 的操作重复执行不得改变结果，ADR-0004 §2）。
var OperationStatusTransitions = map[string][]string{
	OperationStatusPending:     {OperationStatusRunning, OperationStatusFailed},
	OperationStatusRunning:     {OperationStatusApplied, OperationStatusFailed},
	OperationStatusApplied:     {OperationStatusVerified, OperationStatusFailed},
	OperationStatusVerified:    {},
	OperationStatusFailed:      {OperationStatusCompensated},
	OperationStatusCompensated: {},
}

// transitionAllowed 查迁移表判定 from→to；未知 from（不该出现）一律拒绝。
func transitionAllowed(table map[string][]string, from, to string) bool {
	for _, next := range table[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ApplyRunCanTransition 报告运行阶段 from→to 是否为合法迁移。
func ApplyRunCanTransition(from, to string) bool {
	return transitionAllowed(ApplyRunTransitions, from, to)
}

// OperationCanTransition 报告操作状态 from→to 是否为合法迁移。
func OperationCanTransition(from, to string) bool {
	return transitionAllowed(OperationStatusTransitions, from, to)
}

// ApplyRun 是一次 Apply 的运行头（apply_runs，一行 = 一次 Apply，schema v5；
// DDL 照 ADR-0004 §1 原文）。TaskID 是主键，即投影层的 run_id。
type ApplyRun struct {
	TaskID           string `json:"task_id"`
	RelationID       string `json:"relation_id"`
	PlanID           string `json:"plan_id"`
	PlanDigest       string `json:"plan_digest"`
	RelationRevision int    `json:"relation_revision"`
	State            string `json:"state"`
	// Preconditions 是 Apply 前必须仍成立的前置条件集合。
	Preconditions []Precondition `json:"preconditions"`
	// RecoveryRefs 是恢复对象引用（CAS/staging 引用集合）。JSON 形状由 Apply
	// 引擎（staging 原语票）定义，仓储层原样保存；空值归一为 []。
	RecoveryRefs   json.RawMessage `json:"recovery_refs,omitempty"`
	OperationCount int             `json:"operation_count"`
	StagingCleared bool            `json:"staging_cleared"`
	AcknowledgedAt string          `json:"acknowledged_at,omitempty"` // 人工确认时间（恢复收口后）
	CommitID       string          `json:"commit_id,omitempty"`       // committed 后回填
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

// JournalOperation 是 operation_journal 的单行（逐操作当前投影，ADR-0004 §2 第 2 层）。
// RecoveryRef/OwnershipProof/Operation/Result 为引擎定义形状的 JSON，仓储层原样保存；
// 当前行是查询投影，不回退、不覆盖已发生的历史。
type JournalOperation struct {
	TaskID             string          `json:"task_id"`
	OperationID        string          `json:"operation_id"`
	Ordinal            int             `json:"ordinal"`
	Status             string          `json:"status"`
	TargetRelativePath string          `json:"target_relative_path"`
	BeforeDigest       string          `json:"before_digest,omitempty"`
	AfterDigest        string          `json:"after_digest,omitempty"`
	TempRelativePath   string          `json:"temp_relative_path,omitempty"`
	RecoveryRef        json.RawMessage `json:"recovery_ref,omitempty"`
	OwnershipProof     json.RawMessage `json:"ownership_proof,omitempty"`
	Operation          json.RawMessage `json:"operation,omitempty"`
	Result             json.RawMessage `json:"result,omitempty"`
}

// JournalEvent 是 operation_journal_events 的一行（追加历史，ADR-0004 §2 第 3 层）。
// Seq 在任务内单调递增；「最后一个已持久化意图」= 该任务 seq 最大的一行。
// 只追加：仓储层不提供改删历史的方法，库层触发器拒绝 UPDATE/DELETE。
type JournalEvent struct {
	TaskID      string          `json:"task_id"`
	Seq         int             `json:"seq"`
	OperationID string          `json:"operation_id"`
	FromStatus  string          `json:"from_status"` // 初始意图持久化时为空串
	ToStatus    string          `json:"to_status"`
	OccurredAt  string          `json:"occurred_at"`
	Detail      json.RawMessage `json:"detail,omitempty"` // 恢复解释/审计证据，引擎定义形状
}

// SyncCommit 是一次成功 Apply 的提交事实（sync_commits，契约 05 §7 零消费表收口）。
type SyncCommit struct {
	CommitID                  string          `json:"commit_id"`
	RelationID                string          `json:"relation_id"`
	ParentCommitID            string          `json:"parent_commit_id,omitempty"`
	CreatedAt                 string          `json:"created_at"`
	PlanID                    string          `json:"plan_id"`
	VerifiedProjectSnapshotID string          `json:"verified_project_snapshot_id"`
	VerifiedRuntimeSnapshotID string          `json:"verified_runtime_snapshot_id"`
	PreviousBaselineID        string          `json:"previous_baseline_id,omitempty"`
	ResultBaselineID          string          `json:"result_baseline_id"`
	CommitKind                string          `json:"commit_kind"`  // initialize | sync | restore
	Completeness              string          `json:"completeness"` // exact | partial
	RemainingChangeCount      int             `json:"remaining_change_count"`
	Summary                   json.RawMessage `json:"summary,omitempty"` // 摘要 JSON，引擎定义形状
	// Changes 是逐资源变化行；仅 GetForRelation 装载（ListByRelation 只投影提交头）。
	Changes []CommitChange `json:"changes"`
}

// CommitChange 是提交内的单资源变化（commit_changes）。Identity 联
// resource_representations（verified 快照）读取，供 GetCommit 投影资源身份。
type CommitChange struct {
	ResourceID    ResourceID      `json:"resource_id"`
	ChangeKind    string          `json:"change_kind"`
	Identity      Identity        `json:"identity"`
	ProjectBefore *Representation `json:"project_before,omitempty"`
	ProjectAfter  *Representation `json:"project_after,omitempty"`
	RuntimeBefore *Representation `json:"runtime_before,omitempty"`
	RuntimeAfter  *Representation `json:"runtime_after,omitempty"`
}

// PlanConfirmation 是一次计划确认（plan_confirmations，ConfirmPlan 幂等键，
// 契约 05 §7 收口）。ConsumedAt 为令牌消费标记（schema v5 增列）。
type PlanConfirmation struct {
	PlanID            string          `json:"plan_id"`
	PlanDigest        string          `json:"plan_digest"`
	ConfirmationToken string          `json:"confirmation_token"`
	RelationRevision  int             `json:"relation_revision"`
	Acknowledgements  json.RawMessage `json:"acknowledgements,omitempty"` // 确认项 JSON，引擎定义形状
	ConfirmedAt       string          `json:"confirmed_at"`
	ExpiresAt         string          `json:"expires_at"`
	ConsumedAt        string          `json:"consumed_at,omitempty"`
}
