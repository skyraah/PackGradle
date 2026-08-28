// Package adapters 及其子包实现 application/ports 定义的适配器接口：
// filesystem（哈希/原子写/路径安全/binding fingerprint）、packwiz（Project 扫描）、
// prism（Runtime 扫描）。后续阶段按架构文档补充 watcher、junction 等。
//
// 边界约束：
//   - adapters 实现 ports，不做领域决策（身份匹配、冲突判定、计划生成均在 core）；
//   - adapters 可以依赖 core/model 与第三方库，但不得 import application/sync 的实现。
package adapters
