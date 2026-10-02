package api

import (
	"context"
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// errorCodeKey 是本包内部的 context 键：handler 用它把已写出的错误码交给访问日志。
//
// 为什么走 context 而不是解析响应体：解析响应体就要把每个响应缓冲下来再发出，
// 那会破坏流式响应（[01 §2.1.1] 的 S6 要求中转上传流式落盘），
// 也会给每个请求多一次内存拷贝。
type errorCodeKey struct{}

// markCode 在请求的 context 上挂一个错误码。
//
// `context.WithValue` 返回新的 context，而 handler 里往往只有 `r`——
// 所以这里改的是 `*r`。Handler 运行在单个 goroutine 内，不存在数据竞争。
func markCode(r *http.Request, code string) {
	if code == "" {
		return
	}
	*r = *r.WithContext(context.WithValue(r.Context(), errorCodeKey{}, code))
}

// codeOf 读取本请求已记录的错误码。
func codeOf(r *http.Request) string {
	code, _ := r.Context().Value(errorCodeKey{}).(string)
	return code
}

// 跨源（[04 §2.1]）。
//
// 两条规则：
//   - 预检（OPTIONS）只回 CORS 头，**不进入业务处理**；
//   - 其余请求在 Origin 白名单通过后**回显该 Origin**。
//
// `Allow-Credentials: true` 与"回显 Origin"必须成对出现：规范不允许在
// `Allow-Origin: *` 的同时带凭据。回显而不是写 `*`，也是为了让浏览器只把响应
// 交给白名单里的那个源。
const (
	preflightMaxAge = "600"

	// allowedMethods 只列本 API 真正接受的方法。扩展预检会带
	// `Access-Control-Request-Method`，返回超出实际能力的集合会掩盖实现缺口。
	allowedMethods = "GET, POST, OPTIONS"
)

// corsMiddleware 负责跨源头与预检短路。
//
// 白名单校验在 preflight 之后、业务之前（[04 §2.2]："除 `GET /health` 外全部端点
// 都需要 Origin 校验"）。预检本身不是业务请求——它由浏览器发起，
// 且浏览器不会在预检里带 Cookie，因此对它做 Origin 校验既无必要也不影响安全：
// 真正携带数据的请求仍会在下面被拦下。
func corsMiddleware(next http.Handler, origin string, whitelisted func(string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vary: Origin 是必需的：响应内容随请求 Origin 变化，
		// 缺它会让中间缓存（含浏览器缓存）把 A 源的响应喂给 B 源。
		w.Header().Add("Vary", "Origin")

		if r.Method == http.MethodOptions {
			// 预检只回头，不解析请求体、不调 Service。
			w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
			// 回显请求的自定义头，而不是写死一份清单：中转上传会用到
			// 自定义请求头，而其细节属 [04 §2.5]（阶段 2.5），
			// 此时写死清单会在阶段 2.5 变成一处需要同步的旧事实。
			if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
				w.Header().Set("Access-Control-Allow-Headers", requested)
			}
			w.Header().Set("Access-Control-Max-Age", preflightMaxAge)
			if whitelisted(r.Header.Get("Origin")) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// 非预检：白名单通过才回显（[04 §2.1] 的"跨源"一段）。
		if whitelisted(r.Header.Get("Origin")) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		next.ServeHTTP(w, r)
	})
}

