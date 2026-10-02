package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// 端点路径。**这里是路径的唯一定义处**，路由注册、日志模板与测试都引用它，
// 避免出现"注册的是 `/api/plans`，日志里写的是 `/api/plan`"这种漂移。
//
// 取值来自 [04 §2.3] 的端点清单（与 [07 §6.3] 的 `AD-3` 一致）。
const (
	PathHealth     = "/health"
	PathPlans      = "/api/plans"
	PathPlan       = "/api/plan"
	PathPlanStop   = "/api/plan/stop"
	PathPlanRemove = "/api/plan/remove"
	PathPlanRetry  = "/api/plan/retry"
	PathPlanOpen   = "/api/plan/open"
	PathPlanImport = "/api/plan/import"
	PathSource     = "/api/source"
	PathJobs       = "/api/jobs"
	PathSites      = "/api/sites"
	PathPreview    = "/api/preview"
	PathMode       = "/api/mode"
	PathUpload     = "/api/upload"
)

// unknownRouteTag 是未知路径在日志里的占位（见 routeOf 的说明）：
// 访问日志只承认注册过的路径，其余一律记成它，绝不记调用方给的原文（B-303）。
const unknownRouteTag = "/unknown"

// route 是一条路由：路径 + 允许的方法 + 处理函数。
//
// `gate` 表示是否要过 Origin 白名单（[04 §2.2]）。**只有 `GET /health` 是 false**，
// 把它显式写在表里而不是靠"记得给别的端点包一层中间件"来保证：
// 这张表就是端点的完整清单，漏包一个闸门会在这个字段上直接暴露出来。
type route struct {
	method  string
	path    string
	handler http.Handler
	gate    bool
}

// routeTable 返回**按注册顺序排列**的路由表（[04 §2.3] 的端点清单）。
//
// 它同时承担三个职责，避免"注册一处、日志一处、测试一处"的三份事实：
//
//  1. 路由注册（见 Server.build）；
//  2. 日志用的路径白名单（routeOf）——调用方提供的路径只有命中它才会被记录，
//     否则一律记成 `/unknown`（B-303：绝不把调用方给的地址写进日志）；
//  3. `Allow` 响应头的构造（405 时告诉调用方这个路径接受什么方法）。
func (s *Server) routeTable() []route {
	return []route{
		// —— 阶段 2 的端点（[13 §5] 的 D1）——

		// `/health` 免 Origin 校验（[04 §2.2]）。刻意排在第一条：
		// 它是唯一没有闸门的端点，位置靠前能让人一眼看到"只有它"。
		{http.MethodGet, PathHealth, http.HandlerFunc(s.handleHealth), false},

		{http.MethodGet, PathPlans, http.HandlerFunc(s.handlePlansList), true},
		{http.MethodGet, PathPlan, http.HandlerFunc(s.handlePlanGet), true},
		{http.MethodPost, PathPlan, http.HandlerFunc(s.handlePlanCreate), true},
		{http.MethodPost, PathPlanStop, http.HandlerFunc(s.handlePlanStop), true},
		{http.MethodPost, PathPlanRetry, http.HandlerFunc(s.handlePlanRetry), true},
		{http.MethodPost, PathPlanRemove, http.HandlerFunc(s.handlePlanRemove), true},
		{http.MethodPost, PathPlanOpen, http.HandlerFunc(s.handlePlanOpen), true},
		{http.MethodPost, PathSource, http.HandlerFunc(s.handleSource), true},

		// —— 阶段 2 之外的端点：注册并返回 501（[04 §2.3] 的表已定义它们的用途）——
		//
		// 这些端点属阶段 2.5/3/4（[13 §5.1]、[13 §6]）。注册它们而不是留空，
		// 是为了让扩展拿到"尚未实现"而不是"地址不对"——后者会触发
		// 端口重发现之类的无用动作（B-214 的低频重探会被误导）。
		//
		// 注意 `/api/upload` 的请求体上限是 [03 §4.6] 的分片上限（1 MB），
		// **不适用** [03 §4.5] 的 256 KB。当前它是 501，bodyLimit 的 256 KB 只是
		// 提前拒绝超大请求体；阶段 2.5 接线时该端点必须改用 [03 §4.6] 的常量与
		// 流式落盘（[01 §2.1.1] 的 S6）。
		{http.MethodGet, PathJobs, http.HandlerFunc(s.notImplemented), true},
		{http.MethodGet, PathSites, http.HandlerFunc(s.notImplemented), true},
		{http.MethodPost, PathSites, http.HandlerFunc(s.notImplemented), true},
		{http.MethodGet, PathPreview, http.HandlerFunc(s.notImplemented), true},
		{http.MethodGet, PathMode, http.HandlerFunc(s.notImplemented), true},
		{http.MethodPost, PathMode, http.HandlerFunc(s.notImplemented), true},
		{http.MethodPost, PathPlanImport, http.HandlerFunc(s.notImplemented), true},
		{http.MethodPut, PathUpload, http.HandlerFunc(s.notImplemented), true},
		{http.MethodPost, PathUpload, http.HandlerFunc(s.notImplemented), true},
		{http.MethodDelete, PathUpload, http.HandlerFunc(s.notImplemented), true},
	}
}

