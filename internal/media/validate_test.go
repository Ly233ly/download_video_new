package media

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 校验失败（FFprobe 看不到任何可用流）时**不得交付**（[05 §1]、[05 §6]）。
func TestRunDirect_NoStreamsFailsWithoutDelivering(t *testing.T) {
	body := mediaBody(512)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-nostream")
	runner := &probeRunner{respond: func(int64) ([]byte, error) {
		return probeJSON(t, "mov,mp4,m4a,3gp,3g2,mj2", 0), nil
	}}

	d := newDownloader(t, Options{ToolRunner: runner})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err == nil {
		t.Fatal("无可用流必须报错")
	}
	if result.FinalPath != "" {
		t.Error("校验未通过时不得返回交付路径")
	}
	code, ok := CodeOf(err)
	if !ok || code != CodeOutputNoStreams {
		t.Fatalf("错误码 = %v，期望 %s", err, CodeOutputNoStreams)
	}
	assertNothingDelivered(t, dirs)
	// 已证明是坏的字节不得留着：否则下次续传会把它当成"已有分片"复用。
	if _, statErr := os.Stat(dirs.staging()); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("校验失败后应清掉临时产物，实际 %v", statErr)
	}
}

// 视频容器里没有视频流 → `output_no_video`。
func TestRunDirect_NoVideoStreamFails(t *testing.T) {
	dirs, err := runWithProbe(t, "plan-novideo", "mp4", func(size int64) ([]byte, error) {
		return probeJSON(t, "mov,mp4,m4a,3gp,3g2,mj2", 1.0, audioStream()), nil
	})
	if err == nil {
		t.Fatal("缺少视频流必须报错")
	}
	code, ok := CodeOf(err)
	if !ok || code != CodeOutputNoVideo {
		t.Fatalf("错误码 = %v，期望 %s", err, CodeOutputNoVideo)
	}
	assertNothingDelivered(t, dirs)
}

// 纯音频容器里没有音频流 → `output_no_audio`。
func TestRunDirect_NoAudioStreamFails(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-noaudio")
	runner := &probeRunner{respond: func(int64) ([]byte, error) {
		return probeJSON(t, "mov,mp4,m4a,3gp,3g2,mj2", 1.0, videoStream(640, 360)), nil
	}}
	req := dirs.request(server.URL, int64(len(body)))
	req.OutputName = "音频样本.m4a"
	req.Container = "m4a"

	d := newDownloader(t, Options{ToolRunner: runner})
	if _, err := d.Run(context.Background(), req, nil); err == nil {
		t.Fatal("纯音频容器缺少音频流必须报错")
	} else if code, ok := CodeOf(err); !ok || code != CodeOutputNoAudio {
		t.Fatalf("错误码 = %v，期望 %s", err, CodeOutputNoAudio)
	}
	assertNothingDelivered(t, dirs)
}

// 视频容器里没有音轨**不是**失败：无声视频是合法媒体，
// 报 `output_no_audio` 会把好文件判死。
func TestRunDirect_VideoWithoutAudioIsDelivered(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-silent")
	runner := &probeRunner{respond: func(int64) ([]byte, error) {
		return probeJSON(t, "mov,mp4,m4a,3gp,3g2,mj2", 1.0, videoStream(1280, 720)), nil
	}}

	d := newDownloader(t, Options{ToolRunner: runner})
	if _, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil); err != nil {
		t.Fatalf("无声视频应正常交付: %v", err)
	}
}

// 时长校验**本阶段不写死阈值**（[05 §6.1] 的 `N1`）：探测结果与预期差得再多，
// 也不能凭空拦下交付。等阶段 3 用真实样本定出容差后再补这条判定。
func TestRunDirect_DurationMismatchIsNotJudgedYet(t *testing.T) {
	body := mediaBody(1000)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-duration")
	runner := &probeRunner{respond: func(int64) ([]byte, error) {
		// 字节数换算约 1 秒，这里回报 999 秒。
		return probeJSON(t, "mov,mp4,m4a,3gp,3g2,mj2", 999, videoStream(1920, 1080), audioStream()), nil
	}}

	d := newDownloader(t, Options{ToolRunner: runner})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		t.Fatalf("时长容差未定前不得据此拦交付: %v", err)
	}
	if result.Probe.Duration != 999 {
		t.Errorf("ProbeInfo.Duration = %v，期望 999（必须如实回传实测值）", result.Probe.Duration)
	}
}

