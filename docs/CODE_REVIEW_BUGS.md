# PackGradle 代码审查报告（Bug 清单）

> 审查日期：2026-08-15（修复实施：2026-08-15，见文末「修复记录」）
> 范围：`internal/**/*.go`（生产代码）+ `frontend/src/**`（生产代码），不含自动生成的 bindings 与 build/ 产物。
> 自动化基线：`go build -buildvcs=false ./...` ✅、`go vet -buildvcs=false ./...` ✅、`go test -buildvcs=false ./...` 全绿 ✅、`vue-tsc --noEmit` ✅。
> 结论：无编译/类型/测试层面的问题；但文件同步、关联切换与更新检查存在若干**高影响的功能与数据安全 bug**，按严重度分级如下。

---

## 一、高严重度（数据安全 / 核心流程断裂）

### BUG-01 切换到“文件级同步”会先删掉 Junction，导致“选择同步文件”流程必然失败
- 位置：`internal/service/links.go:281-310`（`SetDirLinkMode`）→ `links.go:439-453`（`ListInstanceDirFiles`）→ `frontend/src/components/prism/FileSelectDialog.vue:31-46`
- 现象：用户对已建 Junction 的目录点“切换为文件级同步”后，再点“选择同步文件”，弹窗打开即报“路径无效”并自动关闭。
- 根因：
  1. `SetDirLinkMode(…,"files")` 先执行 `removeDirLinkTargets`，把实例侧 junction 删除，再按（此时通常为空的）`Files` 清单建硬链接，结果实例侧目录消失；
  2. `ListInstanceDirFiles` 此时既不是 junction、目录也不存在 → `fsutil.ListFilesRelative` 返回错误 → 前端关闭弹窗。
- 影响：文件级同步在“一键关联”后的最常见路径上不可用。
- 修复建议：
  - `ListInstanceDirFiles` 在 `mode=files` 且实例侧目录缺失时，回退到列出**项目侧**目录文件（junction 模式下两侧本来就是同一物理目录，文件清单等价）；
  - 同时给 `SelectInstanceFiles` 增加“文件已在项目侧”的快速路径（跳过移动，直接建硬链接并入清单），避免跨卷移动失败。

### BUG-02 `SelectInstanceFiles` 部分失败时仍写全量清单，且“移动→硬链接”无回滚
- 位置：`internal/service/links.go:351-419`
- 现象/风险：
  1. `os.Rename(instSide, projSide)` 成功后若 `os.Link(projSide, instSide)` 失败（跨卷、权限、进程崩溃），文件已经从实例侧**移走**，实例侧永久缺失，但循环结束仍把该文件写入 `Files` 清单并保存配置；
  2. 清单合并使用 `mergeUniqueSorted(既有, clean)`，`clean` 是全部勾选项，**不管单项结果是 linked / skipped / error**。
- 影响：实例侧文件丢失或清单与实际同步状态不符；后续 `CreateAllLinks` 可能把“从未纳入”的文件突然同步。
- 修复建议：
  - 仅把 `res.Status == "linked"`（或经身份校验确认为同一文件）的条目合并进清单；
  - `os.Link` 失败时把已移动的文件 `os.Rename` 回实例侧（回滚）；
  - 更稳妥：改为“复制到项目侧 → 项目侧为权威”前先做同卷判断，跨卷直接给出明确错误；
  - 存在任何 error 时不保存配置或返回错误，由前端提示。

### BUG-03 删除硬链接前不做“同一文件”身份校验，可能误删实例侧独立文件
- 位置：
  - `internal/service/links.go:242-260`（`removeDirLinkTargets` files 模式）
  - `internal/service/helpers.go:81-86`（`removeHardlinkFiles`）
  - 调用方：`UnlinkProject`（prismservice.go:113-132）、`RemoveDirLink`（237-258）、`RemoveProject`（packwizservice.go:85-109）
