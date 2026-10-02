package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// hlsMaster 是一份典型的两档主清单：**低画质排在前面**——这正是
// [05 §4.6.1] 强调"不能靠 FFmpeg 默认选第一个"的原因。
const hlsMaster = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
v/360.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
v/1080.m3u8
`

// hlsMediaPlaylist 是一份没有 `#EXT-X-STREAM-INF` 的清单（单流情形）。
const hlsMediaPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXTINF:10.0,
seg0.ts
`

// dashManifest 是一份两档视频 + 一档音频的 DASH 清单。
const dashManifest = `<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="v1" bandwidth="2000000" width="1280" height="720" />
      <Representation id="v2" bandwidth="6000000" width="1920" height="1080" />
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4">
      <Representation id="a1" bandwidth="128000" />
    </AdaptationSet>
  </Period>
</MPD>
`

// startManifestServer 起一个本地清单服务：`/master` 给主清单，
// 其余路径给固定的 204（FFmpeg 是假件，不会真去取分片）。
func startManifestServer(t *testing.T, contentType, master string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/master" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(master)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, master)
	}))
	t.Cleanup(server.Close)
	return server
}

// startBody 起一个固定应答的服务（用于"提示不成立"的端到端用例）。
func startBody(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// dirFiles 列出目录里的条目名；目录不存在时返回 nil。
func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读目录 %s 失败: %v", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// TestSelectManifestVariant 覆盖 [05 §5.3] 的选择规则。
func TestSelectManifestVariant(t *testing.T) {
	low := manifestVariant{Height: 360, Bandwidth: 800_000, Input: "360"}
	high := manifestVariant{Height: 1080, Bandwidth: 5_000_000, Input: "1080"}
	highCheap := manifestVariant{Height: 1080, Bandwidth: 4_000_000, Input: "1080-cheap"}
	noInfo := manifestVariant{Input: "single"}

	cases := []struct {
		name     string
		variants []manifestVariant
		label    string
		want     string
		wantOK   bool
	}{
		{name: "未声明档位取分辨率最高", variants: []manifestVariant{low, high}, label: "", want: "1080", wantOK: true},
		{name: "声明档位按高度匹配", variants: []manifestVariant{low, high}, label: "1080p", want: "1080", wantOK: true},
		{name: "同高度取带宽最高", variants: []manifestVariant{highCheap, high}, label: "1080p", want: "1080", wantOK: true},
		{name: "无分辨率信息时取第一条", variants: []manifestVariant{noInfo}, label: "", want: "single", wantOK: true},
		// [05 §5.3]：无匹配**报错**，不得退到相近档位。
		{name: "声明档位无匹配不退档", variants: []manifestVariant{low, high}, label: "720p", wantOK: false},
		{name: "空清单无法选择", variants: nil, label: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := selectManifestVariant(tc.variants, tc.label)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v，期望 %v", ok, tc.wantOK)
			}
			if ok && got.Input != tc.want {
				t.Fatalf("选中 %q，期望 %q", got.Input, tc.want)
			}
		})
	}
}

// TestParseHLSManifest 验证 HLS 主清单解析与相对地址解析（[05 §5.3]）。
func TestParseHLSManifest(t *testing.T) {
	base := "https://cdn.example.com/hls/master.m3u8"
	variants, err := parseHLSManifest(hlsMaster, base)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(variants) != 2 {
		t.Fatalf("变体数 = %d，期望 2", len(variants))
	}
	if variants[0].Height != 360 || variants[0].Bandwidth != 800_000 {
		t.Fatalf("第一个变体 = %+v", variants[0])
	}
	// 相对地址必须按主清单地址解析（[05 §5.3] 的"选择结果的落实"）。
	if want := "https://cdn.example.com/hls/v/1080.m3u8"; variants[1].Input != want {
		t.Fatalf("变体地址 = %q，期望 %q", variants[1].Input, want)
	}
	// 每条变体都要带显式 `-map`（[05 §4.6.1]）。
	for _, variant := range variants {
		if len(variant.MapArgs) == 0 {
			t.Fatalf("变体缺少显式 -map: %+v", variant)
		}
	}
}

// TestParseHLSManifest_SingleStream 覆盖 [05 §5.3] 的"不必选择的情形"。
func TestParseHLSManifest_SingleStream(t *testing.T) {
	base := "https://cdn.example.com/hls/media.m3u8"
	variants, err := parseHLSManifest(hlsMediaPlaylist, base)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(variants) != 1 {
		t.Fatalf("变体数 = %d，期望 1", len(variants))
	}
	// 单流清单没有变体地址，输入就是清单自己。
	if variants[0].Input != base {
		t.Fatalf("输入 = %q，期望 %q", variants[0].Input, base)
	}
}

// TestParseDASHManifest_StreamIndex 验证 DASH 的流序号映射（[05 §5.3]）。
func TestParseDASHManifest_StreamIndex(t *testing.T) {
	variants, err := parseDASHManifest(dashManifest, "https://cdn.example.com/movie/manifest.mpd")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(variants) != 2 {
		t.Fatalf("变体数 = %d，期望 2", len(variants))
	}
	chosen, ok := selectManifestVariant(variants, "")
	if !ok {
		t.Fatal("未声明档位时应当能选中一条")
	}
	if chosen.Height != 1080 {
		t.Fatalf("选中高度 = %d，期望 1080", chosen.Height)
	}
	// 1080p 是第二个视频 Representation → `-map 0:v:1`。
	if !hasArg(chosen.MapArgs, "0:v:1") {
		t.Fatalf("视频流序号不对: %v", chosen.MapArgs)
	}
	if !hasArg(chosen.MapArgs, "0:a:0?") {
		t.Fatalf("音频轨未指定: %v", chosen.MapArgs)
	}
}

// TestParseFFmpegTime 覆盖 `HH:MM:SS.ffffff` 解析。
func TestParseFFmpegTime(t *testing.T) {
	cases := []struct {
		value string
		want  float64
		ok    bool
	}{
		{value: "00:00:25.000000", want: 25, ok: true},
		{value: "01:02:03.500000", want: 3723.5, ok: true},
		{value: "N/A", ok: false},
		{value: "", ok: false},
		{value: "25", ok: false},
	}
	for _, tc := range cases {
		got, ok := parseFFmpegTime(tc.value)
		if ok != tc.ok {
			t.Fatalf("parseFFmpegTime(%q) ok = %v，期望 %v", tc.value, ok, tc.ok)
		}
		if ok && got != tc.want {
			t.Fatalf("parseFFmpegTime(%q) = %v，期望 %v", tc.value, got, tc.want)
		}
	}
}

// TestFFmpegProgress_TimeRatio 验证 [05 §4.6.3] 的时长口径：
// `downloaded_bytes` 取 FFmpeg 报告的已写出字节，百分比走 TimeRatio。
func TestFFmpegProgress_TimeRatio(t *testing.T) {
	recorder := &progressRecorder{}
	progress := newFFmpegProgress(100, 0, recorder.record)
	progress.feed([]byte("total_size=4096\nout_time=00:00:25.000000\nprogress=continue\n"))

	frames := recorder.all()
	if len(frames) != 1 {
		t.Fatalf("进度帧数 = %d，期望 1", len(frames))
	}
	if frames[0].Downloaded != 4096 {
		t.Fatalf("downloaded_bytes = %d，期望 4096", frames[0].Downloaded)
	}
	if frames[0].TimeRatio < 0.24 || frames[0].TimeRatio > 0.26 {
		t.Fatalf("TimeRatio = %v，期望约 0.25", frames[0].TimeRatio)
	}
}

// TestFFmpegProgress_NoTotalTime 覆盖"拿不到总时长就不给百分比"。
func TestFFmpegProgress_NoTotalTime(t *testing.T) {
	recorder := &progressRecorder{}
	progress := newFFmpegProgress(0, 0, recorder.record)
	progress.feed([]byte("total_size=4096\nout_time=00:00:25.000000\nprogress=continue\n"))

	frames := recorder.all()
	if len(frames) != 1 {
		t.Fatalf("进度帧数 = %d，期望 1", len(frames))
	}
	if frames[0].TimeRatio != 0 {
		t.Fatalf("TimeRatio = %v，期望 0（不得伪造百分比）", frames[0].TimeRatio)
	}
}

// TestDirectHintMismatch 覆盖 [05 §4.0] 第二步的三种判据。
func TestDirectHintMismatch(t *testing.T) {
	target, err := url.Parse("https://cdn.example.com/media/stream")
	if err != nil {
		t.Fatalf("构造地址失败: %v", err)
	}
	redirected, err := url.Parse("https://cdn.example.com/media/index.m3u8")
	if err != nil {
		t.Fatalf("构造地址失败: %v", err)
	}

	cases := []struct {
		name        string
		contentType string
		finalURL    *url.URL
		want        Kind
	}{
		{name: "Content-Type 是 HLS 清单", contentType: "application/vnd.apple.mpegurl", finalURL: target, want: KindHLS},
		{name: "Content-Type 是 DASH 清单", contentType: "application/dash+xml", finalURL: target, want: KindDASH},
		{name: "Content-Type 带参数也算", contentType: "application/x-mpegurl; charset=utf-8", finalURL: target, want: KindHLS},
		{name: "Content-Type 是网页", contentType: "text/html; charset=utf-8", finalURL: target, want: KindPage},
		{name: "重定向落到清单扩展名", contentType: "video/mp4", finalURL: redirected, want: KindHLS},
		{name: "普通媒体文件不构成改路", contentType: "video/mp4", finalURL: target, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				Header:  http.Header{"Content-Type": []string{tc.contentType}},
				Request: &http.Request{URL: tc.finalURL},
			}
			got := directHintMismatch(resp)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("不该出现改路信号，实际 = %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("应当出现改路信号")
			}
			if got.Actual != tc.want {
				t.Fatalf("Actual = %q，期望 %q", got.Actual, tc.want)
			}
			if strings.Contains(got.Error(), "http") {
				t.Fatalf("信号文本不得含地址: %q", got.Error())
			}
		})
	}
}

// TestRun_DirectHintMismatch_WritesNothing 是"提示不成立"的端到端用例：
// P1 发现响应的 Content-Type 是清单，于是报出改路信号，且**一个字节都没写**。
func TestRun_DirectHintMismatch_WritesNothing(t *testing.T) {
	server := startBody(t, "application/vnd.apple.mpegurl", hlsMaster)
	l := newLayout(t, "plan-mismatch")
	runner := &scriptedRunner{body: []byte("x")}
	d := newDownloader(t, Options{ToolRunner: runner})

	_, err := d.Run(context.Background(), l.request(server.URL, 0), nil)

	var mismatch *RouteMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("错误 = %v，期望 RouteMismatch", err)
	}
	if mismatch.Actual != KindHLS {
		t.Fatalf("Actual = %q，期望 %q", mismatch.Actual, KindHLS)
	}
	if files := dirFiles(t, l.planDir); len(files) != 0 {
		t.Fatalf("改路前不该写任何文件，实际有 %v", files)
	}
	if len(runner.ffmpegCalls()) != 0 {
		t.Fatal("改路前不该调用 FFmpeg")
	}
}

// TestRun_DirectHintMismatch_NotAfterBytes 验证 [05 §4.0] 的"已写入任何字节就禁止降级"：
// 有分片时不再核实（否则等于把已下的一半字节丢掉）。
func TestRun_DirectHintMismatch_NotAfterBytes(t *testing.T) {
	body := mediaBody(2048)
	server := startBody(t, "application/vnd.apple.mpegurl", string(body))
	l := newLayout(t, "plan-partial")
	// 预置一段残缺分片：续传时没有写字节之前就已经有内容了。
	l.writePartial(t, body[:1024])
	runner := &scriptedRunner{body: body}
	d := newDownloader(t, Options{ToolRunner: runner, DisableRangeResume: true})

	// 服务器不支持 Range，会把全量再发一遍，于是"响应长度不足"之类的失败也可能出现；
	// 关键断言是**没有**出现改路信号。
	_, err := d.Run(context.Background(), l.request(server.URL, int64(len(body))), nil)
	var mismatch *RouteMismatch
	if errors.As(err, &mismatch) {
		t.Fatalf("已有字节时不应降级，实际 = %+v", mismatch)
	}
}

// TestRun_HLS_SelectsHighestAndMaps 是 P2 的端到端用例：
// 选最高分辨率、把选择结果显式交给 FFmpeg、产物落到「已完成」。
func TestRun_HLS_SelectsHighestAndMaps(t *testing.T) {
	server := startManifestServer(t, "application/vnd.apple.mpegurl", hlsMaster)
	l := newLayout(t, "plan-hls")
	runner := &scriptedRunner{
		body:             mediaBody(64 * 1024),
		manifestDuration: 100,
		progress:         []string{"total_size=4096", "out_time=00:00:25.000000", "progress=continue"},
	}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request(server.URL+"/master", 0)
	req.Kind = KindHLS

	recorder := &progressRecorder{}
	result, err := d.Run(context.Background(), req, recorder.record)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	call := runner.lastFFmpeg(t)
	// 选中的是 1080p 那档，输入必须是它的绝对地址（[05 §5.3]）。
	if want := server.URL + "/v/1080.m3u8"; argValue(call.Args, "-i") != want {
		t.Fatalf("FFmpeg 输入 = %q，期望 %q", argValue(call.Args, "-i"), want)
	}
	// 必须显式 `-map`（[05 §4.6.1]）：默认只取第一个变体，而清单顺序与画质无关。
	if !hasArg(call.Args, "-map") {
		t.Fatalf("FFmpeg 参数缺少 -map: %v", call.Args)
	}
	if argValue(call.Args, "-c") != "copy" {
		t.Fatalf("-c = %q，期望 copy（streamcopy，B-302）", argValue(call.Args, "-c"))
	}
	// 没有请求头时不得出现 `-headers`（[05 §4.6.1]）。
	if hasArg(call.Args, "-headers") {
		t.Fatalf("无请求头时不该传 -headers: %v", call.Args)
	}
	if want := manifestStagingPath(l.planDir, "mp4"); call.Args[len(call.Args)-1] != want {
		t.Fatalf("输出路径 = %q，期望 %q", call.Args[len(call.Args)-1], want)
	}

	// 产物落到「已完成」。
	if result.FinalPath != filepath.Join(l.outputDir, "样本.mp4") {
		t.Fatalf("成品路径 = %q", result.FinalPath)
	}
	if got := readFile(t, result.FinalPath); len(got) != 64*1024 {
		t.Fatalf("成品长度 = %d，期望 %d", len(got), 64*1024)
	}

	// 阶段顺序与 P1 相同（[05 §2.1]）：merging 只是最后那次原子交付。
	want := []string{PhaseDownloading, PhaseValidating, PhaseMerging}
	if got := recorder.phases(); len(got) != len(want) {
		t.Fatalf("阶段序列 = %v，期望 %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("阶段序列 = %v，期望 %v", got, want)
			}
		}
	}

	// 时长口径：至少有一帧带 TimeRatio（[05 §4.6.3]）。
	foundRatio := false
	for _, frame := range recorder.all() {
		if frame.TimeRatio > 0 {
			foundRatio = true
		}
	}
	if !foundRatio {
		t.Fatal("没有任何带 TimeRatio 的进度帧")
	}
}

// TestRun_HLS_HeadersGoToFFmpeg 验证请求头经 `-headers` 传递
// （[05 §4.6.1] 的"禁止放命令行"唯一例外）。
func TestRun_HLS_HeadersGoToFFmpeg(t *testing.T) {
	server := startManifestServer(t, "application/vnd.apple.mpegurl", hlsMaster)
	l := newLayout(t, "plan-hls-auth")
	runner := &scriptedRunner{body: mediaBody(4096)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request(server.URL+"/master", 0)
	req.Kind = KindHLS
	req.Headers = http.Header{"Authorization": []string{"Bearer secret-token"}}

	if _, err := d.Run(context.Background(), req, nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	call := runner.lastFFmpeg(t)
	headers := argValue(call.Args, "-headers")
	if headers == "" {
		t.Fatalf("请求头没有传给 FFmpeg: %v", call.Args)
	}
	if !strings.Contains(headers, "Authorization: Bearer secret-token") {
		t.Fatalf("-headers 内容不对: %q", headers)
	}
	if !strings.Contains(headers, "\r\n") {
		t.Fatalf("-headers 必须以 CRLF 分隔: %q", headers)
	}
}

// TestRun_HLS_QualityNoMatch 验证"声明的档位匹配不到就报错、不退档"（[05 §5.3]）。
func TestRun_HLS_QualityNoMatch(t *testing.T) {
	server := startManifestServer(t, "application/vnd.apple.mpegurl", hlsMaster)
	l := newLayout(t, "plan-hls-nomatch")
	runner := &scriptedRunner{body: mediaBody(4096)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request(server.URL+"/master", 0)
	req.Kind = KindHLS
	req.QualityLabel = "720p"

	_, err := d.Run(context.Background(), req, nil)
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("错误 = %v，期望带错误码", err)
	}
	if code != CodeManifestNoMatchingStream {
		t.Fatalf("错误码 = %q，期望 %q", code, CodeManifestNoMatchingStream)
	}
	if Retryable(code) {
		t.Fatalf("%q 不该可重试（[05 §7.2]）", code)
	}
	if len(runner.ffmpegCalls()) != 0 {
		t.Fatal("选不出流时不该调用 FFmpeg")
	}
}

// TestRun_HLS_HTMLContentType 覆盖 [05 §4.0] 第二步第三种情形：
// 提示是清单、拿回来的却是网页 → 报改路信号（可改走 P4）。
func TestRun_HLS_HTMLContentType(t *testing.T) {
	server := startBody(t, "text/html; charset=utf-8", "<html><body>登录后可见</body></html>")
	l := newLayout(t, "plan-hls-html")
	runner := &scriptedRunner{body: mediaBody(4096)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request(server.URL, 0)
	req.Kind = KindHLS

	_, err := d.Run(context.Background(), req, nil)
	var mismatch *RouteMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("错误 = %v，期望 RouteMismatch", err)
	}
	if mismatch.Actual != KindPage {
		t.Fatalf("Actual = %q，期望 %q", mismatch.Actual, KindPage)
	}
}

// TestRun_HLS_UnparsableReportsPage 验证"清单解析不了"时报改路信号而不是
// `manifest_invalid`——[05 §4.0] 要给这条页面地址留一条 P4 的路。
func TestRun_HLS_UnparsableReportsPage(t *testing.T) {
	server := startBody(t, "application/vnd.apple.mpegurl", "这不是清单")
	l := newLayout(t, "plan-hls-bad")
	runner := &scriptedRunner{body: mediaBody(4096)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request(server.URL, 0)
	req.Kind = KindHLS

	_, err := d.Run(context.Background(), req, nil)
	var mismatch *RouteMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("错误 = %v，期望 RouteMismatch", err)
	}
	if mismatch.Actual != KindPage {
		t.Fatalf("Actual = %q，期望 %q", mismatch.Actual, KindPage)
	}
}

// TestRun_ManifestFetchFailureIsRetryable 验证清单取不回来算**可重试**
// 的下载失败（[05 §4.6.1]："清单地址失效仍按 §8 计数并最终 failed"）。
func TestRun_ManifestFetchFailureIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	l := newLayout(t, "plan-hls-503")
	runner := &scriptedRunner{body: mediaBody(4096)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request(server.URL, 0)
	req.Kind = KindHLS

	_, err := d.Run(context.Background(), req, nil)
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("错误 = %v，期望带错误码", err)
	}
	if code != CodeDownloadFailed {
		t.Fatalf("错误码 = %q，期望 %q", code, CodeDownloadFailed)
	}
	if !Retryable(code) {
		t.Fatalf("%q 必须可重试（[05 §4.6.1]）", code)
	}
}

// TestRun_HLS_SingleStreamChoosesItself 覆盖"不必选择的情形"（[05 §5.3]）。
func TestRun_HLS_SingleStreamChoosesItself(t *testing.T) {
	server := startManifestServer(t, "application/vnd.apple.mpegurl", hlsMediaPlaylist)
	l := newLayout(t, "plan-hls-single")
	runner := &scriptedRunner{body: mediaBody(8192)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request(server.URL+"/master", 0)
	req.Kind = KindHLS

	if _, err := d.Run(context.Background(), req, nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	call := runner.lastFFmpeg(t)
	if got := argValue(call.Args, "-i"); got != server.URL+"/master" {
		t.Fatalf("FFmpeg 输入 = %q，期望清单自身地址", got)
	}
	if !hasArg(call.Args, "-map") {
		t.Fatalf("FFmpeg 参数缺少 -map: %v", call.Args)
	}
}
