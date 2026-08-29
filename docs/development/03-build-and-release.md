# 构建、调试与发布

## 1. Taskfile 总览

根目录 `Taskfile.yml` 定义顶层任务，并 include 各平台任务
（`build/Taskfile.yml` 通用、`build/{windows,darwin,linux,ios,android}/Taskfile.yml`）。

| 任务 | 作用 |
| --- | --- |
| `task dev` | Wails 开发模式：`wails3 dev -config ./build/config.yml -port 9245` |
| `task build` | 按 `GOOS`（默认宿主 OS）构建桌面应用 |
| `task package` | 打包发布产物（Windows 默认 NSIS 安装器） |
| `task run` | 运行已构建的 `bin/packgradle`（不负责构建，先 `task build`） |
| `task build:server` | 无 GUI 服务器模式构建（`-tags server,production`） |
| `task run:server` | 构建并运行 DEV 服务器（`-tags server`） |
| `task build:docker` | 构建服务器模式 Docker 镜像 |
| `task run:docker` | 构建并运行镜像（宿主机端口默认 8080） |
| `task setup:docker` | 构建交叉编译用 Docker 镜像 `wails-cross` |
| `task test` / `test:vet` / `test:race` | 验收 L0：全量测试 / `go vet` / `-race` 硬门槛（需 mingw-w64 gcc） |
| `task acceptance:headless` / `acceptance:perf` | 验收 L0：headless 全链路两遍（全新数据目录）/ 性能基线冷热扫描与门槛评估（见 [验收规格](../acceptance/p1-acceptance-spec.md) §1.1） |

> 平台范围说明：Taskfile 模板虽含 darwin/linux/ios/android 任务，但当前后端
> `internal/envutil`、`junction`、`links.go` 为 Windows 专属实现，实际构建仅 Windows
> 桌面目标经过验证；跨平台前需先补非 Windows 桩（见 [后端开发指南](./01-backend.md)）。

常用变量：

- `GOOS`：目标系统（`task build GOOS=windows` 等；Wails 也接受 `wails3 build GOOS=...`）。
- `DEV=true`：开发构建（不压缩前端、不 strip Go、保留 inlining）。
- `OBFUSCATED=true`：经 garble 混淆（需先安装 garble）。
- `EXTRA_TAGS`：追加 Go build tags。
- `ARCH` / `GOARCH`：目标架构（默认宿主 ARCH；Windows 支持 amd64/arm64）。
- `PACKAGE_MANAGER`：默认 `yarn`（可 npm/pnpm/bun）。
- `VITE_PORT` / `WAILS_VITE_PORT`：前端 dev 端口，默认 9245。

## 2. 一次完整构建发生了什么

`task build` → `windows:build`（宿主 Windows 为例）→ `build:native`：

1. `common:go:mod:tidy`（run:once，避免并发竞争）。
2. `common:build:frontend`：
   - `install:frontend:deps`（yarn install）
   - `generate:bindings`：`wails3 generate bindings -f '' -clean=true -ts -i`（生产再加 `-tags production`）
   - `frontend:run`：`yarn build`（即 `vue-tsc && vite build --mode production`）
3. `common:generate:icons`：`build/appicon.png → windows/icon.ico`、`darwin/icons.icns`。
4. `generate:syso`：`wails3 generate syso -icon windows/icon.ico -manifest ... -info ...`。
5. `go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui" -o bin/packgradle.exe`。
6. 清理 `*.syso`。

产物：`bin/packgradle.exe`（其他平台类推）。`main.go` 的 `//go:embed all:frontend/dist` 保证前端被打进二进制。

## 3. 开发模式细节（build/config.yml）

- `dev_mode.root_path: .`；debounce 1000ms。
- ignore：`.git`、`node_modules`、`frontend`、`bin` 目录，测试文件 `*_test.go`；watched extension 为 `*.go / *.js / *.ts`。
- executes：
  1. `wails3 build DEV=true`（blocking，构建 dev 二进制）
  2. `wails3 task common:dev:frontend`（background，Vite）
  3. `wails3 task run`（primary，运行应用）

前端目录整体被 dev_mode ignore，Vite HMR 由 Vite 自己负责；Go 改动会触发重建重连。

## 4. 测试与质量门

```bash
go test ./...            # 后端单测
go vet ./...             # Go 静态检查
cd frontend && yarn build   # vue-tsc 类型检查 + 生产构建
```

建议 PR 前：`task build`（或至少 `go test ./...` + `yarn build`）+ 实机 `task dev` 走查。

## 5. Windows 打包 / 签名

- NSIS（默认）：`task package` → `build/windows/nsis/` 生成安装器（默认 `INSTALL_SCOPE=machine`，可 `INSTALL_SCOPE=user`）。
- MSIX：`task package FORMAT=msix`（需先 `wails3 tool msix-install-tools`；产物 `bin/packgradle-<arch>.msix`）。
- 签名：`wails3 setup signing` 配置证书后 `task windows:sign` / `task windows:sign:installer`。
- 交叉编译（非 Windows 上构建 Windows CGO 包）：`task setup:docker` 构建 `wails-cross` 后由 `build:docker` 使用。

## 6. 服务器模式与 Docker

后端在 `server` build tag 下走 Wails 服务器模式（HTTP，无原生 GUI 依赖）：

```bash
task run:server                    # DEV=true 服务器，默认端口见 ServerOptions
task build:server                  # 生产服务器二进制 bin/packgradle-server(.exe)
task build:docker TAG=packgradle:latest
task run:docker PORT=8080          # 容器内端口固定 8080
```

`build:docker` 会先构建生产前端再打镜像（基础镜像默认 `golang:alpine` + `gcr.io/distroless/static-debian12`，Dockerfile 位于 `build/docker/Dockerfile.server`）。若修改端口，需同步修改 Dockerfile 的 `ServerOptions.Port` 说明。

## 7. 发布清单（桌面版）

1. `git status` 干净；`docs/REQUIREMENTS.md` 与本次文档已更新。
2. `task build` 成功；`go test ./...`、`cd frontend && yarn build` 通过。
3. `task package`（按需要 `FORMAT` / `INSTALL_SCOPE` / `ARCH`）。
4. 验证安装器：全新目录安装 → 首次引导 → 导入 pack.toml → 关联实例 → meta/目录同步 → 更新检查。
5. 更新 `build/config.yml` 中 `info.version` 后再打 tag。

## 8. 故障排查

| 症状 | 处理 |
| --- | --- |
| `frontend/bindings` 缺失或过期 | `wails3 generate bindings -f '' -clean=true -ts -i` 后重建前端 |
| `vite` 端口被占 | 默认 9245 strictPort；走查可 `yarn dev --port 5199 --strictPort`；正式 dev 请释放 9245 |
| 构建时报 `.syso` 残留 | 正常流程会自动清理；手动删除根目录 `*.syso` |
| Windows GUI 下子进程闪黑框 | 必须走 `packwiz.runHiddenCmd`（`CREATE_NO_WINDOW`），不要直接 `exec.Command` |
| 多实例配置互相覆盖 | 确认单实例 Mutex 生效；勿绕过 `singleinstance.Acquire` |
| 打包提示 WebView2 缺失 | NSIS 流程会生成 WebView2 bootstrapper（`build/windows/nsis`） |
| Docker 交叉编译无镜像 | 先 `task setup:docker` |
