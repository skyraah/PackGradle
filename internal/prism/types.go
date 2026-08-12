package prism

// Instance 描述一个从 Prism Launcher 实例目录扫描到的实例。
// 解析失败时不中断列表，错误码 JSON 文本落入 Error 字段（同 PackProject.Error 容错哲学）
type Instance struct {
	ID               string `json:"id"`     // 实例 ID = instances/ 下的文件夹名
	Name             string `json:"name"`   // instance.cfg 的 name= 显示名，缺失时回退 ID
	Path             string `json:"path"`   // 实例目录完整路径
	GameDir          string `json:"game_dir"` // 游戏目录（<实例>/minecraft，可能尚不存在）
	Group            string `json:"group"`  // instgroups.json 分组（无分组为空串）
	Minecraft        string `json:"minecraft"`        // mmc-pack.json 中 net.minecraft 组件版本
	Modloader        string `json:"modloader"`        // fabric / forge / neoforge / quilt / liteloader / ""
	ModloaderVersion string `json:"modloader_version"` // 加载器组件版本
	Error            string `json:"error"`  // 解析失败原因（errs JSON 文本）
}

// loaderUIDs 是 mmc-pack.json 组件 uid → 加载器名的映射
// （与 Prism Launcher PackProfile.cpp 的 KNOWN_MODLOADERS 一致）
var loaderUIDs = map[string]string{
	"net.minecraftforge":         "forge",
	"net.neoforged":              "neoforge",
	"net.fabricmc.fabric-loader": "fabric",
	"org.quiltmc.quilt-loader":   "quilt",
	"com.mumfrey.liteloader":     "liteloader",
}

// CreateRequest 程序创建实例的入参（组件取自 packwiz 项目的 pack.toml）
type CreateRequest struct {
	Name             string `json:"name"`
	Minecraft        string `json:"minecraft"`
	Modloader        string `json:"modloader"` // fabric / forge / neoforge / quilt / liteloader / ""
	ModloaderVersion string `json:"modloader_version"`
}

// LinkView 是「项目 ↔ 实例」关联的组装视图（服务层实时扫描实例组装，前端渲染）
type LinkView struct {
	Project       string `json:"project"`
	ProjectPath   string `json:"project_path"`
	InstanceID    string `json:"instance_id"`
	InstanceName  string `json:"instance_name"`  // 实时扫描获取；实例被删/改名时为空
	InstancePath  string `json:"instance_path"`
	InstanceValid bool   `json:"instance_valid"` // 实例当前是否仍可解析
}

// DirLinkView 是目录关联对的视图（含两侧目录实态与同步模式）
type DirLinkView struct {
	Project        string   `json:"project"`
	Instance       string   `json:"instance"`
	ProjectDir     string   `json:"project_dir"`     // 项目根下目录名
	InstanceDir    string   `json:"instance_dir"`    // 实例游戏目录下相对路径
	Mode           string   `json:"mode"`            // ""=整目录 junction / "files"=文件级同步
	Files          []string `json:"files"`           // files 模式的同步文件清单（相对 ProjectDir）
	ProjectExists  bool     `json:"project_exists"`  // 项目侧目录是否存在
	InstanceExists bool     `json:"instance_exists"` // 实例侧目录是否存在
}

// LinkResult 是一键关联中单个条目的建链结果
type LinkResult struct {
	Name   string `json:"name"`   // 相对项目根的条目名
	IsDir  bool   `json:"is_dir"` // 目录（junction）/ 文件（硬链接）
	Status string `json:"status"` // linked / existing / skipped / manual / error
	Detail string `json:"detail"` // 跳过原因或错误文本
}

// VersionDiffItem 是双端版本不一致的单个 mod
type VersionDiffItem struct {
	ID              string `json:"id"`
	ProjectVersion  string `json:"project_version"`  // 项目侧版本（packwiz 元数据）
	InstanceVersion string `json:"instance_version"` // 实例侧版本（.index 元数据）
}

// MetaDiff 是项目 ↔ 实例 mods 元数据的差异（按 index.toml 权威列表 vs 实例 mods/.index 对比）。
// 持久化到 <项目目录>/.cache/metadiff.cache：每次「查看差异」时重新计算并刷新缓存，
// 避免实时监听目录变化带来的性能开销。
type MetaDiff struct {
	FetchedAt    string            `json:"fetched_at"`     // 计算时间（RFC3339）
	InstanceOnly []string          `json:"instance_only"`  // 实例 .index 有、项目 index.toml 无（可拉取）
	ProjectOnly  []string          `json:"project_only"`   // 项目有、实例 .index 无（可推送）
	VersionDiff  []VersionDiffItem `json:"version_diff"`   // 双端版本不一致
}
