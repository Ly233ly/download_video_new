package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// scriptedRunner 是清单（P2）与合并（P3）路径的假执行器：它同时扮演
// FFprobe 与 FFmpeg。
//
// 为什么不复用 [probeRunner]：这两条路径对工具的用法与 P1 不同——
//   - FFprobe 会被问**两种**东西：本地文件（校验成品）与**清单地址**（取总时长）；
//   - FFmpeg 既要真的写出一个产物（否则后面无物可校验），
//     又要在流式调用时回放 `-progress` 行（[05 §4.6.3] 的进度口径靠它）。
type scriptedRunner struct {
	mu    sync.Mutex
	calls []ToolCall

	// probe 按"被探测文件的长度"给出校验应答；nil 时用 [healthyProbe]。
	probe func(size int64) []byte
	// manifestDuration 是探测**清单地址**时给出的总时长（秒）。
	// 0 表示探测失败——那正是 [05 §4.6.3] 允许的"拿不到总时长"。
	manifestDuration float64

	// body 是假 FFmpeg 写出的字节。
	body []byte
	// progress 是假 FFmpeg 回放的 `-progress` 行。
	progress []string
	// fail 为真时假 FFmpeg 报非零退出。
	fail bool
}

func (r *scriptedRunner) Run(ctx context.Context, call ToolCall) (ToolOutput, error) {
	return r.run(ctx, call, nil)
}

// RunStreaming 让假件同时满足 [StreamingRunner]，从而覆盖流式进度那条分支。
func (r *scriptedRunner) RunStreaming(
	ctx context.Context, call ToolCall, onChunk func([]byte),
) (ToolOutput, error) {
	return r.run(ctx, call, onChunk)
}

func (r *scriptedRunner) run(ctx context.Context, call ToolCall, onChunk func([]byte)) (ToolOutput, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()

	// 与 [ProcessRunner] 的契约一致：调用方取消时返回 context 错误。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ToolOutput{ExitCode: -1}, ctxErr
	}

	target := call.Args[len(call.Args)-1]
	if isProbeCall(call) {
		if info, err := os.Stat(target); err == nil {
			if r.probe == nil {
				raw, probeErr := healthyProbe(sizeOf(info))
				if probeErr != nil {
					return ToolOutput{ExitCode: 1, ExitFailure: true}, nil
				}
				return ToolOutput{Stdout: raw}, nil
			}
			return ToolOutput{Stdout: r.probe(sizeOf(info))}, nil
		}
		if r.manifestDuration > 0 {
			return ToolOutput{Stdout: durationProbeJSON(r.manifestDuration)}, nil
		}
		return ToolOutput{ExitCode: 1, ExitFailure: true}, nil
	}

	// FFmpeg：先回放进度，再写出产物（顺序与真实进程一致：进度在跑的过程中产生）。
	for _, line := range r.progress {
		if onChunk != nil {
			onChunk([]byte(line + "\n"))
		}
	}
	if r.fail {
		return ToolOutput{ExitCode: 1, ExitFailure: true}, nil
	}
	if err := os.WriteFile(target, r.body, 0o600); err != nil {
		return ToolOutput{ExitCode: 1, ExitFailure: true}, nil
	}
	return ToolOutput{}, nil
}

// ffmpegCalls 返回所有 FFmpeg 调用。
func (r *scriptedRunner) ffmpegCalls() []ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ToolCall
	for _, call := range r.calls {
		if !isProbeCall(call) {
			out = append(out, call)
		}
	}
	return out
}

// probeCalls 返回所有 FFprobe 调用。
func (r *scriptedRunner) probeCalls() []ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ToolCall
	for _, call := range r.calls {
		if isProbeCall(call) {
			out = append(out, call)
		}
	}
	return out
}

// lastFFmpeg 返回最后一次 FFmpeg 调用；没有就判定测试失败。
func (r *scriptedRunner) lastFFmpeg(t *testing.T) ToolCall {
	t.Helper()
	calls := r.ffmpegCalls()
	if len(calls) == 0 {
		t.Fatal("假 FFmpeg 从未被调用")
	}
	return calls[len(calls)-1]
}

// isProbeCall 按可执行文件名区分 FFprobe 与 FFmpeg（[fakeToolset] 造的路径带名字）。
func isProbeCall(call ToolCall) bool {
	return strings.Contains(strings.ToLower(filepath.Base(call.Path)), "ffprobe")
}

// durationProbeJSON 造一份只有 `format.duration` 的探测应答
// （[Downloader.probeManifestDuration] 只关心时长）。
func durationProbeJSON(seconds float64) []byte {
	return []byte(fmt.Sprintf(
		`{"streams":[],"format":{"format_name":"hls","duration":"%.3f"}}`, seconds))
}

// argValue 返回 `flag` 后面的那个参数值；没有就返回空串。
func argValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// argValues 返回 `flag` 出现的全部参数值（如多个 `-i`）。
func argValues(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

// hasArg 判断参数里是否出现过 `want`。
func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// argPairs 把 `-flag value` 形式的参数收成 "flag=value" 便于断言顺序。
func argPairs(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, flag+"="+args[i+1])
		}
	}
	return out
}
