package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Ly233ly/download_video_new/internal/media"
	"github.com/Ly233ly/download_video_new/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc, err := New(Deps{Store: db})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return svc
}

func TestNew_RequiresStore(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("缺 Store 时应当装配失败")
	}
}

// 键不存在 → 回退默认值，且不报错（[03 §2.4]）。
func TestSetting_FallsBackWhenMissing(t *testing.T) {
	svc := newService(t)

	got, err := svc.Setting(context.Background(), "theme", "light")
	if err != nil {
		t.Fatalf("Setting 不应报错: %v", err)
	}
	if got != "light" {
		t.Errorf("got = %q，期望 light", got)
	}
}

// 值存在但类型不符（这里存的是数字）→ 同样回退默认值，**不得中断**（[03 §2.4]）。
func TestSetting_FallsBackWhenTypeMismatch(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	if err := svc.store.SetSetting(ctx, "theme", `12345`); err != nil {
		t.Fatalf("直接写库失败: %v", err)
	}
	got, err := svc.Setting(ctx, "theme", "light")
	if err != nil {
		t.Fatalf("解码失败不应报错: %v", err)
	}
	if got != "light" {
		t.Errorf("got = %q，期望回退为 light", got)
	}
}

func TestSetSetting_ThenSetting(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	if err := svc.SetSetting(ctx, "theme", "dark"); err != nil {
		t.Fatalf("SetSetting 失败: %v", err)
	}
	got, err := svc.Setting(ctx, "theme", "light")
	if err != nil {
		t.Fatalf("Setting 失败: %v", err)
	}
	if got != "dark" {
		t.Errorf("got = %q，期望 dark", got)
	}
}

func TestTools_ReturnsSnapshot(t *testing.T) {
	svc := newService(t)

	if got := svc.Tools(); got != nil {
		t.Errorf("未提供 Tools 时应返回 nil，实际 %v", got)
	}

	svc.tools = &media.Toolset{
		FFmpeg: media.Tool{Name: media.ToolFFmpeg, Path: `C:\x\ffmpeg.exe`, Version: "8.1.2"},
	}
	tools := svc.Tools()
	if len(tools) != 4 {
		t.Fatalf("工具数量 = %d，期望 4", len(tools))
	}
	if tools[0].Version != "8.1.2" {
		t.Errorf("ffmpeg 版本 = %q", tools[0].Version)
	}
}
