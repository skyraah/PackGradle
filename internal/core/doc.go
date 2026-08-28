// Package core 及其子包承载 PackGradle 新架构的领域核心：
// 领域模型（model）、身份生成（ids）、规范化与 canonical digest（normalize）、
// 三方差异（diff）与确定性计划（plan）。
//
// 边界约束（见 docs/architecture/packgradle-architecture-redesign.md §4.3）：
//   - core 只允许 import Go 标准库；
//   - 禁止 import Wails、SQLite driver、fsnotify、Packwiz/Prism 类型或仓库内其它 internal 包；
//   - 具体文件路径、子进程、数据库和事件总线属于外围实现，不得泄漏进 core。
package core
