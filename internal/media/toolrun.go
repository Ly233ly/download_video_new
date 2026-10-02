package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// ToolCall 是一次外部工具调用的输入。
//
// **参数必须是数组**：`Args` 直接交给 `exec.CommandContext`，
// 任何地方都不把它拼成命令行字符串（[05 §5.2]）。
type ToolCall struct {
	// Path 是可执行文件绝对路径，来自 [internal/media] 的解析结果。
	Path string
	// Args 是完整的参数数组（不含 argv[0]）。
	Args []string
	// Timeout 是本次调用的上限；<= 0 表示用 [Options.ToolTimeout]。
	//
	// **每次调用都有超时**是硬性要求（[05 §5.2]、[12 §3.3] 的 E4）。
	Timeout time.Duration
	// OutputLimit 是 stdout/stderr 各自的捕获上限；超限截断（[05 §5.2]）。
	// <= 0 表示用 [Options.OutputLimit]。
	OutputLimit int
}

// ToolOutput 是一次外部工具调用的结果。
type ToolOutput struct {
	Stdout      []byte
	Stderr      []byte
	Truncated   bool // stdout 或 stderr 是否被截断
	ExitCode    int  // 进程退出码；被取消或未启动时为 -1
	Duration    time.Duration
	TimedOut    bool // 因**内部超时**被终止（与调用方取消 context 区分）
	ExitFailure bool // 非零退出
}

// ToolRunner 执行外部工具。
//
// 抽成接口的**唯一理由是测试**：FFprobe 相关测试必须用注入的假执行器，
// 不能依赖真实二进制（[12 §5.2] 的 T2/T3 精神——测试不依赖外部环境）。
// 生产实现是 [ProcessRunner]。
type ToolRunner interface {
	Run(ctx context.Context, call ToolCall) (ToolOutput, error)
}

// ProcessRunner 用子进程执行工具：[05 §5.2] 的调用规范落在它身上。
//
//	参数数组        Args 原样传入，不拼 shell 字符串
//	超时与取消      context 取消 + 调用级超时，两者都会终止进程
//	输出上限        stdout/stderr 各自限长，超出截断
//	退出码          非零映射为内部错误（调用方再映射为稳定错误码）
//	终止            context 取消时先发 Kill，再等待回收（WaitDelay 兜底）
type ProcessRunner struct{}

// ErrToolTimeout 表示工具调用触发了内部超时（不是调用方取消）。
var ErrToolTimeout = errors.New("媒体工具调用超时")

// Run 执行一次工具调用。返回的 error 只描述**执行层面**的失败
// （未启动、超时、取输出失败）；非零退出码用 `ToolOutput.ExitFailure` 表达，
// 由调用方决定映射成哪个稳定错误码（[05 §5.2]）。
func (ProcessRunner) Run(ctx context.Context, call ToolCall) (ToolOutput, error) {
	timeout := call.Timeout
	if timeout <= 0 {
		timeout = DefaultToolTimeout
	}
	limit := call.OutputLimit
	if limit <= 0 {
		limit = DefaultOutputLimit
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, call.Path, call.Args...)
	// 用户停止时先优雅终止再强杀，并等待回收（[05 §5.2]）。
	// Windows 无 POSIX 信号，`Kill` 是唯一可用手段；`WaitDelay` 保证
	// 子进程握有管道不放时 Wait 也会返回，不会把调用方挂死。
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = ToolWaitDelay

	stdout := &BoundedBuffer{Limit: limit}
	stderr := &BoundedBuffer{Limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// 刻意不给 Stdin：媒体工具不需要输入，留空可避免继承父进程的句柄。

	start := time.Now()
	err := cmd.Run()
	out := ToolOutput{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: stdout.Truncated || stderr.Truncated,
		ExitCode:  -1,
		Duration:  time.Since(start),
	}

	if err != nil {
		// 调用方取消 ≠ 内部超时：两者的处置完全不同（取消不重试、超时可重试），
		// 所以必须分开表达（[05 §5.2] 与 [05 §10]）。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, fmt.Errorf("媒体工具调用被取消: %w", ctxErr)
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			out.TimedOut = true
			return out, ErrToolTimeout
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			out.ExitFailure = true
			out.ExitCode = exitErr.ExitCode()
			return out, nil
		}
		return out, fmt.Errorf("启动媒体工具失败: %w", err)
	}
	return out, nil
}

// BoundedBuffer 是有上限的写缓冲：超出上限后**继续接收但丢弃**，
// 只记下被截断的事实（[05 §5.2] 的"stdout/stderr 捕获且有上限，超限截断"）。
//
// 继续读取而不是提前返回错误：提前返回会让管道写满，反而把子进程卡死。
type BoundedBuffer struct {
	// Limit 是保留的最大字节数。
	Limit int
	buf   bytes.Buffer
	// Truncated 表示已有内容被丢弃。
	Truncated bool
}

// Write 实现 io.Writer。
func (b *BoundedBuffer) Write(p []byte) (int, error) {
	remain := b.Limit - b.buf.Len()
	if remain <= 0 {
		b.Truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		if _, err := b.buf.Write(p[:remain]); err != nil {
			return 0, err
		}
		b.Truncated = true
		return len(p), nil
	}
	if _, err := b.buf.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Bytes 返回已保留的内容（不含被截断的部分）。
func (b *BoundedBuffer) Bytes() []byte { return b.buf.Bytes() }
