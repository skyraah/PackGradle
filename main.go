package main

import (
	"embed"
	"encoding/json"
	"errors"
	"log"

	"packgradle/internal/appconfig"
	"packgradle/internal/errs"
	"packgradle/internal/service"

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
	config, err := appconfig.NewConfigManager()
	if err != nil {
		log.Fatalf("初始化配置失败: %v", err)
	}

	// 创建 Wails 应用。'Bind' 中注册的 Go 服务方法可供前端直接调用。
	app := application.New(application.Options{
		Name:        "PackGradle",
		Description: "packwiz 与 Prism Launcher 整合包开发环境工具",
		Services: []application.Service{
			application.NewService(service.NewEnvService(config)),
			application.NewService(service.NewPackwizService(config)),
			application.NewService(service.NewPrismService(config)),
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
		Title:  "PackGradle",
		Width:  1200,
		Height: 780,
		MinWidth:  940,
		MinHeight: 620,
		BackgroundColour: application.NewRGB(18, 18, 24),
		URL:             "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
