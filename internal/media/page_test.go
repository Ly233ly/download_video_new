package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖 P4（[05 §4.2] 页面解析）。
//
// 假 yt-dlp 只回一份固定的 info JSON——测试关心的是**拿到 JSON 之后做什么**
// （怎么选、凭据给谁、复用哪条路），真实提取能力属于 yt-dlp 自己，不在本包职责内。

// pageRunner 是在 [scriptedRunner] 之上加了假 yt-dlp 的执行器。
//
// 内嵌而不是重写：FFprobe / FFmpeg 的行为（写产物、回放进度）与 P2/P3 测试完全一致，
// 只有 yt-dlp 这一支是新的。
type pageRunner struct {
	scriptedRunner

	// info 是假 yt-dlp 的 stdout。
	info string
	// fail 为真时假 yt-dlp 报非零退出。
	fail bool
	// stderr 是失败时假 yt-dlp 的 stderr（真实工具会在这里吐媒体地址与签名参数）。
	stderr string
	// timedOut 为真时失败同时被标记成超时。
	timedOut bool

	// 观测：`--config-locations` 指向的文件在**调用当时**的内容。
	configBody string
	configPath string
	configErr  error
}

func (r *pageRunner) Run(ctx context.Context, call ToolCall) (ToolOutput, error) {
	if isYtDlpCall(call) {
		return r.ytdlp(call)
	}
	return r.scriptedRunner.run(ctx, call, nil)
}

func (r *pageRunner) RunStreaming(
	ctx context.Context, call ToolCall, onChunk func([]byte),
) (ToolOutput, error) {
	if isYtDlpCall(call) {
		return r.ytdlp(call)
	}
	return r.scriptedRunner.run(ctx, call, onChunk)
}

func (r *pageRunner) ytdlp(call ToolCall) (ToolOutput, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	fail, info, stderr, timedOut := r.fail, r.info, r.stderr, r.timedOut
	r.mu.Unlock()

	// 凭据文件的**生命周期**是这一支要验证的东西：调用时它必须已经写好，
	// 调用结束后必须已经不在了（[05 §4.2] 的"受限临时文件"）。
	if path := argValue(call.Args, "--config-locations"); path != "" {
		raw, err := os.ReadFile(path)
		r.mu.Lock()
		r.configPath, r.configBody, r.configErr = path, string(raw), err
		r.mu.Unlock()
	}

	if fail {
		return ToolOutput{
			Stderr:      []byte(stderr),
			ExitCode:    1,
			ExitFailure: true,
			TimedOut:    timedOut,
		}, nil
	}
	return ToolOutput{Stdout: []byte(info)}, nil
}

func (r *pageRunner) ytdlpCalls() []ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ToolCall
	for _, call := range r.calls {
		if isYtDlpCall(call) {
			out = append(out, call)
		}
	}
	return out
}

// ffmpegCalls 只看**真** FFmpeg。
//
// 内嵌版本的判据是"不是 FFprobe"，而 P4 多了一个 yt-dlp 子进程——不排除它的话，
// "这条路径没有调用 FFmpeg"这类断言会因为解析调用而假阳性。
func (r *pageRunner) ffmpegCalls() []ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ToolCall
	for _, call := range r.calls {
		if !isProbeCall(call) && !isYtDlpCall(call) {
			out = append(out, call)
		}
	}
	return out
}

func isYtDlpCall(call ToolCall) bool {
	return strings.Contains(strings.ToLower(filepath.Base(call.Path)), "yt-dlp")
}

// pageToolset 在 [fakeToolset] 之上补上 yt-dlp 与 Deno 的路径。
func pageToolset(t *testing.T) *Toolset {
	t.Helper()
	tools := fakeToolset(t)
	tools.YtDlp = Tool{Name: ToolYtDlp, Path: filepath.Join(t.TempDir(), "yt-dlp.exe")}
	tools.Deno = Tool{Name: ToolDeno, Path: filepath.Join(t.TempDir(), "deno.exe")}
	return tools
}

// newPageDownloader 造一个带假 yt-dlp 的下载器（未接线站点适配器，见下）。
func newPageDownloader(t *testing.T, runner *pageRunner) *Downloader {
	t.Helper()
	return newPageDownloaderWithHints(t, runner, nil)
}

// newPageDownloaderWithHints 造一个接了站点适配器提示的下载器。
//
// 单独一个 helper 是为了让"未接线"（nil）成为**默认**：既有 19 个用例全都走
// [newPageDownloader]，它们同时就是"PageHints 为 nil 时行为不变"的回归网。
func newPageDownloaderWithHints(t *testing.T, runner *pageRunner, hints PageHints) *Downloader {
	t.Helper()
	d, err := New(pageToolset(t), Options{
		ToolRunner:       runner,
		ProgressInterval: 1,
		PageHints:        hints,
	})
	if err != nil {
		t.Fatalf("构造下载器失败: %v", err)
	}
	return d
}

