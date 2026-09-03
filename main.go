package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"packgradle/internal/appconfig"
	"packgradle/internal/bootstrap"
	"packgradle/internal/errs"
	"packgradle/internal/notify"
	"packgradle/internal/service"
	"packgradle/internal/sessionlog"
	"packgradle/internal/singleinstance"
	"packgradle/internal/store"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// Wails 会将 frontend/dist 下的文件嵌入二进制，作为前端资源服务器。
// 参见 https://pkg.go.dev/embed

//go:embed all:frontend/dist
var assets embed.FS

// localeZH 是唯一文案源 zh-CN.json（前端 i18n 同源；票 #97 起系统通知文案等
// 后端可见的非 err.* 界面文案也从此取，不再有 Go 侧第二文案出处）。
//
//go:embed frontend/src/locales/zh-CN.json
var localeZH []byte

// loadNotifyCopy 从 zh-CN.json 取系统通知文案（票 #97）。解析失败或键缺失退
// notify.DefaultCopy 同文缺省，不阻断启动、不静默丢通知。
func loadNotifyCopy() notify.Copy {
	var kv map[string]string
	if err := json.Unmarshal(localeZH, &kv); err != nil {
		slog.Warn("系统通知文案解析失败（退内置缺省）", "err", err)
		return notify.DefaultCopy()
	}
	return notify.NewCopy(kv["notify.pendingPlan.title"], kv["notify.pendingPlan.body"])
}

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

	// 会话日志（ADR-0011 §1，票 #91）：先定位用户数据根并挂 slog JSON
	// 会话出口（logs/<启动时间戳>/session.log + 启动时保留清理），之后的
	// 启动失败全部进会话文件——Windows GUI 子系统 stderr 无处落地，运行期
	// 日志不得再丢。定位根或建会话文件失败退回 stderr 默认出口，不阻断启动。
	root, err := store.DefaultRoot()
	if err != nil {
		slog.Error("定位用户数据目录失败", "err", err)
		os.Exit(1)
	}
	sess, serr := sessionlog.Open(filepath.Join(root, "logs"), sessionlog.Options{})
	if serr != nil {
		slog.Error("会话日志初始化失败（退回 stderr 出口）", "err", serr)
	} else {
		slog.SetDefault(sess.Logger)
		defer sess.Close()
	}

	config, err := appconfig.NewConfigManager()
	if err != nil {
		slog.Error("初始化配置失败", "err", err)
		os.Exit(1)
	}
	// 旧版全局 [[links]]/[[dir_links]] 一次性迁移到项目级 packgradle.toml
	if err := config.MigrateLegacyProjectConfigs(); err != nil {
		slog.Warn("迁移旧版项目关联配置失败", "err", err)
	}

	// 新架构（P1 只读核心）装配：SQLite 迁移失败必须阻止启动写操作。
	// 保留设置端口接同一 ConfigManager（config.toml [retention]，契约 06 §3.6），
	// SettingsService 随栈装配（票 #57）。
	newStack, err := bootstrap.BuildWithRetention(root, config)
	if err != nil {
		slog.Error("新架构初始化失败", "err", err)
		os.Exit(1)
	}
	defer newStack.Close()
	// 启动触发通道①（票 #64，ADR-0007 §3）：启动后异步建 GC 任务
	//（幂等单飞；安全窗口未开时任务停 pending 自动续排）。
	newStack.StartGC()
	// 监听引擎常驻（票 #92，ADR-0010 §4）：应用运行期对全部健康 relation
	// 常驻监听（窗口开闭无关）；事件源不可用已在装配期降级回手动。
	newStack.StartWatcher()

	// 系统通知平台面装配（票 #97，契约 07 §3.5）：Windows=wails notifications
	// 服务（wintoast 子包，随已 pin 的 wails 模块）；文案取 zh-CN locale 唯一
	// 文案源（非 err.* 界面文案也入 locale）；工作区显示名供 toast 文案槽。
	// 非 Windows 构建恒不弹（v1 平台面，横幅/角标照常）。headless 工具不经此处。
	notify.AttachWails(newStack.Notify, loadNotifyCopy(), func(relationID string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w, err := newStack.App.GetWorkspace(ctx, relationID)
		if err != nil {
			return ""
		}
		return w.Relation.Project.DisplayName
	})

	// 创建 Wails 应用。'Bind' 中注册的 Go 服务方法可供前端直接调用。
	// 新旧并存：legacy 三服务保持既有行为（已冻结）；SyncService 为 P1 只读核心出口，
	// ProjectService/RuntimeService 为端点管理出口（/sources、/runtimes 页），
	// SettingsService 为设置/开关域出口（契约 06 §2）。notifications 服务
	//（票 #97）提供 toast 发送面与 ServiceStartup（wintoast 注册/激活回调）。
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
			application.NewService(notifications.New()),
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
		slog.Error("wails 应用运行失败", "err", err)
		os.Exit(1)
	}
}
