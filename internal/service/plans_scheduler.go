package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Ly233ly/download_video_new/internal/media"
	"github.com/Ly233ly/download_video_new/internal/platform"
	"github.com/Ly233ly/download_video_new/internal/store"
)

// 计划调度（[05 §2] 的状态机与 [05 §9] 的中断恢复）。
//
// 调度循环只做四件事：取到期计划 → 交给下载引擎 → 按节流写进度并推事件 → 按 [05 §8]
// 决定重试或失败。所有"能不能转"的判断都在 store 的 SQL 守卫里，这里只负责"该不该转"。

// DownloadRequest 是交给下载引擎的一次执行请求。
//
// 这些类型是 Service 与引擎之间的**窄契约**：Service 是业务入口（[04 §1.2]），
// 它只需要"能跑一个计划"这件事。默认实现把 [internal/download] 适配到它
// （见 [NewDownloadRunner]），换引擎时只需要换一个适配器。
type DownloadRequest struct {
	PlanID string
	// MediaURL 是**只在内存**的媒体地址（[03 §6]、B-722）。
	MediaURL string
	// Headers 是会话上下文（Cookie / Authorization 等）。同样只在内存（B-303）。
	Headers map[string]string

	MediaKind       string
	OutputName      string
	OutputContainer string
	MergeMode       string
	// QualityLabel 是画质档位（视频号专用，[05 §3.1]）。
	QualityLabel string
	// StreamPlan 是轨道描述；它已经落库，因此**不得包含**签名 URL 或解密键。
	StreamPlan []StreamTrack

	// TempDir 是临时目录的**父目录**（`...\临时`）：按任务归属建 `临时\<plan_id>\`
	// 是引擎的事（[05 §11]）。布局本身由 [internal/platform] 的权威函数给出。
	TempDir string
	// OutputDir 是最终交付目录（`...\已完成`）。
	OutputDir string
	// KnownTotalBytes 是**已知的**预期总长度；0 表示未知——本包对外用 nil 表达未知
	// （[03 §2.1]：不得用 0 表示"长度为零"），这里是它在引擎边界上的对照写法。
	KnownTotalBytes int64
}

// DownloadProgress 是引擎上报的一次进度（[05 §4.1]、[05 §4.4.4]）。
type DownloadProgress struct {
	DownloadedBytes int64
	// TotalBytes 为 nil 表示长度未知——**不得用 0 表示未知**（[03 §2.1]）。
	TotalBytes *int64
	// Phase 取 downloading / merging / validating（[05 §2]）。
	Phase string
	// PhaseDetail 是可直接展示的短文案，不得含路径、URL 或秘密（[03 §2.1]）。
	PhaseDetail string
	// TimeRatio 是**按时长口径**的进度（0..1），0 表示这次没有这个口径。
	//
	// 它存在的理由只有一个：P2（清单下载）的进度按时长算，而它的
	// `total_bytes` 通常是 NULL（[05 §4.6.3]）——只靠 DownloadedBytes/TotalBytes
	// 算不出百分比。**两种口径不得混算**，所以它单独一个字段。
	TimeRatio float64
}

// DownloadResult 是一次成功执行的结果。
type DownloadResult struct {
	// FinalPath 是交付文件的路径；为空时 service 会把这次"成功"当作失败
	// （[03 §2.1] 不允许出现"已完成但没有路径"的记录）。
	FinalPath       string
	PreviewPath     string
	DownloadedBytes int64
	TotalBytes      *int64
}

// DownloadError 是下载引擎报出的稳定错误。
//
// 引擎实现（[05] 的 P1~P5）用 Code 报 [05 §7] 的码；未实现该类型的 error 一律按
// `download_failed`（[05 §7.1] 的可重试默认）处理——**不把原始错误文案展示给用户**。
type DownloadError struct {
	Code string
	// Message 是可安全展示的中文消息；为空时按码表回退（[05 §7.4]）。
	Message string
	// Cause 是内部原因，只进日志（[12 §3.3] 的 E2）。
	Cause error
}

