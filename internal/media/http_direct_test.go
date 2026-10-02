package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// [05 §4.1]：`Range` 断点续传——已有分片时从断点继续，且**结果要与全量下载逐字节相同**。
func TestRunDirect_ResumesFromPartialFile(t *testing.T) {
	body := mediaBody(4096)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-resume")
	dirs.writePartial(t, body[:1024])

	recorder := &progressRecorder{}
	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), recorder.record)
	if err != nil {
		t.Fatalf("续传失败: %v", err)
	}

	if got := string(readFile(t, result.FinalPath)); got != string(body) {
		t.Fatalf("续传结果与源文件不一致：长度 %d，期望 %d", len(got), len(body))
	}
	ranges := media.ranges()
	if len(ranges) != 1 || ranges[0] != "bytes=1024-" {
		t.Fatalf("应当只发一次 `bytes=1024-` 的范围请求，实得 %v", ranges)
	}
	if result.Bytes != int64(len(body)) {
		t.Errorf("交付字节数 = %d，期望 %d", result.Bytes, len(body))
	}
	if _, err := os.Stat(dirs.staging()); !errors.Is(err, os.ErrNotExist) {
		t.Error("交付后临时分片应当已搬走")
	}
}

// [05 §4.1]：服务端**不支持 Range**（对范围请求回 200 全量）时从头下载，
// 结果必须与源一致——不能把全量字节追加到已有分片后面。
func TestRunDirect_ServerIgnoringRangeRestartsFromZero(t *testing.T) {
	body := mediaBody(2048)
	media := &fakeMedia{body: body, ignoreRange: true}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-ignore-range")
	dirs.writePartial(t, body[:512])

	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		t.Fatalf("服务端忽略 Range 时应回退全量并成功: %v", err)
	}

	got := readFile(t, result.FinalPath)
	if len(got) != len(body) {
		t.Fatalf("回退全量后的长度 = %d，期望 %d（把全量追加到了旧分片后面？）", len(got), len(body))
	}
	if string(got) != string(body) {
		t.Error("回退全量后的内容与源文件不一致")
	}
	if ranges := media.ranges(); len(ranges) == 0 || ranges[0] != "bytes=512-" {
		t.Errorf("第一次请求本应带 Range（服务器随后忽略它），实得 %v", ranges)
	}
}

// [05 §4.1]：416 范围无效时**回退全量**。
func TestRunDirect_RangeNotSatisfiableFallsBackToFull(t *testing.T) {
	body := mediaBody(1500)
	media := &fakeMedia{body: body, rejectRange: true}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-416")
	dirs.writePartial(t, body[:300])

	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		t.Fatalf("416 之后应回退全量并成功: %v", err)
	}

	got := readFile(t, result.FinalPath)
	if string(got) != string(body) {
		t.Fatalf("回退全量后的内容与源文件不一致（长度 %d / 期望 %d）", len(got), len(body))
	}
	ranges := media.ranges()
	if len(ranges) < 2 {
		t.Fatalf("应当先试分段再回退全量（两次请求），实得 %d 次: %v", len(ranges), ranges)
	}
	if ranges[0] != "bytes=300-" {
		t.Errorf("第一次请求应带断点范围 bytes=300-，实得 %q", ranges[0])
	}
	if ranges[len(ranges)-1] != "" {
		t.Errorf("回退请求不应再带 Range，实得 %q", ranges[len(ranges)-1])
	}
}

// [05 §4.1]：**没有 `Content-Length` 时不伪造百分比**——
// 所有进度帧的 Total 必须是 0，且不能出现任何暗示总长的值。
func TestRunDirect_NoContentLengthKeepsTotalUnknown(t *testing.T) {
	media := &fakeMedia{body: mediaBody(64 * 1024), noLength: true}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-nolen")
	recorder := &progressRecorder{}

	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(server.URL, 0), recorder.record)
	if err != nil {
		t.Fatalf("无 Content-Length 的下载应成功: %v", err)
	}

	frames := recorder.all()
	if len(frames) == 0 {
		t.Fatal("至少要报一帧进度")
	}
	for i, frame := range frames {
		if frame.Total != 0 {
			t.Errorf("第 %d 帧的 Total = %d，无 Content-Length 时必须为 0（不得伪造总长）", i, frame.Total)
		}
	}
	if result.Bytes != 64*1024 {
		t.Errorf("交付字节数 = %d，期望 %d", result.Bytes, 64*1024)
	}
}