// 缺 FFprobe **不是**跳过校验的理由：宁可失败，也不交付未校验的输出（[05 §1]）。
func TestRun_MissingFFprobeRefusesToDeliver(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-notools")
	d, err := New(nil, Options{ToolRunner: &probeRunner{}})
	if err != nil {
		t.Fatalf("缺工具时应允许构造（不阻塞启动）: %v", err)
	}

	result, runErr := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if runErr == nil {
		t.Fatal("没有 FFprobe 时必须报错")
	}
	if result.FinalPath != "" {
		t.Error("未校验的输出不得交付")
	}
	if code, ok := CodeOf(runErr); !ok || code != CodeDownloadFailed {
		t.Fatalf("错误码 = %v，期望 %s", runErr, CodeDownloadFailed)
	}
	assertNothingDelivered(t, dirs)
}

// FFprobe 非零退出时**不把原始 stderr 给用户**（[05 §5.2]）。
func TestRunDirect_ProbeExitFailureHidesRawStderr(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	secret := "C:\\secret\\path\\internal.mp4: Invalid data found"
	runner := &probeRunner{respond: func(int64) ([]byte, error) {
		return nil, errors.New(secret)
	}}

	dirs := newLayout(t, "plan-probefail")
	d := newDownloader(t, Options{ToolRunner: runner})
	_, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err == nil {
		t.Fatal("FFprobe 失败必须报错")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "Invalid data") {
		t.Errorf("用户可见消息泄露了原始 stderr：%q", err.Error())
	}
	assertNothingDelivered(t, dirs)
}

