package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ly233ly/download_video_new/internal/media"
	"github.com/Ly233ly/download_video_new/internal/store"
)

// 路径自动路由的测试（[05 §4.0]、B-316）。
//
// 这里要一起验两件事：**改路只发生一次**，且**改路之后看得见**。
// 少任何一件，用户能看到的就只有"这次怎么变慢了"，查不出原因。
//
// 注意：本文件的用例**不能只靠 `runOnce` 判断"跑完了"**——那个 helper 等的是
// "计划不再是 queued"（即被调度取走），执行本身还在跑。改路要再调一次引擎，
// 窗口更长，所以这里统一等"引擎被调用了第 N 次"这个确定信号。

// routeCall 记录一次交给引擎的请求，用于事后核对"第几次用了哪条路、哪个地址"。
type routeCall struct {
	kind string
	url  string
}

// routeLog 是并发安全的调用记录。
type routeLog struct {
	mu    sync.Mutex
	calls []routeCall
}

func (l *routeLog) record(req DownloadRequest) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, routeCall{kind: req.MediaKind, url: req.MediaURL})
	return len(l.calls)
}

func (l *routeLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

func (l *routeLog) at(t *testing.T, index int) routeCall {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if index >= len(l.calls) {
		t.Fatalf("没有第 %d 次调用（共 %d 次）", index+1, len(l.calls))
	}
	return l.calls[index]
}

// recordRoute 给测试环境装上可编排的引擎替身，并记下每次请求。
func recordRoute(env *planTestEnv, respond func(call int) (DownloadResult, error)) *routeLog {
	log := &routeLog{}
	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		return respond(log.record(req))
	}
	return log
}

// waitForCalls 等到引擎至少被调用 want 次，再多等一会儿确认没有多出来的第 N+1 次。
//
// 这段等待本身就有意义：它同时证明"该发生的发生了"和"不该发生的没发生"
// （[05 §4.0] 的"只降一次"）。
func waitForCalls(t *testing.T, log *routeLog, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && log.count() < want {
		time.Sleep(5 * time.Millisecond)
	}
	if got := log.count(); got < want {
		t.Fatalf("引擎被调用 %d 次，期望至少 %d 次", got, want)
	}
	time.Sleep(150 * time.Millisecond)
}

// waitForPlanSettled 等这次执行给出结论（完成或确定失败），并返回落库的行。
func waitForPlanSettled(t *testing.T, env *planTestEnv, id string) store.Plan {
	t.Helper()

	var settled store.Plan
	waitForCondition(t, 5*time.Second, func() bool {
		row, err := env.db.GetPlan(context.Background(), id)
		if err != nil {
			return false
		}
		settled = row
		return row.Status == store.PlanStatusCompleted || row.Status == store.PlanStatusFailed
	})
	return settled
}

// ---------------------------------------------------------------------------
// 该不该降：纯函数表（[05 §4.0] 第二步）
// ---------------------------------------------------------------------------