// originGate 是 Origin 白名单闸门（[04 §2.2]、[01 §2.1]）。
//
// 判定规则**逐字**来自规范：
//
//	读请求 Origin 头 → 等于白名单 → 放行；其他值或不带 Origin → 403
//
// 两处刻意的设计：
//   - 用 `subtle.ConstantTimeCompare`：比较的是非秘密的公开常量，这样做只是
//     避免"通过响应时间判断前缀匹配进度"这类无意义的可观测差异，成本为零；
//   - 缺失 Origin **也是 403**，不是放行。扩展的跨源请求由浏览器自动带上
//     `Origin`，因此"没有 Origin"只能说明调用方不是扩展——这正是要拒的对象。
func originGate(next http.Handler, whitelisted func(string) bool, s *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if !whitelisted(origin) {
			// 只记"有没有带 Origin"，**不记 Origin 的值**：它虽是公开常量，
			// 但拒绝日志会把白名单候选写进日志，与"不回显"的处置保持一致。
			logEvent(r.Context(), slog.LevelWarn, "origin_rejected",
				r.Method, s.routeOf(r), http.StatusForbidden, CodeForbiddenOrigin,
				"origin_present", origin != "")
			writeError(w, http.StatusForbidden, CodeForbiddenOrigin, "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimitMiddleware 给每个请求设上体积上限（[03 §4.5] 的 256 KB）。
//
// 挂在中间件而不是在各 handler 里各写一次：上限是**全端点**的规则
// （[04 §2.1]），漏一个端点就是一个无上限的入口。用 `http.MaxBytesReader`
// 是因为它在**读取时**就截断（[01 §2.1.1] 的 S7：读到超限即刻返回，
// 不等读完整个请求体），而 `Content-Length` 只对老实声明的调用方有效。
//
// 唯一例外是中转上传端点（[04 §2.5]）：它属阶段 2.5，届时该端点在
// 自己的 handler 里换用 [03 §4.6] 的分片上限，**不继承**这里的 256 KB。
func bodyLimitMiddleware(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// timeoutMiddleware 给每个请求装上**有界预算**（[04 §1.1] 的 G1、[01 §9] 的 N3/N4）。
//
// 关键点：它给的是 `context` 的截止时间，**不是** `http.Server.ReadTimeout` /
// `WriteTimeout` 这类"整条连接"的死线——[01 §2.1.1] 的 S5 明确要求后两者设 0，
// 需要闲置上限时用 `http.ResponseController` 续期。这样：
//   - Service 调用总会拿到一个带截止时间的 context（规范禁止无 context 的调用）；
//   - 长事务端点（阶段 2.5 的中转上传）可以用 ResponseController 续期，
//     不会被这里一刀切断。
func timeoutMiddleware(next http.Handler, budget time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// accessLog 记录一条请求级结构化日志。
//
// **不记的东西**（这条比"记什么"更重要，[12 §4.3]、B-303）：
// 完整 URL 与查询串、Cookie、`Authorization`、请求体、签名参数。
// 因此这里只取 `routeOf(r)`（路由模板，如 `/api/plan`）而不是 `r.URL.Path`
// ——后者的查询串由调用方提供，可能本身就是签名 URL。
func accessLog(s *Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			// handler 一个字节都没写（例如被预检短路）：按 200 记。
			status = http.StatusOK
		}

		level := slog.LevelInfo
		event := "request"
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
			event = "request_failed"
		} else if status >= http.StatusBadRequest {
			level = slog.LevelWarn
			event = "request_rejected"
		}

		logEvent(r.Context(), level, event, r.Method, s.routeOf(r), status, codeOf(r),
			"ms", time.Since(start).Milliseconds(),
			"origin_present", r.Header.Get("Origin") != "")
	})
}

// statusRecorder 记录状态码，并把底层 `http.ResponseWriter` 的能力透传出去。
//
// 透传是必需的：阶段 2.5 的流式落盘要用 `http.ResponseController`
// （S5 的续期、S6 的流式），而它按接口类型探测底层 writer——
// 包一层却漏掉 `Unwrap` 就会让续期静默失效。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap 暴露底层 writer，供 `http.ResponseController` 使用（Go 1.20+ 约定）。
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush 透传流式能力；底层不支持时是**静默无操作**而不是报错——
// `http.Flusher` 的约定本就不保证所有 writer 都支持。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom 透传 `io.ReaderFrom`（`io.Copy` 的快路径）：缺少它会让
// S6 要求的流式落盘退化成逐块拷贝，对 GB 级中转是实打实的性能损失。
func (s *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(s.ResponseWriter, src)
}

// equalOrigin 做常量时间比较，两侧都先去空白。
//
// 去空白是必要的：`Origin` 头理论上不含空白，但中间设备会加，
// 而"因为多了一个空格就 403"是极难排查的失败。
func equalOrigin(got, want string) bool {
	return subtle.ConstantTimeCompare(
		[]byte(strings.TrimSpace(got)),
		[]byte(strings.TrimSpace(want))) == 1
}
