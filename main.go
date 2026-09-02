package main

import (
	"embed"
	"encoding/json"
	"errors"
	"log"

	"packgradle/internal/appconfig"
	"packgradle/internal/bootstrap"
	"packgradle/internal/errs"
	"packgradle/internal/service"
	"packgradle/internal/singleinstance"
	"packgradle/internal/store"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails 会将 frontend/dist 下的文件嵌入二进制，作为前端资源服务器。
// 参见 https://pkg.go.dev/embed

//go:embed all:frontend/dist
var assets embed.FS

// marshalError 将结构化错误（errs.AppError）序列化为 JSON 传给前端，
// 前端从 err.cause 读取 {code, args, detail} 并按语言文件渲染。
// 非结构化错误（如透传的底层错误）走默认序列化。
func marshalError(err error) []byte {
	var appErr *errs.AppError
	if ok := errors.As(err, &appErr); ok {
		if b, jerr := json.Marshal(appErr); jerr == nil {
			return b
		}
	}
	return nil // 交给 Wails 默认序列化
}

func main() {
	// 单实例防护：多实例并存会各自持有 config 内存状态、写盘互相覆盖
	// （表现为删除项目后配置复活），检测到已有实例时提示并退出
	if !singleinstance.Acquire(`Local\PackGradle_SingleInstance`) {
		singleinstance.NotifyAlreadyRunning()
	}

	config, err := appconfig.NewConfigManager()
	if err != nil {
		log.Fatalf("初始化配置失败: %v", err)
	}
	// 旧版全局 [[links]]/[[dir_links]] 一次性迁移到项目级 packgradle.toml
	if err := config.MigrateLegacyProjectConfigs(); err != nil {
		log.Printf("迁移旧版项目关联配置失败: %v", err)
	}

	// 新架构（P1 只读核心）装配：SQLite 迁移失败必须阻止启动写操作。
	// 保留设置端口接同一 ConfigManager（config.toml [retention]，契约 06 §3.6），
	// SettingsService 随栈装配（票 #57）。
	newStackRoot, err := store.DefaultRoot()
	if err != nil {
		log.Fatalf("定位用户数据目录失败: %v", err)
	}
	newStack, err := bootstrap.BuildWithRetention(newStackRoot, config)
	if err != nil {
		log.Fatalf("新架构初始化失败: %v", err)
	}
	defer newStack.Close()
	// 启动触发通道①（票 #64，ADR-0007 §3）：启动后异步建 GC 任务
	//（幂等单飞；安全窗口未开时任务停 pending 自动续排）。
	newStack.StartGC()

	// 创建 Wails 应用。'Bind' 中注册的 Go 服务方法可供前端直接调用。
	// 新旧并存：legacy 三服务保持既有行为（已冻结）；SyncService 为 P1 只读核心出口，
	// ProjectService/RuntimeService 为端点管理出口（/sources、/runtimes 页），
	// SettingsService 为设置/开关域出口（契约 06 §2）。
	app := application.New(application.Options{
		Name:        "PackGradle",
		Description: "packwiz 与 Prism Launcher 整合包开发环境工具",
		Services: []application.Service{
			application.NewService(service.NewEnvService(config)),
			application.NewService(service.NewPackwizService(config)),
			application.NewService(service.NewPrismService(config)),
			application.NewService(newStack.Service),
			application.NewService(newStack.ProjectService),
			application.NewService(newStack.RuntimeService),
			application.NewService(newStack.Settings),
		},
		MarshalError: marshalError,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "PackGradle",
		Width:            1200,
		Height:           780,
		MinWidth:         940,
		MinHeight:        620,
		Frameless:        true,
		BackgroundColour: application.NewRGB(18, 18, 24),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