- 风险：游戏/启动器常用“删除后重建”方式写配置或 mod 文件。一旦实例侧文件不再是硬链接（已成为独立文件），解除关联/移除目录/移除项目时会直接 `os.Remove`，**删除实例侧用户数据且项目侧无法找回**。
- 修复建议：
  - 删除前用 `CreateFile` + `GetFileInformationByHandle` 比较实例侧与项目侧文件的卷序列号 + 文件 ID；相同才删，不同则跳过并返回警示；
  - 与 BUG-06/07 一起把清理逻辑收敛到一个带安全校验的公共函数。

### BUG-04 单 mod 更新把“显示名”当作 `.pw.toml` 文件名传给 packwiz
- 位置：`frontend/src/components/projects/CheckUpdatesDialog.vue:69-86` → `UpdateMods(project, u.name)` → `internal/packwiz/cli.go:43-53`
- 证据：packwiz 官方源码 `cmd/update.go` 单 mod 分支用 `index.FindMod(args[0])` 查找，找不到时提示 *“use the name of the .pw.toml file (defaults to the project slug)”*；而 `ParseUpdateOutput` 捕获的 `u.name` 是更新输出中的显示名（`modData.Name`），mod 被改名后与 slug 不一致。
- 影响：显示名 ≠ slug 的 mod 单更必然失败（"Can't find this file"）；与 slug 同名时碰巧可用。
- 修复建议：
  - 前端在调用前用父级项目数据 `project.mods` 把 `u.name` 反查为 `mod.id` 再传给 `UpdateMods`；
  - 或后端 `UpdateCheckResult` 直接返回可定位的 `mod_id`（解析时匹配 index 条目）。

---

## 二、中严重度（状态错乱 / 一致性 / 卡死）

### BUG-05 项目“改关联”到另一个实例时不清理旧实例链接
- 位置：`internal/service/prismservice.go:98-110`（`LinkProject`）
- 现象：项目已关联实例 A 且已建 junction/硬链接，再关联到实例 B：`pc.Instance` 被直接覆盖，A 实例侧的链接全部残留，而配置已“忘记”A。残留 junction 仍指向项目目录，A 里的游戏可能继续改动项目文件。
- 修复建议：`LinkProject` 若 `pc.Instance != "" && != instanceID`，先按旧实例执行 `removeDirLinkTargets` + `removeHardlinkFiles`，清理成功（或旧实例已不存在）后再切换并保存。

### BUG-06 定位不到实例目录时，解除关联/移除目录“跳过清理并直接清配置”
- 位置：`UnlinkProject`（prismservice.go:121-131）、`RemoveDirLink`（242-258）
- 现象：`scanInstancesSafe()` 定位失败返回 nil 时，清理被静默跳过，随后 `packgradle.toml` 被清空保存。实例目录一旦恢复可访问，工具已不再记录这些链接，**孤儿链接无法再用工具清理**。
- 修复建议：
  - 定位失败时返回错误并保留配置（用户修好实例目录后重试）；
  - 确需强制解除时提供 `force` 参数，并在 UI 明示“链接未清理，需手动处理”。

### BUG-07 移除项目时只清理整目录 Junction，遗漏文件级同步的硬链接
- 位置：`internal/service/packwizservice.go:85-109`（`cleanupProjectLinks`）
- 现象：`RemoveProject` 只遍历 `pc.DirLinks` 中 junction 并删除，files 模式的 `DirLinks[].Files` 硬链接不处理；只删 `pc.FileLinks`（顶层文件）。随后 `packgradle.toml` 被删除，文件级硬链接残留实例侧且不可追溯。
- 修复建议：复用 `PrismService.removeDirLinkTargets`（含 files 分支 + 空目录清理）的等价公共实现，并叠加 BUG-03 的身份校验。