// routeOf 返回日志用的路由模板，**绝不返回调用方提供的原文**。
//
// 访问日志必须在路由匹配之前就能给出模板（被闸门拒绝的请求走不到 handler），
// 而那些请求的 `r.URL.Path` 由调用方提供——它可能本身就是一个签名 URL。
// 因此这里只承认注册过的路径，其余一律记成 `/unknown`：
// 日志少一点细节，换"绝不把调用方提供的地址写进日志"（B-303、B-722）。
func (s *Server) routeOf(r *http.Request) string {
	// ServeMux（Go 1.22+）在匹配成功后会填 `Pattern`（形如 `GET /api/plan`），
	// 优先用它——它是服务端自己注册的模板，不含任何调用方输入。
	if r.Pattern != "" {
		if _, path, found := strings.Cut(r.Pattern, " "); found && s.knownRoute(path) {
			return path
		}
	}
	if _, ok := s.routeMethods[r.URL.Path]; ok {
		return r.URL.Path
	}
	return unknownRouteTag
}

// knownRoute 报告路径是否是注册过的端点。
func (s *Server) knownRoute(path string) bool {
	_, ok := s.routeMethods[path]
	return ok
}

// build 注册全部路由并装配中间件链（[04 §2.3] 的端点清单）。
//
// 链的**顺序不可调换**，从外到内：
//
//	accessLog ← 记到拒绝为止的每一次请求（含 403/404/405）
//	bodyLimit ← 读**之前**设限（[01 §2.1.1] 的 S7）
//	timeout   ← 给 handler 与 Service 调用一个有界预算（G1、N3/N4）
//	cors      ← 预检在业务之前短路（[04 §2.1]："只回 CORS 头、不进入业务处理"）
//	jsonStatus← 把 mux 自产的 404/405 换成 [04 §2.4] 的 JSON 格式
//	mux       ← 路由；各端点的 Origin 校验由路由表的 gate 决定
//
// Origin 白名单**不在链上**：`GET /health` 必须免校验（[04 §2.2]），
// 而在链上做就得为它写一个例外分支——例外分支是将来最容易漏改的地方。
// 于是闸门按路由表的 `gate` 字段逐个端点包，谁暴露谁负责，一眼可查。
//
// **刻意不注册 `/` 兜底 handler**：注册它会让 ServeMux 把所有未匹配的请求
// （包括"路径存在但方法不对"）都交给它，405 与 `Allow` 头就永远不会产生
// （实测过：注册 `/` 后 `GET /api/plan/stop` 变成 404）。
// 因此 404 交给 mux 自产，再由 jsonStatus 换成 JSON 格式。
func (s *Server) build() http.Handler {
	table := s.routeTable()

	s.routeMethods = make(map[string]map[string]struct{}, len(table))
	whitelisted := func(origin string) bool { return equalOrigin(origin, s.origin) }

	mux := http.NewServeMux()
	for _, rt := range table {
		handler := rt.handler
		if rt.gate {
			handler = originGate(handler, whitelisted, s)
		}
		mux.Handle(rt.method+" "+rt.path, handler)

		methods, ok := s.routeMethods[rt.path]
		if !ok {
			methods = make(map[string]struct{})
			s.routeMethods[rt.path] = methods
		}
		methods[rt.method] = struct{}{}
	}

	return accessLog(s,
		bodyLimitMiddleware(
			timeoutMiddleware(
				corsMiddleware(jsonStatus(mux, s), s.origin, whitelisted),
				RequestTimeout),
			MaxRequestBodyBytes))
}

