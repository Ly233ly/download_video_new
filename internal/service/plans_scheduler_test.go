package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ly233ly/download_video_new/internal/store"
)

// 调度与重试的测试（[05 §2]、[05 §8]、[05 §9]、[05 §10]）。
//
// 这些都是**契约级**行为：B-306（单计划异常不终止循环）、B-308（取消不被完成覆盖）、
// B-315（重试计数持久化）。

// ---------------------------------------------------------------------------
// 退避公式（[05 §8]：min(30 × 2^(attempt-1), 1800) 秒）
// ---------------------------------------------------------------------------

func TestRetryBackoff_MatchesFormula(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 30 * time.Second},   // 非法输入按第 1 次处理
		{attempt: 1, want: 30 * time.Second},   // 30 × 2^0
		{attempt: 2, want: 60 * time.Second},   // 30 × 2^1
		{attempt: 3, want: 120 * time.Second},  // 30 × 2^2
		{attempt: 4, want: 240 * time.Second},  //
		{attempt: 5, want: 480 * time.Second},  //
		{attempt: 6, want: 960 * time.Second},  //
		{attempt: 7, want: 1800 * time.Second}, // 1920 被上限夹住
		{attempt: 20, want: 1800 * time.Second},
		{attempt: 9999, want: 1800 * time.Second}, // 不得整数溢出
	}
	for _, tc := range cases {
		if got := retryBackoff(tc.attempt); got != tc.want {
			t.Errorf("retryBackoff(%d) = %v，期望 %v", tc.attempt, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 执行与重试决策
// ---------------------------------------------------------------------------

// runOnce 走一遍真实的执行路径（dispatchDue → runPlan），并等待它结束。
func runOnce(t *testing.T, env *planTestEnv, id string) {
	t.Helper()
	ctx := context.Background()
	env.svc.dispatchDue(ctx)
	waitForCondition(t, 3*time.Second, func() bool {
		plan, err := env.db.GetPlan(ctx, id)
		if err != nil {
			return false
		}
		return plan.Status != store.PlanStatusQueued || env.svc.plan.activeCount() == 0
	})
}

func TestRunPlan_CompletesAndWritesFinalPath(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		total := int64(4096)
		onProgress(DownloadProgress{DownloadedBytes: 2048, TotalBytes: &total, Phase: store.PlanPhaseDownloading, PhaseDetail: "正在下载"})
		return DownloadResult{
			FinalPath:       `C:\已完成\video.mp4`,
			PreviewPath:     `C:\预览\video.jpg`,
			DownloadedBytes: 4096,
			TotalBytes:      &total,
		}, nil
	}

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	runOnce(t, env, created.ID)

	plan := env.planRow(t, created.ID)
	if plan.Status != store.PlanStatusCompleted {
		t.Fatalf("状态 = %s，期望 completed", plan.Status)
	}
	if plan.FinalPath == "" {
		t.Error("completed 必须带 final_path（[03 §2.1]）")
	}
	if plan.Progress != 100 {
		t.Errorf("progress = %v，期望 100", plan.Progress)
	}
	// 交付路径来自引擎，且必须一路传到内存上下文（[03 §6] 允许落盘程序自身产物路径）。
	if env.runner.lastRequest().MediaURL == "" {
		t.Error("下载引擎没有收到媒体地址")
	}
	// 终态必须**立即**推送（[04 §4.2]：不受 200 ms 节流限制）。
	last, ok := env.pub.lastProgress()
	if !ok || last.ID != created.ID || last.Status != store.PlanStatusCompleted {
		t.Errorf("终态未推送完整 PlanView：%+v", last)
	}
}

func TestRunPlan_SchedulesRetryWithBackoff(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		return DownloadResult{}, &DownloadError{Code: CodeDownloadFailed, Cause: errors.New("网络中断")}
	}

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	runOnce(t, env, created.ID)

	plan := env.planRow(t, created.ID)
	if plan.Status != store.PlanStatusQueued {
		t.Fatalf("状态 = %s，期望 queued（第 1 次失败应当安排重试）", plan.Status)
	}
	if plan.AttemptCount != 1 {
		t.Errorf("attempt_count = %d，期望 1", plan.AttemptCount)
	}
	if plan.NextAttemptAt == nil {
		t.Fatal("next_attempt_at 未写入（B-315）")
	}
	wantAt := env.clock.Now().Add(30 * time.Second).Unix()
	if got := int64(*plan.NextAttemptAt); got != wantAt {
		t.Errorf("next_attempt_at = %d，期望 %d（首次退避 30 秒）", got, wantAt)
	}
}

// [03 §2.4]：retry_max = 0 表示不自动重试。
func TestRunPlan_DoesNotRetryWhenRetryMaxZero(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()
	if err := env.db.SetSetting(ctx, settingRetryMax, "0"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		return DownloadResult{}, &DownloadError{Code: CodeDownloadFailed}
	}

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	runOnce(t, env, created.ID)

	plan := env.planRow(t, created.ID)
	if plan.Status != store.PlanStatusFailed {
		t.Fatalf("状态 = %s，期望 failed（retry_max = 0 不重试）", plan.Status)
	}
	if plan.ErrorCode != CodeDownloadFailed {
		t.Errorf("error_code = %q，期望 %q", plan.ErrorCode, CodeDownloadFailed)
	}
	if plan.NextAttemptAt != nil {
		t.Error("failed 计划不该有 next_attempt_at")
	}
}

func TestRunPlan_FailsImmediatelyOnNonRetryableCode(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		return DownloadResult{}, &DownloadError{Code: CodeOutputNoVideo}
	}

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	runOnce(t, env, created.ID)

	plan := env.planRow(t, created.ID)
	if plan.Status != store.PlanStatusFailed {
		t.Fatalf("状态 = %s，期望 failed（[05 §7.2] 的不可重试码）", plan.Status)
	}
	if plan.AttemptCount != 0 {
		t.Errorf("attempt_count = %d，期望 0（[05 §8]：不可重试时计数不再增加）", plan.AttemptCount)
	}
}