### BUG-08 项目级配置“读-改-写”无并发保护，且临时文件名固定
- 位置：`internal/appconfig/tomlfile.go:24-49`（固定 `path+".tmp"`）、`projectconfig.go`、所有 `LoadProjectConfig → 修改 → SaveProjectConfig` 路径
- 风险：Wails 可并发调用服务方法（不同对话框/页面同时操作同一项目）。两个并发操作各自读旧配置、各自写同一 `.tmp` 再 rename，会出现更新丢失或一方 rename 失败。全局 `config.toml` 有 mutex，项目级 `packgradle.toml` **没有**。
- 修复建议：
  - `ConfigManager` 增加按项目路径分片的 `sync.Mutex`（或 map+锁），所有项目级读写走同一入口；
  - `WriteTomlAtomic` 改用 `os.CreateTemp(dir, name+".*.tmp")` 唯一临时名。

### BUG-09 packwiz 子进程没有超时，网络卡住时 UI 永久转圈
- 位置：`internal/packwiz/cli.go:20-53`（`RunRefresh` / `RunCheckUpdates` / `RunUpdateMods` 均 `CombinedOutput()` 无限等待）
- 现象：packwiz 检查/更新依赖网络，代理失效或端点挂起时进程不退出，前端 loading 永久。
- 修复建议：改用 `exec.CommandContext` + `context.WithTimeout`（refresh 建议 5 分钟、update/check 建议 15 分钟，可配），超时返回 `err.packwiz.timeout` 错误码。

### BUG-10 MetaDiff 对 CurseForge 源 mod 的版本差异永远不可见
- 位置：`internal/service/meta.go:149-172`
- 根因：项目侧 `ModInfo.Version` 来自 `pw.toml` 顶层或 `[update.*]`；CF 源只有 `project-id/file-id` 没有版本字符串，真实版本在 `.cache/modversion.cache` 的 `displayName`。`MetaDiff` 只在 `pv != ""` 时比较，CF mod 因此永远不进 `version_diff`。
- 修复建议：项目侧版本回退链改为 `mod.Version → CfCache[project:file].DisplayName`，并对 `pv=="" && iv!=""` 的情况也产生差异项（或标记为“项目侧无版本信息”）。

### BUG-11 更新检查对话框允许“检查中/应用中”并发触发命令
- 位置：`frontend/src/components/projects/CheckUpdatesDialog.vue`
- 现象：
  1. `checking=true` 时“应用全部更新”和每行“更新”按钮仍可点（只互斥了 `applyingAll`/`updating`）；
  2. 关闭后立即重开，旧 `runCheck` 还在跑，会重叠两次检查。
- 风险：同一项目并发执行多个 packwiz 命令，可能互相覆盖 `index.toml`/`pack.toml`。
- 修复建议：`checking || applyingAll || updating !== ''` 统一禁用所有变更按钮；`runCheck` 入口处若 `checking` 已为真则直接返回；关闭弹窗时可用 AbortController 标记丢弃旧结果。

### BUG-12 服务层未校验客户端传入的目录/文件参数，存在越界读写风险
- 位置：
  - `ManualLinkDir(dir)`、`AddDirLink(projectDir)`、`SetDirLinkMode(dir)`、`SetDirLinkFiles(dir, files)`、`SelectInstanceFiles(dir, files)`、`ListDirFiles(dir)`、`ListInstanceDirFiles(dir)`（`internal/service/links.go` / `prismservice.go`）
  - `normalizeFileList`（helpers.go:89-102）不拒绝绝对路径与 `..`。
- 风险：`dir = "..\\..\\Desktop"`、`files = ["..\\..\\secret.txt"]` 这类参数会被 `filepath.Join` 拼出项目/实例根以外的路径；`SelectInstanceFiles` 甚至会对任意文件执行 **移动**。桌面端用户本身有文件权限，但恶意/错误调用可造成计划外破坏。
- 修复建议：
  - 公共 `validateRelDirArg`：拒绝空、绝对路径、盘符、`.`/`..` 段、非法字符；
  - 校验后 `filepath.Join(root, arg)` 必须仍在 `filepath.Clean(root)` 之内（`filepath.Rel` 校验）；
  - `normalizeFileList` 同样拒绝绝对路径与 `..` 段。

