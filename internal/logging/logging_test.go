package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 保存并恢复全局 logger：测试之间不得共享可变全局状态（[12 §5.3]）。
func swapDefault(t *testing.T) {
	t.Helper()
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	return string(raw)
}

// Debug 默认关闭、Info 落盘、字段齐全（[12 §4.1]、[12 §4.2]）。
func TestSetup_WritesFileAndHonoursLevel(t *testing.T) {
	swapDefault(t)
	dir := t.TempDir()

	closeFn, err := Setup(Options{Dir: dir})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	defer func() { _ = closeFn() }()

	slog.Debug("开发诊断", "component", "test", "event", "debug_event")
	slog.Info("生命周期", "component", "test", "event", "info_event")

	text := readLog(t, dir)
	if strings.Contains(text, "debug_event") {
		t.Error("Debug 级别默认应关闭")
	}
	if !strings.Contains(text, "info_event") {
		t.Errorf("Info 日志未落盘: %q", text)
	}
	if !strings.Contains(text, "component=test") {
		t.Errorf("缺少 component 字段: %q", text)
	}
}

func TestSetup_DebugEnabled(t *testing.T) {
	swapDefault(t)
	dir := t.TempDir()

	closeFn, err := Setup(Options{Dir: dir, Debug: true})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	defer func() { _ = closeFn() }()

	slog.Debug("开发诊断", "component", "test", "event", "debug_event")
	if text := readLog(t, dir); !strings.Contains(text, "debug_event") {
		t.Errorf("Debug 开启后仍未落盘: %q", text)
	}
}

// 敏感值必须被兜底过滤（[12 §4.3]）。
func TestSetup_RedactsSensitiveValues(t *testing.T) {
	swapDefault(t)
	dir := t.TempDir()

	closeFn, err := Setup(Options{Dir: dir})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	defer func() { _ = closeFn() }()

	slog.Info("带秘密",
		"component", "test", "event", "secret_event",
		"cookie", "SID=abc123",
		"authorization", "Bearer xyz",
		"decode_key", "0011aabb")

	text := readLog(t, dir)
	for _, leaked := range []string{"SID=abc123", "Bearer xyz", "0011aabb"} {
		if strings.Contains(text, leaked) {
			t.Errorf("敏感值 %q 未被过滤: %q", leaked, text)
		}
	}
	if !strings.Contains(text, "[已隐藏]") {
		t.Errorf("未出现隐藏标记: %q", text)
	}
}

// 耗时达到阈值必须记一条事件（[12 §4.4]）。
func TestSlow_LogsAboveThreshold(t *testing.T) {
	swapDefault(t)
	dir := t.TempDir()

	closeFn, err := Setup(Options{Dir: dir})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	defer func() { _ = closeFn() }()

	done := Slow("test", "slow_event", time.Now())
	time.Sleep(SlowThreshold + 20*time.Millisecond)
	done()

	if text := readLog(t, dir); !strings.Contains(text, "slow_event") {
		t.Errorf("超阈值操作未记录: %q", text)
	}
}

func TestSlow_SilentBelowThreshold(t *testing.T) {
	swapDefault(t)
	dir := t.TempDir()

	closeFn, err := Setup(Options{Dir: dir})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	defer func() { _ = closeFn() }()

	Slow("test", "fast_event", time.Now())()

	if raw, err := os.ReadFile(filepath.Join(dir, logFileName)); err == nil {
		if strings.Contains(string(raw), "fast_event") {
			t.Error("未超阈值的操作不应记录")
		}
	}
}
