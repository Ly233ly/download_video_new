// Package app 是应用装配与生命周期：进程级资源的建立与释放、启动顺序、托盘（[01 §3]）。
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Ly233ly/download_video_new/internal/logging"
	"github.com/Ly233ly/download_video_new/internal/media"
	"github.com/Ly233ly/download_video_new/internal/platform"
	"github.com/Ly233ly/download_video_new/internal/proxy"
	"github.com/Ly233ly/download_video_new/internal/service"
	"github.com/Ly233ly/download_video_new/internal/store"
)

// ProductName 是面向用户的产品全称（B-101）。
const ProductName = "留底下载器"

// shutdownStepTimeout 是关闭时单个步骤的上限（[01 §5.3]：2 s，超限跳过并记日志，不阻塞后续步骤）。
const shutdownStepTimeout = 2 * time.Second

// Options 是 main 包传入的装配参数。资源由 main 负责嵌入（main.go），
// 这里只接收字节——避免同一份图标在仓库里存两处（设计原则 3）。
type Options struct {
	TrayIcon []byte
	// Debug 开启 Debug 级别日志（[12 §4.2]：默认关闭）。
	Debug bool
	// Console 让日志同时写 stderr。
	Console bool
}

// Core 持有进程级资源与生命周期。
type Core struct {
	instance *platform.Instance
	db       *store.DB
	tools    *media.Toolset
	svc      *service.Service
	tray     *tray
	opts     Options
	closeLog func() error

	// warnings 是启动期的可读警告（[01 §6.3] 的 P2 要求恢复失败对用户可见）。
	// 只在 Bootstrap 内写入，之后只读，因此不需要加锁。
	warnings []string

	uiMu  sync.RWMutex
	uiCtx context.Context

	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Bootstrap 按 [01 §5.1] 的顺序启动，**顺序不可调换**。
//
// 已实现：① 单实例互斥体检查（B-1002）、② 代理恢复（D6，排在数据库之前且不依赖它）、
// ③ 打开数据库（[03 §7]）。未实现的步骤留待各自交付物：④ 配置=D4、⑤ 本地 API=阶段 2、
// ⑥ inbox 消费=阶段 4、⑦ 后台循环=阶段 4、⑧ Wails UI=main.go。
//
// 返回 (nil, nil) 表示"已有实例在运行，本进程已唤醒它并应直接退出"（T-STB-02）。
func Bootstrap(opts Options) (*Core, error) {
	inst, acquired, err := platform.Acquire()
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, nil
	}

	core := &Core{instance: inst, opts: opts}

	// 日志（D5）：横切能力，早于代理恢复——恢复结果必须能落盘。
	logDir, err := platform.LogDir()
	if err != nil {
		inst.Close()
		return nil, err
	}
	closeLog, err := logging.Setup(logging.Options{Dir: logDir, Debug: opts.Debug, Console: opts.Console})
	if err != nil {
		inst.Close()
		return nil, err
	}
	core.closeLog = closeLog

	// 【启动第 2 步】代理恢复（D6）。
	// 它必须排在最前且不依赖数据库/配置/UI：数据库损坏或版本过新都可能让后续步骤终止，
	// 那时用户会带着一个失效的系统代理断网（[01 §5.1]、[01 §6.3] 的 P1）。
	if err := core.restoreProxy(); err != nil {
		// P2：失败保留凭据文件并给出可读提示，但**不终止启动**——用户还需要能打开程序处理。
		core.warnings = append(core.warnings, err.Error())
		slog.Error("代理恢复未完成", "component", "proxy", "event", "restore_incomplete", "err", err)
	}

	// 【启动第 3 步】打开数据库。
	dbPath, err := platform.DatabasePath()
	if err != nil {
		core.closeResources()
		return nil, err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		core.closeResources()
		return nil, err
	}
	core.db = db

	// 【D7】解析随包媒体工具。缺工具**不阻塞启动**（阶段 1 尚无下载能力），
	// 但缺失必须可见——README 设计原则：不静默降级。
	if exePath, err := os.Executable(); err != nil {
		core.warnings = append(core.warnings, "无法定位程序路径，媒体工具未解析")
		slog.Warn("定位程序路径失败", "component", "media", "event", "exe_path_failed", "err", err)
	} else {
		tools, toolsErr := media.ResolveDefault(exePath)
		core.tools = tools
		if toolsErr != nil {
			core.warnings = append(core.warnings, toolsErr.Error())
			slog.Warn("媒体工具不完整", "component", "media", "event", "tools_incomplete", "err", toolsErr)
		} else {
			slog.Info("媒体工具已就绪", "component", "media", "event", "tools_ready")
		}
	}

	// 【D4】Service 层：Wails 绑定与 HTTP handler 的**唯一**业务入口（[04 §1.2]）。
	svc, err := service.New(service.Deps{Store: db, Tools: core.tools})
	if err != nil {
		core.closeResources()
		return nil, err
	}
	core.svc = svc

	slog.Info("启动完成", "component", "app", "event", "bootstrapped")
	return core, nil
}

