package ui

import (
	"context"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// 计划相关的绑定方法（[04 §3.2]）。
//
// 本层**只做参数校验与转发**（[04 §1.1] 的 G5、[01 §9] 的 N9）：状态校验、
// 重试决策、降级逻辑一律在 internal/service。所以这里应当一直很薄——
// 一旦出现"判断业务状态"的 if，就说明逻辑放错了层。

// PlansListRequest 是 PlansList 的入参（[04 §3.2] 的 `{status[], offset, limit}`）。
type PlansListRequest struct {
	// Statuses 为空表示不按状态过滤。
	Statuses []string `json:"statuses"`
	Offset   int      `json:"offset"`
	Limit    int      `json:"limit"`
}

// AppStatus 返回应用状态与能力（[04 §3.2]）。
//
// 绑定方法叫 `AppStatus`、业务侧叫 `Health`——同一件事的两个视角（一个面向
// 界面、一个面向 HTTP 的 `/health`），Service 只实现一次（[04 §1.2]）。
func (u *UI) AppStatus(ctx context.Context) (service.HealthView, error) {
	return u.svc.Health(ctx)
}

// PlansList 返回计划列表。
//
// B-310：任务列表不用页码翻页，界面按 offset/limit 连续读取。
func (u *UI) PlansList(ctx context.Context, req PlansListRequest) (service.Paged[service.PlanView], error) {
	return u.svc.PlansList(ctx, req.Statuses, req.Offset, req.Limit)
}

// PlanGet 返回单个计划详情。
func (u *UI) PlanGet(ctx context.Context, id string) (service.PlanView, error) {
	return u.svc.PlanGet(ctx, id)
}

// PlanCreate 创建下载计划。
//
// `req.Streams[].URL` 是媒体地址，**只在内存使用**：它不会写进 `plans` 表
// （[03 §2.1]：本表没有媒体地址列、也不得新增；B-722 禁止媒体 URL 进入
// 数据库、日志与诊断）。
func (u *UI) PlanCreate(ctx context.Context, req service.CreatePlanRequest) (service.PlanView, error) {
	return u.svc.PlanCreate(ctx, req)
}

// PlanStop 停止计划。B-308：停止之后，后续的完成回调不得覆盖取消状态。
func (u *UI) PlanStop(ctx context.Context, id string) (service.PlanView, error) {
	return u.svc.PlanStop(ctx, id)
}

// PlanRetry 重试计划。
//
// 阶段 2 的媒体地址只在内存，重启后上下文不可重建——此时 Service 会以
// `context_expired` 拒绝，而不是让它在队列里空转（[03 §2.1]、05 §9）。
func (u *UI) PlanRetry(ctx context.Context, id string) (service.PlanView, error) {
	return u.svc.PlanRetry(ctx, id)
}

// PlanRemove 删除计划记录。
//
// **只删记录，不删文件**——「已完成」目录里的交付副本是用户资产（B-402/B-405）。
func (u *UI) PlanRemove(ctx context.Context, id string) error {
	return u.svc.PlanRemove(ctx, id)
}
