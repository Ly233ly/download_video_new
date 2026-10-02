// Wails 生命周期接线：把框架回调转发给 internal/app，本文件不含业务逻辑。
package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Ly233ly/download_video_new/internal/app"
)

type lifecycle struct {
	// core 在绑定生成模式下为 nil（见 main.go）——该模式下钩子不会被触发，
	// 但保留判空以免将来误用。
	core *app.Core
}

func (l *lifecycle) startup(ctx context.Context) {
	if l.core == nil {
		return
	}
	l.core.AttachUI(ctx)
}

func (l *lifecycle) shutdown(ctx context.Context) {
	if l.core == nil {
		return
	}
	l.core.Shutdown(ctx)
}

// beforeClose 让"关闭窗口"退化为隐藏到托盘（B-812/B-813）：
// 退出只由托盘菜单或界面动作触发，返回 true 阻止窗口真正关闭。
func (l *lifecycle) beforeClose(ctx context.Context) bool {
	if l.core == nil {
		return false
	}
	runtime.WindowHide(ctx)
	return true
}