// Service 返回业务入口，供 Wails 绑定层与（阶段 2 的）HTTP handler 共用。
func (c *Core) Service() *service.Service { return c.svc }

// StartupWarnings 返回启动期警告，供界面在就绪后提示用户。
func (c *Core) StartupWarnings() []string { return c.warnings }

// AttachUI 由 Wails 的 OnStartup 调用：拿到 UI 上下文后启动托盘与唤醒监听。
func (c *Core) AttachUI(ctx context.Context) {
	c.uiMu.Lock()
	c.uiCtx = ctx
	c.uiMu.Unlock()

	c.runCtx, c.cancel = context.WithCancel(context.Background())

	c.startTray(c.runCtx, c.opts.TrayIcon)

	// 重复启动或 IDM hook 发来唤醒事件 → 把窗口带到前台。
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.instance.Wait(c.runCtx, c.ShowWindow)
	}()

	for _, w := range c.warnings {
		slog.Warn("启动警告", "component", "app", "event", "startup_warning", "detail", w)
	}
	slog.Info("界面已就绪", "component", "app", "event", "ui_attached")
}

// ShowWindow 显示并前置主窗口（托盘菜单与唤醒事件都走这里）。
func (c *Core) ShowWindow() {
	ctx := c.ui()
	if ctx == nil {
		return
	}
	runtime.WindowShow(ctx)
	runtime.WindowUnminimise(ctx)
}

// HideWindow 隐藏主窗口（B-813：关闭窗口即隐藏，进程仍在托盘）。
func (c *Core) HideWindow() {
	if ctx := c.ui(); ctx != nil {
		runtime.WindowHide(ctx)
	}
}

// Quit 请求退出（托盘菜单或界面动作，B-812）。
func (c *Core) Quit() {
	if ctx := c.ui(); ctx != nil {
		runtime.Quit(ctx)
	}
}

// Shutdown 按 [01 §5.2] 的顺序收尾。每一步都有独立预算，
// **任一步超时都不得阻塞后续步骤**（[01 §5.3]）。
func (c *Core) Shutdown(_ context.Context) {
	if c.cancel != nil {
		c.cancel()
	}
	if c.tray != nil {
		c.tray.stop() // 让 systray 消息循环返回
	}

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownStepTimeout):
		slog.Warn("后台任务未在预算内结束", "component", "app", "event", "shutdown_timeout")
	}

	// TODO(阶段 5)：先停止捕获子进程，再恢复代理。
	// [01 §6.3] 的 P5：退出也必须执行恢复；无凭据文件时为无操作。
	if err := c.restoreProxy(); err != nil {
		slog.Warn("退出时代理恢复失败", "component", "proxy", "event", "restore_failed", "err", err)
	}

	c.closeResources()
	slog.Info("已退出", "component", "app", "event", "stopped")
}

// Close 释放进程级句柄。由 main 在 wails.Run 返回后调用。
func (c *Core) Close() {
	c.closeResources()
	if c.instance != nil {
		c.instance.Close()
	}
}

// restoreProxy 执行一次代理恢复检查，带 [01 §5.3] 的 3 s 预算。
func (c *Core) restoreProxy() error {
	path, err := platform.ProxyRestorePath()
	if err != nil {
		return err
	}

	type result struct {
		outcome proxy.Outcome
		err     error
	}
	// 结果通道带缓冲：即使超时返回，这个 goroutine 也能结束而不泄漏（[12 §9] 的 C1/C3）。
	ch := make(chan result, 1)
	go func() {
		defer logging.Slow("proxy", "proxy_restore", time.Now())()
		outcome, err := proxy.Restore(path, proxy.RegistryBackend{})
		ch <- result{outcome: outcome, err: err}
	}()

	select {
	case r := <-ch:
		slog.Info("代理恢复检查完成",
			"component", "proxy", "event", "restore_checked", "outcome", string(r.outcome))
		return r.err
	case <-time.After(proxy.RestoreTimeout):
		return fmt.Errorf("代理恢复超过 %s 预算，请手动检查系统代理设置", proxy.RestoreTimeout)
	}
}

// closeResources 关闭可重复关闭的资源；失败只记日志，不阻断退出（[01 §5.2]）。
func (c *Core) closeResources() {
	if c.db != nil {
		if err := c.db.Close(); err != nil {
			slog.Warn("关闭数据库失败", "component", "store", "event", "close_failed", "err", err)
		}
		c.db = nil
	}
	if c.closeLog != nil {
		if err := c.closeLog(); err != nil {
			slog.Warn("关闭日志失败", "component", "logging", "event", "close_failed", "err", err)
		}
		c.closeLog = nil
	}
}

func (c *Core) ui() context.Context {
	c.uiMu.RLock()
	defer c.uiMu.RUnlock()
	return c.uiCtx
}
