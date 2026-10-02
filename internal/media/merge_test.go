package media

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// trackServer 是按路径返回不同字节的假媒体服务，并记录被请求过的路径。
type trackServer struct {
	mu     sync.Mutex
	bodies map[string][]byte
	seen   []string
}

func (s *trackServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.URL.Path)
	body, ok := s.bodies[r.URL.Path]
	s.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *trackServer) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func startTracks(t *testing.T, bodies map[string][]byte) (*trackServer, *httptest.Server) {
	t.Helper()
	handler := &trackServer{bodies: bodies}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return handler, server
}

// TestRun_Tracks_MapsEveryInput 是 P3 的端到端用例（T-DL-15）：
// 两条轨各自取回、合并时**逐条显式 `-map`**、不加 `-shortest`。
func TestRun_Tracks_MapsEveryInput(t *testing.T) {
	videoBody := mediaBody(8192)
	audioBody := mediaBody(4096)
	handler, server := startTracks(t, map[string][]byte{
		"/video": videoBody,
		"/audio": audioBody,
	})

	l := newLayout(t, "plan-tracks")
	runner := &scriptedRunner{body: mediaBody(16 * 1024)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request("", 0)
	req.Tracks = []Track{
		{Kind: trackKindVideo, URL: server.URL + "/video", TotalBytes: int64(len(videoBody))},
		{Kind: trackKindAudio, URL: server.URL + "/audio", TotalBytes: int64(len(audioBody))},
	}

	recorder := &progressRecorder{}
	result, err := d.Run(context.Background(), req, recorder.record)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	// 两条轨都被取回。
	if got := handler.paths(); len(got) != 2 {
		t.Fatalf("被请求的路径 = %v，期望两条轨各一次", got)
	}

	call := runner.lastFFmpeg(t)
	wantInputs := []string{
		filepath.Join(l.planDir, "video.bin"),
		filepath.Join(l.planDir, "audio.bin"),
	}
	inputs := argValues(call.Args, "-i")
	if len(inputs) != 2 || inputs[0] != wantInputs[0] || inputs[1] != wantInputs[1] {
		t.Fatalf("-i 参数 = %v，期望 %v", inputs, wantInputs)
	}

	// **必须**逐条显式 `-map`：不写时 FFmpeg 只从第一个输入取视频和音频，
	// 第二条输入的音轨会整个丢掉（[05 §4.6.2]）。
	maps := argPairs(call.Args, "-map")
	wantMaps := []string{"-map=0:v:0", "-map=1:a:0"}
	if len(maps) != len(wantMaps) {
		t.Fatalf("-map 参数 = %v，期望 %v", maps, wantMaps)
	}
	for i := range wantMaps {
		if maps[i] != wantMaps[i] {
			t.Fatalf("-map 参数 = %v，期望 %v", maps, wantMaps)
		}
	}

	// **不加** `-shortest`：它会按最短的轨截断，把合法长轨切掉。
	if hasArg(call.Args, "-shortest") {
		t.Fatalf("不得加 -shortest: %v", call.Args)
	}
	if argValue(call.Args, "-c") != "copy" {
		t.Fatalf("-c = %q，期望 copy（B-302）", argValue(call.Args, "-c"))
	}

	// 阶段顺序：先合并、再校验成品（[05 §4.6.2]、T-DL-15）。
	wantPhases := []string{PhaseDownloading, PhaseMerging, PhaseValidating}
	got := recorder.phases()
	if len(got) != len(wantPhases) {
		t.Fatalf("阶段序列 = %v，期望 %v", got, wantPhases)
	}
	for i := range wantPhases {
		if got[i] != wantPhases[i] {
			t.Fatalf("阶段序列 = %v，期望 %v", got, wantPhases)
		}
	}

	if result.FinalPath != filepath.Join(l.outputDir, "样本.mp4") {
		t.Fatalf("成品路径 = %q", result.FinalPath)
	}
	if got := readFile(t, result.FinalPath); len(got) != 16*1024 {
		t.Fatalf("成品长度 = %d，期望 %d", len(got), 16*1024)
	}
}

// TestRun_Tracks_ReusesCompleteTrack 验证"已完整取回的轨道不重下"
// （[05 §4.6.2]）。
func TestRun_Tracks_ReusesCompleteTrack(t *testing.T) {
	videoBody := mediaBody(8192)
	audioBody := mediaBody(4096)
	handler, server := startTracks(t, map[string][]byte{
		"/video": videoBody,
		"/audio": audioBody,
	})

	l := newLayout(t, "plan-tracks-resume")
	if err := os.MkdirAll(l.planDir, 0o700); err != nil {
		t.Fatalf("建临时目录失败: %v", err)
	}
	// 视频轨已经完整落盘，且它的字节数是**声明过**的。
	if err := os.WriteFile(filepath.Join(l.planDir, "video.bin"), videoBody, 0o600); err != nil {
		t.Fatalf("预置视频轨失败: %v", err)
	}

	runner := &scriptedRunner{body: mediaBody(16 * 1024)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request("", 0)
	req.Tracks = []Track{
		{Kind: trackKindVideo, URL: server.URL + "/video", TotalBytes: int64(len(videoBody))},
		{Kind: trackKindAudio, URL: server.URL + "/audio", TotalBytes: int64(len(audioBody))},
	}

	if _, err := d.Run(context.Background(), req, nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	if got := handler.paths(); len(got) != 1 || got[0] != "/audio" {
		t.Fatalf("被请求的路径 = %v，期望只请求 /audio", got)
	}
}

// TestRun_Tracks_RewritesUnverifiableTrack 验证"无法确认完整时重下"
// （[05 §4.6.2]）：没有声明字节数，磁盘上有文件也不算数。
func TestRun_Tracks_RewritesUnverifiableTrack(t *testing.T) {
	videoBody := mediaBody(8192)
	audioBody := mediaBody(4096)
	handler, server := startTracks(t, map[string][]byte{
		"/video": videoBody,
		"/audio": audioBody,
	})

	l := newLayout(t, "plan-tracks-unverifiable")
	if err := os.MkdirAll(l.planDir, 0o700); err != nil {
		t.Fatalf("建临时目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(l.planDir, "video.bin"), videoBody[:100], 0o600); err != nil {
		t.Fatalf("预置视频轨失败: %v", err)
	}

	runner := &scriptedRunner{body: mediaBody(16 * 1024)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request("", 0)
	// 刻意**不**声明字节数。
	req.Tracks = []Track{
		{Kind: trackKindVideo, URL: server.URL + "/video"},
		{Kind: trackKindAudio, URL: server.URL + "/audio"},
	}

	if _, err := d.Run(context.Background(), req, nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	paths := handler.paths()
	if len(paths) != 2 {
		t.Fatalf("被请求的路径 = %v，期望两条轨都重下", paths)
	}
}

// TestRun_Tracks_UnknownTotalStaysUnknown 验证 [05 §4.6.3]：
// 任一轨未声明字节数时 `total_bytes` 为 NULL，**不得**用已声明之和充数。
func TestRun_Tracks_UnknownTotalStaysUnknown(t *testing.T) {
	videoBody := mediaBody(8192)
	audioBody := mediaBody(4096)
	_, server := startTracks(t, map[string][]byte{
		"/video": videoBody,
		"/audio": audioBody,
	})

	l := newLayout(t, "plan-tracks-unknown")
	runner := &scriptedRunner{body: mediaBody(16 * 1024)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request("", 0)
	req.Tracks = []Track{
		{Kind: trackKindVideo, URL: server.URL + "/video", TotalBytes: int64(len(videoBody))},
		// 音频轨未声明字节数。
		{Kind: trackKindAudio, URL: server.URL + "/audio"},
	}

	recorder := &progressRecorder{}
	if _, err := d.Run(context.Background(), req, recorder.record); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	for _, frame := range recorder.all() {
		if frame.Total != 0 {
			t.Fatalf("有轨未声明时 total 必须为 0，实际 %d（帧 %+v）", frame.Total, frame)
		}
	}
}

// TestRun_Tracks_MergeFailureNotRetryable 验证合并失败归为 `merge_failed`
// 且**不可重试**（[05 §4.6.2]、[05 §7.2]）。
func TestRun_Tracks_MergeFailureNotRetryable(t *testing.T) {
	videoBody := mediaBody(8192)
	audioBody := mediaBody(4096)
	_, server := startTracks(t, map[string][]byte{
		"/video": videoBody,
		"/audio": audioBody,
	})

	l := newLayout(t, "plan-tracks-fail")
	runner := &scriptedRunner{body: mediaBody(16 * 1024), fail: true}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request("", 0)
	req.Tracks = []Track{
		{Kind: trackKindVideo, URL: server.URL + "/video", TotalBytes: int64(len(videoBody))},
		{Kind: trackKindAudio, URL: server.URL + "/audio", TotalBytes: int64(len(audioBody))},
	}

	_, err := d.Run(context.Background(), req, nil)
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("错误 = %v，期望带错误码", err)
	}
	if code != CodeMergeFailed {
		t.Fatalf("错误码 = %q，期望 %q", code, CodeMergeFailed)
	}
	if Retryable(code) {
		t.Fatalf("%q 不可重试（输入是本地文件，重试只会重复失败）", code)
	}
}

// TestRun_Tracks_MissingURL 验证有轨缺地址时**先检查完再动手**：
// 不下载任何一条轨，也不去调 FFmpeg。
func TestRun_Tracks_MissingURL(t *testing.T) {
	videoBody := mediaBody(8192)
	handler, server := startTracks(t, map[string][]byte{"/video": videoBody})

	l := newLayout(t, "plan-tracks-nourl")
	runner := &scriptedRunner{body: mediaBody(1024)}
	d := newDownloader(t, Options{ToolRunner: runner})

	req := l.request("", 0)
	req.Tracks = []Track{
		{Kind: trackKindVideo, URL: server.URL + "/video", TotalBytes: int64(len(videoBody))},
		{Kind: trackKindAudio},
	}

	_, err := d.Run(context.Background(), req, nil)
	if err == nil {
		t.Fatal("应当报错")
	}
	var coded *Error
	if !errors.As(err, &coded) {
		t.Fatalf("错误 = %v，期望带错误码", err)
	}
	if coded.Code != CodeDownloadFailed {
		t.Fatalf("错误码 = %q，期望 %q", coded.Code, CodeDownloadFailed)
	}
	if got := handler.paths(); len(got) != 0 {
		t.Fatalf("地址不全时不该开始下载，实际请求了 %v", got)
	}
	if len(runner.ffmpegCalls()) != 0 {
		t.Fatal("缺地址时不该调用 FFmpeg")
	}
}