// jsonStatusWriter 把 ServeMux 自产的 404 与 405 换成 [04 §2.4] 的 JSON 错误格式。
//
// mux 这两种响应都是 `text/plain` 的纯文本（实测 `Method Not Allowed\n` 与
// `404 page not found\n`），既不符合 [04 §2.4] 的响应格式，也无法被扩展按 code
// 分支。而它们**不经过任何 handler**，所以只能在紧贴 mux 的 writer 上截。
//
// 其余状态一律原样透传——这个包装器**不改变任何业务响应**：
// 只有 404/405 这两种"没有业务 handler 参与"的情形才被改写。
type jsonStatusWriter struct {
	http.ResponseWriter
	server      *Server
	request     *http.Request
	intercepted bool
}

func (w *jsonStatusWriter) WriteHeader(status int) {
	// 只截 **mux 自产**的 404/405：它们不经过任何 handler，`r.Pattern` 仍为空。
	//
	// 判据必须是"Pattern 为空"而不是只看状态码——**业务 handler 也会写 404**
	// （例如 `plan_not_found`），那是已经带正确错误码的完整响应，
	// 再截一次会把它改写成 `not_found`，让扩展拿到错的码
	// （这个 bug 也被本包的测试捕获过）。
	if !w.intercepted && w.request != nil && w.request.Pattern == "" &&
		(status == http.StatusNotFound || status == http.StatusMethodNotAllowed) {
		w.intercepted = true
		w.writeJSONError(status)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

// writeJSONError 写出与状态码对应的 JSON 错误信封。
//
// 两个细节：
//   - `Content-Type` 必须先删再设：mux 已经写过 `text/plain`，显式删除能让
//     "为什么这里是 JSON" 这件事在代码里看得见；
//   - `Allow` 头由 mux 在此之前设好，**原样保留**——405 没有它等于没告诉调用方
//     正确的用法。
func (w *jsonStatusWriter) writeJSONError(status int) {
	code := CodeNotFound
	if status == http.StatusMethodNotAllowed {
		code = CodeMethodNotAllowed
	}

	w.Header().Del("Content-Type")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if w.request != nil {
		markCode(w.request, code)
		if w.server != nil {
			logEvent(w.request.Context(), slogLevelFor(status), "request_rejected",
				w.request.Method, w.server.routeOf(w.request), status, code)
		}
	}

	w.ResponseWriter.WriteHeader(status)

	body, err := json.Marshal(errorEnvelope{
		OK:    false,
		Error: errorBody{Code: code, Message: messageFor(code)},
	})
	if err == nil {
		_, _ = w.ResponseWriter.Write(body)
	}
}

// Write 在已截获时必须**吞掉**后续写入。
//
// 为什么必需：`http.Error` 的顺序是"设 Content-Type → WriteHeader → 写正文"，
// 因此 mux 在我们换掉响应之后还会写它那段 `404 page not found\n` /
// `Method Not Allowed\n`。不透传的实测后果是响应体变成
// `{"ok":false,...}Method Not Allowed` ——**不再是合法 JSON**，
// 扩展的解码会直接失败（这个 bug 正是被本包的测试捕获的）。
func (w *jsonStatusWriter) Write(b []byte) (int, error) {
	if w.intercepted {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// jsonStatus 包装 handler，拦截 mux 自产的 404/405（见 jsonStatusWriter 的说明）。
func jsonStatus(next http.Handler, s *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&jsonStatusWriter{ResponseWriter: w, server: s, request: r}, r)
	})
}

// notImplemented 把阶段 2 之外的端点统一挂成 501（见 routeTable 的说明）。
func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	s.writeNotImplemented(w, r)
}