func (e *DownloadError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

// Unwrap 暴露内部原因，供 errors.Is/As 与日志使用。
func (e *DownloadError) Unwrap() error { return e.Cause }

// Runner 是下载引擎的最小契约（[05 §4]）。
//
// 实现方**必须**保证：
//   - onProgress 串行调用（本实现内部也做了保护，但契约如此更清晰）；
//   - ctx 取消后尽快返回，已写入的临时产物由引擎按 [05 §10] 清理；
//   - 返回的 Result.FinalPath 指向已交付的文件。
type Runner interface {
	Run(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error)
}

// EagleProbe 是 Eagle 可用性探测（阶段 4 落地，[08]）。
//
// 为 nil 时视为**不可用**：B-211 要求不可用时禁用补导入口并说明文件会保留，
// 宁可禁用也不假装可用。
type EagleProbe interface {
	Available(ctx context.Context) bool
}

// 调度相关的常量。
//
// 取值来源：
//   - 并发、重试上限、节流间隔的**默认值**来自 [03 §2.4] 的配置键表（那里的值是权威）；
//   - 200 ms 的进度推送间隔来自 [04 §4.2]；
//   - 退避公式与上限来自 [05 §8]。
const (
	// scheduleInterval 是调度轮询间隔。取 500 ms：既是"到达 next_attempt_at 即执行"的
	// 体感上限，也不会让空闲进程有明显的唤醒开销（[11] 的 PF-A3 要求空闲 CPU ≈ 0）。
	scheduleInterval = 500 * time.Millisecond
	// progressPushInterval 是进度推送间隔（[04 §4.2]：每 200 ms 推送一次完整 PlanView）。
	progressPushInterval = 200 * time.Millisecond

	defaultDownloadConcurrency = 3 // [03 §2.4]：download_concurrency 默认 3
	maxDownloadConcurrency     = 32
	defaultRetryMax            = 5 // [03 §2.4]：retry_max 默认 5，范围 0–20
	maxRetryMax                = 20
	defaultProgressThrottleMS  = 500 // [03 §2.4]：progress_throttle_ms 默认 500，范围 100–5000
	minProgressThrottleMS      = 100
	maxProgressThrottleMS      = 5000

	// maxRetryBackoffSeconds 是 [05 §8] 退避公式的上限：min(30 × 2^(attempt-1), 1800)。
	maxRetryBackoffSeconds = 1800

	// dnsLookupTimeout 是创建期主机名解析的超时（[04 §1.1] 的 G1：每个跨边界调用必须有超时）。
	dnsLookupTimeout = 3 * time.Second
	// dbWriteTimeout 是后台写库的超时。它必须大于 [03 §7] 的 busy_timeout（5000 ms），
	// 否则等锁还没结束，调用方就先超时了。
	dbWriteTimeout = 10 * time.Second
	// healthCacheTTL 是健康结果的复用窗口（[02 B-307] 的 single-flight 之上再加一层缓存）。
	healthCacheTTL = 3 * time.Second
	// healthProbeTimeout 是单次健康探测的预算。
	healthProbeTimeout = 3 * time.Second
)

// planRuntime 是计划业务与调度的运行时状态。
//
// 单独成结构（而不是把十几个字段摊进 Service）：[D4] 的 Service 骨架只需多一个字段，
// 计划相关的状态与生命周期全部收在这里。
type planRuntime struct {
	runner  Runner
	pub     Publisher
	eagle   EagleProbe
	resolve HostResolver
	now     func() time.Time
	version string

	// sourcesMu 保护下面三张表。它们都是**内存**状态：
	//   - sources 持媒体地址与会话凭据（[03 §6]：不得落盘，用完即弃）；
	//   - cancels 持每个运行中计划的取消函数（[05 §10]）；
	//   - active 是正在执行的计划集合，防止同一计划被并发调度两次。
	sourcesMu sync.Mutex
	sources   map[string]planSource
	cancels   map[string]context.CancelFunc
	active    map[string]bool

	// progressMu 保护节流状态（[03 §2.4] 的 progress_throttle_ms、[04 §4.2] 的 200 ms）。
	progressMu sync.Mutex
	throttles  map[string]*planThrottle

	// healthMu 保护健康探测的 single-flight 与缓存（[02 B-307]）。
	healthMu   sync.Mutex
	healthCall *healthCall
	healthView HealthView
	healthAt   time.Time
	// eagleOK 是最近一次探测得到的 Eagle 可用性，供派生布尔使用（不触发额外探测）。
	eagleOK bool

	// loopMu 保护调度循环的生命周期（[12 §9] 的 C1：不得有无主的 goroutine）。
	loopMu     sync.Mutex
	loopCancel context.CancelFunc
	loopDone   chan struct{}
	wg         sync.WaitGroup
}

// planThrottle 是一条计划的节流状态：写库与推送各有各的间隔。
type planThrottle struct {
	writeInterval time.Duration
	lastWrite     time.Time
	lastPush      time.Time
}

// healthCall 是一轮进行中的健康探测。
type healthCall struct {
	done chan struct{}
	view HealthView
	err  error
}

// newPlanRuntime 装配运行时状态并填默认值。
func newPlanRuntime(deps Deps) *planRuntime {
	r := &planRuntime{
		runner:    deps.Runner,
		pub:       deps.Publisher,
		eagle:     deps.Eagle,
		resolve:   deps.Resolver,
		now:       deps.Now,
		version:   deps.Version,
		sources:   make(map[string]planSource),
		cancels:   make(map[string]context.CancelFunc),
		active:    make(map[string]bool),
		throttles: make(map[string]*planThrottle),
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.resolve == nil {
		r.resolve = defaultHostResolver
	}
	return r
}

// nowSeconds 返回 [03 §1] 的 P3 时间表示：REAL 类型的 Unix 秒（含小数）。
func (r *planRuntime) nowSeconds() float64 {
	return float64(r.now().UnixMilli()) / 1000
}

// ---------------------------------------------------------------------------
// 内存注册表（[03 §6]：媒体地址与会话凭据只在内存）
// ---------------------------------------------------------------------------

// remember 记住一条计划的下载上下文。
func (r *planRuntime) remember(id string, source planSource) {
	r.sourcesMu.Lock()
	defer r.sourcesMu.Unlock()
	r.sources[id] = source
}

// forget 丢弃一条计划的全部内存状态（取消函数、地址、凭据、节流）。
// 只在计划记录被删除时调用——那是唯一确定"再用不到它"的时刻。
func (r *planRuntime) forget(id string) {
	r.sourcesMu.Lock()
	delete(r.sources, id)
	delete(r.cancels, id)
	r.sourcesMu.Unlock()

	r.endThrottle(id)
}

// forgetSource 只丢弃地址与凭据，保留节流状态。
//
// 调用时机很关键：**计划完成或被取消之后**，以及**记录被删除时**。
// 失败后刻意**不**调用它——自动重试与用户手动重试都需要那份会话上下文
// （[03 §6] 要求它只在内存，而"只在内存"不等于"用完立刻丢"）。
func (r *planRuntime) forgetSource(id string) {
	r.sourcesMu.Lock()
	delete(r.sources, id)
	r.sourcesMu.Unlock()
}

// endThrottle 清掉一条计划的节流状态（一次执行结束时调用）。
func (r *planRuntime) endThrottle(id string) {
	r.progressMu.Lock()
	delete(r.throttles, id)
	r.progressMu.Unlock()
}

// lookupSource 读取内存里的下载上下文。
func (r *planRuntime) lookupSource(id string) (planSource, bool) {
	r.sourcesMu.Lock()
	defer r.sourcesMu.Unlock()
	source, ok := r.sources[id]
	return source, ok
}

// sourceForRun 取本次执行要用的地址：内存注册表优先，缺失时按 [05 §9] 的可重建性回退。
//
// 回退的合法性来自重启恢复的同一套判据（store.contextRebuildable 的等价判断）：
// 直链与可重新解析的页面可以用 source_url 重建；视频号与浏览器中转不行。
func (r *planRuntime) sourceForRun(plan store.Plan) (planSource, bool) {
	if source, ok := r.lookupSource(plan.ID); ok {
		return source, true
	}
	return rebuildSource(plan)
}

// sourceForRetry 是手动重试用的取地址逻辑，语义与 sourceForRun 相同。
func (r *planRuntime) sourceForRetry(plan store.Plan) (planSource, bool) {
	return r.sourceForRun(plan)
}

// canProvideSource 报告该计划当前能否拿到可用地址——派生布尔 canRetry 的依据。
func (r *planRuntime) canProvideSource(plan store.Plan) bool {
	_, ok := r.sourceForRun(plan)
	return ok
}

// rebuildSource 判断重启后能否从落库字段重建下载上下文（[05 §9]）。
//
// 能重建的只有"地址本身就是可重复访问的 URL"这一类：source_url 落的是归一化且去敏感的
// 页面地址（[03 §2.1]、[03 §6]），直链与可重新解析的页面可以据此重来；
// 视频号（会话 + 解密键）与浏览器中转（字节在浏览器侧）都不行。
func rebuildSource(plan store.Plan) (planSource, bool) {
	if plan.SourceURL == "" {
		return planSource{}, false
	}
	switch plan.MediaKind {
	case store.PlanMediaDirect, store.PlanMediaHLS, store.PlanMediaDASH, store.PlanMediaPage:
		// 不带 Headers：会话凭据本来就不落盘，重启后无法恢复（B-303/B-722）。
		return planSource{mediaURL: plan.SourceURL}, true
	default:
		return planSource{}, false
	}
}

// registerCancel 登记运行中计划的取消函数。
func (r *planRuntime) registerCancel(id string, cancel context.CancelFunc) {
	r.sourcesMu.Lock()
	defer r.sourcesMu.Unlock()
	r.cancels[id] = cancel
}

// clearCancel 撤销登记。
func (r *planRuntime) clearCancel(id string) {
	r.sourcesMu.Lock()
	defer r.sourcesMu.Unlock()
	delete(r.cancels, id)
}

// cancelRunning 取消某条计划的执行（[05 §10]：用户点停止）。
func (r *planRuntime) cancelRunning(id string) {
	r.sourcesMu.Lock()
	cancel, ok := r.cancels[id]
	r.sourcesMu.Unlock()
	if ok {
		cancel()
	}
}

// activeCount 返回正在执行的计划数。
func (r *planRuntime) activeCount() int {
	r.sourcesMu.Lock()
	defer r.sourcesMu.Unlock()
	return len(r.active)
}

// tryActivate 尝试占用一个执行位。
//
// 两个拒绝条件：该计划已经在跑（防止一轮调度被重复触发），或并发已满
// （[03 §2.4] 的 download_concurrency 约束出站下载）。
func (r *planRuntime) tryActivate(id string, limit int) bool {
	r.sourcesMu.Lock()
	defer r.sourcesMu.Unlock()
	if r.active[id] {
		return false
	}
	if len(r.active) >= limit {
		return false
	}
	r.active[id] = true
	return true
}

// deactivate 释放执行位。
func (r *planRuntime) deactivate(id string) {
	r.sourcesMu.Lock()
	defer r.sourcesMu.Unlock()
	delete(r.active, id)
}

// eagleAvailable 返回最近一次健康探测得到的 Eagle 可用性。
//
// **不触发新探测**：列表里每条计划都可能问一次，不能每次都去打 Eagle。
// 未探测过时返回 false——宁可禁用补导入口（B-211），也不假装可用。
func (r *planRuntime) eagleAvailable() bool {
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	return r.eagleOK
}

// beginThrottle 为一条计划登记节流间隔（每次执行开始时调用一次）。
func (r *planRuntime) beginThrottle(id string, writeInterval time.Duration) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	r.throttles[id] = &planThrottle{writeInterval: writeInterval}
}

// markProgress 返回本次进度是否该写库、是否该推送。
//
// 两个间隔是独立的：[03 §2.4] 的 progress_throttle_ms 管写库，[04 §4.2] 的 200 ms 管推送。
// 首次调用两个都放行——否则第一个进度事件会被无谓地压掉。
func (r *planRuntime) markProgress(id string, now time.Time) (write, push bool) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	throttle, ok := r.throttles[id]
	if !ok {
		return true, true
	}
	if throttle.lastWrite.IsZero() || now.Sub(throttle.lastWrite) >= throttle.writeInterval {
		throttle.lastWrite = now
		write = true
	}
	if throttle.lastPush.IsZero() || now.Sub(throttle.lastPush) >= progressPushInterval {
		throttle.lastPush = now
		push = true
	}
	return write, push
}