// [05 §4.4.3]：浏览器中转（P6）的失败一律转 failed，不进入自动重试。
func TestRunPlan_BrowserPlanNeverAutoRetries(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		// 故意用一个"可重试"的码，验证 P6 的类型判断优先于错误码分类。
		return DownloadResult{}, &DownloadError{Code: CodeDownloadFailed}
	}

	req := baseRequest()
	req.MediaKind = store.PlanMediaBrowser
	created, err := env.svc.PlanCreate(ctx, req)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	runOnce(t, env, created.ID)

	plan := env.planRow(t, created.ID)
	if plan.Status != store.PlanStatusFailed {
		t.Fatalf("状态 = %s，期望 failed（P6 不自动重试）", plan.Status)
	}
}

func TestRunPlan_StopsRetryingAfterBudgetExhausted(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()
	// retry_max = 2：第一次失败 → 安排第 2 次；第二次失败 → 转 failed。
	if err := env.db.SetSetting(ctx, settingRetryMax, "2"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		return DownloadResult{}, &DownloadError{Code: CodeDownloadFailed}
	}

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	runOnce(t, env, created.ID)
	first := env.planRow(t, created.ID)
	if first.Status != store.PlanStatusQueued || first.AttemptCount != 1 {
		t.Fatalf("第 1 次失败后 = %s/%d，期望 queued/1", first.Status, first.AttemptCount)
	}

	// 把下次尝试时间提前到"已到期"，再跑一次。
	if err := env.db.MarkPlanQueuedForRetry(ctx, created.ID, 1, env.svc.plan.nowSeconds()-1); err != nil {
		t.Fatalf("提前到期失败: %v", err)
	}
	runOnce(t, env, created.ID)

	second := env.planRow(t, created.ID)
	if second.Status != store.PlanStatusFailed {
		t.Fatalf("第 2 次失败后 = %s，期望 failed（重试预算耗尽）", second.Status)
	}
	// attempt_count 记录的是**已安排的自动重试次数**：预算耗尽时不再安排重试，
	// 计数停在上一次写入的值（[05 §8]：耗尽即转 failed，不再增加）。
	if second.AttemptCount != 1 {
		t.Errorf("attempt_count = %d，期望 1（预算耗尽后不再增加）", second.AttemptCount)
	}
	if second.ErrorCode != CodeDownloadFailed {
		t.Errorf("error_code = %q，期望 %q", second.ErrorCode, CodeDownloadFailed)
	}
}

