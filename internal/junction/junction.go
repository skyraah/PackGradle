package junction

// junction 包封装 Windows NTFS Junction 的创建、检测与删除。
// Junction 无需管理员权限即可创建（符号链接才需要），但仅限目录、仅限 NTFS 卷。
//
// Manager 抽象了链接操作，生产环境使用 Windows 原生实现（FSCTL_SET_REPARSE_POINT，
// 无 cmd mklink 的引号问题，天然支持中文/空格路径），测试可注入内存假实现。

// Manager 是目录链接操作的抽象
type Manager interface {
	// Create 将 link 目录创建为指向 target 的 junction（target 必须是绝对路径且已存在）
	Create(link, target string) error
	// Remove 删除链接本身（不触碰目标内容）；位置不是 junction 时返回错误
	Remove(link string) error
	// IsJunction 判断位置是否为 junction（不存在/普通目录/文件返回 false）
	IsJunction(link string) (bool, error)
	// TargetOf 返回 junction 的目标绝对路径；非 junction 时返回错误
	TargetOf(link string) (string, error)
}
