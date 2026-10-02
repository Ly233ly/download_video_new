package app

import (
	"context"
	"log/slog"
	"sync"

	"fyne.io/systray"
)

// tray 是系统托盘（D1 交付物）。
//
// 库选型：`fyne.io/systray` —— Windows 上纯 Go（无 CGO），本机无 gcc 也能构建，
// 已实测（[CONTEXT §3.4] 明确不要为 Wails 装 MinGW）。许可证 MIT。
//
// 气泡通知（B-810）在阶段 2 接入下载完成事件时使用：该库的 ShowMessage 走
// Shell_NotifyIcon 的气泡通道，正好是"下载完成后托盘气泡提示"这条用户要求。
type tray struct {
	core *Core
	icon []byte
	ctx  context.Context
	wg   *sync.WaitGroup
}

// startTray 在独立 goroutine 里跑托盘消息循环。
// systray.Run 会阻塞，且必须与 Wails 的主循环分开。
func (c *Core) startTray(ctx context.Context, icon []byte) {
	t := &tray{core: c, icon: icon, ctx: ctx, wg: &c.wg}
	c.tray = t

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		systray.Run(t.onReady, t.onExit)
	}()
}

func (t *tray) onReady() {
	if len(t.icon) > 0 {
		systray.SetIcon(t.icon)
	}
	systray.SetTitle(ProductName)
	systray.SetTooltip(ProductName)

	show := systray.AddMenuItem("显示主窗口", "显示"+ProductName+"窗口")
	quit := systray.AddMenuItem("退出", "退出"+ProductName)

	// 菜单事件循环：可取消、可等待，随 runCtx 结束（[12 §9] 的 C1）。
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			select {
			case <-t.ctx.Done():
				return
			case <-show.ClickedCh:
				t.core.ShowWindow()
			case <-quit.ClickedCh:
				t.core.Quit()
				return
			}
		}
	}()
}

func (t *tray) onExit() {
	slog.Info("托盘已退出", "component", "app", "event", "tray_exit")
}

// stop 结束托盘消息循环；重复调用安全。
func (t *tray) stop() {
	systray.Quit()
}