### BUG-13 实例扫描失败被吞成“空列表”，前端误报“未找到实例”
- 位置：`internal/prism/instance.go:17-37`（`ScanInstances` 对 `ReadDir` 错误直接 `return nil`）、`prismservice.go:33-39`
- 现象：实例根目录存在但无读取权限/IO 错误时，返回 `(nil, nil)`，前端显示“未在 Prism 中找到任何实例”，掩盖真实错误。
- 修复建议：`ScanInstances` 返回 `([]Instance, error)`；`Overview` 把扫描错误写入 `LocateError`，前端显示可操作提示。

### BUG-14 文件级同步依赖同卷 Rename/Hardlink，跨卷时全部失败且 UI 不展示错误数
- 位置：`links.go:393-405`、`helpers.go:192-196`；`frontend/src/components/prism/FileSelectDialog.vue:56-71`
- 现象：项目盘（如 D:）与 Prism 实例盘（C:）不同卷时，`os.Rename`/`os.Link` 报 `ERROR_NOT_SAME_DEVICE`；前端只统计 `linked/skipped`，errors 不可见。
- 修复建议：
  - 后端同卷预检，跨卷改用“复制 + 身份记录”的降级策略或返回专用错误码 `err.sync.cross_volume`；
  - 前端 snackbar 必须展示 `error` 数量与详情。

### BUG-15 违反“Go 零文案”契约与硬编码中文
- 位置：
  - `internal/service/helpers.go:57`：`errs.NewDetail("err.prism.instances_dir_not_found", "目录不存在", dir)` —— Detail 是硬编码中文，前端会渲染成“无法定位实例目录: D:\x: 目录不存在”；
  - `frontend/src/components/prism/MetaDiffDialog.vue:177`：`'项目 ' + v.project_version + ' → 实例 ' + v.instance_version` 硬编码中文，未走 i18n。
- 修复建议：前者 Detail 去掉（路径已作为 args 传入）；后者新增 `prism.metaVersionDiffText` 翻译键。

### BUG-16 全局 `user-select: none` 导致 CLI 输出无法选中复制
- 位置：`frontend/src/assets/main.css:10-20`、`OutputDialog.vue` / `CheckUpdatesDialog.vue` 的 `<pre>`
- 现象：只有 `input/textarea` 恢复了文本选择，`pre.output-pre` 继承 body 的 `user-select: none`，用户无法复制 packwiz 输出。
- 修复建议：`pre, .selectable { user-select: text; -webkit-user-select: text; }`。

---

## 三、低严重度（健壮性 / 一致性 / 体验）

### BUG-17 实例创建不校验空版本与 Windows 保留名
- 位置：`internal/prism/create.go:18-77`、`sanitizeInstanceID`（92-103）
- 问题：`Minecraft`/`ModloaderVersion` 为空仍照写 `mmc-pack.json`；`CON/PRN/NUL/COM1`、结尾点/空格等非法目录名未处理，创建时以笼统错误失败。
- 建议：校验 MC 版本必填；`sanitizeInstanceID` 补保留名/结尾点空格处理，失败给出 `err.prism.create_invalid_name`。

### BUG-18 自动检测回写 config 失败被静默吞掉
- 位置：`internal/service/detect.go:78-79`（`_ = s.config.SetToolPath(...)`）
- 建议：至少 `log.Printf`；条件允许时把“检测成功但持久化失败”反馈到 `ToolInfo`。

### BUG-19 `.pgignore` 解析失败时静默退化为“不过滤”
- 位置：`internal/pgignore/pgignore.go:48-56`
- 风险：用户写错规则后点“一键关联”，`.git/.cache/pack.toml` 都会被建链（与提示预期相反）。
- 建议：`Load` 返回 `(matcher, warning)`；前端在关联结果中提示“忽略规则解析失败，已跳过过滤”。

