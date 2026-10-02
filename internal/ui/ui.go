// Package ui 是 Wails 绑定层（[01 §3]）：**只做参数校验与转发**，
// 业务规则一律在 internal/service（[04 §1.2] 的 G5、[01 §9] 的 N9）。
//
// 绑定方法的路径是 `window.go.ui.UI.<Method>`，前端的唯一调用点是
// `frontend/src/bindings/index.ts`（[16 §3.1] 的 S1）。
package ui

import (
	"context"
	"errors"
	"fmt"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// 主题取值（[09 §4.3]：浅色、深色）。
const (
	ThemeLight = "light"
	ThemeDark  = "dark"
)

// ThemeDefault 是首次启动的默认主题（B-804：首启为浅色）。
const ThemeDefault = ThemeLight

// ThemeSettingKey 是主题在 settings 表里的键（[03 §2.4]）。
const ThemeSettingKey = "theme"

// UI 是暴露给前端的绑定对象。
type UI struct {
	svc *service.Service
}

// New 装配绑定层。缺 Service 属于装配错误：启动期直接失败（[12 §3.2]）。
func New(svc *service.Service) (*UI, error) {
	if svc == nil {
		return nil, errors.New("绑定层需要 Service 依赖")
	}
	return &UI{svc: svc}, nil
}

// GetTheme 返回当前主题。
//
// 读取失败或取值非法时返回默认值而不是报错——[03 §2.4]：解码失败必须回退到
// 调用方默认值，不得中断启动。界面因此总能拿到一个可渲染的主题。
func (u *UI) GetTheme(ctx context.Context) (string, error) {
	theme, err := u.svc.Setting(ctx, ThemeSettingKey, ThemeDefault)
	if err != nil {
		return ThemeDefault, nil
	}
	return normalizeTheme(theme), nil
}

// SetTheme 持久化主题（B-804）。非法取值属于输入错误：直接拒绝，不重试（[12 §3.2]）。
func (u *UI) SetTheme(ctx context.Context, theme string) error {
	if theme != ThemeLight && theme != ThemeDark {
		return fmt.Errorf("未知的主题取值")
	}
	return u.svc.SetSetting(ctx, ThemeSettingKey, theme)
}

func normalizeTheme(theme string) string {
	if theme == ThemeDark {
		return ThemeDark
	}
	return ThemeLight
}