func TestFallbackFor_OnlyThreeRoutesCanChange(t *testing.T) {
	hls := &media.RouteMismatch{Actual: media.KindHLS, Address: "https://cdn.example.com/index.m3u8"}
	dash := &media.RouteMismatch{Actual: media.KindDASH, Address: "https://cdn.example.com/manifest.mpd"}
	page := &media.RouteMismatch{Actual: media.KindPage}
	wechat := &media.RouteMismatch{Actual: media.KindWechat}
	plain := errors.New("普通的下载失败")

	cases := []struct {
		name     string
		hint     string
		runErr   error
		pageURL  string
		wantNext string
		wantAddr string
		wantOK   bool
	}{
		{
			name: "直链提示但实际是清单（用重定向后的地址）",
			hint: store.PlanMediaDirect, runErr: hls, pageURL: "https://example.com/watch?v=1",
			wantNext: store.PlanMediaHLS, wantAddr: "https://cdn.example.com/index.m3u8", wantOK: true,
		},
		{
			name: "直链提示但实际是 DASH 清单",
			hint: store.PlanMediaDirect, runErr: dash, pageURL: "https://example.com/watch?v=1",
			wantNext: store.PlanMediaDASH, wantAddr: "https://cdn.example.com/manifest.mpd", wantOK: true,
		},
		{
			name: "直链提示但实际是网页（退回页面解析）",
			hint: store.PlanMediaDirect, runErr: page, pageURL: "https://example.com/watch?v=1",
			wantNext: store.PlanMediaPage, wantAddr: "https://example.com/watch?v=1", wantOK: true,
		},
		{
			name: "清单提示但实际是网页",
			hint: store.PlanMediaHLS, runErr: page, pageURL: "https://example.com/watch?v=1",
			wantNext: store.PlanMediaPage, wantAddr: "https://example.com/watch?v=1", wantOK: true,
		},
		{
			name: "网页提示但实际是网页（自己不再降级）",
			hint: store.PlanMediaPage, runErr: page, pageURL: "https://example.com/watch?v=1",
			wantOK: false,
		},
		{
			name: "没有来源页面地址可退（[05 §4.0]：为空不得降级）",
			hint: store.PlanMediaDirect, runErr: page, pageURL: "",
			wantOK: false,
		},
		{
			name: "同类不换（清单提示、清单实际）",
			hint: store.PlanMediaHLS, runErr: hls, pageURL: "https://example.com/watch?v=1",
			wantOK: false,
		},
		{
			name: "视频号没有备胎",
			hint: store.PlanMediaWechat, runErr: page, pageURL: "https://example.com/watch?v=1",
			wantOK: false,
		},
		{
			name: "浏览器中转没有备胎",
			hint: store.PlanMediaBrowser, runErr: page, pageURL: "https://example.com/watch?v=1",
			wantOK: false,
		},
		{
			name: "实际是视频号（不是可降的目标）",
			hint: store.PlanMediaDirect, runErr: wechat, pageURL: "https://example.com/watch?v=1",
			wantOK: false,
		},
		{
			name: "不是改路信号（普通失败不换路）",
			hint: store.PlanMediaDirect, runErr: plain, pageURL: "https://example.com/watch?v=1",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next, address, ok := fallbackFor(tc.hint, tc.runErr, tc.pageURL)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v，期望 %v", ok, tc.wantOK)
			}
			if next != tc.wantNext {
				t.Errorf("改走 = %q，期望 %q", next, tc.wantNext)
			}
			if address != tc.wantAddr {
				t.Errorf("地址 = %q，期望 %q", address, tc.wantAddr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 一次执行内的降级（走真实的 dispatchDue → runPlan）
// ---------------------------------------------------------------------------

func TestRunPlan_FallsBackToManifest(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	log := recordRoute(env, func(call int) (DownloadResult, error) {
		if call == 1 {
			return DownloadResult{}, &media.RouteMismatch{
				Actual:  media.KindHLS,
				Address: "https://cdn.example.com/index.m3u8",
			}
		}
		return DownloadResult{FinalPath: `C:\已完成\v.mp4`, DownloadedBytes: 100}, nil
	})

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	runOnce(t, env, created.ID)
	waitForCalls(t, log, 2)
	plan := waitForPlanSettled(t, env, created.ID)

	if log.count() != 2 {
		t.Fatalf("引擎被调用 %d 次，期望 2 次（原始一次 + 改道后一次）", log.count())
	}
	if first := log.at(t, 0); first.kind != store.PlanMediaDirect {
		t.Errorf("第一次的提示 = %q，期望 direct（提示来自扩展，本包不改它）", first.kind)
	}
	second := log.at(t, 1)
	if second.kind != store.PlanMediaHLS {
		t.Errorf("第二次实际走的 = %q，期望 hls", second.kind)
	}
	if second.url != "https://cdn.example.com/index.m3u8" {
		t.Errorf("第二次的地址 = %q，期望重定向之后的那条", second.url)
	}

	if plan.Status != store.PlanStatusCompleted {
		t.Fatalf("状态 = %s，期望 completed", plan.Status)
	}
	if plan.ResolvedKind == nil || *plan.ResolvedKind != store.PlanMediaHLS {
		t.Errorf("resolved_kind = %v，期望 hls（[05 §4.0]：实际走的路径要落库）", plan.ResolvedKind)
	}
	// 成功路径本来就不写 attempt_count（只有转回重试时才写），所以这里期望 0：
	// "改道不消耗重试预算"的真正证据在 TestRunPlan_FallsBackOnlyOnce——
	// 那里两次都失败，计数必须停在 1（一次尝试）而不是 2。
	if plan.AttemptCount != 0 {
		t.Errorf("attempt_count = %d，期望 0（成功后不写尝试计数，改道也不增加）", plan.AttemptCount)
	}
}

func TestRunPlan_FallsBackToPageUsingSourceURL(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	log := recordRoute(env, func(call int) (DownloadResult, error) {
		if call == 1 {
			return DownloadResult{}, &media.RouteMismatch{Actual: media.KindPage}
		}
		return DownloadResult{FinalPath: `C:\已完成\v.mp4`, DownloadedBytes: 100}, nil
	})

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	runOnce(t, env, created.ID)
	waitForCalls(t, log, 2)
	plan := waitForPlanSettled(t, env, created.ID)

	if log.count() != 2 {
		t.Fatalf("引擎被调用 %d 次，期望 2 次", log.count())
	}
	second := log.at(t, 1)
	if second.kind != store.PlanMediaPage {
		t.Errorf("第二次实际走的 = %q，期望 page", second.kind)
	}
	// 退到页面解析时用的是**落库的 source_url**（[05 §4.0] 第二步），
	// 不是原地址：原来那个地址已经被证明不是媒体。
	if second.url != plan.SourceURL {
		t.Errorf("第二次的地址 = %q，期望落库的 source_url %q", second.url, plan.SourceURL)
	}
	if plan.ResolvedKind == nil || *plan.ResolvedKind != store.PlanMediaPage {
		t.Errorf("resolved_kind = %v，期望 page", plan.ResolvedKind)
	}
}

func TestRunPlan_FallsBackOnlyOnce(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	log := recordRoute(env, func(call int) (DownloadResult, error) {
		// 每次都报"实际是清单"：第二次之后必须原样冒出去，不许再降。
		return DownloadResult{}, &media.RouteMismatch{
			Actual:  media.KindHLS,
			Address: "https://cdn.example.com/index.m3u8",
		}
	})

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	runOnce(t, env, created.ID)
	waitForCalls(t, log, 2)

	if log.count() != 2 {
		t.Fatalf("引擎被调用 %d 次，期望 2 次——只降一次，禁止链式降级（[05 §4.0]）", log.count())
	}
	plan := env.planRow(t, created.ID)
	if plan.Status == store.PlanStatusCompleted {
		t.Error("两次都失败却记成了 completed")
	}
	if plan.ResolvedKind == nil || *plan.ResolvedKind != store.PlanMediaHLS {
		t.Errorf("resolved_kind = %v，期望 hls（降过一次就记下来）", plan.ResolvedKind)
	}
	// 两次引擎调用、一次尝试：改道不是重试，不得消耗重试预算（[05 §8]）。
	if plan.AttemptCount != 1 {
		t.Errorf("attempt_count = %d，期望 1——改道不是重试", plan.AttemptCount)
	}
}

// TestPlanView_ExposesResolvedKind 保证"实际走的路径"真能到界面（B-316）。
func TestPlanView_ExposesResolvedKind(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	log := recordRoute(env, func(call int) (DownloadResult, error) {
		if call == 1 {
			return DownloadResult{}, &media.RouteMismatch{
				Actual:  media.KindDASH,
				Address: "https://cdn.example.com/manifest.mpd",
			}
		}
		return DownloadResult{FinalPath: `C:\已完成\v.mp4`, DownloadedBytes: 100}, nil
	})

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	runOnce(t, env, created.ID)
	waitForCalls(t, log, 2)
	waitForPlanSettled(t, env, created.ID)

	view, err := env.svc.PlanGet(ctx, created.ID)
	if err != nil {
		t.Fatalf("PlanGet 失败：%v", err)
	}
	if view.ResolvedKind == nil {
		t.Fatal("resolvedKind 是 null——界面就没法显示实际走了哪条路")
	}
	if *view.ResolvedKind != store.PlanMediaDASH {
		t.Errorf("resolvedKind = %q，期望 dash", *view.ResolvedKind)
	}
	if view.MediaKind != store.PlanMediaDirect {
		t.Errorf("mediaKind = %q，期望保留 direct（那是扩展给的提示，不是实际路径）", view.MediaKind)
	}
}

// ---------------------------------------------------------------------------
// 进度的两种口径（[05 §4.6.3]）
// ---------------------------------------------------------------------------

func TestFromEngineProgress_TimeRatioBecomesDetail(t *testing.T) {
	progress := fromEngineProgress(media.Progress{
		Downloaded: 0,
		Total:      0, // 长度未知：清单类下载的常态
		TimeRatio:  0.42,
		Phase:      media.PhaseDownloading,
	})

	if progress.TotalBytes != nil {
		t.Errorf("长度未知时不得给出总字节，实际 %d", *progress.TotalBytes)
	}
	if progress.TimeRatio != 0.42 {
		t.Errorf("TimeRatio = %v，期望原样透传", progress.TimeRatio)
	}
	if !strings.Contains(progress.PhaseDetail, "42%") {
		t.Errorf("文案 = %q，期望带上时长进度（否则界面上看不出它在动）", progress.PhaseDetail)
	}
}

func TestFromEngineProgress_KnownTotalWins(t *testing.T) {
	progress := fromEngineProgress(media.Progress{
		Downloaded: 2048,
		Total:      4096,
		TimeRatio:  0.9, // 有字节口径时不得混算时长口径
		Phase:      media.PhaseDownloading,
	})

	if progress.TotalBytes == nil || *progress.TotalBytes != 4096 {
		t.Fatalf("TotalBytes = %v，期望 4096", progress.TotalBytes)
	}
	if strings.Contains(progress.PhaseDetail, "%") {
		t.Errorf("有字节口径时不该再拼百分比，文案 = %q", progress.PhaseDetail)
	}
}

func TestPercentOf_ClampsToOneAndHundred(t *testing.T) {
	cases := []struct {
		ratio float64
		want  int
	}{
		{ratio: 0, want: 1},       // 已经动了一点：显示 0% 像卡住了
		{ratio: 0.001, want: 1},   //
		{ratio: 0.5, want: 50},    //
		{ratio: 0.999, want: 100}, //
		{ratio: 1, want: 100},     //
		{ratio: 1.5, want: 100},   // 引擎偶尔报出略大于 1 的比例
	}
	for _, tc := range cases {
		if got := percentOf(tc.ratio); got != tc.want {
			t.Errorf("percentOf(%v) = %d，期望 %d", tc.ratio, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 轨道映射（[05 §4.6.2] 的 P3）
// ---------------------------------------------------------------------------

func TestToEngineTracks_NeedsTwoAddresses(t *testing.T) {
	cases := []struct {
		name   string
		tracks []StreamTrack
		want   int
	}{
		{name: "一条轨不构成 P3", tracks: []StreamTrack{{Kind: "video", URL: "https://a/1.mp4"}}, want: 0},
		{
			name: "两条带地址的轨",
			tracks: []StreamTrack{
				{Kind: "video", URL: "https://a/v.mp4", Bytes: 100},
				{Kind: "audio", URL: "https://a/a.m4a", Bytes: 50},
			},
			want: 2,
		},
		{
			name: "落库投影（没有地址）不构成 P3",
			tracks: []StreamTrack{
				{Kind: "video", Quality: "1080p"},
				{Kind: "audio", Quality: "128k"},
			},
			want: 0,
		},
		{
			name: "两条里只有一条带地址",
			tracks: []StreamTrack{
				{Kind: "video", URL: "https://a/v.mp4"},
				{Kind: "audio", Quality: "128k"},
			},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toEngineTracks(DownloadRequest{StreamPlan: tc.tracks})
			if len(got) != tc.want {
				t.Fatalf("轨数 = %d，期望 %d", len(got), tc.want)
			}
			if tc.want == 2 && got[0].TotalBytes != 100 {
				t.Errorf("轨上的声明字节数没传下去：%d", got[0].TotalBytes)
			}
		})
	}
}
