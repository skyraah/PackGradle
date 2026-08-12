// Package singleinstance 提供单实例互斥：防止多个 PackGradle 实例并存。
//
// 背景：每个实例各自持有 config 内存状态，多实例并存时一个实例删除项目
// 写盘成功后，另一个实例（内存仍是旧状态）后续任意写盘会把旧数据覆盖回去，
// 表现为「删除后配置复活」。Windows 命名互斥体进程退出自动释放，无残留文件。
//
// Windows 实现基于命名互斥体（Local\ 会话级命名空间）；非 Windows 平台不限制。
package singleinstance
