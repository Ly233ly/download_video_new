package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Ly233ly/download_video_new/internal/store"
)

// 计划业务（[05]、[04 §3.2] 的"计划"组方法）。
//
// Service 是**唯一业务入口**（[04 §1.2]）：Wails 绑定与 HTTP handler 都调这里，
// 它们自己不得做状态校验、重试决策或降级判断。

// 配置键（[03 §2.4] 的已知键，只列本包用到的）。
const (
	settingBrowserDownloadMode = "browser_download_mode"
	settingDownloadConcurrency = "download_concurrency"
	settingRetryMax            = "retry_max"
	settingProgressThrottleMS  = "progress_throttle_ms"
	settingOutputDir           = "output_dir"
)

// PlanView 是计划对外的投影（[04 §7] 的 I2 正在回填字段，当前以 [03 §2.1] 的列为准）。
//
// **不投影 stream_plan**：它是内部轨道描述，界面用不到；更重要的是它贴近秘密边界，
// 少一份外泄面就少一份风险（[03 §6]）。
type PlanView struct {
	ID              string `json:"id"`
	SourceURL       string `json:"sourceUrl"`
	SourceTitle     string `json:"sourceTitle"`
	MediaKind       string `json:"mediaKind"`
	OutputName      string `json:"outputName"`
	OutputContainer string `json:"outputContainer"`
	MergeMode       string `json:"mergeMode"`
	QualityLabel    string `json:"qualityLabel"`

	Status   string  `json:"status"`
	Phase    string  `json:"phase"`
	Progress float64 `json:"progress"`
	// DownloadedBytes / TotalBytes 沿用 [03 §2.1] 的三态：TotalBytes 为 null 表示长度未知，
	// **不得用 0 表示未知**。
	DownloadedBytes int64  `json:"downloadedBytes"`
	TotalBytes      *int64 `json:"totalBytes"`
	PhaseDetail     string `json:"phaseDetail"`

	// FinalPath / PreviewPath 是程序自身产物路径，允许持久化也允许展示（[03 §6]）。
	FinalPath   string `json:"finalPath,omitempty"`
	PreviewPath string `json:"previewPath,omitempty"`

	AttemptCount  int      `json:"attemptCount"`
	NextAttemptAt *float64 `json:"nextAttemptAt,omitempty"`
	ErrorCode     string   `json:"errorCode,omitempty"`
	ErrorMessage  string   `json:"errorMessage,omitempty"`

	ImportToEagle     bool     `json:"importToEagle"`
	DeleteAfterImport bool     `json:"deleteAfterImport"`
	CreatedAt         float64  `json:"createdAt"`
	UpdatedAt         float64  `json:"updatedAt"`
	CompletedAt       *float64 `json:"completedAt,omitempty"`

	// 派生布尔：界面**只投影真实状态**，不自行推断（B-313）。
	// 前端据此决定按钮可用性，判断逻辑只存在于这里。
	CanRetry  bool `json:"canRetry"`
	CanStop   bool `json:"canStop"`
	CanOpen   bool `json:"canOpen"`
	CanImport bool `json:"canImport"`
}

// Paged 是列表返回（[04 §3.1] 的 `Paged<T>`，[04 §3.1] 的 B3 要求列表必须有分页或硬上限）。
type Paged[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// HealthView 是健康与能力快照（[04 §2.3] 的 `/health`）。
//
// 字段集由 [04 §7] 的 I5 定稿（阶段 2）；这里先落地**已经确定要求存在的**那些：
// B-503 要求发布 `eagleAvailable`，B-220 的模式读取要能被界面看到。
type HealthView struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	// DatabaseOK 表示主库可读。
	DatabaseOK bool `json:"databaseOk"`
	// FFmpegAvailable / FFprobeAvailable 反映 [05 §5.1] 的工具解析结果。
	FFmpegAvailable  bool `json:"ffmpegAvailable"`
	FFprobeAvailable bool `json:"ffprobeAvailable"`
	// EagleAvailable 是 [02 B-503] 要求健康接口必须发布的字段。
	EagleAvailable bool `json:"eagleAvailable"`
	// BrowserDownloadMode 反映 [03 §2.4] 的 browser_download_mode。
	BrowserDownloadMode bool `json:"browserDownloadMode"`
	// ActivePlans 是当前正在执行的计划数，用于核对 `download_concurrency` 是否被遵守。
	ActivePlans int `json:"activePlans"`
}