// resetPush 让下一次进度立即推送（终态前的最后一次进度不该被 200 ms 挡住）。
func (r *planRuntime) resetPush(id string) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	if throttle, ok := r.throttles[id]; ok {
		throttle.lastPush = time.Time{}
	}
}

// ---------------------------------------------------------------------------
// 调度循环（[05 §2]）
// ---------------------------------------------------------------------------

// StartPlans 启动调度循环：先执行 [05 §9] 的中断恢复，再进入轮询。
//
// 没有下载引擎时**不启动循环**并返回错误：静默空转会让"计划创建成功却永远不动"
// 变成难查的问题（[12 §3.3] 的"不静默降级"）。
func (s *Service) StartPlans(ctx context.Context) error {
	if s.plan.runner == nil {
		return errors.New("缺少下载引擎依赖，调度循环未启动")
	}

	s.plan.loopMu.Lock()
	if s.plan.loopCancel != nil {
		s.plan.loopMu.Unlock()
		return errors.New("调度循环已在运行")
	}
	s.plan.loopMu.Unlock()

	if err := s.recoverInterrupted(ctx); err != nil {
		return err
	}

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	s.plan.loopMu.Lock()
	s.plan.loopCancel = cancel
	s.plan.loopDone = done
	s.plan.loopMu.Unlock()

	go func() {
		defer close(done)
		s.runScheduleLoop(loopCtx)
	}()
	return nil
}