// writeServiceError 把 Service 的错误翻译成响应（[04 §1.2] 的 G5：只转发）。
//
// 码与消息都来自 Service，本包**不改写语义**；状态码由 statusForServiceCode
// 一处映射。原始错误只进**本地日志**（[12 §4.3] 允许本地日志带路径排错，
// 但用户可见消息与诊断导出不得带）。
func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status := statusForServiceCode(err)
	code := codeOfServiceError(err)
	if code == "" {
		code = CodeInternalError
	}
	markCode(r, code)

	logEvent(r.Context(), slogLevelFor(status), "service_error",
		r.Method, s.routeOf(r), status, code, "detail", err.Error())

	message, raw := messageOfServiceError(err)
	if raw {
		// 原始错误可能带绝对路径（来自 os / net/http / 驱动），
		// 必须过净化（[04 §2.4]）。
		message = sanitizeMessage(message)
	}
	writeError(w, status, code, message)
}

// jsonDecodeError 是本包的解析失败错误。
//
// 它**只带本包的中文文案**，不带原始解析错误：`json` 的错误会回显字段名与偏移，
// 那是堆栈类信息（[12 §3.3] 的 E2）。需要细节时看日志（[12 §4.3] 允许本地日志
// 记路径，但错误消息不得含路径与堆栈）。
type jsonDecodeError struct{ cause error }

func (e *jsonDecodeError) Error() string { return "请求体解析失败" }
func (e *jsonDecodeError) Unwrap() error { return e.cause }

// withPlanID 是 `/api/plan/*` 那一组端点的公共前置：解析出计划 ID。
//
// 返回 false 表示"参数没给或给了个非字符串"，此时响应已经写好（400），
// 调用方直接返回即可。**它不判断 ID 是否存在**——那是 Service 的业务判断。
//
// 顺序上**先解析请求体再读查询参数**：请求体可能因为超限或非法 JSON 而失败，
// 先读体能让"体超限"稳定地报 413，而不是被"缺 id"的 400 抢先掩盖。
func (s *Server) withPlanID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeDecodeError(w, r, err)
		return "", false
	}

	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" {
		markCode(r, CodeInvalidRequest)
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "")
		return "", false
	}
	return id, true
}

// writeDecodeError 把请求体解析失败映射成响应。
//
// 只有一种特判：`*http.MaxBytesError` → 413（[03 §4.5] 的 256 KB 上限、
// [01 §2.1.1] 的 S7"读到超限即刻返回"）。其余一律 400 + `invalid_request`，
// 且**不回显**原始解析错误。
func (s *Server) writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		markCode(r, CodeRequestTooLarge)
		logEvent(r.Context(), slog.LevelWarn, "request_too_large",
			r.Method, s.routeOf(r), http.StatusRequestEntityTooLarge, CodeRequestTooLarge)
		writeError(w, http.StatusRequestEntityTooLarge, CodeRequestTooLarge, "")
		return
	}

	markCode(r, CodeInvalidRequest)
	var decodeErr *jsonDecodeError
	if errors.As(err, &decodeErr) && decodeErr.cause != nil {
		// 细节只进本地日志（[12 §4.3]），不进响应。
		logEvent(r.Context(), slog.LevelWarn, "body_decode_failed",
			r.Method, s.routeOf(r), http.StatusBadRequest, CodeInvalidRequest,
			"detail", decodeErr.cause.Error())
	}
	writeError(w, http.StatusBadRequest, CodeInvalidRequest, "")
}

// decodeJSON 解析请求体（[04 §1.2]：Handler 只做参数解析）。
//
// 三道边界：
//   - 体积由 `http.MaxBytesReader` 在**读取时**兜住（[01 §2.1.1] 的 S7），
//     超限变成 `*http.MaxBytesError`，由 `writeDecodeError` 映射成 413
//     ——必须在**读取前**设限，读完再判断就等于已经把整个体读进内存了；
//   - **未知字段容忍**：调用方多带字段不报错（服务端先行、扩展随后跟进是常态），
//     代价是拼错字段名不会立刻暴露，这个代价由契约测试而非运行时严格模式来兜；
//   - 空请求体视为零值：允许"只带查询参数的 POST"（例如 `/api/plan/stop`）。
//
// 体积与字段的这种分工是刻意的：**上限是硬约束**（[03 §4.5] 写死 256 KB），
// 而字段严格性是契约演进问题，不该由运行时把关。
//
// dst 必须是指针；本函数不做反射校验，只依赖调用点都传结构体指针。
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // 空体合法
		}
		return &jsonDecodeError{cause: err}
	}
	return nil
}