// PlanCreate 创建下载计划（[05 §3]、[04 §3.2]）。
//
// 校验不通过时**拒绝且不落库**（[05 §3.2]）；通过后媒体地址只进内存注册表
// （[03 §6]、B-722），数据库里只有归一化且去敏感的来源地址与轨道描述。
func (s *Service) PlanCreate(ctx context.Context, req CreatePlanRequest) (PlanView, error) {
	draft, err := s.validateCreatePlan(ctx, req)
	if err != nil {
		return PlanView{}, err
	}

	id, err := newPlanID()
	if err != nil {
		return PlanView{}, newError(CodeInternal, err)
	}
	draft.plan.ID = id
	if draft.plan.OutputName == "" {
		// [03 §4.4] 第 5 条：清洗后为空则回退到内容 ID 或时间戳。这里用计划 ID——
		// 它稳定、唯一，且不泄露任何来源信息。
		draft.plan.OutputName = sanitizeOutputName(id, id)
	}

	if err := s.store.InsertPlan(ctx, draft.plan); err != nil {
		return PlanView{}, mapStoreError(err)
	}

	// 先记住地址再发事件：前端收到 plansChanged 后会立刻拉列表，不该看到一条查不到地址的计划。
	s.plan.remember(id, draft.source)
	s.plan.publishPlans()

	plan, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return PlanView{}, mapStoreError(err)
	}
	return s.planView(plan), nil
}

// PlanGet 读取单条计划。
func (s *Service) PlanGet(ctx context.Context, id string) (PlanView, error) {
	plan, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return PlanView{}, mapStoreError(err)
	}
	return s.planView(plan), nil
}

// PlansList 返回计划列表（[04 §3.2] 的 `PlansList`）。
//
// 默认按 `updated_at DESC`（[03 §5] 的 Q2），单次返回上限 200（[03 §4.5]）。
func (s *Service) PlansList(ctx context.Context, statuses []string, offset, limit int) (Paged[PlanView], error) {
	plans, err := s.store.ListPlans(ctx, statuses, offset, limit)
	if err != nil {
		return Paged[PlanView]{}, mapStoreError(err)
	}
	total, err := s.store.CountPlans(ctx, statuses)
	if err != nil {
		return Paged[PlanView]{}, mapStoreError(err)
	}

	// 回显实际生效的分页参数，让前端知道结果被夹过（[03 §4.5] 的上限）。
	effectiveLimit, effectiveOffset := store.ClampPlanPaging(offset, limit)

	items := make([]PlanView, 0, len(plans))
	for _, plan := range plans {
		items = append(items, s.planView(plan))
	}
	return Paged[PlanView]{
		Items:  items,
		Total:  total,
		Offset: effectiveOffset,
		Limit:  effectiveLimit,
	}, nil
}

// PlanStop 停止计划（[05 §10]）。
//
// 顺序是刻意的：**先把状态写成 canceled，再取消 context**。反过来会出现一个窗口——
// 下载引擎已经返回、完成回调先落库，取消反而变成 no-op（B-308 要防的正是这个）。
func (s *Service) PlanStop(ctx context.Context, id string) (PlanView, error) {
	plan, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return PlanView{}, mapStoreError(err)
	}

	switch plan.Status {
	case store.PlanStatusCanceled:
		// 幂等：重复点停止不是错误（界面可能因为事件延迟又点了一次）。
		return s.planView(plan), nil
	case store.PlanStatusCompleted, store.PlanStatusFailed:
		// [05 §10]：已完成计划停止不影响文件；失败计划已经停下。两者都只需告知状态已变。
		return PlanView{}, newError(CodePlanStateChanged, fmt.Errorf("状态 %s 不支持停止", plan.Status))
	}

	if err := s.store.MarkPlanCanceled(ctx, id); err != nil {
		if errors.Is(err, store.ErrPlanStateChanged) {
			// 并发：读取与写入之间状态变了。重新读一次，按终态语义回答。
			latest, getErr := s.store.GetPlan(ctx, id)
			if getErr != nil {
				return PlanView{}, mapStoreError(getErr)
			}
			if latest.Status == store.PlanStatusCanceled {
				return s.planView(latest), nil
			}
			return PlanView{}, newError(CodePlanStateChanged, err)
		}
		return PlanView{}, mapStoreError(err)
	}

	// [05 §10]：取消 context，让下载引擎优雅终止子进程，再强杀。
	s.plan.cancelRunning(id)
	// 地址与凭据用完即弃：取消后不再需要，**秘密不必继续驻留内存**（[03 §6]）。
	s.plan.forgetSource(id)

	latest, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return PlanView{}, mapStoreError(err)
	}
	view := s.planView(latest)
	s.plan.publishPlan(view)
	return view, nil
}