// 进度必须**单调不减**，且阶段按 下载 → 校验 → 落盘 推进（[05 §2]、[05 §6]）。
func TestRunDirect_ProgressIsMonotonicAndPhased(t *testing.T) {
	body := mediaBody(128 * 1024)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-progress")
	recorder := &progressRecorder{}

	d := newDownloader(t, Options{})
	if _, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), recorder.record); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	frames := recorder.all()
	last := int64(-1)
	for i, frame := range frames {
		if frame.Downloaded < last {
			t.Fatalf("第 %d 帧下载量回退：%d → %d", i, last, frame.Downloaded)
		}
		last = frame.Downloaded
		if frame.Total != 0 && frame.Total != int64(len(body)) {
			t.Errorf("第 %d 帧的总长 = %d，期望 %d", i, frame.Total, len(body))
		}
		if frame.Downloaded > frame.Total && frame.Total > 0 {
			t.Errorf("第 %d 帧下载量 %d 超过总长 %d", i, frame.Downloaded, frame.Total)
		}
	}

	// 阶段顺序的理由见 [Downloader.execute]：校验通过之后才允许触碰「已完成」目录。
	phases := recorder.phases()
	want := []string{PhaseDownloading, PhaseValidating, PhaseMerging}
	if strings.Join(phases, ">") != strings.Join(want, ">") {
		t.Errorf("阶段推进 = %v，期望 %v", phases, want)
	}

	// 下载阶段收尾时必须达到总长，否则进度条永远差一截。
	var lastDownloading int64
	for _, frame := range frames {
		if frame.Phase == PhaseDownloading {
			lastDownloading = frame.Downloaded
		}
	}
	if lastDownloading != int64(len(body)) {
		t.Errorf("下载阶段最后一帧 = %d，期望 %d", lastDownloading, len(body))
	}
}

// 分片已经等于预期总长时**不重复取字节**，但仍必须走校验（[05 §6] 是唯一门槛）。
func TestRunDirect_SkipsTransferWhenPartialIsComplete(t *testing.T) {
	body := mediaBody(1024)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-complete")
	dirs.writePartial(t, body)

	runner := &probeRunner{}
	d := newDownloader(t, Options{ToolRunner: runner})
	if _, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil); err != nil {
		t.Fatalf("完整分片应直接进入校验: %v", err)
	}
	if media.requestCount() != 0 {
		t.Errorf("分片已完整时不应再发请求，实得 %d 次", media.requestCount())
	}
	if runner.callCount() != 1 {
		t.Errorf("校验次数 = %d，期望 1（跳过下载不等于跳过校验）", runner.callCount())
	}
}

// 请求头（页面凭据）必须原样带到请求上，且**只用于本次请求**（B-303/B-722）。
func TestRunDirect_ForwardsCredentialHeaders(t *testing.T) {
	body := mediaBody(512)
	media := &fakeMedia{body: body, requireAuth: map[string]string{
		"Authorization": "Bearer secret-token",
		"Cookie":        "session=abc",
	}}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-headers")
	req := dirs.request(server.URL, int64(len(body)))
	req.Headers = http.Header{
		"Authorization": []string{"Bearer secret-token"},
		"Cookie":        []string{"session=abc"},
	}

	d := newDownloader(t, Options{})
	if _, err := d.Run(context.Background(), req, nil); err != nil {
		t.Fatalf("带凭据的下载失败: %v", err)
	}
	if got := media.auth(); len(got) != 1 || got[0] != "Bearer secret-token" {
		t.Errorf("服务端看到的 Authorization = %v", got)
	}
}

// 跨来源重定向时凭据**不得**跟着走（B-304 / [05 §4.2]）。
//
// 这里刻意让"另一个来源"是同主机**不同端口**：`net/http` 的默认策略只比主机名
// （实测 `shouldHeaderBeCopiedOnRedirect` 忽略端口），所以默认策略会放行；
// 本包的策略必须拦住它。
func TestRunDirect_DoesNotForwardCredentialsAcrossOrigins(t *testing.T) {
	body := mediaBody(512)
	target := &fakeMedia{body: body}
	targetServer := startMedia(t, target)

	redirector := &fakeMedia{redirectTo: targetServer.URL}
	redirectServer := startMedia(t, redirector)

	dirs := newLayout(t, "plan-redirect")
	req := dirs.request(redirectServer.URL, int64(len(body)))
	req.Headers = http.Header{
		"Authorization": []string{"Bearer secret-token"},
		"Cookie":        []string{"session=abc"},
	}

	d := newDownloader(t, Options{})
	if _, err := d.Run(context.Background(), req, nil); err == nil {
		t.Fatal("跨来源重定向必须中止（不得把凭据送到另一个来源）")
	}
	for _, got := range target.auth() {
		if got != "" {
			t.Fatalf("跨来源重定向泄露了 Authorization: %q", got)
		}
	}
	if target.requestCount() != 0 {
		t.Errorf("凭据保护应当发生在发请求之前，目标服务却被访问了 %d 次", target.requestCount())
	}
	if entries, readErr := os.ReadDir(dirs.outputDir); readErr != nil || len(entries) != 0 {
		t.Errorf("中止后不得交付任何文件，实得 %v（err=%v）", entries, readErr)
	}
}

