package app

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"fyne.io/systray"
)

// tray 是系统托盘（D1 交付物）。
//
// 库选型：`fyne.io/systray` —— Windows 上纯 Go（无 CGO），本机无 gcc 也能构建，
// 已实测（[CONTEXT §3.4] 明确不要为 Wails 装 MinGW）。许可证 MIT。
//
// 气泡通知（B-810）**当前未实现**——见 notify 的注释：本库没有通知 API。
type tray struct {
	core *Core
	icon []byte
	ctx  context.Context
	wg   *sync.WaitGroup

	// ready 表示托盘图标已注册（onReady 之后、onExit 之前）。
	// ShowMessage 在图标尚未注册时没有可挂载的通知区域，因此必须由它把关。
	ready atomic.Bool
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
	t.ready.Store(true)

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
	t.ready.Store(false)
	slog.Info("托盘已退出", "component", "app", "event", "tray_exit")
}

// notify 本应弹一次托盘气泡（B-810），**但当前托盘库没有这个能力**。
//
// 实测事实（2026-10-02）：`fyne.io/systray@v1.12.2` 的公开 API 只有
// SetIcon / SetIconFromFilePath / SetTitle / SetTooltip / AddMenuItem / Quit 等，
// **没有任何通知函数**——全库 grep 无 ShowMessage / Notify / NIF_INFO。
// 它内部持有 Shell_NotifyIcon 的 nid 与窗口句柄且未导出，因此也无法在库外
// 自行补一条 NIF_INFO。
//
// 所以这里按"不静默降级"的要求**记一条 Warn 并返回**，而不是假装通知已发出。
// 用户硬性需求第 3 条（下载完成后托盘气泡提示）的实现路径待定：换一个带通知
// 能力的托盘库，或引入 Windows toast——两者都是依赖变更，须按 [12 §8.2]
// 说明用途与许可证，并在 T-UI-09 验收前落地。该缺口记入 [13 §7.7]。
func (t *tray) notify(title, message string) {
	if t == nil || !t.ready.Load() {
		return
	}
	slog.Warn("托盘通知未实现（当前托盘库无通知 API）",
		"component", "app", "event", "tray_notify_unsupported",
		"title", title, "detail", message)
}

// stop 结束托盘消息循环；重复调用安全。
func (t *tray) stop() {
	systray.Quit()
}
