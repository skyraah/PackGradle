// Package service 包含旧架构的 Wails 服务（EnvService / PackwizService / PrismService）。
//
// ⚠️ LEGACY — 功能冻结（自 2026-08-22 起）：
//
// 本包对应 docs/architecture/packgradle-architecture-redesign.md 定义的重写前架构，
// 以项目名/实例目录名作为身份、以 Junction/硬链接充当同步模型。新架构的
// Relation/LogicalResource/ObservedSnapshot/SyncPlan 等语义一律实现在
// internal/{core,application,adapters,store,transport}，禁止在本包内追加。
//
// 允许的改动仅限：缺陷修复（保持现有行为）、只读迁移输入支持（legacy-import）。
// 复用本包解析知识时，以在新 adapter 中重新实现并满足新契约测试为准，
// 不得为了“少改代码”反向修改新领域模型。
package service