// StopPlans 停止调度循环并等待在飞的计划收尾。
func (s *Service) StopPlans(ctx context.Context) {
	s.plan.loopMu.Lock()
	cancel, done := s.plan.loopCancel, s.plan.loopDone
	s.plan.loopCancel, s.plan.loopDone = nil, nil
	s.plan.loopMu.Unlock()

	if cancel == nil {
		return
	}
	cancel()

	finished := make(chan struct{})
	go func() {
		if done != nil {
			<-done
		}
		s.plan.wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-ctx.Done():
		// 超时不阻塞退出：计划的状态已经落库，重启后由 [05 §9] 的中断恢复兜底。
		slog.Warn("调度循环未在预算内结束", "component", "service", "event", "scheduler_stop_timeout")
	}
}

// runScheduleLoop 是调度循环本体（[12 §9] 的 C1：它的生命周期由 loopCtx 控制）。
func (s *Service) runScheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(scheduleInterval)
	defer ticker.Stop()

	// 启动后立即调度一次：不能因为等第一个 tick 而让新建的计划白等 500 ms。
	s.dispatchDue(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchDue(ctx)
		}
	}
}

// dispatchDue 取到期计划并交给执行体。
//
// **单个计划的异常不得终止这个循环**（[02 B-306]）：所以每个计划跑在自己的 goroutine 里，
// 且带 panic 兜底（见 runPlan）；本函数的任何失败都只记日志。
func (s *Service) dispatchDue(ctx context.Context) {
	limit := s.downloadConcurrency(ctx)
	due, err := s.store.ListDuePlans(ctx, s.plan.nowSeconds(), limit)
	if err != nil {
		slog.Error("读取待调度计划失败", "component", "service", "event", "due_query_failed", "err", err)
		return
	}
	for _, plan := range due {
		if ctx.Err() != nil {
			return
		}
		if !s.plan.tryActivate(plan.ID, limit) {
			// 要么并发已满，要么这条计划已经在跑——两种都只需等下一轮。
			continue
		}
		s.plan.wg.Add(1)
		go func(p store.Plan) {
			defer s.plan.wg.Done()
			defer s.plan.deactivate(p.ID)
			s.runPlan(ctx, p)
		}(plan)
	}
}