// B-306：单个计划的异常（这里是引擎 panic）不得终止后台处理循环。
func TestRunPlan_PanicDoesNotKillScheduleLoop(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()
	// 并发 1：让两条计划串行，第二个能不能跑起来就取决于循环是否还活着。
	if err := env.db.SetSetting(ctx, settingDownloadConcurrency, "1"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	var mu sync.Mutex
	exploded := false
	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		mu.Lock()
		first := !exploded
		exploded = true
		mu.Unlock()
		if first {
			panic("引擎内部炸了")
		}
		return DownloadResult{FinalPath: `C:\已完成\ok.mp4`, DownloadedBytes: 1}, nil
	}

	boom, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	ok, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if err := env.svc.StartPlans(ctx); err != nil {
		t.Fatalf("StartPlans 失败: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		env.svc.StopPlans(stopCtx)
	}()

	// 循环必须活着把第二条计划跑完。
	if !waitForCondition(t, 10*time.Second, func() bool {
		second, err := env.db.GetPlan(ctx, ok.ID)
		return err == nil && second.Status == store.PlanStatusCompleted
	}) {
		t.Fatal("第二条计划没有被执行——单个计划的异常终止了后台循环（B-306）")
	}

	first := env.planRow(t, boom.ID)
	if first.Status != store.PlanStatusFailed {
		t.Errorf("panic 的计划状态 = %s，期望 failed（不得永远挂在 running）", first.Status)
	}
}

// B-308：用户在校验阶段停止后，完成状态不得覆盖取消状态（端到端）。
func TestPlanStop_DuringValidationBeatsLateCompletion(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	release := make(chan struct{})
	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		// **刻意无视 ctx 取消**：模拟"字节已经下完、完成回调正在路上"的那一刻。
		<-release
		return DownloadResult{FinalPath: `C:\已完成\late.mp4`, DownloadedBytes: 100}, nil
	}

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	env.svc.dispatchDue(ctx)
	if !waitForCondition(t, 3*time.Second, func() bool {
		plan, err := env.db.GetPlan(ctx, created.ID)
		return err == nil && plan.Status == store.PlanStatusRunning
	}) {
		t.Fatal("计划没有进入 running")
	}

	stopped, err := env.svc.PlanStop(ctx, created.ID)
	if err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if stopped.Status != store.PlanStatusCanceled {
		t.Fatalf("停止后状态 = %s，期望 canceled", stopped.Status)
	}

	// 放行迟到的完成回调。
	close(release)
	if !waitForCondition(t, 3*time.Second, func() bool { return env.svc.plan.activeCount() == 0 }) {
		t.Fatal("执行体没有退出")
	}

	plan := env.planRow(t, created.ID)
	if plan.Status != store.PlanStatusCanceled {
		t.Errorf("状态 = %s，期望仍是 canceled——完成状态覆盖了取消状态（B-308）", plan.Status)
	}
	if plan.FinalPath != "" {
		t.Errorf("final_path = %q，取消的计划不得写入交付路径", plan.FinalPath)
	}
}

// ---------------------------------------------------------------------------
// 并发约束（[03 §2.4] 的 download_concurrency）
// ---------------------------------------------------------------------------

