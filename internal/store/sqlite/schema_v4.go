package sqlite

// schemaV4 增补 rebind_preparations 表（T12，票 #22）：重绑预检的 Prepare/Apply
// 两段式中间状态。与创建预检（preparations）分表：输入形状（单侧 + 关系引用 +
// 影响计数）与消费语义不同，新端点草稿按 side 二选一存 new_endpoint_json。
// 无既有数据，无回填需求。
const schemaV4 = `
CREATE TABLE rebind_preparations (
	preparation_id TEXT PRIMARY KEY,
	relation_id TEXT NOT NULL REFERENCES relations(id),
	side TEXT NOT NULL CHECK(side IN ('project','runtime')),
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	consumed_at TEXT NULL,
	input_root_path TEXT NOT NULL,
	new_endpoint_json TEXT NULL,
	fingerprint_changed INTEGER NOT NULL CHECK(fingerprint_changed IN (0,1)),
	baseline_inheritance TEXT NOT NULL DEFAULT 'reinitialize' CHECK(baseline_inheritance IN ('reinitialize','inherit')),
	invalidated_plan_count INTEGER NOT NULL DEFAULT 0,
	checks_json TEXT NOT NULL
);
`
