package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// 本文件是下载包测试的脚手架。
//
// [12 §5.2] 的三条硬要求在这里落地：
//   - T2 使用临时目录：所有测试只用 `t.TempDir()`，绝不碰用户目录；
//   - T3 不依赖外网：字节来自 `httptest` 起的本地假服务；
//   - FFprobe 用**注入的假执行器**，不依赖真实二进制（测试机的
//     `media-tools/` 可能根本没装，真二进制不属于单元测试的依赖）。

// fakeToolset 造一个"路径存在但内容无关"的工具集：FFprobe 由假执行器接管，
// 因此这里只需要一个非空路径，证明"工具已解析"这一步。
func fakeToolset(t *testing.T) *Toolset {
	t.Helper()
	return &Toolset{
		FFmpeg:  Tool{Name: ToolFFmpeg, Path: filepath.Join(t.TempDir(), "ffmpeg.exe")},
		FFprobe: Tool{Name: ToolFFprobe, Path: filepath.Join(t.TempDir(), "ffprobe.exe")},
	}
}

// probeRunner 是注入的假 FFprobe。
//
// `respond` 按"被探测文件的长度"给出结论，这样测试可以断言
// "校验针对的是**刚下载完的那份字节**"，而不用猜文件名。
type probeRunner struct {
	mu      sync.Mutex
	calls   []ToolCall
	respond func(size int64) ([]byte, error)
}

func (p *probeRunner) Run(ctx context.Context, call ToolCall) (ToolOutput, error) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()

	// 与 [ProcessRunner] 保持同样的契约：调用方取消时返回 context 错误，
	// 工具非零退出时只报 `ExitFailure`（原始错误文本由调用方自己留日志）。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ToolOutput{ExitCode: -1}, ctxErr
	}

	path := call.Args[len(call.Args)-1]
	info, err := os.Stat(path)
	if err != nil {
		return ToolOutput{ExitCode: 1, ExitFailure: true}, nil
	}
	respond := p.respond
	if respond == nil {
		respond = healthyProbe
	}
	raw, err := respond(sizeOf(info))
	if err != nil {
		return ToolOutput{ExitCode: 1, ExitFailure: true}, nil
	}
	return ToolOutput{Stdout: raw}, nil
}

func (p *probeRunner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *probeRunner) lastCall() ToolCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return ToolCall{}
	}
	return p.calls[len(p.calls)-1]
}

func sizeOf(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}

// healthyProbe 返回一份"有视频有音频"的探测结果，时长按字节数换算，
// 让不同大小的样本也有可信的 duration。
func healthyProbe(size int64) ([]byte, error) {
	duration := float64(size) / 1000.0
	payload := fmt.Sprintf(`{
      "streams": [
        {"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1280, "height": 720,
         "r_frame_rate": "30/1", "duration": "%[1]f", "bit_rate": "1500000"},
        {"index": 1, "codec_type": "audio", "codec_name": "aac", "channels": 2,
         "sample_rate": "44100", "duration": "%[1]f", "bit_rate": "128000"}
      ],
      "format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "%[1]f"}
    }`, duration)
	return []byte(payload), nil
}

// probeJSON 造一份自定义的 FFprobe JSON（测试直接控制流的构成）。
func probeJSON(t *testing.T, container string, duration float64, streams ...map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"streams": streams,
		"format":  map[string]any{"format_name": container, "duration": strconv.FormatFloat(duration, 'f', 3, 64)},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("构造探测 JSON 失败: %v", err)
	}
	return raw
}

// videoStream 造一条视频流。
func videoStream(width, height int) map[string]any {
	return map[string]any{
		"index": 0, "codec_type": "video", "codec_name": "h264",
		"width": width, "height": height, "r_frame_rate": "30/1",
	}
}

// audioStream 造一条音频流。
func audioStream() map[string]any {
	return map[string]any{
		"index": 1, "codec_type": "audio", "codec_name": "aac",
		"channels": 2, "sample_rate": "44100",
	}
}

// fakeMedia 是一个本地假媒体服务，覆盖 [05 §4.1] 的三种状态码路径。
type fakeMedia struct {
	mu sync.Mutex
	// body 是完整的媒体内容。
	body []byte
	// ignoreRange 为真时对任何请求都回 200 + 全量（"服务器不支持 Range"）。
	ignoreRange bool
	// rejectRange 为真时对任何带 Range 的请求回 416。
	rejectRange bool
	// noLength 为真时不发 `Content-Length`（无长度语义）。
	noLength bool
	// requireAuth 非空时要求请求头逐值匹配，否则 401。
	requireAuth map[string]string
	// redirectTo 非空时先 302 到该地址（用于验证凭据不跨主机转发）。
	redirectTo string

	// 观测值。
	rangeHeaders []string
	seenAuth     []string
	requests     int
}

