package sqlite

// schemaV3 是 v3 迁移 DDL（契约 03 §2.6，票 #21）：sync_plans 增列 requested_exactness。
// 既有行以保守默认 allow_partial 回填（未声明 exact 的计划按部分完成对待）；
// 全新库按 v1→v2→v3 顺序执行，同样落到该列定义。
const schemaV3 = `
ALTER TABLE sync_plans ADD COLUMN requested_exactness TEXT NOT NULL DEFAULT 'allow_partial'
	CHECK(requested_exactness IN ('exact','allow_partial'));
`