// PlanRetry 手动重试（[05 §2.2]：failed 只能手动重试）。
//
// 与自动重试的区别：手动重试**重置自动重试预算**。否则一次耗尽之后，"手动重试"只能跑一次，
// 用户得反复点——那不是重试，是折磨。计数仍然落库（B-315），只是这里赋予新的预算。
func (s *Service) PlanRetry(ctx context.Context, id string) (PlanView, error) {
	plan, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return PlanView{}, mapStoreError(err)
	}
	if !manuallyRetryable(plan, plan.ErrorCode) {
		return PlanView{}, newError(CodePlanNotRetryable, fmt.Errorf("状态 %s 或错误码 %s 不支持重试",
			plan.Status, plan.ErrorCode))
	}

	// 地址来源：内存注册表优先，缺失时按 [05 §9] 的可重建性用 source_url 兜底。
	source, ok := s.plan.sourceForRetry(plan)
	if !ok {
		// 上下文不可恢复（视频号会话、浏览器中转、一次性签名地址）→ 只能重新创建任务。
		return PlanView{}, newError(CodeContextExpired, nil)
	}

	// attempt_count 归零：重新获得 retry_max 次自动重试预算。
	if err := s.store.MarkPlanQueuedForRetry(ctx, id, 0, s.plan.nowSeconds()); err != nil {
		return PlanView{}, mapStoreError(err)
	}
	s.plan.remember(id, source)

	latest, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return PlanView{}, mapStoreError(err)
	}
	view := s.planView(latest)
	s.plan.publishPlan(view)
	return view, nil
}

// PlanRemove 删除计划记录（[05 §7.3] 的 `plan_not_found` 是这里唯一的外部错误）。
//
// 只删记录，**不删文件**：`已完成` 目录里的产物是用户资产（[05 §11]、B-402/B-405）。
// 未完成的计划必须先停止——否则后台线程会继续为一个已经不存在的记录写状态。
func (s *Service) PlanRemove(ctx context.Context, id string) error {
	plan, err := s.store.GetPlan(ctx, id)
	if err != nil {
		return mapStoreError(err)
	}
	if !plan.IsTerminal() {
		return newError(CodePlanStateChanged, fmt.Errorf("状态 %s 不支持删除，请先停止", plan.Status))
	}

	if err := s.store.RemovePlan(ctx, id); err != nil {
		return mapStoreError(err)
	}
	// 记录没了，内存里的地址与凭据也一并释放。
	s.plan.forget(id)
	s.plan.publishPlans()
	return nil
}

// Health 返回健康与能力快照（[04 §2.3]）。
//
// B-307：**健康探测的并发刷新必须复用同一轮结果（single-flight）**——
// 界面启动、扩展轮询与诊断页可能同时来，不能各自去打一遍 Eagle。
func (s *Service) Health(ctx context.Context) (HealthView, error) {
	r := s.plan

	r.healthMu.Lock()
	if r.healthCall != nil {
		// 已有探测在飞：等它的结果，不重复发起（single-flight）。
		call := r.healthCall
		r.healthMu.Unlock()
		select {
		case <-call.done:
			return call.view, call.err
		case <-ctx.Done():
			return HealthView{}, ctx.Err()
		}
	}
	if !r.healthAt.IsZero() && r.now().Sub(r.healthAt) < healthCacheTTL {
		// 刚探过：直接复用，避免事件驱动的界面高频刷新打爆下游。
		view := r.healthView
		r.healthMu.Unlock()
		return view, nil
	}
	call := &healthCall{done: make(chan struct{})}
	r.healthCall = call
	r.healthMu.Unlock()

	view, err := s.probeHealth(ctx)

	// 先写结果再关 channel：等待方在 <-done 之后读到的是最终值（channel 关闭建立 happens-before）。
	call.view, call.err = view, err

	r.healthMu.Lock()
	r.healthCall = nil
	if err == nil {
		r.healthView = view
		r.healthAt = r.now()
		r.eagleOK = view.EagleAvailable
	}
	r.healthMu.Unlock()

	close(call.done)
	return view, err
}

