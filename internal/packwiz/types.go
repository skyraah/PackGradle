package packwiz

// ModInfo 描述 packwiz 项目中的一个 mod（对应 index.toml 中 mods/ 条目指向的文件）
type ModInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Side    string `json:"side"`    // client / server / both（中文标签由前端按翻译键渲染）
	Version string `json:"version"` // pw.toml 中的 version（不一定存在）
	File    string `json:"file"`    // 下载文件名
	Path    string `json:"path"`    // pw.toml 完整路径
	// CurseForge 源信息（0 表示非 CurseForge 源）
	CfProjectID int64 `json:"cf_project_id"`
	CfFileID    int64 `json:"cf_file_id"`
	// 本地缓存的 CurseForge 文件信息（获取后填充）
	CfVersion     string `json:"cf_version"`      // displayName（版本）
	CfFileDate    string `json:"cf_file_date"`    // 发布日期
	CfReleaseType int    `json:"cf_release_type"` // 1=正式版 2=测试版 3=Alpha
}

// PackProject 描述一个已导入的 packwiz 项目
type PackProject struct {
	Name             string    `json:"name"`
	Path             string    `json:"path"`       // pack.toml 所在目录
	PackToml         string    `json:"pack_toml"`  // pack.toml 完整路径
	Version          string    `json:"version"`
	Author           string    `json:"author"`
	PackFormat       string    `json:"pack_format"`
	Minecraft        string    `json:"minecraft"`
	Modloader        string    `json:"modloader"`          // fabric / forge / neoforge / quilt ...
	ModloaderVersion string    `json:"modloader_version"`
	Mods             []ModInfo `json:"mods"`
	Error            string    `json:"error"` // 解析失败时的原因
}

// RefreshResult 是 packwiz CLI 执行结果
type RefreshResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// ModUpdateInfo 是 packwiz update 检查结果中单个 mod 的信息
type ModUpdateInfo struct {
	Name        string `json:"name"`
	HasUpdate   bool   `json:"has_update"`
	CurrentFile string `json:"current_file"`
	LatestFile  string `json:"latest_file"`
	Error       string `json:"error"`
}

// UpdateCheckResult 是 packwiz update 检查结果
type UpdateCheckResult struct {
	OK      bool            `json:"ok"`
	Output  string          `json:"output"`
	Updates []ModUpdateInfo `json:"updates"` // 有更新的 mod
	Errors  []ModUpdateInfo `json:"errors"`  // 检查失败 / 跳过 / 无更新源的 mod
}
