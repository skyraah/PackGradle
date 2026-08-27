package model

// MappingRule 定义一条受管范围映射规则（架构文档 §3.4）。
type MappingRule struct {
	ID                 string   `json:"id"`
	ResourceKind       string   `json:"resource_kind"`
	ProjectPrefix      string   `json:"project_prefix"`
	RuntimePrefix      string   `json:"runtime_prefix"`
	Include            []string `json:"include,omitempty"`
	Exclude            []string `json:"exclude,omitempty"`
	Direction          string   `json:"direction"`       // bidirectional | project_to_runtime | runtime_to_project | ignore
	Materialization    string   `json:"materialization"` // copy by default
	MergePolicy        string   `json:"merge_policy"`    // manual | text_diff3 | toml_semantic | packwiz
	RuntimeLocalPolicy string   `json:"runtime_local"`   // exclude | report
}

// MappingPolicy 是 Relation 的版本化受管范围声明。
// 修改会递增 Relation revision，使旧 Plan 立即 stale。
type MappingPolicy struct {
	SchemaVersion int           `json:"schema_version"`
	PolicyID      string        `json:"policy_id"`
	Revision      int           `json:"revision"`
	Rules         []MappingRule `json:"rules"`
}
