// Package transport 是新架构的 Wails 出口层：
// DTO 定义与转换、SyncService 注册、事件桥。
//
// 边界约束：
//   - Go domain model 不直接作为 Wails DTO 暴露，全部经显式转换；
//   - 顶层 DTO 带 schema_version，slice 归一为空数组而非 null；
//   - 调用级错误经 errs.AppError 结构化传递（复用 main.go 的 MarshalError）；
//   - transport 不含领域判断（stale/expired 等状态投影由 application 计算）。
package transport