func TestDispatch_RespectsDownloadConcurrency(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()
	if err := env.db.SetSetting(ctx, settingDownloadConcurrency, "2"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	release := make(chan struct{})
	var peak, current int
	var mu sync.Mutex
	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		mu.Lock()
		current++
		if current > peak {
			peak = current
		}
		mu.Unlock()
		<-release
		mu.Lock()
		current--
		mu.Unlock()
		return DownloadResult{FinalPath: `C:\已完成\x.mp4`, DownloadedBytes: 1}, nil
	}

	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		created, err := env.svc.PlanCreate(ctx, baseRequest())
		if err != nil {
			t.Fatalf("创建失败: %v", err)
		}
		ids = append(ids, created.ID)
	}

	// 连续调度两轮：第二轮不该越过上限。
	env.svc.dispatchDue(ctx)
	env.svc.dispatchDue(ctx)

	if !waitForCondition(t, 3*time.Second, func() bool { return env.svc.plan.activeCount() == 2 }) {
		t.Fatalf("同时执行数 = %d，期望 2（download_concurrency）", env.svc.plan.activeCount())
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	got := peak
	mu.Unlock()
	if got > 2 {
		t.Errorf("并发峰值 = %d，超过 download_concurrency = 2", got)
	}

	close(release)
	if !waitForCondition(t, 5*time.Second, func() bool { return env.svc.plan.activeCount() == 0 }) {
		t.Fatal("执行体没有退出")
	}
}

// ---------------------------------------------------------------------------
// 进度：写库节流与 200 ms 推送（[03 §2.4]、[04 §4.2]）
// ---------------------------------------------------------------------------

func TestOnProgress_ThrottlesWriteAndPushSeparately(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := env.db.MarkPlanRunning(ctx, created.ID, store.PlanPhaseDownloading, store.PlanMediaDirect); err != nil {
		t.Fatalf("置运行失败: %v", err)
	}
	plan := env.planRow(t, created.ID)

	// 写库间隔 500 ms（配置默认值），推送间隔 200 ms（[04 §4.2]）。
	env.svc.plan.beginThrottle(created.ID, 500*time.Millisecond)
	total := int64(1000)

	env.svc.onProgress(ctx, plan, DownloadProgress{DownloadedBytes: 100, TotalBytes: &total, Phase: store.PlanPhaseDownloading})
	env.clock.Advance(100 * time.Millisecond)
	env.svc.onProgress(ctx, plan, DownloadProgress{DownloadedBytes: 200, TotalBytes: &total, Phase: store.PlanPhaseDownloading})

	if got := env.planRow(t, created.ID).DownloadedBytes; got != 100 {
		t.Errorf("downloaded_bytes = %d，期望 100（100 ms 内的第二次不该写库）", got)
	}
	if got := env.pub.progressCount(); got != 1 {
		t.Errorf("推送次数 = %d，期望 1（两次进度间隔 100 ms，不足 200 ms）", got)
	}

	env.clock.Advance(150 * time.Millisecond) // 累计 250 ms
	env.svc.onProgress(ctx, plan, DownloadProgress{DownloadedBytes: 300, TotalBytes: &total, Phase: store.PlanPhaseDownloading})

	if got := env.planRow(t, created.ID).DownloadedBytes; got != 100 {
		t.Errorf("downloaded_bytes = %d，期望仍为 100（250 ms 未到 500 ms）", got)
	}
	if got := env.pub.progressCount(); got != 2 {
		t.Errorf("推送次数 = %d，期望 2（250 ms 已过 200 ms）", got)
	}

	env.clock.Advance(400 * time.Millisecond) // 累计 650 ms
	env.svc.onProgress(ctx, plan, DownloadProgress{DownloadedBytes: 400, TotalBytes: &total, Phase: store.PlanPhaseMerging})

	if got := env.planRow(t, created.ID).DownloadedBytes; got != 400 {
		t.Errorf("downloaded_bytes = %d，期望 400（650 ms 已过写库节流）", got)
	}
	if got := env.planRow(t, created.ID).Phase; got != store.PlanPhaseMerging {
		t.Errorf("phase = %q，期望 merging", got)
	}
}

// 未知总长度时不得伪造百分比（[05 §4.1]）。
func TestOnProgress_DoesNotFakePercentWhenTotalUnknown(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := env.db.MarkPlanRunning(ctx, created.ID, store.PlanPhaseDownloading, store.PlanMediaDirect); err != nil {
		t.Fatalf("置运行失败: %v", err)
	}
	plan := env.planRow(t, created.ID)
	env.svc.plan.beginThrottle(created.ID, 0)

	env.svc.onProgress(ctx, plan, DownloadProgress{DownloadedBytes: 999999, Phase: store.PlanPhaseDownloading})

	got := env.planRow(t, created.ID)
	if got.Progress != 0 {
		t.Errorf("progress = %v，期望 0（长度未知不得伪造百分比）", got.Progress)
	}
	if got.TotalBytes != nil {
		t.Errorf("total_bytes = %v，期望 NULL", *got.TotalBytes)
	}
}

