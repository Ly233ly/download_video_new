package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// serviceAPI 是本包用到的 Service 方法集。
//
// 为什么用接口而不是直接把 `*service.Service` 写进 `Server` 字段：
//
//   - `*service.Service` **隐式**满足它（Go 的鸭子类型），生产装配一个字符都不用改；
//   - 测试可以注入替身（[12 §5] 要求覆盖错误响应格式与转发行为，而真造一个
//     Service 就要造数据库与业务规则——那既慢又把本包的测试变成集成测试）；
//   - 下面那条编译期断言把"Service 必须提供这些方法"钉死在编译期：
//     `internal/service` 少一个方法，本包立刻编译失败并点名到方法。
//
// 接口**只列本包真正调用的方法**（[12 §2]：接口是名词，按使用方需要定义），
// 多一个方法就会让"Service 该提供什么"的边界变模糊。
type serviceAPI interface {
	Health(ctx context.Context) (service.HealthView, error)
	PlanCreate(ctx context.Context, req service.CreatePlanRequest) (service.PlanView, error)
	PlanGet(ctx context.Context, id string) (service.PlanView, error)
	PlansList(ctx context.Context, statuses []string, offset, limit int) (service.Paged[service.PlanView], error)
	PlanStop(ctx context.Context, id string) (service.PlanView, error)
	PlanRetry(ctx context.Context, id string) (service.PlanView, error)
	PlanRemove(ctx context.Context, id string) error

	// Setting 只在**装配期**用一次：读 `settings.extension_origin` 这个
	// 开发调试覆盖值（[04 §2.2]）。请求路径上不读它——Origin 判定因此不依赖
	// 数据库，数据库暂时不可用时也不会把已放行的调用方踢出去。
	Setting(ctx context.Context, key, fallback string) (string, error)
}

// 编译期断言：真实的 `*service.Service` 必须满足上面的方法集。
//
// 它是本包与 Service 之间唯一的静态耦合点。断言失败时编译器会直接指出
// 缺哪个方法，而不是等到运行期返回一堆 500。
var _ serviceAPI = (*service.Service)(nil)

// 本文件的职责：**参数解析 → 调用 Service → 序列化结果**（[04 §1.2]）。
//
// 每个 handler 都不含业务判断：不做状态校验（"已完成的计划能不能停止"）、
// 不做重试决策（"这个错误该不该重试"）、不做降级逻辑——那些都在 Service 里，
// 违反即触犯 [04 §1.2] 的 G5 与 [01 §9] 的 N9。
//
// 三条所有 handler 共有的形状：
//
//  1. 一律用 `context.WithTimeout` 给 Service 调用一个有界预算（G1、
//     [01 §9] 的 N3/N4：无 context 的调用是禁止项）；
//  2. 一律经 `writeServiceError` 转发失败，码与消息都来自 Service；
//  3. 一律只调一个 Service 方法——需要两次调用才能完成的操作说明业务逻辑
//     泄漏到了 Handler 里。

// handleHealth 是 `GET /health`：健康与能力（[04 §2.3]）。
//
// **免 Origin 校验**（[04 §2.2] 的明确例外）：扩展在发现桌面端时还没有任何上下文，
// 且该端点的响应不含用户数据——它只回答"在不在、能不能用"。这也是
// `B-213`（找不到桌面端时给启动引导）与 `B-214`（低频重探）能工作的前提。
//
// `eagleAvailable` 由 Service 判定（[07 §3] 的 `AD-4`：能力字段全集是待确认项
// `04 I5`，阶段 2 回填）。本包**不自己探测 Eagle**——那会变成第二套业务逻辑。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	view, err := s.service.Health(ctx)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeData(w, view)
}