### BUG-20 CurseForge 每请求新建 `http.Client`；批量缓存逐条全量重写
- 位置：`internal/curseforge/client.go:51`、`cache.go:53-63`
- 影响：连接不复用；批量 8 并发下 `Upsert` 串行读-改-写整文件 O(n²)。（GAP-4/5、NFR-4 已记录）
- 建议：包级共享 Client（保持 15s 超时）；批量获取完成后一次性合并写盘。

### BUG-21 旧配置迁移部分失败后重跑会重复追加 DirLinks
- 位置：`internal/appconfig/config.go:176-209`
- 场景：项目 A 迁移成功、项目 B 写盘失败 → 函数返回错误，Legacy 字段未清；下次启动重跑时项目 A 的 `LegacyDirLinks` 再次 append，产生重复条目。
- 建议：迁移前对既有 `pc.DirLinks` 去重；或先全部收集成功后再统一清 Legacy 字段。

### BUG-22 `index.file` / index 条目未做路径边界校验
- 位置：`internal/packwiz/parse.go:51-56,106-137`
- 问题：`index.file` 为 `..\..\x.toml` 或 `[[files]]` 写 `mods/../../x.pw.toml` 时，`filepath.Join` 会越出项目目录读取文件。本地用户可控，风险低，但应 `filepath.Rel` 校验并拒绝 `..` 段。

### BUG-23 工具路径环境变量值不展开 `%VAR%`
- 位置：`internal/envutil/path.go:31-35`（`FindExecutable` 对 `os.Getenv(envVar)` 未调用 `expandEnv`）
- 建议：对 envVar 值先 `expandEnv` 再 `resolveToolPath`。

### BUG-24 `loadProjects/loadTools/loadOverview` 的 `force` 在请求进行中被忽略
- 位置：`frontend/src/stores/projects.ts:25-39`、`env.ts:11-25`、`instances.ts:11-25`
- 现象：已有 in-flight 请求时，`load*(true)` 直接返回旧请求；语义上“强制刷新”未兑现。影响小，建议加请求代数（generation）或在完成后按需补一次刷新。

### BUG-25 API Key 保存提示依据未 trim 的本地值
- 位置：`frontend/src/views/SettingsView.vue:133-143`
- 现象：输入 `"   "` 保存时后端清空，但提示按 `apiKey.value` 非空显示“已保存”。建议按 `apiKey.value.trim()` 判断。

### BUG-26 打包元数据仍是模板占位
- 位置：`build/config.yml:8-14`
- 问题：companyName `"My Company"`、productName `"My Product"` 等会进入安装包/应用属性。（NFR-6 已记录）

### BUG-27 文档与产品一致性问题（非代码）
- `docs/FRONTEND.md` 提到“单 mod 更新（打通原 GAP-3）”，但存在 BUG-04；`REQUIREMENTS.md` 中 GAP-3 已闭环的说法需在修复后复核。

---

## 四、自动化检查结论

| 检查 | 结果 |
|---|---|
| `go build -buildvcs=false ./...` | ✅ 通过 |
| `go vet -buildvcs=false ./...` | ✅ 通过 |
| `go test -buildvcs=false ./...` | ✅ 11/11 包通过 |
| `vue-tsc --noEmit` | ✅ 通过 |
| i18n 静态键交叉核对 | ✅ 无缺失（动态键 `side.*` / `tool.source.*` 取值均在语言文件中） |

---

## 五、修复记录（2026-08-15）