// ---------------------------------------------------------------------------
// 中断恢复（[05 §9]）
// ---------------------------------------------------------------------------

func TestStartPlans_RecoversInterruptedAndPublishes(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	// 一条 running 的直链（可重建）与一条 running 的视频号（不可恢复）。
	direct, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := env.db.MarkPlanRunning(ctx, direct.ID, store.PlanPhaseDownloading, store.PlanMediaDirect); err != nil {
		t.Fatalf("置运行失败: %v", err)
	}

	wechatReq := baseRequest()
	wechatReq.MediaKind = store.PlanMediaWechat
	wechatReq.QualityLabel = "xWT111"
	wechatReq.Wechat = &WechatContext{DecodeKey: "k"}
	wechat, err := env.svc.PlanCreate(ctx, wechatReq)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := env.db.MarkPlanRunning(ctx, wechat.ID, store.PlanPhaseDownloading, store.PlanMediaWechat); err != nil {
		t.Fatalf("置运行失败: %v", err)
	}

	// 重启后的第一件事就是恢复（[05 §9]）。
	if err := env.svc.StartPlans(ctx); err != nil {
		t.Fatalf("StartPlans 失败: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer env.svc.StopPlans(stopCtx)

	if !waitForCondition(t, 3*time.Second, func() bool {
		plan, err := env.db.GetPlan(ctx, wechat.ID)
		return err == nil && plan.Status == store.PlanStatusFailed
	}) {
		t.Fatal("视频号计划未被恢复流程置为 failed")
	}
	if got := env.planRow(t, wechat.ID).ErrorCode; got != store.CodeContextExpired {
		t.Errorf("error_code = %q，期望 context_expired", got)
	}
	if !env.pub.plansChangedFired() {
		t.Error("恢复后应发出 plansChanged（[04 §4.1]）")
	}
}

// 未注入引擎时必须**自建默认引擎**：装配层（main/app）只给出 Store 与 Tools
// 就能让计划真的跑起来，否则"计划创建成功却永远不动"会变成难查的问题。
func TestStartPlans_UsesDefaultRunnerWhenNotInjected(t *testing.T) {
	env := newPlanTestEnv(t, func(deps *Deps) { deps.Runner = nil })
	if env.svc.plan.runner == nil {
		t.Fatal("未注入 Runner 时应当自建默认引擎")
	}

	ctx := context.Background()
	if err := env.svc.StartPlans(ctx); err != nil {
		t.Fatalf("自建引擎后调度应当可以启动: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	env.svc.StopPlans(stopCtx)
}

func TestStartPlans_RejectsDoubleStart(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	if err := env.svc.StartPlans(ctx); err != nil {
		t.Fatalf("第一次启动失败: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		env.svc.StopPlans(stopCtx)
	}()

	if err := env.svc.StartPlans(ctx); err == nil {
		t.Fatal("重复启动调度应当被拒绝")
	}
}

// 调度循环必须真的把计划跑起来（端到端：创建 → 完成）。
func TestScheduleLoop_RunsQueuedPlan(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		return DownloadResult{FinalPath: `C:\已完成\loop.mp4`, DownloadedBytes: 7}, nil
	}

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := env.svc.StartPlans(ctx); err != nil {
		t.Fatalf("StartPlans 失败: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer env.svc.StopPlans(stopCtx)

	if !waitForCondition(t, 5*time.Second, func() bool {
		plan, err := env.db.GetPlan(ctx, created.ID)
		return err == nil && plan.Status == store.PlanStatusCompleted
	}) {
		t.Fatal("调度循环没有把 queued 计划跑起来")
	}
}

// ---------------------------------------------------------------------------
// 内存上下文（媒体地址与会话凭据）的生命周期
// ---------------------------------------------------------------------------

// 失败后必须**保留**地址与会话凭据：自动重试与用户手动重试都要用它
// （[03 §6] 要求它只在内存，但"只在内存"不等于"用完立刻丢"）。
func TestRunPlan_KeepsSourceForRetryAfterFailure(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	env.runner.handler = func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
		return DownloadResult{}, &DownloadError{Code: CodeDownloadFailed}
	}

	req := baseRequest()
	req.Headers = map[string]string{"Cookie": "session=abc"}
	created, err := env.svc.PlanCreate(ctx, req)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	runOnce(t, env, created.ID)

	plan := env.planRow(t, created.ID)
	if plan.Status != store.PlanStatusQueued {
		t.Fatalf("状态 = %s，期望 queued（等待重试）", plan.Status)
	}
	source, ok := env.svc.plan.lookupSource(created.ID)
	if !ok {
		t.Fatal("等待重试期间内存里必须保留下载上下文，否则自动重试会丢掉会话凭据")
	}
	if source.headers["Cookie"] != "session=abc" {
		t.Errorf("会话凭据丢失：%v", source.headers)
	}
}

// 完成后必须释放地址与凭据：文件已交付，秘密没有继续驻留内存的理由（[03 §6]）。
func TestRunPlan_ReleasesSourceAfterCompletion(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	runOnce(t, env, created.ID)

	if got := env.planRow(t, created.ID).Status; got != store.PlanStatusCompleted {
		t.Fatalf("状态 = %s，期望 completed", got)
	}
	if _, ok := env.svc.plan.lookupSource(created.ID); ok {
		t.Error("完成后内存里不该继续保留媒体地址与会话凭据")
	}
}

// ---------------------------------------------------------------------------
// 分类与阶段归一
// ---------------------------------------------------------------------------

func TestClassifyDownloadError_NeverLeaksRawError(t *testing.T) {
	raw := errors.New("https://cdn.example.com/a.mp4?sign=x 请求失败：C:\\tmp\\a.part")
	code, message := classifyDownloadError(raw)
	if code != CodeDownloadFailed {
		t.Errorf("错误码 = %s，期望 download_failed（[05 §7.1] 的可重试默认）", code)
	}
	if message == "" || message == raw.Error() {
		t.Fatalf("未分类错误的消息 = %q，不得回显原始错误（[12 §3.3] 的 E2）", message)
	}
	if strings.Contains(message, "://") || strings.Contains(message, `\`) {
		t.Errorf("消息含路径或 URL：%q", message)
	}

	code, message = classifyDownloadError(&DownloadError{Code: CodeDiskFull})
	if code != CodeDiskFull || message != MessageForCode(CodeDiskFull) {
		t.Errorf("分类结果 = %s/%q，期望 %s 及其码表消息", code, message, CodeDiskFull)
	}
}

func TestNormalizeProgressPhase_FallsBackToDownloading(t *testing.T) {
	cases := map[string]string{
		store.PlanPhaseMerging:     store.PlanPhaseMerging,
		store.PlanPhaseValidating:  store.PlanPhaseValidating,
		store.PlanPhaseDownloading: store.PlanPhaseDownloading,
		"":                         store.PlanPhaseDownloading,
		"bogus":                    store.PlanPhaseDownloading,
	}
	for input, want := range cases {
		if got := normalizeProgressPhase(input); got != want {
			t.Errorf("normalizeProgressPhase(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestDownloadConcurrencyAndRetryMaxAreClamped(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	if err := env.db.SetSetting(ctx, settingDownloadConcurrency, "0"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	if got := env.svc.downloadConcurrency(ctx); got < 1 {
		t.Errorf("download_concurrency = %d，期望被夹到至少 1", got)
	}

	if err := env.db.SetSetting(ctx, settingRetryMax, "999"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	if got := env.svc.retryMax(ctx); got != maxRetryMax {
		t.Errorf("retry_max = %d，期望被夹到 %d（[03 §2.4] 的范围 0–20）", got, maxRetryMax)
	}

	if err := env.db.SetSetting(ctx, settingProgressThrottleMS, "1"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	if got := env.svc.progressThrottle(ctx); got != minProgressThrottleMS*time.Millisecond {
		t.Errorf("progress_throttle = %v，期望被夹到 %v", got, minProgressThrottleMS*time.Millisecond)
	}
}
