// Package logging 是结构化日志：字段固定、按大小轮转、敏感值兜底过滤（[12 §4]）。
//
// 字段约定（[12 §4.1]）：`level` / `time` / `component` / `event` / `code` + 非敏感上下文标识。
// 调用方负责带上 `component` 与 `event`，例如：
//
//	slog.Info("启动完成", "component", "app", "event", "bootstrapped")
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	logFileName = "liudi.log"

	// 轮转上限（[12 §4.5]）：按大小轮转，默认 10 MB × 5 个文件，避免无限增长。
	maxSizeMB   = 10
	maxBackups  = 5
	logFileMode = 0o600
)

// SlowThreshold 是关键操作的耗时阈值，超过即记一条结构化事件（[12 §4.4]）。
const SlowThreshold = 50 * time.Millisecond

// sensitiveKeys 是敏感键名的**子串**（小写匹配）。
//
// 这是**兜底**，不是许可：真正的规则是"日志中不得出现任何秘密"（[12 §4.3]）。
// 兜底的意义是"写日志的人漏了也不会把秘密落盘"——符合 README 设计原则 2
// （对手是程序自己的 bug，不是攻击者）。
//
// 刻意**不含** `url`：日志排错需要普通 URL，规范禁止的是"完整签名 URL"。
var sensitiveKeys = []string{
	"cookie", "authorization", "decode_key", "decrypt",
	"token", "secret", "password", "credential",
}

// Options 是日志初始化参数。
type Options struct {
	// Dir 是日志目录；为空表示只写 stderr。
	Dir string
	// Debug 是否启用 Debug 级别（[12 §4.2]：默认关闭）。
	Debug bool
	// Console 是否同时写 stderr。
	Console bool
}

// Setup 初始化全局 logger，返回的关闭函数应在退出时调用。
func Setup(opts Options) (func() error, error) {
	level := slog.LevelInfo
	if opts.Debug {
		level = slog.LevelDebug
	}

	var rotator *lumberjack.Logger
	writers := make([]io.Writer, 0, 2)

	if opts.Dir != "" {
		if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建日志目录失败: %w", err)
		}
		rotator = &lumberjack.Logger{
			Filename:   filepath.Join(opts.Dir, logFileName),
			MaxSize:    maxSizeMB,
			MaxBackups: maxBackups,
			Compress:   false,
		}
		writers = append(writers, rotator)
	}
	if opts.Console || rotator == nil {
		writers = append(writers, os.Stderr)
	}
	if len(writers) == 0 {
		return nil, errors.New("日志未配置任何输出目标")
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redact,
	})))

	return func() error {
		if rotator != nil {
			return rotator.Close()
		}
		return nil
	}, nil
}

// redact 兜底替换敏感字段的值，绝不原样落盘。
func redact(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.TimeKey, slog.LevelKey, slog.MessageKey:
		return a
	}
	lower := strings.ToLower(a.Key)
	for _, key := range sensitiveKeys {
		if strings.Contains(lower, key) {
			return slog.String(a.Key, "[已隐藏]")
		}
	}
	return a
}

// Slow 返回一个供 defer 调用的函数：操作耗时达到阈值时记一条 Warn 事件（[12 §4.4]）。
//
//	func Download(...) {
//	    defer logging.Slow("media", "download_step", time.Now())()
//	    ...
//	}
func Slow(component, event string, start time.Time) func() {
	return func() {
		if d := time.Since(start); d >= SlowThreshold {
			slog.Warn("操作耗时超阈值",
				"component", component, "event", event,
				"ms", d.Milliseconds(), "threshold_ms", SlowThreshold.Milliseconds())
		}
	}
}