// runPlan 执行一条计划。
func (s *Service) runPlan(ctx context.Context, plan store.Plan) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// [02 B-306]：单个计划的异常（含引擎里的 panic）不得终止后台处理循环。
			// 这里把 panic 变成一次明确的失败，而不是让计划永远挂在 running。
			slog.Error("计划执行出现 panic", "component", "service", "event", "plan_panic",
				"plan_id", plan.ID, "panic", fmt.Sprint(recovered))
			writeCtx, cancel := dbContext(ctx)
			defer cancel()
			s.failPlan(writeCtx, plan, CodeDownloadFailed, MessageForCode(CodeDownloadFailed))
		}
	}()

	// queued → running，并写入本次实际执行的路径（[05 §4.0] 第一步按提示执行，此后才可能降级）。
	// 守卫失败说明状态已经变了（用户停止、记录被删），本轮什么都不做。
	if err := s.store.MarkPlanRunning(ctx, plan.ID, store.PlanPhaseDownloading, plan.MediaKind); err != nil {
		if !errors.Is(err, store.ErrPlanStateChanged) && !errors.Is(err, store.ErrPlanNotFound) {
			slog.Error("置运行状态失败", "component", "service", "event", "mark_running_failed",
				"plan_id", plan.ID, "err", err)
		}
		return
	}
	s.publishPlanByID(ctx, plan.ID)

	source, ok := s.plan.sourceForRun(plan)
	if !ok {
		// 没有可用地址就没有下载的可能（[05 §9]：上下文不可恢复 → context_expired）。
		writeCtx, cancel := dbContext(ctx)
		defer cancel()
		s.failPlan(writeCtx, plan, CodeContextExpired, "")
		return
	}

	// 计划级 context：用户点停止时取消它（[05 §10]）。它挂在整个调度循环的 ctx 之下，
	// 因此退出程序时也会被取消。
	planCtx, cancelPlan := context.WithCancel(ctx)
	defer cancelPlan()
	s.plan.registerCancel(plan.ID, cancelPlan)
	defer s.plan.clearCancel(plan.ID)

	s.plan.beginThrottle(plan.ID, s.progressThrottle(ctx))
	// 只清节流状态：地址与会话凭据**保留到计划进入终态**，
	// 否则自动重试会丢掉 Cookie/Authorization（[05 §4.2] 的凭据只在内存，但重试要用它）。
	defer s.plan.endThrottle(plan.ID)

	// 从这一刻起的写库都用不受循环取消影响的上下文（见 dbContext）：
	// 关闭程序时正在收尾的计划仍要能落到终态。
	writeCtx, cancelWrite := dbContext(ctx)
	defer cancelWrite()

	// 目录布局由 [internal/platform] 的权威函数给出（[01 §8]、[05 §11]）：
	// 这里**不自己拼** `临时\<plan_id>\` 之类的结构，否则同一份布局会长出第二处定义。
	// 「已完成」目录由上层创建——引擎刻意不替上层建目录（见 internal/download 的交付测试）。
	dirs, err := s.outputDirs(ctx)
	if err != nil {
		slog.Error("解析输出目录失败", "component", "service", "event", "output_dirs_failed",
			"plan_id", plan.ID, "err", err)
		s.failPlan(writeCtx, plan, CodeDownloadFailed, "")
		return
	}
	if err := dirs.EnsureCompleted(); err != nil {
		slog.Error("创建输出目录失败", "component", "service", "event", "output_dir_unavailable",
			"plan_id", plan.ID, "err", err)
		s.failPlan(writeCtx, plan, CodeDownloadFailed, "")
		return
	}

	// 路径自动路由（[05 §4.0]）：先按提示执行；引擎在还没写入任何字节时发现提示
	// 不成立，会回报改路信号，executePlan 据此按 §4.0 第二步改一次路。
	result, runErr := s.executePlan(planCtx, writeCtx, plan, source, dirs)

	if runErr != nil {
		s.handleFailure(writeCtx, plan, runErr)
		return
	}
	if result.FinalPath == "" {
		// 引擎报成功却没给路径——[03 §2.1] 不允许写"已完成但没有路径"的记录，
		// 所以按失败处置（这是引擎的 bug，但必须在这里兜住）。
		slog.Error("下载引擎报告成功但未给出交付路径", "component", "service",
			"event", "missing_final_path", "plan_id", plan.ID)
		s.failPlan(writeCtx, plan, CodeOutputNoStreams, "")
		return
	}

	// 状态写入与路径写入在 store 里是同事务的（[03 §2.1]）；
	// 守卫只接受 running，因此 B-308 在这里被机械保证。
	if err := s.store.MarkPlanCompleted(writeCtx, plan.ID, result.FinalPath,
		result.DownloadedBytes, result.TotalBytes); err != nil {
		if errors.Is(err, store.ErrPlanStateChanged) {
			// **B-308**：用户在看校验阶段已经点了停止——完成状态不得覆盖取消状态。
			slog.Info("完成回调被忽略：计划已是终态", "component", "service",
				"event", "complete_ignored", "plan_id", plan.ID)
			return
		}
		slog.Error("写入完成状态失败", "component", "service", "event", "mark_completed_failed",
			"plan_id", plan.ID, "err", err)
		return
	}
	// 终态**立即**推送，不受 200 ms 节流限制（[04 §4.2]）。
	// 完成后不再需要地址与会话凭据：文件已经交付，秘密没有继续驻留内存的理由（[03 §6]）。
	s.plan.forgetSource(plan.ID)
	s.publishPlanByID(writeCtx, plan.ID)
}

