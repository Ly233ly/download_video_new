// 留底下载器 · 主程序入口（装配层）。
//
// 本文件与 app.go 只做装配：任何业务逻辑都在 internal/（[12 §1.1]）。
// 入口必须在仓库根：Wails v2 的 `wails build` 在 wails.json 所在目录执行
// `go build .`（实测：主包放子目录会报 `no Go files`），且前端产物经
// `//go:embed` 嵌入——而 embed **不允许 `..` 路径**。
package main

import (
	"context"
	"embed"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/Ly233ly/download_video_new/internal/app"
	"github.com/Ly233ly/download_video_new/internal/ui"
)

//go:embed all:frontend/dist
var assets embed.FS

// 托盘图标与 Wails 的窗口图标共用同一份文件，不在仓库里存第二份（设计原则 3）。
//
//go:embed build/windows/icon.ico
var trayIcon []byte

// debugEnabled 读环境变量 `LIUDI_DEBUG=1` 开启 Debug 级别（[12 §4.2]：默认关闭）。
func debugEnabled() bool { return os.Getenv("LIUDI_DEBUG") == "1" }

func main() {
	// 绑定生成模式（`-tags bindings`）：Wails 会执行 main() 但不触发任何生命周期钩子，
	// 因此这里必须跳过全部真实初始化（见 bindings_mode_normal.go）。
	var core *app.Core
	if !bindingsMode {
		var err error
		core, err = app.Bootstrap(app.Options{TrayIcon: trayIcon, Debug: debugEnabled()})
		if err != nil {
			slog.Error("启动失败", "component", "app", "event", "bootstrap_failed", "err", err)
			os.Exit(1)
		}
		if core == nil {
			// 已有实例在运行：本进程已完成唤醒并应退出（B-1002、T-STB-02）。
			return
		}
		defer core.Close()
	}

	hooks := &lifecycle{core: core}

	// Wails 绑定层与（阶段 2 的）HTTP handler 必须共用**同一个** Service（[04 §1.2]）。
	var (
		uiBinding    *ui.UI
		initialTheme = ui.ThemeDefault
	)
	if core != nil {
		binding, err := ui.New(core.Service())
		if err != nil {
			slog.Error("绑定层装配失败", "component", "ui", "event", "binding_failed", "err", err)
			return // defer core.Close() 仍会执行
		}
		uiBinding = binding
		initialTheme = ui.InitialTheme(context.Background(), core.Service())
	}
	bound := []interface{}{}
	if uiBinding != nil {
		bound = append(bound, uiBinding)
	}

	// 窗口尺寸是阶段 1 的临时值；最终值属 [09 §9] 的 U4（阶段 6）。
	err := wails.Run(&options.App{
		Title:     app.ProductName,
		Width:     1180,
		Height:    760,
		MinWidth:  940,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
			// 首帧主题由中间件注入（B-803），实现见 internal/ui/asset_theme.go。
			Middleware: ui.ThemeMiddleware(initialTheme),
		},
		OnStartup:     hooks.startup,
		OnShutdown:    hooks.shutdown,
		OnBeforeClose: hooks.beforeClose,
		Bind:          bound,
	})
	if err != nil {
		slog.Error("窗口运行失败", "component", "app", "event", "window_failed", "err", err)
		os.Exit(1)
	}
}