// 校验阶段被取消 → 输出仍不得交付（[05 §10] 与 [05 §1] 同时成立）。
func TestRunDirect_CancelDuringValidateDoesNotDeliver(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-cancel-validate")

	ctx, cancel := context.WithCancel(context.Background())
	// 假执行器在"探测"期间把 context 取消掉——模拟用户在 validating 阶段点了停止。
	runner := &probeRunner{respond: func(int64) ([]byte, error) {
		cancel()
		return healthyProbe(256)
	}}

	d := newDownloader(t, Options{ToolRunner: runner})
	result, err := d.Run(ctx, dirs.request(server.URL, int64(len(body))), nil)
	if err == nil {
		t.Fatal("取消后必须返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("应可识别为 context.Canceled，实得 %v", err)
	}
	if result.FinalPath != "" {
		t.Error("取消后不得交付")
	}
	assertNothingDelivered(t, dirs)
}

// 校验器的 panic **不得**带走调用方（[12 §3.2]：运行期 panic 只允许在启动期，
// B-306 禁止单计划异常终止后台循环）。
func TestRunDirect_ProbePanicBecomesError(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-panic")
	runner := &probeRunner{respond: func(int64) ([]byte, error) {
		panic("假 FFprobe 崩了")
	}}

	d := newDownloader(t, Options{ToolRunner: runner})
	_, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err == nil {
		t.Fatal("校验器 panic 必须转成错误")
	}
	if code, ok := CodeOf(err); !ok || code != CodeDownloadFailed {
		t.Fatalf("错误码 = %v，期望 %s", err, CodeDownloadFailed)
	}
	assertNothingDelivered(t, dirs)
}

// 校验通过的产物必须带着实测的媒体事实回来（[Result.Probe]）。
func TestRunDirect_ResultCarriesProbeFacts(t *testing.T) {
	body := mediaBody(65_536)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-facts")
	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	if result.Bytes != int64(len(body)) {
		t.Errorf("Bytes = %d，期望 %d", result.Bytes, len(body))
	}
	if result.Probe.Size != int64(len(body)) {
		t.Errorf("Probe.Size = %d，期望 %d", result.Probe.Size, len(body))
	}
	if !result.Probe.HasVideo || !result.Probe.HasAudio {
		t.Errorf("Probe 应报告视频与音频都存在，实得 %+v", result.Probe)
	}
	if result.Probe.StreamCount != 2 {
		t.Errorf("StreamCount = %d，期望 2", result.Probe.StreamCount)
	}
	if result.Probe.Duration <= 0 {
		t.Errorf("Duration = %v，期望正数", result.Probe.Duration)
	}
	if got := result.Probe.Streams[0]; got.Width != 1280 || got.Height != 720 || got.Codec != "h264" {
		t.Errorf("第一条流的实测值不对：%+v", got)
	}
	if result.Probe.Streams[1].SampleHz != 44100 || result.Probe.Streams[1].Channels != 2 {
		t.Errorf("第二条音频流的实测值不对：%+v", result.Probe.Streams[1])
	}
}

// `-print_format json` 的输出无法解析时必须报错而**不是**猜一个容器/时长。
func TestParseProbeOutput_RejectsUnparsableJSON(t *testing.T) {
	if _, err := parseProbeOutput([]byte("这不是 JSON")); err == nil {
		t.Fatal("不可解析的输出必须报错")
	}
}

// 流级时长为 `N/A` 时退回容器时长；两者都没有就保持 0（不猜）。
func TestParseProbeOutput_FallsBackToStreamDuration(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","duration":"12.5"}],
      "format":{"format_name":"matroska,webm"}}`)
	info, err := parseProbeOutput(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.Duration != 12.5 {
		t.Errorf("Duration = %v，期望 12.5（退回流级时长）", info.Duration)
	}
	if info.Container != "matroska,webm" {
		t.Errorf("Container = %q", info.Container)
	}
}

// 容器取值的大小写与点号差异不影响轨道判定。
func TestWantsTrack_NormalizesContainer(t *testing.T) {
	for _, container := range []string{"m4a", "M4A", " .m4a "} {
		if wantsVideo(container) {
			t.Errorf("%q 是纯音频容器，不应要求视频流", container)
		}
		if !wantsAudio(container) {
			t.Errorf("%q 必须要求音频流", container)
		}
	}
	for _, container := range []string{"mp4", "mkv", "webm", "ts", ""} {
		if !wantsVideo(container) {
			t.Errorf("%q 应当要求视频流", container)
		}
		if wantsAudio(container) {
			t.Errorf("%q 不应强制要求音频流（无声视频是合法媒体）", container)
		}
	}
}

// runWithProbe 跑一次"下载成功但校验按给定响应判定"的流程，返回布局与错误。
func runWithProbe(
	t *testing.T, planID, container string, respond func(size int64) ([]byte, error),
) (layout, error) {
	t.Helper()
	body := mediaBody(512)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, planID)
	req := dirs.request(server.URL, int64(len(body)))
	req.Container = container
	if container == "m4a" {
		req.OutputName = "样本.m4a"
	}

	d := newDownloader(t, Options{ToolRunner: &probeRunner{respond: respond}})
	_, err := d.Run(context.Background(), req, nil)
	return dirs, err
}

// assertNothingDelivered 断言「已完成」目录里没有任何文件（未通过校验的输出永不交付）。
func assertNothingDelivered(t *testing.T, dirs layout) {
	t.Helper()
	entries, err := os.ReadDir(dirs.outputDir)
	if err != nil {
		t.Fatalf("读输出目录失败: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("「已完成」目录不应有文件，实得 %v", names)
	}
}

// 目录参数非法时返回的错误必须**可安全展示**（[05 §7.4]）。
func TestErrorOf_MessageIsSafeForDisplay(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "秘密目录")
	for _, code := range []Code{
		CodeDownloadFailed, CodeDiskFull, CodeOutputNoStreams, CodeOutputNoVideo,
		CodeOutputNoAudio, CodeOutputDurationMismatch, CodeOutputNameExhausted,
	} {
		err := ErrorOf(code, errors.New("cause 里可能带 "+secretPath))
		if strings.Contains(err.Error(), secretPath) {
			t.Errorf("%s 的消息泄露了 cause：%q", code, err.Error())
		}
		if err.Error() == "" {
			t.Errorf("%s 必须有可安全展示的消息（[05 §7.4]）", code)
		}
		if !strings.ContainsAny(err.Error(), "下载文件磁盘校验同名失败空间") {
			t.Errorf("%s 的消息不像中文文案：%q", code, err.Error())
		}
	}
	// 未登记的码也必须给兜底文案，不能是空串。
	if got := Code("不存在的码").Message(); got == "" {
		t.Error("未登记的错误码必须有兜底消息")
	}
}

// 错误码的重试归属只能来自 [05 §7]，不得在别处各写一份。
func TestRetryable_OnlyDownloadFailedIsRetryable(t *testing.T) {
	if !Retryable(CodeDownloadFailed) {
		t.Error("download_failed 属 [05 §7.1] 的可重试集合")
	}
	for _, code := range []Code{
		CodeDiskFull, CodeOutputNoStreams, CodeOutputNoVideo,
		CodeOutputNoAudio, CodeOutputDurationMismatch, CodeOutputNameExhausted,
	} {
		if Retryable(code) {
			t.Errorf("%s 属 [05 §7.2] 的不可重试集合", code)
		}
	}
}

// 错误链上必须能取到码，且 `Error()` 不带 cause（[12 §3.3] 的 E2）。
func TestCodeOf_FindsCodeThroughWrapping(t *testing.T) {
	base := ErrorOf(CodeDiskFull, errors.New("内部原因"))
	wrapped := errors.New("外层包装: " + base.Error())
	if _, ok := CodeOf(wrapped); ok {
		t.Error("普通包装不应被当成带码错误")
	}
	if code, ok := CodeOf(base); !ok || code != CodeDiskFull {
		t.Errorf("CodeOf = %v, %v", code, ok)
	}
	if !errors.Is(base, base.cause) {
		t.Error("Unwrap 应暴露 cause 供 errors.Is 使用")
	}
}

// 构造期只拒绝明显非法的选项：负数间隔/超时/上限都是编程错误（[12 §3.2]）。
func TestNew_RejectsNegativeOptions(t *testing.T) {
	cases := map[string]Options{
		"负的进度间隔": {ProgressInterval: -time.Millisecond},
		"负的工具超时": {ToolTimeout: -time.Second},
		"负的输出上限": {OutputLimit: -1},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(nil, opts); err == nil {
				t.Fatal("负值选项必须被拒绝")
			}
		})
	}
}

// 零值选项必须能直接构造出可用的下载器（调用方不该被迫填一堆默认值）。
func TestNew_ZeroOptionsAreUsable(t *testing.T) {
	d, err := New(nil, Options{})
	if err != nil {
		t.Fatalf("零值选项应当可用: %v", err)
	}
	if d == nil {
		t.Fatal("必须返回下载器")
	}
	if d.progress != DefaultProgressInterval {
		t.Errorf("进度间隔 = %v，期望默认值 %v", d.progress, DefaultProgressInterval)
	}
	if d.toolWait != DefaultToolTimeout {
		t.Errorf("工具超时 = %v，期望默认值 %v", d.toolWait, DefaultToolTimeout)
	}
	if d.outLimit != DefaultOutputLimit {
		t.Errorf("输出上限 = %d，期望默认值 %d", d.outLimit, DefaultOutputLimit)
	}
	if !d.rangeOn {
		t.Error("断点续传默认必须是**开启**的（零值即启用，见 Options.DisableRangeResume）")
	}
	if _, ok := d.runner.(ProcessRunner); !ok {
		t.Errorf("默认执行器 = %T，期望 ProcessRunner", d.runner)
	}
}

// 显式关闭续传时不得再发 Range（服务端对范围请求处理有 bug 时的退路）。
func TestRunDirect_DisableRangeResumeSendsNoRange(t *testing.T) {
	body := mediaBody(1024)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-norange")
	dirs.writePartial(t, body[:256])

	d := newDownloader(t, Options{DisableRangeResume: true})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		t.Fatalf("关闭续传后应全量下载: %v", err)
	}
	for _, value := range media.ranges() {
		if value != "" {
			t.Errorf("关闭续传后不得带 Range，实得 %q", value)
		}
	}
	if string(readFile(t, result.FinalPath)) != string(body) {
		t.Error("全量下载的内容与源不一致")
	}
}

// 分片比预期总长还长时不得续传（预期值已过期，远端可能换了文件）：
// 必须从头取，绝不拿一段来路不明的字节去校验。
func TestRunDirect_OversizedPartialRestartsFromZero(t *testing.T) {
	body := mediaBody(4096)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-oversize")
	dirs.writePartial(t, mediaBody(8192)) // 比"预期总长"还大

	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		t.Fatalf("过期分片应触发从头下载: %v", err)
	}
	if string(readFile(t, result.FinalPath)) != string(body) {
		t.Errorf("结果长度 %d，期望 %d（旧分片的尾巴被留下了？）", len(readFile(t, result.FinalPath)), len(body))
	}
	for _, value := range media.ranges() {
		if value != "" {
			t.Errorf("过期分片不得用于续传，实得 Range=%q", value)
		}
	}
}

// 预期总长未知（0）时不阻止续传：此时"分片是否属于同一文件"无法判定，
// 但 `[05 §4.1]` 的断点语义要求尽量从断点继续，且长度自检由响应头兜底。
func TestRunDirect_UnknownTotalStillResumes(t *testing.T) {
	body := mediaBody(2048)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-unknown")
	dirs.writePartial(t, body[:512])

	d := newDownloader(t, Options{})
	if _, err := d.Run(context.Background(), dirs.request(server.URL, 0), nil); err != nil {
		t.Fatalf("总长未知时也应能续传: %v", err)
	}
	ranges := media.ranges()
	if len(ranges) != 1 || ranges[0] != "bytes=512-" {
		t.Errorf("应当从 512 续传，实得 %v", ranges)
	}
}

// `Content-Range` 与请求偏移不符时必须报错而不是把两段拼起来（会拼出坏文件）。
func TestRunDirect_MismatchedContentRangeFails(t *testing.T) {
	body := mediaBody(1024)
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 请求的是 bytes=512-，却回了从 0 开始的一段。
		w.Header().Set("Content-Range", "bytes 0-511/1024")
		w.Header().Set("Content-Length", "512")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[:512])
	}))

	dirs := newLayout(t, "plan-badrange")
	dirs.writePartial(t, body[:512])

	d := newDownloader(t, Options{})
	if _, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil); err == nil {
		t.Fatal("Content-Range 与请求偏移不符必须报错")
	}
	assertNothingDelivered(t, dirs)
}

// 同一个计划被并发提交时**必须串行执行**：两个执行体同时往
// `临时\<plan_id>\direct.bin` 追加会拼出坏文件，而坏文件仍可能通过结构校验
// （容器头还在）——那就变成"交付了一个内容错误但校验通过的文件"。
//
// 注意本用例**不要求**"只交付一份"：`Request` 里没有"本计划已交付过"的输入，
// 所以同内容重复执行会产生多个**唯一名**的副本。这是 [05 §11] 同名策略的
// 必然结果、不是缺陷——去重（B-506）是另一个模块的判定。
func TestRun_SerializesSamePlanConcurrently(t *testing.T) {
	body := mediaBody(256 * 1024)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-concurrent")
	d := newDownloader(t, Options{})

	const workers = 4
	var wg sync.WaitGroup
	results := make([]Result, workers)
	errs := make([]error, workers)
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start // 尽量让几个执行体同时冲进来
			results[index], errs[index] = d.Run(
				context.Background(), dirs.request(server.URL, int64(len(body))), nil)
		}(i)
	}
	close(start)
	wg.Wait()

	// 每次执行都必须独立成功，且交付内容必须与源**逐字节相同**：
	// 只要串行化失效，落盘的就是两段交叉写入的字节。
	delivered := make(map[string]bool, workers)
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 次执行失败: %v", i, errs[i])
		}
		if got := string(readFile(t, results[i].FinalPath)); got != string(body) {
			t.Fatalf("第 %d 次交付的内容与源不一致（长度 %d，期望 %d）", i, len(got), len(body))
		}
		if delivered[results[i].FinalPath] {
			t.Fatalf("两次执行交付到了同一个路径：%s", results[i].FinalPath)
		}
		delivered[results[i].FinalPath] = true
	}

	entries, err := os.ReadDir(dirs.outputDir)
	if err != nil {
		t.Fatalf("读输出目录失败: %v", err)
	}
	if len(entries) != workers {
		t.Errorf("交付文件数 = %d，期望 %d（每次执行一份，各自唯一名）", len(entries), workers)
	}
}