// executePlan 执行一次计划，并在 [05 §4.0] 允许时改一次路。
//
// 改路只可能发生在"引擎还没有写入任何字节"的时候：引擎在那种情况下回报
// `*media.RouteMismatch`，本函数据此改写媒体地址与提示路径，然后再跑一次。
// **只降一次**——§4.0 明令禁止链式降级，否则"这次为什么变慢了"永远查不清。
func (s *Service) executePlan(
	planCtx context.Context, writeCtx context.Context, plan store.Plan,
	source planSource, dirs platform.OutputDirs,
) (DownloadResult, error) {
	resolved := plan.MediaKind
	mediaURL := source.mediaURL
	streamPlan := source.tracks
	if len(streamPlan) == 0 {
		// 内存里没有轨道地址（进程重启过）：落库投影里**没有地址**（[03 §2.1.1]），
		// 因此取不回 P3 的两条轨——[05 §9] 允许这种计划按"还能重新解析的路径"重试。
		streamPlan = decodeStreamPlan(plan.StreamPlan)
	}

	fellBack := false
	for {
		req := DownloadRequest{
			PlanID:          plan.ID,
			MediaURL:        mediaURL,
			Headers:         source.headers,
			MediaKind:       resolved,
			OutputName:      plan.OutputName,
			OutputContainer: plan.OutputContainer,
			MergeMode:       plan.MergeMode,
			QualityLabel:    plan.QualityLabel,
			StreamPlan:      streamPlan,
			TempDir:         dirs.Temp,
			OutputDir:       dirs.Completed,
			KnownTotalBytes: knownTotalBytes(plan.TotalBytes),
		}

		result, runErr := s.plan.runner.Run(planCtx, req, func(progress DownloadProgress) {
			s.onProgress(planCtx, plan, progress)
		})
		if runErr == nil || fellBack {
			return result, runErr
		}

		next, address, ok := fallbackFor(resolved, runErr, plan.SourceURL)
		if !ok {
			return result, runErr
		}

		// 改路必须让用户看得见（B-316），但**不增加 attempt_count**（B-315）：
		// 它发生在同一次执行内，用户看到的仍是"第一次尝试"。
		if err := s.store.UpdatePlanResolvedKind(writeCtx, plan.ID, next); err != nil {
			// 状态已经变了（用户点了停止、记录被删）：不再改路，按原样上报这次的结果。
			return result, runErr
		}
		slog.Info("下载路径改道", "component", "service", "event", "route_fallback",
			"plan_id", plan.ID, "hint", resolved, "actual", next)
		resolved = next
		fellBack = true
		if strings.TrimSpace(address) != "" {
			mediaURL = address
		}
		// 换路后原来的轨道选择作废：清单与页面解析各自会给出新的轨道。
		streamPlan = nil
		s.publishPlanByID(writeCtx, plan.ID)
	}
}

// fallbackFor 按 [05 §4.0] 第二步判断这次能不能改路，能就给出新路径与新地址。
//
// 只有三种情形可以改：提示是直链却拿到清单、提示是直链却拿到网页、提示是清单却
// 解析不出来。其余一律不改——下载中断、网络错误与超时属 [05 §8] 的重试；
// 字节或校验失败说明这条路本身能走通；视频号与浏览器模式没有备胎。
//
// "已经写入任何字节"不会走到这里：引擎在那种情况下根本不回报改路信号。
func fallbackFor(hint string, runErr error, pageURL string) (next string, address string, ok bool) {
	var mismatch *media.RouteMismatch
	if !errors.As(runErr, &mismatch) {
		return "", "", false
	}
	if hint != store.PlanMediaDirect && hint != store.PlanMediaHLS && hint != store.PlanMediaDASH {
		return "", "", false
	}
	switch mismatch.Actual {
	case media.KindHLS, media.KindDASH:
		if hint != store.PlanMediaDirect {
			// 一种清单换成另一种清单不属 §4.0 的三种情形。
			return "", "", false
		}
		// 表格第 1 行：用**该清单的地址**（发生过同源重定向时是重定向后的地址）。
		return string(mismatch.Actual), mismatch.Address, true
	case media.KindPage:
		// 表格第 2、3 行：改用**页面地址**重新解析；页面地址不可用时不许降。
		if strings.TrimSpace(pageURL) == "" {
			return "", "", false
		}
		return store.PlanMediaPage, pageURL, true
	default:
		return "", "", false
	}
}