// 同一来源内的重定向（例如站点把 `/a` 跳到 `/b`）应当照常跟随——
// 保护凭据不能变成"任何重定向都失败"。
func TestRunDirect_FollowsSameOriginRedirect(t *testing.T) {
	body := mediaBody(256)
	var target *httptestServer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
			return
		}
		http.Redirect(w, r, target.URL+"/final", http.StatusFound)
	})
	target = newHTTPTestServer(t, handler)

	dirs := newLayout(t, "plan-same-origin")
	req := dirs.request(target.URL+"/start", int64(len(body)))
	req.Headers = http.Header{"Authorization": []string{"Bearer secret-token"}}

	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("同来源重定向应被跟随: %v", err)
	}
	if string(readFile(t, result.FinalPath)) != string(body) {
		t.Error("同来源重定向后的内容与源文件不一致")
	}
}

// 重定向次数必须有上限（[12 §4] 的 C2：无上限的跨边界调用不允许）。
func TestRunDirect_RedirectLoopIsBounded(t *testing.T) {
	var server *httptestServer
	hops := 0
	server = newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, server.URL+"/loop", http.StatusFound)
	}))

	dirs := newLayout(t, "plan-loop")
	d := newDownloader(t, Options{})
	if _, err := d.Run(context.Background(), dirs.request(server.URL+"/loop", 0), nil); err == nil {
		t.Fatal("无限重定向必须报错而不是一直转下去")
	}
	if hops > MaxRedirects+1 {
		t.Errorf("重定向跳数 %d 超过上限 %d", hops, MaxRedirects)
	}
}

// 读取器出现 `(0, nil)`（合法的空读）时循环不能原地打转：
// 真实网络里少见、但在 reader 包装层上完全可能，一旦空转就是死循环。
func TestStreamTo_ToleratesEmptyReads(t *testing.T) {
	payload := mediaBody(4096)
	reader := &emptyReadInjector{data: payload, every: 3}

	var sink bytes.Buffer
	var lastFrame Progress
	written, err := streamTo(reader, &sink, 0, int64(len(payload)), time.Millisecond, func(p Progress) {
		lastFrame = p
	})
	if err != nil {
		t.Fatalf("空读不应导致失败: %v", err)
	}
	if written != int64(len(payload)) {
		t.Errorf("written = %d，期望 %d", written, len(payload))
	}
	if sink.Len() != len(payload) {
		t.Errorf("落盘长度 = %d，期望 %d", sink.Len(), len(payload))
	}
	if string(sink.Bytes()) != string(payload) {
		t.Error("落盘内容与源不一致")
	}
	if lastFrame.Downloaded != int64(len(payload)) || lastFrame.Phase != PhaseDownloading {
		t.Errorf("末帧 = %+v，期望下载完成的那一帧", lastFrame)
	}
}

// emptyReadInjector 每 `every` 次读取夹一次 `(0, nil)`。
type emptyReadInjector struct {
	data    []byte
	at      int
	every   int
	pending bool
}

func (r *emptyReadInjector) Read(p []byte) (int, error) {
	if r.pending {
		r.pending = false
		return 0, nil // 合法但无进展的一次读取
	}
	if r.at >= len(r.data) {
		return 0, io.EOF
	}
	r.every--
	if r.every <= 0 {
		r.pending = true
		r.every = 3
	}
	n := copy(p, r.data[r.at:])
	r.at += n
	return n, nil
}