| 编号 | 状态 | 修复要点 |
|---|---|---|
| BUG-01 | ✅ 已修复 | `ListInstanceDirFiles` 在 files 模式且实例侧目录缺失时回退项目侧清单；`SelectInstanceFiles` 增加“文件已在项目侧”快速路径 |
| BUG-02 | ✅ 已修复 | 移动后硬链接失败回滚 Rename；仅 `linked` 文件写入清单；新增回归测试 |
| BUG-03 | ✅ 已修复 | `hardlinkPointsTo`（`os.SameFile`）身份校验后删除；顶层与 files 模式统一保护；新增测试 |
| BUG-04 | ✅ 已修复 | 前端按 `project.mods` 把显示名反查为 mod id 再调用 `UpdateMods`；查不到时明确提示 |
| BUG-05 | ✅ 已修复 | `LinkProject` 切换实例前清理旧实例侧链接，定位失败则报错 |
| BUG-06 | ✅ 已修复 | `UnlinkProject`/`RemoveDirLink` 定位失败时报错并保留配置，不再静默清空 |
| BUG-07 | ✅ 已修复 | `RemoveProject` 复用 `removeProjectLinkTargets`，files 模式硬链接一并清理 |
| BUG-08 | ✅ 已修复 | `WithProjectConfigLock` 按项目串行化读-改-写；`WriteTomlAtomic` 改用 `CreateTemp` 唯一临时名 |
| BUG-09 | ✅ 已修复 | packwiz CLI 全部 `exec.CommandContext` + 5/15 分钟超时，超时返回 `err.packwiz.timeout` |
| BUG-10 | ✅ 已修复 | `MetaDiff` 项目侧版本回退 CF 缓存 displayName |
| BUG-11 | ✅ 已修复 | `checking` 期间禁用应用/单更按钮；`runCheck` 重入保护 |
| BUG-12 | ✅ 已修复 | `validateRelDir` / `normalizeFileListStrict` 拒绝绝对路径与 `..`，覆盖全部客户端目录/文件参数；新增测试 |
| BUG-13 | ✅ 已修复 | `ScanInstances` 返回错误；`Overview.LocateError` 透出 `err.prism.scan_failed` |
| BUG-14 | ✅ 已修复 | 跨卷 `ERROR_NOT_SAME_DEVICE` 转为 `err.sync.cross_volume`（移动前不破坏文件）；前端提示失败数 |
| BUG-15 | ✅ 已修复 | 移除 Go 硬编码中文 detail；MetaDiff 差异文案走 i18n |
| BUG-16 | ✅ 已修复 | `pre` 允许文本选择，CLI 输出可复制 |
| BUG-17 | ✅ 已修复 | MC/加载器版本必填校验；保留设备名/结尾点空格处理；新增测试 |
| BUG-18 | ✅ 已修复 | 检测回写失败记录日志 |
| BUG-19 | ✅ 已加固 | `.git/.cache/index.toml/pack.toml/packgradle.toml/.pgignore` 由 `pgignore.CoreExcluded` 无条件排除，即使 `.pgignore` 解析失败也不会误建链 |
| BUG-20 | ✅ 已修复 | 共享 `http.Client`；新增 `UpsertMany` 批量合并写缓存（消除 O(n²)） |
| BUG-21 | ✅ 已修复 | 迁移追加去重；纯 `[[dir_links]]` 项目也迁移；新增测试覆盖 |
| BUG-22 | ✅ 已修复 | `index.file` 与 `mods/` 条目越界校验，非法条目忽略/报错；新增测试 |
| BUG-23 | ✅ 已修复 | 工具环境变量值先 `expandEnv` 展开 |
| BUG-24 | ✅ 已修复 | 三个 store 的 in-flight force 请求结束后自动补刷 |
| BUG-25 | ✅ 已修复 | API Key 保存提示按 trim 后值判断 |
| BUG-26 | ✅ 已修复 | `build/config.yml` 填充 PackGradle 实际元数据 |
| BUG-27 | ✅ 已同步 | 本文与 REQUIREMENTS.md 更新修复状态 |

**修复后验证**：`go build/vet/test ./...` 全绿（新增 `links_fix_test.go`、`instance_test` 扫描错误用例、`create_test` 保留名校验、`parse_test` 越界用例、`client_test` UpsertMany 用例），`vue-tsc --noEmit` 通过。

**总体评价**：架构清晰（错误码契约、原子写、单实例、路由化前端均属加分项），自动化质量门禁干净；风险集中在**文件系统破坏性操作的安全校验、项目级配置并发、跨卷场景与 packwiz 交互细节**。上述风险已按 BUG-01~04 → BUG-05~16 → BUG-17~27 顺序修复。