func (f *fakeMedia) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.rangeHeaders = append(f.rangeHeaders, r.Header.Get("Range"))
	f.seenAuth = append(f.seenAuth, r.Header.Get("Authorization"))
	body := f.body
	ignoreRange, rejectRange, noLength := f.ignoreRange, f.rejectRange, f.noLength
	requireAuth, redirectTo := f.requireAuth, f.redirectTo
	f.mu.Unlock()

	for key, want := range requireAuth {
		if r.Header.Get(key) != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	if redirectTo != "" {
		http.Redirect(w, r, redirectTo, http.StatusFound)
		return
	}

	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && rejectRange {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	if rangeHeader == "" || ignoreRange {
		if !noLength {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}

	start, err := parseRangeStart(rangeHeader)
	if err != nil || start > int64(len(body)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	rest := body[start:]
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
	w.Header().Set("Content-Length", strconv.Itoa(len(rest)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(rest)
}

// parseRangeStart 解析 `bytes=<start>-` 形式的请求头。
func parseRangeStart(value string) (int64, error) {
	const prefix = "bytes="
	if len(value) <= len(prefix) || value[:len(prefix)] != prefix {
		return 0, errors.New("无法解析的 Range")
	}
	rest := value[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '-' {
			return strconv.ParseInt(rest[:i], 10, 64)
		}
	}
	return 0, errors.New("无法解析的 Range")
}

// startMedia 起一个本地假媒体服务（[12 §5.2] 的 T3：不依赖外网）。
func startMedia(t *testing.T, media *fakeMedia) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(media)
	t.Cleanup(server.Close)
	return server
}

func (f *fakeMedia) ranges() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rangeHeaders...)
}

func (f *fakeMedia) auth() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenAuth...)
}

func (f *fakeMedia) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// layout 是测试用的目录布局：`<base>/临时/<plan_id>/` 与 `<base>/已完成/`。
type layout struct {
	base      string
	tempRoot  string
	outputDir string
	planDir   string
}

func newLayout(t *testing.T, planID string) layout {
	t.Helper()
	base := t.TempDir()
	tempRoot := filepath.Join(base, "临时")
	planDir := filepath.Join(tempRoot, planID)
	outputDir := filepath.Join(base, "已完成")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("建输出目录失败: %v", err)
	}
	return layout{base: base, tempRoot: tempRoot, outputDir: outputDir, planDir: planDir}
}

// staging 返回本计划的临时产物路径。
func (l layout) staging() string { return filepath.Join(l.planDir, stagingName) }

// request 造一个填好目录的最小请求。
func (l layout) request(url string, total int64) Request {
	return Request{
		PlanID:     filepath.Base(l.planDir),
		URL:        url,
		OutputName: "样本.mp4",
		Container:  "mp4",
		TempDir:    l.tempRoot,
		OutputDir:  l.outputDir,
		TotalBytes: total,
	}
}

// writePartial 预置一段"上次下载剩下的分片"（断点续传的输入）。
func (l layout) writePartial(t *testing.T, content []byte) {
	t.Helper()
	if err := os.MkdirAll(l.planDir, 0o700); err != nil {
		t.Fatalf("建计划临时目录失败: %v", err)
	}
	if err := os.WriteFile(l.staging(), content, 0o600); err != nil {
		t.Fatalf("写预置分片失败: %v", err)
	}
}

// progressRecorder 收集进度回调，并检查 [05 §4.1] 的进度语义。
type progressRecorder struct {
	mu     sync.Mutex
	frames []Progress
}

func (p *progressRecorder) record(progress Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames = append(p.frames, progress)
}

func (p *progressRecorder) all() []Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Progress(nil), p.frames...)
}

// phases 返回出现过的阶段，按出现顺序去重。
func (p *progressRecorder) phases() []string {
	var out []string
	for _, frame := range p.all() {
		if len(out) == 0 || out[len(out)-1] != frame.Phase {
			out = append(out, frame.Phase)
		}
	}
	return out
}

// newDownloader 造一个注入了假 FFprobe 的下载器。
func newDownloader(t *testing.T, opts Options) *Downloader {
	t.Helper()
	if opts.ToolRunner == nil {
		opts.ToolRunner = &probeRunner{}
	}
	if opts.ProgressInterval == 0 {
		// 测试要把节流压到最短，才能观察到中间的进度帧；
		// 生产默认见 DefaultProgressInterval，调用方可用 `progress_throttle_ms` 覆盖。
		opts.ProgressInterval = time.Millisecond
	}
	d, err := New(fakeToolset(t), opts)
	if err != nil {
		t.Fatalf("构造下载器失败: %v", err)
	}
	return d
}

// readFile 读文件内容；失败即测试失败。
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return raw
}

// mediaBody 造一段可辨认的媒体内容（前 8 字节是"签名"，便于断言拼接正确）。
func mediaBody(size int) []byte {
	body := make([]byte, size)
	copy(body, []byte("MEDIABYT"))
	for i := 8; i < size; i++ {
		body[i] = byte('a' + i%26)
	}
	return body
}
