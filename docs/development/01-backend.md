# 后端开发指南

## 1. 环境与前置

- Go 1.25+（`go.mod` 声明 `go 1.25.0`）。
- Wails v3 CLI（`wails3`，版本与 `go.mod` 中 beta.7 匹配）。
- **Windows 开发环境**：`internal/envutil` 为无 build tag 的 Windows 实现（直接依赖 `golang.org/x/sys/windows`），`junction`/`singleinstance` 虽有非 Windows 桩，但 `links.go`/`helpers.go` 同样依赖 Windows API——当前后端按 Windows 专属开发，跨平台适配前先补 envutil 等桩。
- 推荐安装 Task（taskfile.dev）；也可以直接执行 `go build` / `go test`。

常用命令（在仓库根目录）：

```bash
go build ./...          # 只编译 Go（不生成绑定）
go test ./...           # 运行全部单测（当前代码须在 Windows 上编译运行）
go vet ./...            # 静态检查
task dev                # Wails dev 模式（含绑定生成、前端 dev server、热重载）
```

## 2. 分层规则

| 层 | 规则 |
| --- | --- |
| `internal/errs` | 唯一错误构造入口；所有面向用户的错误必须是 `errs.New / NewDetail` 的 `err.*` 码。 |
| `internal/appconfig` | 唯一配置读写入口；上层不得直接 os.WriteFile 写配置。 |
| `internal/packwiz / prism / curseforge / envutil / fsutil / junction / pgignore` | 纯工具/域层：不依赖 Wails、不产生中文文案、可单测。 |
| `internal/service` | 唯一与 Wails 接触层：方法签名即前后端契约；组合工具层完成业务。 |
| `main.go` | 只做装配（单实例、配置、服务注册、窗口），不写业务。 |

## 3. 新增一个后端方法（完整流程）

1. **选择服务**：环境/工具 → `EnvService`；packwiz 项目/CF 更新 → `PackwizService`；Prism 关联/同步 → `PrismService`。
2. **写 Go 方法**（注意导出方法名首字母大写才被 Wails 绑定）：

   ```go
   // MyService 的返回类型必须是可 JSON 序列化的结构体/基础类型/slice。
   // 最后一个返回值可声明 error：有错误时前端 Promise reject。
   func (s *PackwizService) MyNewMethod(projectName string) (MyResult, error) {
       proj, err := s.findProject(projectName)
       if err != nil {
           return MyResult{}, err
       }
       return MyResult{Name: proj.Name}, nil
   }
   ```

3. **定义 DTO**：与返回结构同包或 `packwiz/prism` 类型文件；每个字段显式 `json:"snake_case"`；补充文档注释（注释会被生成到 TS 绑定中）。
4. **错误码**：`errs.New("err.xxx.yyy", args...)`；需要在语言文件补键（见前端开发指南）。
5. **前端参数校验**：任何来自前端的路径/清单先走 `validateRelDir` / `normalizeFileListStrict` / `filepath.Abs` 等安全函数。
6. **配置写操作**：全局配置用 `ConfigManager` 自带锁；项目级配置必须包裹 `appconfig.WithProjectConfigLock(projectPath, func() error { ... })`，Load/Save 在锁内完成。
7. **生成绑定并验证**：

   ```bash
   wails3 generate bindings -f '' -clean=true -ts -i
   go build ./...
   ```

   检查 `frontend/bindings/packgradle/internal/service/<service>.ts` 出现新方法。

## 4. 错误处理约定

- 用户可理解的失败 → `errs.New / NewDetail`（错误码 + 参数 + 底层 detail）。
- 预期内的「单项失败不影响整体」→ 把错误放进结果字段（`PackProject.Error` / `Instance.Error` / `PrismOverview.locate_error`），不要整体抛错。
- CLI 类方法（refresh/update）→ 返回 `RefreshResult{OK:false, Output: err.Error()}`，不抛错，保证前端能拿到输出。
- 底层错误不直接裸抛给前端：包一层 `errs.NewDetail`，让前端可识别错误码；日志里同时 `log.Printf` 关键上下文。
- 删除/清理操作宁可报错保留配置，也不静默清空导致孤儿链接（见 `UnlinkProject` 注释）。

## 5. 并发与文件安全约定

- 全局配置：`ConfigManager.mu`（已实现）。
- 项目配置：`WithProjectConfigLock` 按项目路径分锁；不要在锁内再次调用 Load/Save（无锁版本不会死锁，但语义上重复）。
- 批量网络请求：并发上限 8（`FetchAllModVersions` 的 semaphore 模式可复用）。
- 子进程：用 `packwiz.runHiddenCmd` 模式（`CREATE_NO_WINDOW`），必须有 `context.WithTimeout`；当前 `refresh=5min`、`update/check=15min`。
- 链接删除前做身份校验（`junctionPointsTo` / `hardlinkPointsTo`），只删仍指向项目侧的链接，不误删用户数据。
- 跨卷硬链接会失败：`SelectInstanceFiles` 已把 `ERROR_NOT_SAME_DEVICE` 转为 `err.sync.cross_volume`，新功能如涉及移动/链接需同样处理。

## 6. 测试约定

- 纯逻辑优先单测：现有覆盖 `packwiz`（解析/更新输出）、`prism`（扫描/创建/meta 转换）、`appconfig`（TOML/迁移）、`curseforge`（httptest）、`envutil`、`pgignore`、`service`（prism/link 流程）、`junction`（内存实现 + Windows 集成）。
- 涉及 Windows 注册表/真实 junction 的测试用 `junction.NewMemoryManager()` 或临时目录隔离。
- 跑测试：`go test ./...`；新方法建议至少补一个 happy path + 一个错误码断言（`errs.CodeOf(err)`）。

## 7. 关键文件速查

| 改什么 | 去哪改 |
| --- | --- |
| 全局配置字段 | `internal/appconfig/config.go`（注意旧字段迁移） |
| 项目级配置字段 | `internal/appconfig/projectconfig.go` |
| packwiz 解析/CLI | `internal/packwiz/parse.go` / `cli.go` / `update.go` |
| Prism 扫描/创建/meta | `internal/prism/instance.go` / `create.go` / `meta.go` |
| CurseForge API/缓存 | `internal/curseforge/client.go` / `cache.go` |
| 服务方法 | `internal/service/*.go` |
| 应用装配 | `main.go` |
