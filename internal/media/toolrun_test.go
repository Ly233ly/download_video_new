package media

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 本文件里的"子进程"用例分两类：
//
//   - 进程行为类（捕获输出、非零退出、输出上限）用**测试二进制自身**当子进程：
//     它必定存在、退出码可精确指定，不依赖 cmd.exe/powershell 的命令行怪癖。
//   - 终止行为类（超时、取消）需要用会长时间驻留的外部命令，
//     找不到就跳过并**说明原因**（[12 §5.3]：跳过失败测试必须记录原因）。
const (
	helperEnvKey   = "LIUDI_DOWNLOAD_TEST_HELPER"
	helperStdout   = "helper-stdout-内容"
	helperStderr   = "helper-stderr-细节"
	helperExitCode = 7
)

// TestMain 让测试二进制在被子进程方式调用时扮演"媒体工具"。
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnvKey); mode != "" {
		runHelperProcess(mode)
		return
	}
	os.Exit(m.Run())
}

// runHelperProcess 是子进程侧的行为，只由上面的 TestMain 调用。
func runHelperProcess(mode string) {
	switch mode {
	case "ok":
		_, _ = os.Stdout.WriteString(helperStdout + "\n")
		os.Exit(0)
	case "countargs":
		// 关键：这一行必须在测试框架解析标志之前执行——参数若被拼接，
		// `os.Args` 就散不回原样（`-test.run=TestMain` 也会被吃掉）。
		_, _ = os.Stdout.WriteString(strconv.Itoa(len(os.Args)-1) + "\n")
		os.Exit(0)
	case "fail":
		_, _ = os.Stdout.WriteString(helperStdout + "\n")
		_, _ = os.Stderr.WriteString(helperStderr + "\n")
		os.Exit(helperExitCode)
	case "flood":
		for i := 0; i < 4096; i++ {
			_, _ = os.Stdout.WriteString("0123456789abcdef\n")
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

// helperToolCall 造一次"把测试二进制当工具调用"的请求。
func helperToolCall(t *testing.T, mode string, opts ToolCall) ToolCall {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试二进制路径失败: %v", err)
	}
	opts.Path = exe
	opts.Args = []string{"-test.run=TestMain"}
	t.Setenv(helperEnvKey, mode)
	return opts
}

// 正常退出：stdout 被完整捕获，退出码为 0，不算失败（[05 §5.2]）。
func TestProcessRunner_CapturesStdoutAndZeroExit(t *testing.T) {
	call := helperToolCall(t, "ok", ToolCall{Timeout: 60 * time.Second})
	out, err := ProcessRunner{}.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if out.ExitFailure {
		t.Fatalf("正常退出不应标记为失败（退出码 %d）", out.ExitCode)
	}
	if !strings.Contains(string(out.Stdout), helperStdout) {
		t.Errorf("stdout = %q，期望包含 %q", out.Stdout, helperStdout)
	}
	if out.Duration <= 0 {
		t.Error("必须记录耗时（[12 §4.4] 的耗时口径）")
	}
	if out.Truncated {
		t.Error("未超限时不得标记截断")
	}
}

// 非零退出码要**结构化上报**：退出码给调用方映射错误码，
// stderr 只留给本地日志，不拼进用户可见消息（[05 §5.2]）。
func TestProcessRunner_NonZeroExitIsStructured(t *testing.T) {
	call := helperToolCall(t, "fail", ToolCall{Timeout: 60 * time.Second})
	out, err := ProcessRunner{}.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("非零退出不应是执行层错误: %v", err)
	}
	if !out.ExitFailure {
		t.Fatal("非零退出必须标记 ExitFailure")
	}
	if out.ExitCode != helperExitCode {
		t.Errorf("退出码 = %d，期望 %d", out.ExitCode, helperExitCode)
	}
	if !strings.Contains(string(out.Stderr), helperStderr) {
		t.Errorf("stderr = %q，期望包含 %q", out.Stderr, helperStderr)
	}
}

// 输出上限必须真的生效：超限截断并置 `Truncated`，而不是把内存吃光（[05 §5.2]）。
func TestProcessRunner_TruncatesOutputBeyondLimit(t *testing.T) {
	call := helperToolCall(t, "flood", ToolCall{Timeout: 60 * time.Second, OutputLimit: 64})
	out, err := ProcessRunner{}.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if len(out.Stdout) > 64 {
		t.Errorf("stdout 长度 = %d，超过上限 64", len(out.Stdout))
	}
	if !out.Truncated {
		t.Error("超限必须置 Truncated")
	}
	if out.ExitFailure {
		t.Error("截断不等于失败：工具本身是正常退出的")
	}
}

// 参数数组必须逐个传给子进程，不经任何 shell 拼接（[05 §5.2]）。
//
// 用测试二进制自身作工具的好处：参数里的空格、引号与 shell 元字符若被
// 拼接成一个字符串，`os.Args` 就散不回原样，这里用"计数"来验证这一点。
func TestProcessRunner_PassesArgumentsAsArray(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试二进制路径失败: %v", err)
	}
	t.Setenv(helperEnvKey, "countargs")

	out, err := ProcessRunner{}.Run(context.Background(), ToolCall{
		Path: exe,
		// 参数里刻意带空格、引号与 shell 元字符（它们都不能被重新切分或转义）。
		Args: []string{
			"-test.run=TestMain",
			"带 空格",
			`"引号"`,
			`与 & 符号`,
		},
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if out.ExitFailure {
		t.Fatalf("子进程应当正常退出（exit=%d, stderr=%q）", out.ExitCode, out.Stderr)
	}
	got := strings.TrimSpace(string(out.Stdout))
	// 4 个参数（含 `-test.run`）必须原样到达；被拼接或被 shell 解释过就会变成别的数。
	if got != "4" {
		t.Errorf("子进程收到的参数个数 = %q，期望 4（参数被拼接或转义了？）", got)
	}
}

// 可执行文件不存在时只报"启动失败"，不能被误判成超时或非零退出。
func TestProcessRunner_MissingExecutableIsNotTimeout(t *testing.T) {
	_, err := ProcessRunner{}.Run(context.Background(), ToolCall{
		Path:    filepath.Join(t.TempDir(), "不存在-"+strconv.Itoa(os.Getpid())+".exe"),
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("可执行文件不存在必须报错")
	}
	if errors.Is(err, ErrToolTimeout) {
		t.Error("启动失败不得被当成超时")
	}
}

// requireSleepyCommand 找一个能长时间驻留的外部命令，用于验证终止行为。
func requireSleepyCommand(t *testing.T) []string {
	t.Helper()
	if path, err := exec.LookPath("ping.exe"); err == nil {
		// `-n 60` 约 60 秒；`-w` 不生效于本机回环，无所谓，我们只要求它别太快结束。
		return []string{path, "-n", "60", "127.0.0.1"}
	}
	if path, err := exec.LookPath("timeout.exe"); err == nil {
		return []string{path, "/t", "60", "/nobreak"}
	}
	t.Skip("本机没有 ping.exe / timeout.exe，跳过终止行为用例")
	return nil
}

// 每次调用都要有超时（[05 §5.2]）：超时必须终止子进程，并**标记为超时**，
// 且不能与"调用方取消"混为一谈——两者的处置完全不同。
func TestProcessRunner_TimeoutKillsChildAndIsFlagged(t *testing.T) {
	argv := requireSleepyCommand(t)

	start := time.Now()
	out, err := ProcessRunner{}.Run(context.Background(), ToolCall{
		Path:    argv[0],
		Args:    argv[1:],
		Timeout: 300 * time.Millisecond,
	})
	if !errors.Is(err, ErrToolTimeout) {
		t.Fatalf("应当报 ErrToolTimeout，实得 %v", err)
	}
	if !out.TimedOut {
		t.Error("必须标记 TimedOut，供调用方区分超时与取消")
	}
	// 上限是"内部超时 + 强杀后回收的等待上限"；这里给足余量但必须远小于
	// 子进程的自然结束时间（约 60 秒），否则说明根本没杀掉它。
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("超时后 %v 才返回，子进程可能没被终止", elapsed)
	}
}

// 调用方的 context 取消要**原样保留**：它决定上层是 `canceled` 而不是 `failed`（[05 §10]）。
func TestProcessRunner_CancelKeepsContextError(t *testing.T) {
	argv := requireSleepyCommand(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := ProcessRunner{}.Run(ctx, ToolCall{
		Path:    argv[0],
		Args:    argv[1:],
		Timeout: 60 * time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消必须可被识别为 context.Canceled，实得 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("取消后 %v 才返回，子进程可能没被终止", elapsed)
	}
}