// writeNotImplemented 是阶段 2 之外的端点的统一占位（[13 §5] 的交付物范围）。
//
// 用 501 + `not_implemented` 而不是 404：路径确实存在（[04 §2.3] 已定义），
// 返回 404 会让扩展把"还没做"误判成"地址写错了"，从而去做端口重发现之类的
// 无用功。未实现的端点**不接受请求体**，也就不触发任何业务路径。
func (s *Server) writeNotImplemented(w http.ResponseWriter, r *http.Request) {
	markCode(r, CodeNotImplemented)
	logEvent(r.Context(), slogLevelFor(http.StatusNotImplemented), "not_implemented",
		r.Method, s.routeOf(r), http.StatusNotImplemented, CodeNotImplemented)
	writeError(w, http.StatusNotImplemented, CodeNotImplemented, "")
}

// listQuery 是列表端点的解析结果（[04 §2.3] 的 `{status[], offset, limit}`）。
type listQuery struct {
	statuses []string
	offset   int
	limit    int
}

// parseListQuery 解析 `status` / `offset` / `limit`。
//
// 只做**参数合法性**校验：是不是整数、有没有超过上限。**不校验状态取值是否合法**
// ——`status` 的取值域是 [03 §3.1] 的业务定义，由 Service 判定并返回稳定码
// （[04 §1.2] 的 G5：Handler 不写业务判断）。
//
// 超上限**拒绝而非截断**：静默截断会让调用方以为拿全了（[03 §5] 的 Q1 要求有界，
// 但"有界"不等于"可以悄悄少给"）。
func parseListQuery(r *http.Request) (listQuery, error) {
	query := r.URL.Query()
	out := listQuery{offset: 0, limit: DefaultListLimit}

	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return listQuery{}, fmt.Errorf("offset 非法")
		}
		out.offset = offset
	}

	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return listQuery{}, fmt.Errorf("limit 非法")
		}
		if limit > MaxListLimit {
			return listQuery{}, fmt.Errorf("limit 超上限")
		}
		out.limit = limit
	}

	out.statuses = parseStatuses(query["status"])
	if len(out.statuses) > MaxStatusFilter {
		return listQuery{}, fmt.Errorf("status 数量超上限")
	}
	return out, nil
}

// parseStatuses 把 `status=a,b` 与重复的 `status=a&status=b` 两种写法统一成去重列表。
//
// 两种都支持是因为扩展与服务端不必约定同一种写法，而这两种写法在 HTTP 里都合法。
// 去重保持**首次出现顺序**：顺序可能影响 Service 的查询计划，无故重排会让
// "同一个请求两次发出不同 SQL"这种问题难以复现。
func parseStatuses(values []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			status := strings.TrimSpace(part)
			if status == "" {
				continue
			}
			if _, ok := seen[status]; ok {
				continue
			}
			seen[status] = struct{}{}
			out = append(out, status)
		}
	}
	return out
}

// slogLevelFor 把 HTTP 状态映射到日志级别（[12 §4.2]）：
// 4xx 是可恢复的调用方问题（Warn），5xx 是操作失败（Error）。
func slogLevelFor(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// ctxWithRequestBudget 给单个请求的 Service 调用加预算（[04 §1.1] 的 G1）。
//
// 与中间件里的同名能力重复是**刻意的**：中间件的预算覆盖整个 handler，
// 而这里是"这一次 Service 调用"的边界。两者叠加以更短者为准，
// 保证任何一条路径上的调用都是有界的（[01 §9] 的 N3/N4 禁止无超时等待）。
func ctxWithRequestBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, RequestTimeout)
}