// probeHealth 执行一轮真实探测。**它只在 single-flight 的临界区里被调用一次**。
func (s *Service) probeHealth(ctx context.Context) (HealthView, error) {
	view := HealthView{Version: s.plan.version}

	// 主库可读是最基本的能力：失败时 Health 仍然要返回一个**如实**的结果，
	// 而不是把原始错误抛给界面（[12 §3.3] 的 E2）。[04 §2.3] 的 /health 是免 Origin 端点，
	// 它的语义是"报告能力"，不是"健康检查失败就报错"。
	probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	var one int
	err := s.store.SQL().QueryRowContext(probeCtx, `SELECT 1`).Scan(&one)
	view.DatabaseOK = err == nil && one == 1

	if s.tools != nil {
		view.FFmpegAvailable = s.tools.FFmpeg.Path != ""
		view.FFprobeAvailable = s.tools.FFprobe.Path != ""
	}
	if s.plan.eagle != nil {
		view.EagleAvailable = s.plan.eagle.Available(ctx)
	}
	mode, modeErr := s.settingInt(ctx, settingBrowserDownloadMode, 0)
	if modeErr == nil {
		view.BrowserDownloadMode = mode != 0
	}
	view.ActivePlans = s.plan.activeCount()
	view.OK = view.DatabaseOK
	return view, nil
}

// planView 把 store 记录投影成对外视图，并计算派生布尔。
func (s *Service) planView(plan store.Plan) PlanView {
	view := PlanView{
		ID:                plan.ID,
		SourceURL:         plan.SourceURL,
		SourceTitle:       plan.SourceTitle,
		MediaKind:         plan.MediaKind,
		OutputName:        plan.OutputName,
		OutputContainer:   plan.OutputContainer,
		MergeMode:         plan.MergeMode,
		QualityLabel:      plan.QualityLabel,
		Status:            plan.Status,
		Phase:             plan.Phase,
		Progress:          plan.Progress,
		DownloadedBytes:   plan.DownloadedBytes,
		TotalBytes:        plan.TotalBytes,
		PhaseDetail:       plan.PhaseDetail,
		FinalPath:         plan.FinalPath,
		PreviewPath:       plan.PreviewPath,
		AttemptCount:      plan.AttemptCount,
		NextAttemptAt:     plan.NextAttemptAt,
		ErrorCode:         plan.ErrorCode,
		ErrorMessage:      plan.ErrorMessage,
		ImportToEagle:     plan.ImportToEagle,
		DeleteAfterImport: plan.DeleteAfterImport,
		CreatedAt:         plan.CreatedAt,
		UpdatedAt:         plan.UpdatedAt,
		CompletedAt:       plan.CompletedAt,
	}

	view.CanStop = plan.Status == store.PlanStatusQueued || plan.Status == store.PlanStatusRunning
	view.CanRetry = manuallyRetryable(plan, plan.ErrorCode) && s.plan.canProvideSource(plan)
	// [02 B-309]：completed 必须能打开所在文件夹；路径为空时不给出入口（B-313：只投影真实状态）。
	view.CanOpen = plan.Status == store.PlanStatusCompleted && plan.FinalPath != ""
	// [02 B-211]：Eagle 不可用时补导入口禁用并说明文件会保留。可用性由 Health 探测刷新，
	// 未探测过时**视为不可用**——宁可禁用，也不假装可用。
	view.CanImport = plan.Status == store.PlanStatusCompleted && plan.FinalPath != "" &&
		plan.ImportToEagle && s.plan.eagleAvailable()
	return view
}

// settingInt 读取整数配置，两条回退规则来自 [03 §2.4]：键不存在、解码失败都回退默认值，
// 且**不得中断**调用方。
func (s *Service) settingInt(ctx context.Context, key string, fallback int) (int, error) {
	raw, ok, err := s.store.GetSetting(ctx, key)
	if err != nil {
		return fallback, err
	}
	if !ok {
		return fallback, nil
	}
	var value int
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return fallback, nil
	}
	return value, nil
}

// mapStoreError 把仓储错误映射成稳定的对外错误（[12 §3.1]）。
//
// 未识别的错误一律归为 CodeInternal：**绝不复用原始错误的文案**（[12 §3.3] 的 E2/E3）。
func mapStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrPlanNotFound):
		return newError(CodePlanNotFound, err)
	case errors.Is(err, store.ErrPlanStateChanged):
		return newError(CodePlanStateChanged, err)
	default:
		return newError(CodeInternal, err)
	}
}

// newPlanID 生成计划 ID：`p_` + 16 字节随机数的十六进制。
//
// 不引入 uuid 依赖：go.mod 里的 uuid 是 Wails 的间接依赖（[12 §8.2] 要求每个依赖有明确用途），
// 而这里只需要一个不可猜测、全局唯一的稳定标识——标准库就够。
func newPlanID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成计划标识失败: %w", err)
	}
	return "p_" + hex.EncodeToString(buf[:]), nil
}