// handleFailure 按 [05 §8] 决定重试还是失败。
func (s *Service) handleFailure(ctx context.Context, plan store.Plan, runErr error) {
	code, message := classifyDownloadError(runErr)
	attempt := plan.AttemptCount + 1

	// [05 §4.4.3]：浏览器中转的失败**不进入自动重试**——字节在浏览器侧，
	// 桌面端没有取字节的途径，送回 queued 只会空转或丢掉已收字节。
	// [05 §8]：不可重试的错误码直接 failed，attempt_count 不再增加。
	retryMax := s.retryMax(ctx)
	retryable := IsRetryableCode(code) &&
		plan.MediaKind != store.PlanMediaBrowser &&
		attempt < retryMax

	if retryable {
		nextAttemptAt := s.plan.nowSeconds() + retryBackoff(attempt).Seconds()
		if err := s.store.MarkPlanQueuedForRetry(ctx, plan.ID, attempt, nextAttemptAt); err != nil {
			if errors.Is(err, store.ErrPlanStateChanged) {
				// 计划已被停止或删除：迟到的失败不该写任何东西。
				return
			}
			slog.Error("转回等待重试失败", "component", "service", "event", "retry_schedule_failed",
				"plan_id", plan.ID, "code", code, "err", err)
			return
		}
		slog.Info("计划将重试", "component", "service", "event", "plan_retry_scheduled",
			"plan_id", plan.ID, "code", code, "attempt", attempt)
		// 转回 queued 也是一次状态变化，及时告知界面（[04 §4.1]）。
		s.publishPlanByID(ctx, plan.ID)
		return
	}

	s.failPlan(ctx, plan, code, message)
}

// failPlan 写失败终态并推送。
func (s *Service) failPlan(ctx context.Context, plan store.Plan, code, message string) {
	if message == "" {
		message = MessageForCode(code)
	}
	if err := s.store.MarkPlanFailed(ctx, plan.ID, code, message); err != nil {
		if errors.Is(err, store.ErrPlanStateChanged) {
			// 已是终态（典型是用户已经停止）——不覆盖（B-308）。
			return
		}
		slog.Error("写入失败状态失败", "component", "service", "event", "mark_failed_failed",
			"plan_id", plan.ID, "code", code, "err", err)
		return
	}
	s.publishPlanByID(ctx, plan.ID)
}

// onProgress 处理一次进度上报：按节流写库，按 [04 §4.2] 推送完整视图。
func (s *Service) onProgress(ctx context.Context, plan store.Plan, progress DownloadProgress) {
	write, push := s.plan.markProgress(plan.ID, s.plan.now())

	if write {
		phase := normalizeProgressPhase(progress.Phase)
		// [05 §4.6.3]：清单下载（P2）的进度是**时长口径**，而它的 total_bytes 通常是
		// NULL——靠字节算不出百分比，所以把比例单独交给 store（两种口径不得混算）。
		var ratio *float64
		if progress.TimeRatio > 0 {
			value := progress.TimeRatio
			if value > 1 {
				value = 1
			}
			ratio = &value
		}
		if err := s.store.UpdateProgress(ctx, plan.ID, progress.DownloadedBytes,
			progress.TotalBytes, ratio, phase, progress.PhaseDetail); err != nil {
			// 状态守卫失败（已取消、已删除）不是错误：本计划已经停下，进度自然作废。
			if !errors.Is(err, store.ErrPlanStateChanged) && !errors.Is(err, store.ErrPlanNotFound) {
				slog.Warn("写入进度失败", "component", "service", "event", "progress_write_failed",
					"plan_id", plan.ID, "err", err)
			}
		}
	}
	if push {
		s.publishPlanByID(ctx, plan.ID)
	}
}

// publishPlanByID 读取最新记录并推送完整视图。
//
// 读取失败（记录被删、ctx 已取消）时静默返回：事件只是通知，权威状态在库里，
// 前端下次拉取自然会看到真相（[04 §4.1]）。
func (s *Service) publishPlanByID(ctx context.Context, id string) {
	plan, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return
	}
	s.plan.publishPlan(s.planView(plan))
}

// recoverInterrupted 执行 [05 §9] 的中断恢复并逐条告知界面。
func (s *Service) recoverInterrupted(ctx context.Context) error {
	report, err := s.store.RecoverInterrupted(ctx, s.plan.nowSeconds())
	if err != nil {
		return mapStoreError(err)
	}
	if len(report.RequeuedIDs) == 0 && len(report.FailedIDs) == 0 {
		return nil
	}
	slog.Info("中断恢复完成", "component", "service", "event", "recovery_done",
		"requeued", len(report.RequeuedIDs), "failed", len(report.FailedIDs))
	// 批量变化用 plansChanged 通知一次（[04 §4.1]），再对失败的逐条推送，
	// 让界面能直接把「请重新创建任务」展示在对应条目上。
	s.plan.publishPlans()
	for _, id := range report.FailedIDs {
		s.publishPlanByID(ctx, id)
	}
	return nil
}

// outputDirs 解析输出目录布局（[01 §8]）。
//
// 根目录取自 [03 §2.4] 的 `output_dir`：**空表示默认目录**。
// 解析交给 [platform.ResolveOutputDirs]，本包不自己拼路径——布局只有一个权威出处。
func (s *Service) outputDirs(ctx context.Context) (platform.OutputDirs, error) {
	root, err := s.Setting(ctx, settingOutputDir, "")
	if err != nil {
		return platform.OutputDirs{}, err
	}
	return platform.ResolveOutputDirs(root)
}

