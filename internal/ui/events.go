package ui

import (
	"context"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// 事件名（[04 §4.1]）。前端**不轮询**，只订阅这几个。
//
// planChanged / plansChanged 是「通知 + 重查」：Go 是唯一权威状态，事件只负责
// 告诉界面"东西变了"。planProgress 是唯一的例外——下载进度高频变化，走
// 「通知 + 重查」会产生大量往返，所以直接推完整 PlanView 让前端覆盖（[04 §4.2]）。
const (
	EventPlanChanged  = "planChanged"  // 载荷 {"id": ...}，前端重新 PlanGet(id)
	EventPlansChanged = "plansChanged" // 载荷 {}，批量变化，前端重新 PlansList()
	EventPlanProgress = "planProgress" // 载荷为完整 PlanView，前端直接覆盖该条
	EventAppNotice    = "appNotice"    // 载荷 {level, code, message}
)

// EventSink 把 Service 的事件发布接口接到 Wails 的事件推送。
//
// 两个时序要点：
//
//  1. UI 上下文由 Wails 在 OnStartup 时才给出，而 Service 在更早的 Bootstrap
//     就装配好了——所以用 Attach 延后绑定，而不是构造时注入。
//  2. 未绑定时**静默丢弃**：启动早期产生的事件没有窗口可送达，这不是错误。
//     [04 §4.4] 明确前端启动/重连时直接调 PlansList() 对齐，**不做事件重放**。
type EventSink struct {
	mu  sync.RWMutex
	ctx context.Context
}

// NewEventSink 创建一个尚未绑定 UI 上下文的事件出口。
func NewEventSink() *EventSink { return &EventSink{} }

// Attach 绑定 Wails 的 UI 上下文（由 OnStartup 调用）。
func (s *EventSink) Attach(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
}

// Detach 解绑（关闭时调用），之后的事件不再送达。
func (s *EventSink) Detach() {
	s.mu.Lock()
	s.ctx = nil
	s.mu.Unlock()
}

// PlanChanged 通知某个计划发生变化（[04 §4.1]）。
func (s *EventSink) PlanChanged(id string) {
	s.emit(EventPlanChanged, map[string]string{"id": id})
}

// PlansChanged 通知列表发生批量变化（[04 §4.1]）。
func (s *EventSink) PlansChanged() {
	s.emit(EventPlansChanged, struct{}{})
}

// PlanProgress 推送一次**完整** PlanView（[04 §4.2]）。
//
// 载荷之所以是完整视图而不是增量：推送由同一个 goroutine 串行发出，后到的
// 必然更新，因此**不需要版本号**，前端直接覆盖即可。
//
// 签名必须与 `service.PlanViewPublisher` 逐字一致——Service 用类型断言探测
// 这个**可选**能力：实现了就推完整视图，没实现就退化成 `PlanChanged(id)` +
// 前端重查。写成 `any` 会静默失去这个能力，且不会有编译错误。
func (s *EventSink) PlanProgress(view service.PlanView) {
	s.emit(EventPlanProgress, view)
}

// AppNotice 弹出用户可见提示（[04 §4.1]）。
func (s *EventSink) AppNotice(level, code, message string) {
	s.emit(EventAppNotice, map[string]string{
		"level":   level,
		"code":    code,
		"message": message,
	})
}

func (s *EventSink) emit(name string, payload any) {
	s.mu.RLock()
	ctx := s.ctx
	s.mu.RUnlock()
	if ctx == nil {
		return
	}
	runtime.EventsEmit(ctx, name, payload)
}