// `Content-Length` 声明了却读不满时算失败，且**保留分片**供下次续传（[05 §4.1]）。
func TestRunDirect_TruncatedBodyFailsAndKeepsPartial(t *testing.T) {
	truncated := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mediaBody(1024)) // 只写一半就结束
	}))

	dirs := newLayout(t, "plan-truncated")
	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(truncated.URL, 4096), nil)
	if err == nil {
		t.Fatal("响应体长度不足时必须报错")
	}
	if result.FinalPath != "" {
		t.Error("失败时不得返回交付路径")
	}
	code, ok := CodeOf(err)
	if !ok || code != CodeDownloadFailed {
		t.Errorf("错误码 = %v，期望 %s", err, CodeDownloadFailed)
	}
	// 保留分片：它就是断点续传的起点。
	if _, statErr := os.Stat(dirs.staging()); statErr != nil {
		t.Errorf("截断失败时应保留分片以便续传，实际 %v", statErr)
	}
}

// 4xx 与 5xx 都必须落到 `download_failed`（[05 §7.1] 的可重试码），
// 且**用户可见消息里不得出现 URL 或路径**（[12 §3.3] 的 E3）。
func TestRunDirect_HTTPErrorMapsToDownloadFailed(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			statusServer := newStatusServer(t, status)
			dirs := newLayout(t, "plan-status")
			d := newDownloader(t, Options{})
			_, err := d.Run(context.Background(), dirs.request(statusServer.URL, 10), nil)
			if err == nil {
				t.Fatal("非 2xx 响应必须报错")
			}
			code, ok := CodeOf(err)
			if !ok || code != CodeDownloadFailed {
				t.Fatalf("错误码 = %v，期望 %s", err, CodeDownloadFailed)
			}
			if strings.Contains(err.Error(), statusServer.URL) {
				t.Errorf("用户可见消息里出现了 URL：%q", err.Error())
			}
			if strings.Contains(err.Error(), dirs.base) {
				t.Errorf("用户可见消息里出现了路径：%q", err.Error())
			}
		})
	}
}

// `newStatusServer` 起一个永远返回指定状态码的本地服务。
func newStatusServer(t *testing.T, status int) *httptestServer {
	t.Helper()
	return newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
}

// 取消后**不得交付**，且临时产物必须是"有明确归属"的那一个才被清理（[05 §10]、B-404）。
func TestRun_CanceledDoesNotDeliverAndCleansOwnedTemp(t *testing.T) {
	handlerStarted := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mediaBody(1024))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		once.Do(func() { close(handlerStarted) })
		<-release // 卡住连接，等测试取消
	})
	server := newHTTPTestServer(t, handler)

	dirs := newLayout(t, "plan-cancel")

	// 预置一个**不属于本次**的文件：它在计划目录的父目录里，
	// 清理逻辑绝不能碰它（[05 §1]：无法证明归属的文件永不删除）。
	bystander := filepath.Join(dirs.tempRoot, "别人的文件.bin")
	if err := os.MkdirAll(dirs.tempRoot, 0o700); err != nil {
		t.Fatalf("建临时根目录失败: %v", err)
	}
	if err := os.WriteFile(bystander, []byte("keep-me"), 0o600); err != nil {
		t.Fatalf("写旁观文件失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := newDownloader(t, Options{})
	errCh := make(chan error, 1)
	go func() {
		_, err := d.Run(ctx, dirs.request(server.URL, 1048576), nil)
		errCh <- err
	}()

	<-handlerStarted
	cancel()
	close(release)

	err := <-errCh
	if err == nil {
		t.Fatal("取消后必须返回错误（不得交付）")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("取消应可被 errors.Is(err, context.Canceled) 识别，实得 %v", err)
	}
	if entries, readErr := os.ReadDir(dirs.outputDir); readErr != nil || len(entries) != 0 {
		t.Errorf("取消后「已完成」目录必须为空，实得 %v（err=%v）", entries, readErr)
	}
	if _, statErr := os.Stat(dirs.staging()); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("取消后应清掉本次的临时分片，实际 %v", statErr)
	}
	if _, statErr := os.Stat(bystander); statErr != nil {
		t.Errorf("清理越界：删掉了不属于本次计划的文件（%v）", statErr)
	}
	// 本次创建的临时目录应被回收（[05 §11] 的"清理自己创建的空目录"）。
	if _, statErr := os.Stat(dirs.planDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("本次新建的空计划目录应被回收，实际 %v", statErr)
	}
}

// 每次尝试都从零进度开场，避免界面上挂着上一轮的旧值。
func TestRun_ReportsFreshProgressPerAttempt(t *testing.T) {
	status := http.StatusInternalServerError
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))

	dirs := newLayout(t, "plan-fresh")
	recorder := &progressRecorder{}
	d := newDownloader(t, Options{})
	if _, err := d.Run(context.Background(), dirs.request(server.URL, 1024), recorder.record); err == nil {
		t.Fatal("5xx 必须失败")
	}
	frames := recorder.all()
	if len(frames) == 0 {
		t.Fatal("失败也应报进度帧（否则界面停在旧值）")
	}
	if frames[0].Downloaded != 0 || frames[0].Total != 1024 {
		t.Errorf("首帧应为 0/1024，实得 %d/%d", frames[0].Downloaded, frames[0].Total)
	}
}