// knownTotalBytes 把三态的总长度压成引擎边界的写法：nil（未知）→ 0。
//
// 只在这一个方向上做转换：反过来（0 → nil）会让"长度为零"被误读成"未知"（[03 §2.1]）。
func knownTotalBytes(total *int64) int64 {
	if total == nil {
		return 0
	}
	return *total
}

// downloadConcurrency 读取出站下载的并发上限（[03 §2.4] 的 download_concurrency）。
func (s *Service) downloadConcurrency(ctx context.Context) int {
	value, err := s.settingInt(ctx, settingDownloadConcurrency, defaultDownloadConcurrency)
	if err != nil {
		slog.Warn("读取下载并发配置失败，使用默认值", "component", "service",
			"event", "setting_read_failed", "key", settingDownloadConcurrency, "err", err)
		return defaultDownloadConcurrency
	}
	return clampInt(value, 1, maxDownloadConcurrency)
}

// retryMax 读取自动重试上限（[03 §2.4] 的 retry_max，范围 0–20，0 表示不自动重试）。
func (s *Service) retryMax(ctx context.Context) int {
	value, err := s.settingInt(ctx, settingRetryMax, defaultRetryMax)
	if err != nil {
		slog.Warn("读取重试上限失败，使用默认值", "component", "service",
			"event", "setting_read_failed", "key", settingRetryMax, "err", err)
		return defaultRetryMax
	}
	return clampInt(value, 0, maxRetryMax)
}

// progressThrottle 读取进度写库节流间隔（[03 §2.4] 的 progress_throttle_ms，范围 100–5000 ms）。
func (s *Service) progressThrottle(ctx context.Context) time.Duration {
	value, err := s.settingInt(ctx, settingProgressThrottleMS, defaultProgressThrottleMS)
	if err != nil {
		slog.Warn("读取进度节流配置失败，使用默认值", "component", "service",
			"event", "setting_read_failed", "key", settingProgressThrottleMS, "err", err)
		value = defaultProgressThrottleMS
	}
	value = clampInt(value, minProgressThrottleMS, maxProgressThrottleMS)
	return time.Duration(value) * time.Millisecond
}

// retryBackoff 计算 [05 §8] 的退避：min(30 × 2^(attempt-1), 1800) 秒。
func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// 位移前夹住指数：2^30 秒早已远超上限，继续放大只会整数溢出（[05 §8] 的上限是 1800 秒）。
	if attempt > 30 {
		attempt = 30
	}
	seconds := 30 * (1 << (attempt - 1))
	if seconds > maxRetryBackoffSeconds {
		seconds = maxRetryBackoffSeconds
	}
	return time.Duration(seconds) * time.Second
}

// classifyDownloadError 把引擎错误映射成稳定错误码与可展示消息（[05 §7]）。
//
// **任何情况下都不回显原始错误文案**（[12 §3.3] 的 E2/E3）：引擎的 error 里可能带 URL、
// 路径或响应片段，那些东西不允许出现在用户可见消息里。
func classifyDownloadError(err error) (code, message string) {
	var downloadErr *DownloadError
	if errors.As(err, &downloadErr) {
		code = downloadErr.Code
		if code == "" {
			code = CodeDownloadFailed
		}
		message = downloadErr.Message
		if message == "" {
			message = MessageForCode(code)
		}
		return code, message
	}
	// 上下文取消与未分类错误都按可重试的 download_failed 处置：
	// 若计划已被用户停止，store 的状态守卫会拒绝写入，不会污染终态。
	return CodeDownloadFailed, MessageForCode(CodeDownloadFailed)
}

// normalizeProgressPhase 把引擎上报的阶段夹到 [03 §2.1] 的取值域。
//
// 未知值一律回落为 downloading：进度写库高频，**不能因为一个拼错的阶段名就整条丢掉**，
// 但也不能把非法值写进有 CHECK 约束的列。
func normalizeProgressPhase(phase string) string {
	switch phase {
	case store.PlanPhaseMerging, store.PlanPhaseValidating:
		return phase
	default:
		return store.PlanPhaseDownloading
	}
}

// decodeStreamPlan 把落库的 stream_plan 还原成轨道列表。
//
// 解析失败返回 nil（交给引擎自行探测）：轨道描述只是提示，**不该因为一个坏 JSON 就
// 让一条已经存在的计划永久失败**。写入时 store 已经校验过它是 JSON 数组。
func decodeStreamPlan(raw string) []StreamTrack {
	if raw == "" || raw == "[]" {
		return nil
	}
	var tracks []StreamTrack
	if err := jsonUnmarshal(raw, &tracks); err != nil {
		return nil
	}
	return tracks
}

// dbContext 返回一个**不随调度循环取消而中断**的写库上下文。
//
// 关闭程序时正在收尾的计划仍要能写完成/失败状态：否则重启后只能靠中断恢复兜底，
// 用户会看到一条"下载完了却仍是运行中"的记录。
func dbContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), dbWriteTimeout)
}

// clampInt 把配置值夹进文档给定的范围。
//
// 越界值不报错而是夹住：[03 §2.4] 的范围是给设置界面的校验依据，
// 而调度侧遇到脏数据时应当继续工作（[12 §3.2]：环境错误降级，不终止进程）。
func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