// handlePlansList 是 `GET /api/plans`：计划列表（[04 §2.3]）。
//
// 入参与 [04 §3.2] 的 `PlansList` 同形：`{status[], offset, limit}`。
// 分页与状态过滤的**取值域判定**在 Service；本包只保证"参数是合法整数、
// 没超过 [03 §4.5] 的上限"。
func (s *Server) handlePlansList(w http.ResponseWriter, r *http.Request) {
	query, err := parseListQuery(r)
	if err != nil {
		markCode(r, CodeInvalidRequest)
		logEvent(r.Context(), slog.LevelWarn, "list_query_invalid",
			r.Method, s.routeOf(r), http.StatusBadRequest, CodeInvalidRequest,
			"detail", err.Error())
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	page, err := s.service.PlansList(ctx, query.statuses, query.offset, query.limit)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeData(w, page)
}

// handlePlanGet 是 `GET /api/plan`：单个计划详情（[04 §2.3]）。
func (s *Server) handlePlanGet(w http.ResponseWriter, r *http.Request) {
	id, ok := s.withPlanID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	view, err := s.service.PlanGet(ctx, id)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeData(w, view)
}

// handlePlanCreate 是 `POST /api/plan`：创建下载计划（[04 §2.3]）。
//
// 请求体字段见 data.go（按 [03 §2.1] 的 `plans` 列与 [05 §3.1] 的入参表设计；
// [04 §7] 的 `I1` 回填后以文档为准）。
//
// 媒体地址校验、目标主机检查、容器与合并方式的合法性**全在 Service**
// （[05 §3.2] 的创建期校验表）。本包不预检，也不因为"看起来像本机地址"提前拒绝：
// 那会让 `blocked_local_target` 这类稳定码的归属从 Service 漂到 Handler。
func (s *Server) handlePlanCreate(w http.ResponseWriter, r *http.Request) {
	var body createPlanBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeDecodeError(w, r, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	view, err := s.service.PlanCreate(ctx, body.toServiceRequest())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeData(w, view)
}

// handlePlanStop 是 `POST /api/plan/stop`：停止（[04 §2.3]）。
//
// 停止后的状态写入、取消 context、产物清理全在 Service（[05 §10]，B-308 要求
// 完成状态不得覆盖取消状态）。本包不判断"当前状态能不能停"——那会绕过 Service
// 这个唯一业务入口（G5）。
func (s *Server) handlePlanStop(w http.ResponseWriter, r *http.Request) {
	id, ok := s.withPlanID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	view, err := s.service.PlanStop(ctx, id)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeData(w, view)
}

// handlePlanRetry 是 `POST /api/plan/retry`：重试（[04 §2.3]）。
//
// "这个状态/错误码能不能重试"是 [05 §7.1]/[05 §7.2] 的业务规则，由 Service 判定
// 并返回 `plan_not_retryable`。本包**不**在这里读状态、也不看 `attempt_count`。
func (s *Server) handlePlanRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := s.withPlanID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	view, err := s.service.PlanRetry(ctx, id)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeData(w, view)
}

// handlePlanRemove 是 `POST /api/plan/remove`：删除记录（[04 §2.3]）。
//
// 删的是**记录**，不是文件：文件归属与删除许可由 Service 按 B-401/B-404 判定
// （"无法证明归属的文件永不删除"）。成功时 `data` 为 `null`，扩展只需判 `ok`。
func (s *Server) handlePlanRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := s.withPlanID(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	if err := s.service.PlanRemove(ctx, id); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeData(w, nil)
}

// handlePlanOpen 是 `POST /api/plan/open`：打开文件或所在目录（[04 §2.3]）。
//
// **尚未接线**：`internal/service` 当前没有对应的打开方法（本轮给定的 Service
// 接口清单里也没有它）。规范把该端点列在 [04 §2.3] 的 POST 表内、交付物属
// [13 §5] 的 D1，因此这里**注册路由并返回 501**，而不是 404——路径是存在的，
// 返回 404 会让扩展把"还没做"误判成"地址写错了"。
//
// 接线方式已定：加一个 `Service.PlanOpen(ctx, id, target)`，由 Service 判定
// `plan_file_missing` / `plan_file_not_owned` / `open_folder_unavailable`
// （[05 §7.3]），本 handler 只解析 `id` 与 `target` 并转发。
func (s *Server) handlePlanOpen(w http.ResponseWriter, r *http.Request) {
	s.writeNotImplemented(w, r)
}

// handleSource 是 `POST /api/source`：上报来源事件（[04 §2.3]）。
//
// **尚未接线**，理由同 `handlePlanOpen`：来源事件的类型全集是待确认项 `04 I4`，
// `internal/service` 也还没有对应方法。事件的语义与去重规则属业务，必须落在 Service。
//
// 接线时的一条硬约束：`sourceUrl` 只用于来源展示与站点规则判定，**不得进日志**
// （[04 §2.5] 的注解、B-303）。本包的日志不记请求体，天然满足这一条——
// 接线时不要为了排错把它加进来。
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	s.writeNotImplemented(w, r)
}