// 目录参数不合法时必须**报错而不是猜**：宁可不下载，也不写到来路不明的路径。
func TestRun_RejectsInvalidDirectories(t *testing.T) {
	cases := map[string]Request{
		"缺地址":   {TempDir: "C:\\tmp", OutputDir: "C:\\out", OutputName: "a.mp4"},
		"缺临时目录": {URL: "http://example.invalid/a.mp4", OutputDir: "C:\\out", OutputName: "a.mp4"},
		"缺输出目录": {URL: "http://example.invalid/a.mp4", TempDir: "C:\\tmp", OutputName: "a.mp4"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDownloader(t, Options{})
			if _, err := d.Run(context.Background(), req, nil); err == nil {
				t.Fatal("非法目录参数必须报错")
			} else if _, ok := CodeOf(err); !ok {
				t.Errorf("错误必须带稳定错误码，实得 %v", err)
			}
		})
	}
}

// `plan_id` 不得借目录穿越跑出临时根目录（[05 §11] 的归属要求）。
func TestResolveDirs_PlanIDCannotEscapeTempDir(t *testing.T) {
	base := t.TempDir()
	_, err := resolveDirs(Request{
		URL:       "http://example.invalid/a.mp4",
		PlanID:    "..",
		TempDir:   filepath.Join(base, "临时"),
		OutputDir: filepath.Join(base, "已完成"),
	})
	if err == nil {
		t.Fatal("plan_id = `..` 必须被拒绝")
	}
}

// 下载用的默认客户端必须关闭透明解压：否则 `Content-Length` 与实际落盘字节
// 对不上，断点偏移与长度自检会同时失效（[05 §4.1]）。
func TestDefaultHTTPClient_DisablesTransparentDecompression(t *testing.T) {
	client := DefaultHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("默认客户端的 Transport 类型 = %T", client.Transport)
	}
	if !transport.DisableCompression {
		t.Error("必须关闭透明解压（DisableCompression）")
	}
	if client.Timeout != 0 {
		t.Error("不得设置固定超时：直链可能持续很久，超时应由 context 承担")
	}
}

// 一次成功的下载应当只在校验阶段调用一次 FFprobe，且参数是**数组**、
// 文件路径是最后一个参数（[05 §5.2]：禁止拼 shell 字符串）。
func TestRunDirect_ProbeUsesArgumentArrayWithFilePathLast(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-args")
	runner := &probeRunner{}
	d := newDownloader(t, Options{ToolRunner: runner})
	if _, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	call := runner.lastCall()
	if call.Path == "" {
		t.Fatal("必须带上 FFprobe 的可执行文件路径")
	}
	if len(call.Args) == 0 || call.Args[len(call.Args)-1] != dirs.staging() {
		t.Errorf("参数数组的最后一项应是待校验文件，实得 %v", call.Args)
	}
	for _, arg := range call.Args {
		if strings.ContainsAny(arg, "|&>") {
			t.Errorf("参数里出现 shell 元字符，疑似拼接了命令行：%q", arg)
		}
	}
	if call.Timeout <= 0 {
		t.Error("每次工具调用都必须有超时（[05 §5.2]）")
	}
}

// 超限的捕获必须被标记（[05 §5.2]：输出有上限、超限截断）。
func TestBoundedBuffer_TruncatesBeyondLimit(t *testing.T) {
	buf := &BoundedBuffer{Limit: 4}
	if _, err := buf.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if string(buf.Bytes()) != "abcd" {
		t.Errorf("内容 = %q，期望前 4 字节", buf.Bytes())
	}
	if !buf.Truncated {
		t.Error("超限必须置 Truncated")
	}
	if _, err := buf.Write([]byte("ij")); err != nil {
		t.Fatalf("继续写入失败: %v", err)
	}
	if string(buf.Bytes()) != "abcd" {
		t.Errorf("截断后不得再增长，实得 %q", buf.Bytes())
	}
}

// 工具调用超时必须与调用方取消**区分开**（处置不同：一个是可重试的下载失败，
// 一个是 canceled）。`ProcessRunner` 的两条分支都由
// [toolrun_test.go] 的用例覆盖（那里用测试二进制自身当子进程，不依赖本机命令）。
