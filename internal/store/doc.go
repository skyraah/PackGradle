// Package store 及其子包承载新架构的本地状态存储：
// paths（用户数据目录布局）、sqlite（packgradle.db 唯一元数据权威）、
// objectstore（SHA-256 内容寻址对象库）。
//
// 边界约束（ADR-005/ADR-009）：
//   - 本机状态只写入用户数据目录，永不写入 Project 工作树；
//   - store 只依赖 core/model 与 SQLite driver，不 import application/adapters。
package store