// pageRequest 把一个目录布局变成 P4 的请求（容器决定要不要视频，见 [05 §5.3]）。
func pageRequest(l layout, pageURL, container, quality string) Request {
	req := l.request(pageURL, 0)
	req.Kind = KindPage
	req.Container = container
	req.QualityLabel = quality
	return req
}

// encodePageInfo 把 map 编码成假 yt-dlp 的 stdout。
func encodePageInfo(t *testing.T, info map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("编码假 info 失败: %v", err)
	}
	return string(raw)
}

// pageFormat 造一条 formats[] 记录；`extra` 覆盖默认值。
func pageFormat(url string, extra map[string]any) map[string]any {
	format := map[string]any{"url": url, "protocol": "https"}
	for key, value := range extra {
		format[key] = value
	}
	return format
}

// TestParsePageInfo 覆盖解析失败的两条路（[05 §4.2]：失败码 `page_resolve_failed`）。
func TestParsePageInfo(t *testing.T) {
	if _, err := parsePageInfo(nil); err == nil {
		t.Error("空输出被当成了解析成功")
	}
	if _, err := parsePageInfo([]byte("{不是 JSON")); err == nil {
		t.Error("坏 JSON 被当成了解析成功")
	}

	info, err := parsePageInfo([]byte(`{"id":"abc","formats":[{"url":"https://x/1","vcodec":"avc1"}]}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.ID != "abc" || len(info.Formats) != 1 {
		t.Fatalf("解析结果 = %+v", info)
	}
}

// TestSelectPageChoices 覆盖 [05 §5.3] 的三条分支。
func TestSelectPageChoices(t *testing.T) {
	videoOnly := ytdlpFormat{URL: "https://cdn/v", VCodec: "avc1", ACodec: "none", Height: 1080, TBR: 5000}
	muxed := ytdlpFormat{URL: "https://cdn/m", VCodec: "avc1", ACodec: "mp4a", Height: 720, TBR: 2500}
	audio := ytdlpFormat{URL: "https://cdn/a", VCodec: "none", ACodec: "mp4a", ABR: 128}
	lowAudio := ytdlpFormat{URL: "https://cdn/a2", VCodec: "none", ACodec: "mp4a", ABR: 64}

	cases := []struct {
		name      string
		formats   []ytdlpFormat
		container string
		label     string
		wantLen   int
		wantTrack []string
		wantCode  Code
	}{
		{
			name: "自带音频时只要一条轨", formats: []ytdlpFormat{muxed},
			container: "mp4", wantLen: 1, wantTrack: []string{"video"},
		},
		{
			name: "分离轨给出两条", formats: []ytdlpFormat{videoOnly, audio},
			container: "mp4", wantLen: 2, wantTrack: []string{"video", "audio"},
		},
		{
			name: "纯音频容器只取音频", formats: []ytdlpFormat{videoOnly, lowAudio, audio},
			container: "mp3", wantLen: 1, wantTrack: []string{"audio"},
		},
		{
			name: "没有格式就是没有媒体", formats: nil,
			container: "mp4", wantCode: CodePageMediaUnavailable,
		},
		{
			name: "只有视频而容器要音频", formats: []ytdlpFormat{videoOnly},
			container: "m4a", wantCode: CodePageMediaUnavailable,
		},
		{
			name: "声明档位无匹配不退档", formats: []ytdlpFormat{videoOnly, audio},
			container: "mp4", label: "720p", wantCode: CodeManifestNoMatchingStream,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			choices, err := selectPageChoices(ytdlpInfo{Formats: tc.formats}, tc.container, tc.label)
			if tc.wantCode != "" {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("选择失败: %v", err)
			}
			if len(choices) != tc.wantLen {
				t.Fatalf("轨数 = %d，期望 %d", len(choices), tc.wantLen)
			}
			for i, track := range tc.wantTrack {
				if choices[i].track != track {
					t.Errorf("第 %d 条轨的角色 = %q，期望 %q", i, choices[i].track, track)
				}
			}
		})
	}
}

// 纯音频容器选码率最高的那条（并列时看总码率）。
func TestSelectPageChoices_AudioPicksHighestBitrate(t *testing.T) {
	choices, err := selectPageChoices(ytdlpInfo{Formats: []ytdlpFormat{
		{URL: "https://cdn/low", VCodec: "none", ACodec: "mp4a", ABR: 64},
		{URL: "https://cdn/high", VCodec: "none", ACodec: "mp4a", ABR: 256},
	}}, "m4a", "")
	if err != nil {
		t.Fatalf("选择失败: %v", err)
	}
	if choices[0].url != "https://cdn/high" {
		t.Fatalf("选中 = %q，期望码率最高的那条", choices[0].url)
	}
}

// 未声明档位时取分辨率最高（顺序与画质无关，[05 §5.3]）。
func TestPickVideoFormat_HighestResolution(t *testing.T) {
	low := ytdlpFormat{URL: "low", VCodec: "avc1", Height: 480, TBR: 1000}
	high := ytdlpFormat{URL: "high", VCodec: "avc1", Height: 1080, TBR: 4000}
	got, err := pickVideoFormat([]ytdlpFormat{low, high}, "")
	if err != nil {
		t.Fatalf("选择失败: %v", err)
	}
	if got == nil || got.URL != "high" {
		t.Fatalf("选中 = %+v，期望分辨率最高的那条", got)
	}
}

// 同分辨率时取码率最高。
func TestPickVideoFormat_SameHeightPrefersBitrate(t *testing.T) {
	cheap := ytdlpFormat{URL: "cheap", VCodec: "avc1", Height: 1080, TBR: 2000}
	rich := ytdlpFormat{URL: "rich", VCodec: "avc1", Height: 1080, TBR: 6000}
	got, err := pickVideoFormat([]ytdlpFormat{cheap, rich}, "1080p")
	if err != nil {
		t.Fatalf("选择失败: %v", err)
	}
	if got == nil || got.URL != "rich" {
		t.Fatalf("选中 = %+v，期望码率最高的那条", got)
	}
}

// [05 §4.0]：解析出的来源是清单还是直链，由 yt-dlp 的 protocol 决定。
func TestFormatKind(t *testing.T) {
	cases := map[string]Kind{
		"m3u8_native":        KindHLS,
		"m3u8":               KindHLS,
		"M3U8":               KindHLS,
		"http_dash_segments": KindDASH,
		"dash":               KindDASH,
		"https":              KindDirect,
		"http":               KindDirect,
		"":                   KindDirect,
	}
	for protocol, want := range cases {
		if got := formatKind(protocol); got != want {
			t.Errorf("formatKind(%q) = %q，期望 %q", protocol, got, want)
		}
	}
}

// 声明的字节数优先于估算；都没有就是 0（= 未知，不猜，[05 §4.6.3]）。
func TestFormatBytes(t *testing.T) {
	if got := formatBytes(ytdlpFormat{Filesize: 100, FilesizeApprox: 999}); got != 100 {
		t.Errorf("精确字节数未优先: %d", got)
	}
	if got := formatBytes(ytdlpFormat{FilesizeApprox: 999}); got != 999 {
		t.Errorf("估算字节数未使用: %d", got)
	}
	if got := formatBytes(ytdlpFormat{}); got != 0 {
		t.Errorf("都没有时应当是 0，得到 %d", got)
	}
}

// TestScopedMediaHeaders 覆盖 B-304：页面凭据不得跨主机送给媒体 CDN。
func TestScopedMediaHeaders(t *testing.T) {
	session := http.Header{"Cookie": {"SESSION=abc"}, "Authorization": {"Bearer t"}}
	base := http.Header{"Referer": {"https://site/watch"}}

	t.Run("同主机时叠加会话凭据", func(t *testing.T) {
		got := scopedMediaHeaders(cloneHeader(base), "https://site/watch", "https://site/media.mp4", session)
		if got.Get("Cookie") != "SESSION=abc" || got.Get("Authorization") != "Bearer t" {
			t.Fatalf("同主机时凭据丢失: %v", got)
		}
		if got.Get("Referer") == "" {
			t.Error("yt-dlp 给的请求头被覆盖了")
		}
	})

	t.Run("跨主机时一条凭据都不带", func(t *testing.T) {
		got := scopedMediaHeaders(cloneHeader(base), "https://site/watch", "https://cdn.example.net/m.mp4", session)
		if got.Get("Cookie") != "" || got.Get("Authorization") != "" {
			t.Fatalf("页面凭据被送到了别的 CDN: %v", got)
		}
		if got.Get("Referer") == "" {
			t.Error("媒体自己的请求头不该被丢掉")
		}
	})

	t.Run("没有会话凭据时原样返回", func(t *testing.T) {
		got := scopedMediaHeaders(base, "https://site/watch", "https://site/m.mp4", nil)
		if got.Get("Referer") == "" || len(got) != 1 {
			t.Fatalf("结果 = %v", got)
		}
	})

	// 端口不同但主机名相同：按 [05 §4.2] 的措辞（"不同主机"）仍算同主机。
	if !sameHost("https://site:8443/a", "https://site/b") {
		t.Error("同一主机名的不同端口被判成了跨主机")
	}
	if sameHost("https://a.example/a", "https://b.example/b") {
		t.Error("不同主机名被判成了同主机")
	}
}

// 凭据文件的格式必须能被 yt-dlp 读懂，且必须拒绝头注入（[05 §4.2]）。
func TestWriteResolveConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), resolveConfigName)
	headers := http.Header{"Cookie": {"SESSION=abc"}, "X-Api-Key": {`quote"and\slash`}}
	if err := writeResolveConfig(path, headers); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	body := string(readFile(t, path))
	if !strings.Contains(body, "--add-headers") {
		t.Fatalf("配置里没有 --add-headers: %q", body)
	}
	if !strings.Contains(body, `--add-headers "Cookie: SESSION=abc"`) {
		t.Fatalf("Cookie 行的格式不对: %q", body)
	}
	// 值里的引号与反斜杠必须转义，否则 yt-dlp 的切分会把一行拆成两行。
	if !strings.Contains(body, `X-Api-Key: quote\"and\\slash`) {
		t.Fatalf("特殊字符未转义: %q", body)
	}
	// 名字排序后逐行写：两次调用产生同一份文件（可预测、可 diff）。
	if strings.Index(body, "Cookie") > strings.Index(body, "X-Api-Key") {
		t.Errorf("请求头没有按名字排序: %q", body)
	}

	t.Run("拒绝换行（HTTP 头注入）", func(t *testing.T) {
		broken := filepath.Join(t.TempDir(), resolveConfigName)
		if err := writeResolveConfig(broken, http.Header{"Cookie": {"a\r\nX-Evil: 1"}}); err == nil {
			t.Fatal("含换行的请求头被写进了配置")
		}
		if _, err := os.Stat(broken); !os.IsNotExist(err) {
			t.Error("被拒绝的请求头仍然留下了文件")
		}
	})

	t.Run("拒绝空名字", func(t *testing.T) {
		broken := filepath.Join(t.TempDir(), resolveConfigName)
		if err := writeResolveConfig(broken, http.Header{"": {"v"}}); err == nil {
			t.Fatal("空请求头名字被接受")
		}
	})
}

// [05 §4.2] 的请求头预算（6 KB）。`-headers` 在命令行上，超了会以"启动工具失败"告终。
func TestHeaderBudgetExceeded(t *testing.T) {
	small := http.Header{"Cookie": {strings.Repeat("a", 1024)}}
	if headerBudgetExceeded(small) {
		t.Error("1 KB 的请求头被判成超预算")
	}
	// 计量的是 `Key: Value\r\n` 的实际形态，不是 value 本身。
	big := http.Header{"Cookie": {strings.Repeat("a", maxHeaderBudget)}}
	if !headerBudgetExceeded(big) {
		t.Error("超过 6 KB 的请求头没有被拦下")
	}
}

// TestRun_Page_SeparateTracks 是 P4 的主链路：页面 → 两条独立轨 → 复用 P3 合并。
func TestRun_Page_SeparateTracks(t *testing.T) {
	media := startMedia(t, &fakeMedia{body: mediaBody(4096)})
	runner := &pageRunner{info: encodePageInfo(t, map[string]any{
		"id": "abc", "title": "样本",
		"formats": []any{
			pageFormat(media.URL+"/video", map[string]any{
				"format_id": "v", "vcodec": "avc1", "acodec": "none",
				"width": 1920, "height": 1080, "protocol": "https", "filesize": 4096,
			}),
			pageFormat(media.URL+"/audio", map[string]any{
				"format_id": "a", "vcodec": "none", "acodec": "mp4a",
				"abr": 128, "protocol": "https", "filesize": 4096,
			}),
		},
	})}
	runner.body = mediaBody(512)

	d := newPageDownloader(t, runner)
	l := newLayout(t, "p1")
	progress := &progressRecorder{}

	result, err := d.Run(context.Background(), pageRequest(l, "https://site/watch?v=1", "mp4", ""), progress.record)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	if result.Bytes != 512 {
		t.Fatalf("成品字节 = %d，期望 512", result.Bytes)
	}
	if got := readFile(t, result.FinalPath); len(got) != 512 {
		t.Fatalf("落盘字节 = %d", len(got))
	}

	// yt-dlp 的参数：JSON 输出、不取播放列表、最后是页面地址本身。
	calls := runner.ytdlpCalls()
	if len(calls) != 1 {
		t.Fatalf("yt-dlp 调用次数 = %d，期望 1", len(calls))
	}
	args := calls[0].Args
	for _, want := range []string{"-J", "--no-playlist", "--no-warnings"} {
		if !hasArg(args, want) {
			t.Errorf("缺少参数 %s: %v", want, args)
		}
	}
	if args[len(args)-1] != "https://site/watch?v=1" {
		t.Errorf("页面地址不在末尾: %v", args)
	}
	// Deno 必须显式给路径（它不在 PATH 里）。
	if got := argValue(args, "--js-runtimes"); !strings.HasPrefix(got, "deno:") {
		t.Errorf("--js-runtimes = %q，期望 deno:<路径>", got)
	}
	if calls[0].OutputLimit != pageResolveOutputLimit {
		t.Errorf("输出上限 = %d，期望 %d（默认 256 KB 会截断 info JSON）",
			calls[0].OutputLimit, pageResolveOutputLimit)
	}

	// 合并：两条轨各下各的，然后一次性合并（[05 §4.6.2]）。
	ffmpeg := runner.ffmpegCalls()
	if len(ffmpeg) != 1 {
		t.Fatalf("FFmpeg 调用次数 = %d，期望 1（只合并，不重下）", len(ffmpeg))
	}
	inputs := argValues(ffmpeg[0].Args, "-i")
	if len(inputs) != 2 {
		t.Fatalf("输入数 = %d，期望 2: %v", len(inputs), inputs)
	}
	if maps := argValues(ffmpeg[0].Args, "-map"); len(maps) != 2 ||
		maps[0] != "0:v:0" || maps[1] != "1:a:0" {
		t.Errorf("显式 -map 不对: %v", ffmpeg[0].Args)
	}
	if hasArg(ffmpeg[0].Args, "-shortest") {
		t.Error("合并加了 -shortest（[05 §4.6.2] 明确禁止）")
	}

	// 阶段顺序：先下载各轨，再合并，最后校验成品。
	phases := progress.phases()
	if len(phases) != 3 || phases[0] != PhaseDownloading || phases[1] != PhaseMerging || phases[2] != PhaseValidating {
		t.Fatalf("阶段序列 = %v，期望 downloading→merging→validating", phases)
	}
}

// 解析出的地址是清单时复用 P2（[05 §4.6.4]）。
func TestRun_Page_HLS(t *testing.T) {
	manifest := startManifestServer(t, "application/vnd.apple.mpegurl", hlsMaster)
	runner := &pageRunner{info: encodePageInfo(t, map[string]any{
		"id": "abc",
		"formats": []any{pageFormat(manifest.URL+"/master", map[string]any{
			"format_id": "hls", "vcodec": "avc1", "acodec": "mp4a", "protocol": "m3u8_native",
		})},
	})}
	runner.manifestDuration = 62.5
	runner.body = mediaBody(256)

	d := newPageDownloader(t, runner)
	l := newLayout(t, "p1")

	if _, err := d.Run(context.Background(), pageRequest(l, "https://site/watch", "mp4", ""), nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	ffmpeg := runner.ffmpegCalls()
	if len(ffmpeg) != 1 {
		t.Fatalf("FFmpeg 调用次数 = %d，期望 1", len(ffmpeg))
	}
	// 未声明档位 → 取分辨率最高（1080），输入是**变体的绝对地址**（[05 §5.3]）。
	if got := argValue(ffmpeg[0].Args, "-i"); got != manifest.URL+"/v/1080.m3u8" {
		t.Fatalf("FFmpeg 输入 = %q，期望最高画质变体", got)
	}
}

// 解析失败是可重试的（[05 §7.1]），且错误文本不得带上页面地址（B-722）。
func TestRun_Page_ResolveFailure(t *testing.T) {
	runner := &pageRunner{fail: true}
	d := newPageDownloader(t, runner)
	l := newLayout(t, "p1")

	_, err := d.Run(context.Background(), pageRequest(l, "https://site/watch?token=secret", "mp4", ""), nil)
	assertCode(t, err, CodePageResolveFailed)
	if !Retryable(CodePageResolveFailed) {
		t.Error("page_resolve_failed 应当可重试（[05 §7.1]）")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "site") {
		t.Fatalf("错误文本泄露了页面地址: %v", err)
	}
	if len(dirFiles(t, l.planDir)) != 0 {
		t.Error("解析失败却留下了临时产物")
	}
}

// 没有 yt-dlp 时如实报解析失败，而不是把页面当地址去下载。
func TestRun_Page_MissingYtDlp(t *testing.T) {
	runner := &pageRunner{info: `{}`}
	tools := fakeToolset(t)
	tools.YtDlp = Tool{Name: ToolYtDlp, Path: ""}
	d, err := New(tools, Options{ToolRunner: runner, ProgressInterval: 1})
	if err != nil {
		t.Fatalf("构造下载器失败: %v", err)
	}
	l := newLayout(t, "p1")

	_, runErr := d.Run(context.Background(), pageRequest(l, "https://site/watch", "mp4", ""), nil)
	assertCode(t, runErr, CodePageResolveFailed)
	if len(runner.ytdlpCalls()) != 0 {
		t.Error("没有 yt-dlp 却调用了工具")
	}
}

// 声明档位无匹配时不退档、不静默选默认流（[05 §5.3]）。
func TestRun_Page_QualityNoMatch(t *testing.T) {
	media := startMedia(t, &fakeMedia{body: mediaBody(1024)})
	runner := &pageRunner{info: encodePageInfo(t, map[string]any{
		"formats": []any{pageFormat(media.URL+"/v", map[string]any{
			"vcodec": "avc1", "acodec": "mp4a", "height": 1080, "protocol": "https",
		})},
	})}
	runner.body = mediaBody(128)

	d := newPageDownloader(t, runner)
	l := newLayout(t, "p1")

	_, err := d.Run(context.Background(), pageRequest(l, "https://site/watch", "mp4", "720p"), nil)
	assertCode(t, err, CodeManifestNoMatchingStream)
	if len(runner.ffmpegCalls()) != 0 {
		t.Error("无匹配档位却启动了 FFmpeg")
	}
	if len(dirFiles(t, l.planDir)) != 0 {
		t.Error("无匹配档位却写入了临时产物")
	}
}

// 页面凭据经**受限临时文件**传给 yt-dlp：不进命令行，用完即删（[05 §4.2]、B-722）。
func TestRun_Page_CredentialsUseConfigFile(t *testing.T) {
	media := startMedia(t, &fakeMedia{body: mediaBody(1024)})
	runner := &pageRunner{info: encodePageInfo(t, map[string]any{
		"formats": []any{pageFormat(media.URL+"/v", map[string]any{
			"vcodec": "avc1", "acodec": "mp4a", "height": 720, "protocol": "https",
		})},
	})}
	runner.body = mediaBody(64)

	d := newPageDownloader(t, runner)
	l := newLayout(t, "p1")
	req := pageRequest(l, "https://site/watch", "mp4", "")
	req.Headers = http.Header{"Cookie": {"SESSION=abc"}}

	if _, err := d.Run(context.Background(), req, nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	if runner.configErr != nil {
		t.Fatalf("yt-dlp 被调用时凭据文件不可读: %v", runner.configErr)
	}
	if !strings.Contains(runner.configBody, "SESSION=abc") {
		t.Fatalf("凭据没有进配置文件: %q", runner.configBody)
	}
	// **不进命令行**：参数里不得出现凭据内容。
	for _, arg := range runner.ytdlpCalls()[0].Args {
		if strings.Contains(arg, "SESSION=abc") {
			t.Fatalf("凭据出现在了命令行上: %v", runner.ytdlpCalls()[0].Args)
		}
	}
	// 用完即删：文件里含 Cookie，比 info JSON 更敏感。
	if _, err := os.Stat(runner.configPath); !os.IsNotExist(err) {
		t.Fatalf("凭据文件没有删除: %s", runner.configPath)
	}
}

// 容器只要音频时，P4 也只取音频（[05 §5.3]）。
func TestRun_Page_AudioOnly(t *testing.T) {
	media := startMedia(t, &fakeMedia{body: mediaBody(2048)})
	runner := &pageRunner{info: encodePageInfo(t, map[string]any{
		"formats": []any{
			pageFormat(media.URL+"/v", map[string]any{"vcodec": "avc1", "acodec": "none", "height": 1080}),
			pageFormat(media.URL+"/a", map[string]any{"vcodec": "none", "acodec": "mp4a", "abr": 128}),
		},
	})}
	runner.body = mediaBody(300)

	d := newPageDownloader(t, runner)
	l := newLayout(t, "p1")
	req := pageRequest(l, "https://site/watch", "m4a", "")
	req.OutputName = "样本.m4a"

	result, err := d.Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	// 纯音频走 P1：字节就是那条音频地址的完整响应，不需要 FFmpeg 参与
	// （[05 §4.6.2] 的合并前提是"两条分离轨"）。
	if calls := runner.ffmpegCalls(); len(calls) != 0 {
		t.Errorf("纯音频却调用了 FFmpeg: %v", calls)
	}
	if result.Bytes != 2048 {
		t.Fatalf("成品字节 = %d，期望 2048", result.Bytes)
	}
}

// assertCode 断言错误携带指定错误码（[05 §7] 的错误码是界面文案的唯一来源）。
func assertCode(t *testing.T, err error, want Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际没有错误", want)
	}
	got, ok := CodeOf(err)
	if !ok {
		t.Fatalf("错误没有错误码: %v", err)
	}
	if got != want {
		t.Fatalf("错误码 = %s，期望 %s（%v）", got, want, err)
	}
}

// cloneHeader 复制一份请求头，避免子用例之间的观测互相污染。
func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	out := make(http.Header, len(headers))
	for key, values := range headers {
		out[key] = append([]string(nil), values...)
	}
	return out
}

// ---------------------------------------------------------------------------
// 站点适配器提示（[14 §5] 的 `resolve` / `errors`；[05 §4.2] 的追加条款）
//
// 本包不 import internal/adapter（包划分见 [01 §3]），提示只经由 [PageHints]
// 这个窄契约进来。下面用替身把"声明说了什么"与"引擎怎么用它"分开测。
// ---------------------------------------------------------------------------

// stubPageHints 是测试用的声明替身。
type stubPageHints struct {
	// canonical 为 nil 时原样返回入参，等于"没有声明规范化规则"。
	canonical func(pageURL string) string
	// translate 为 nil 时一律不命中，等于"没有声明错误映射"。
	translate func(pageURL, raw string) (string, string, bool)

	// 观测：声明的入参。
	canonicalInputs []string
	translateRaw    []string
}

func (h *stubPageHints) CanonicalPageURL(pageURL string) string {
	h.canonicalInputs = append(h.canonicalInputs, pageURL)
	if h.canonical == nil {
		return pageURL
	}
	return h.canonical(pageURL)
}

func (h *stubPageHints) TranslateResolveError(pageURL, raw string) (string, string, bool) {
	h.translateRaw = append(h.translateRaw, raw)
	if h.translate == nil {
		return "", "", false
	}
	return h.translate(pageURL, raw)
}

// muxedPageRunner 造一个"能从假媒体服务器直接下载成片"的假 yt-dlp。
func muxedPageRunner(t *testing.T, body int) *pageRunner {
	t.Helper()
	srv := startMedia(t, &fakeMedia{body: mediaBody(body)})
	runner := &pageRunner{info: encodePageInfo(t, map[string]any{
		"formats": []any{pageFormat(srv.URL+"/v", map[string]any{
			"vcodec": "avc1", "acodec": "mp4a", "height": 720, "protocol": "https",
		})},
	})}
	runner.body = mediaBody(64)
	return runner
}

// M12（[14 §10]）：页面地址必须在**启动解析工具之前**规范化——交给 yt-dlp 的
// 只能是规范地址（抖音的 `?modal_id=` 弹层地址就是一例，旧 tests/test_media.py:665-687）。
func TestRun_Page_CanonicalizesBeforeToolStart(t *testing.T) {
	runner := muxedPageRunner(t, 1024)
	const raw = "https://site/jingxuan?modal_id=7662692425235828009&from_page=feed"
	hints := &stubPageHints{canonical: func(string) string { return "https://site/video/7662692425235828009" }}

	d := newPageDownloaderWithHints(t, runner, hints)
	l := newLayout(t, "p1")

	if _, err := d.Run(context.Background(), pageRequest(l, raw, "mp4", ""), nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	calls := runner.ytdlpCalls()
	if len(calls) != 1 {
		t.Fatalf("yt-dlp 调用次数 = %d，期望 1", len(calls))
	}
	if got := calls[0].Args[len(calls[0].Args)-1]; got != "https://site/video/7662692425235828009" {
		t.Fatalf("交给 yt-dlp 的地址 = %q，期望规范地址", got)
	}
	// 原始地址不得出现在命令行上（它带着弹层参数，解析器认不出）。
	for _, arg := range calls[0].Args {
		if arg == raw {
			t.Fatalf("原始地址仍然被交给了 yt-dlp: %v", calls[0].Args)
		}
	}
	if len(hints.canonicalInputs) != 1 || hints.canonicalInputs[0] != raw {
		t.Fatalf("规范化入参 = %v，期望原始地址", hints.canonicalInputs)
	}
}

// 坏声明不能吞掉好地址：声明给不出结果时按原地址交给工具。
func TestRun_Page_CanonicalizeFallsBackToOriginal(t *testing.T) {
	runner := muxedPageRunner(t, 1024)
	hints := &stubPageHints{canonical: func(string) string { return "  " }}

	d := newPageDownloaderWithHints(t, runner, hints)
	l := newLayout(t, "p1")

	const raw = "https://site/watch?v=1"
	if _, err := d.Run(context.Background(), pageRequest(l, raw, "mp4", ""), nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	if got := runner.ytdlpCalls()[0].Args[len(runner.ytdlpCalls()[0].Args)-1]; got != raw {
		t.Fatalf("交给 yt-dlp 的地址 = %q，期望退回原地址", got)
	}
}

// 命中声明时按声明的码与文案报错（[05 §4.2] 的追加条款），且**不可重试**。
//
// 同一用例覆盖 B-722：stderr 原文（真实工具会在里面吐媒体地址与签名参数）
// 只允许在内存里过一遍——不进 `Error()`、不进 cause、也不进日志。
func TestRun_Page_SiteErrorFromDeclaration(t *testing.T) {
	const (
		secret = "https://cdn.example.com/video.mp4?signature=b7f3SECRET"
		code   = "douyin_session_expired"
		msg    = "抖音需要当前浏览器的新鲜会话，请刷新抖音页面后重试"
	)
	runner := &pageRunner{
		fail:   true,
		stderr: "ERROR: [Douyin] 7662692425235828009: Fresh cookies (session) are needed for " + secret,
	}
	hints := &stubPageHints{translate: func(pageURL, raw string) (string, string, bool) {
		if !strings.Contains(raw, "Fresh cookies") {
			return "", "", false
		}
		return code, msg, true
	}}

	d := newPageDownloaderWithHints(t, runner, hints)
	l := newLayout(t, "p1")

	_, err := d.Run(context.Background(), pageRequest(l, "https://site/watch?v=1", "mp4", ""), nil)
	if err == nil {
		t.Fatal("解析失败却没有报错")
	}
	assertCode(t, err, Code(code))
	if err.Error() != msg {
		t.Fatalf("错误文案 = %q，期望声明里的 %q", err.Error(), msg)
	}
	if RetryableError(err) {
		t.Error("站点声明的码必须按不可重试处理（[05 §4.2] 的追加条款）")
	}

	var site *SiteError
	if !errors.As(err, &site) {
		t.Fatalf("错误不是 *SiteError: %v", err)
	}
	if site.Code != Code(code) || site.Message != msg {
		t.Fatalf("SiteError = %+v", site)
	}
	if !strings.Contains(site.cause.Error(), "退出码 1") {
		t.Fatalf("cause 里应当只有不含原文的事实: %v", site.cause)
	}

	// stderr 原文不得出现在错误的任何一个出口里。
	for _, got := range []string{err.Error(), site.cause.Error(), fmt.Sprintf("%v", err)} {
		if strings.Contains(got, secret) || strings.Contains(got, "signature=b7f3SECRET") {
			t.Fatalf("stderr 原文泄露到了错误文本: %q", got)
		}
	}
	if len(hints.translateRaw) != 1 || !strings.Contains(hints.translateRaw[0], "Fresh cookies") {
		t.Fatalf("声明没有拿到 stderr 原文: %v", hints.translateRaw)
	}
	if len(dirFiles(t, l.planDir)) != 0 {
		t.Error("解析失败却留下了临时产物")
	}
}

// 未命中声明时仍是通用的 `page_resolve_failed`（[05 §4.2] 的追加条款）。
func TestRun_Page_UnmappedResolveErrorStaysGeneric(t *testing.T) {
	runner := &pageRunner{fail: true, stderr: "ERROR: Unsupported URL: https://cdn/x?signature=zzz"}
	hints := &stubPageHints{translate: func(string, string) (string, string, bool) {
		return "", "", false
	}}

	d := newPageDownloaderWithHints(t, runner, hints)
	l := newLayout(t, "p1")

	_, err := d.Run(context.Background(), pageRequest(l, "https://site/watch?v=1", "mp4", ""), nil)
	assertCode(t, err, CodePageResolveFailed)
	if !RetryableError(err) {
		t.Error("未命中的解析失败应当仍可重试（[05 §7.1]）")
	}
	if strings.Contains(err.Error(), "signature=zzz") {
		t.Fatalf("错误文本泄露了 stderr 原文: %v", err)
	}
}

// 超时不是站点状态：即使工具同时报非零退出，也留在可重试的通用码上。
func TestRun_Page_TimeoutIsNotTranslated(t *testing.T) {
	runner := &pageRunner{fail: true, timedOut: true, stderr: "ERROR: Fresh cookies are needed"}
	hints := &stubPageHints{translate: func(string, string) (string, string, bool) {
		return "douyin_session_expired", "抖音需要当前浏览器的新鲜会话，请刷新抖音页面后重试", true
	}}

	d := newPageDownloaderWithHints(t, runner, hints)
	l := newLayout(t, "p1")

	_, err := d.Run(context.Background(), pageRequest(l, "https://site/watch?v=1", "mp4", ""), nil)
	assertCode(t, err, CodePageResolveFailed)
	if !RetryableError(err) {
		t.Error("超时应当留在可重试一侧（[05 §7.1]）")
	}
	if len(hints.translateRaw) != 0 {
		t.Fatalf("超时不该去问声明: %v", hints.translateRaw)
	}
}

// 声明给不出文案时不能顶替通用码：`DownloadError` 的消息为空会回落到
// "程序内部错误"（[05 §7.4]），那等于把声明的信息丢掉。
func TestRun_Page_DeclarationWithoutMessageIsIgnored(t *testing.T) {
	runner := &pageRunner{fail: true, stderr: "ERROR: Fresh cookies are needed"}
	hints := &stubPageHints{translate: func(string, string) (string, string, bool) {
		return "douyin_session_expired", "", true
	}}

	d := newPageDownloaderWithHints(t, runner, hints)
	l := newLayout(t, "p1")

	_, err := d.Run(context.Background(), pageRequest(l, "https://site/watch?v=1", "mp4", ""), nil)
	assertCode(t, err, CodePageResolveFailed)
	var site *SiteError
	if errors.As(err, &site) {
		t.Fatalf("缺文案的声明不该生效: %+v", site)
	}
}

// 未接线（PageHints 为 nil）时行为与没有适配器时完全一致：地址原样、通用码。
func TestRun_Page_NilHintsBehavesAsBefore(t *testing.T) {
	runner := &pageRunner{fail: true, stderr: "ERROR: Fresh cookies are needed"}

	d := newPageDownloaderWithHints(t, runner, nil)
	l := newLayout(t, "p1")

	_, err := d.Run(context.Background(), pageRequest(l, "https://site/watch?v=1", "mp4", ""), nil)
	assertCode(t, err, CodePageResolveFailed)
	if got := runner.ytdlpCalls()[0].Args[len(runner.ytdlpCalls()[0].Args)-1]; got != "https://site/watch?v=1" {
		t.Fatalf("未接线时地址被改动了: %q", got)
	}
	if strings.Contains(err.Error(), "Fresh cookies") {
		t.Fatalf("未接线时错误文本泄露了 stderr: %v", err)
	}
}
