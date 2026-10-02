package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// 本包自己产生的错误码。
//
// 与**业务**错误码（`plan_not_found`、`plan_retryable` 等）的区别：业务码由 Service
// 定义并返回，本包只转发（[04 §1.2] 的 G5、[05 §7.3]）；而下面这些是"请求根本
// 没走到业务层"的协议级失败——Origin 不匹配、JSON 解析失败、体积超限等。
// 规范里没有登记它们（[05 §7] 只列下载与计划类），因此在这里集中定义并集中给出
// 可安全展示的中文消息（[05 §7.4] 的要求同样适用）。
//
// 这组码需要在阶段 2 回填到 [05 §7] 或 [04 §2.4] 的表中（[03 §3.4] 的新增流程）。
const (
	// CodeForbiddenOrigin：Origin 缺失或不等于白名单 → 403（[04 §2.2]）。
	//
	// 刻意用**固定文案**：不回显收到的 Origin，也不说明白名单是什么。
	// 前者会把扩展 ID 写进用户可见的错误与日志，后者等于把白名单交给调用方。
	CodeForbiddenOrigin = "forbidden_origin"

	// CodeNotFound：路径没有注册（未实现的端点也走 404，见 routes.go）。
	CodeNotFound = "not_found"

	// CodeMethodNotAllowed：路径存在但方法不对 → 405。
	CodeMethodNotAllowed = "method_not_allowed"

	// CodeInvalidRequest：请求体不是合法 JSON、必填参数缺失或取值超界 → 400。
	//
	// **不区分**"缺 id"与"limit 不是数字"：细分对扩展没有可操作价值，
	// 而细分就要回显字段名与原始解析错误，那是堆栈类信息（[12 §3.3] 的 E2）。
	CodeInvalidRequest = "invalid_request"

	// CodeRequestTooLarge：请求体超过 256 KB → 413（[03 §4.5]、[01 §2.1.1] 的 S7）。
	CodeRequestTooLarge = "request_too_large"

	// CodeNotImplemented：阶段 2 尚未交付的端点 → 501。
	// 它们已在 [04 §2.3] 定义（属阶段 2.5/3/4），**注册但未接线**能让扩展拿到
	// 明确的"还没做"，而不是 404 那种"路径写错了"的误导。
	CodeNotImplemented = "not_implemented"

	// CodeInternalError：Service 返回了不带稳定码的错误，或响应序列化失败 → 500。
	//
	// 它是**兜底**而不是常态：出现即说明某处没有按 [12 §3.1] 返回稳定码，
	// 需要去补码，而不是在这里加分支（[12 §3.3] 的 E1 禁止吞错，故必记日志）。
	CodeInternalError = "internal_error"
)

// codeMessages 是本包自产错误码的可安全展示消息（[05 §7.4]）。
//
// 硬性要求：**不含路径、堆栈或秘密**（[04 §2.4]、[12 §3.3] 的 E3、B-303）。
// 排错需要的细节走结构化日志，不走用户可见文案。
var codeMessages = map[string]string{
	CodeForbiddenOrigin:  "请求来源不被允许",
	CodeNotFound:         "接口不存在",
	CodeMethodNotAllowed: "请求方法不被允许",
	CodeInvalidRequest:   "请求参数无效",
	CodeRequestTooLarge:  "请求内容过大",
	CodeNotImplemented:   "该功能尚未实现",
	CodeInternalError:    "服务内部错误，请稍后重试",
}

// messageFor 返回本包错误码的可展示消息；未登记时返回**兜底文案而不是空串**——
// 空消息会在界面上显示一片空白（与 [05 §7.4] 的处理一致）。
func messageFor(code string) string {
	if msg, ok := codeMessages[code]; ok {
		return msg
	}
	return "请求处理失败"
}

// successEnvelope / errorEnvelope 是 [04 §2.4] 规定的响应信封。
//
// 用两个结构体而不是一个 `map`：字段名是契约的一部分，写错一个字母
// 就会让扩展静默走错分支，而结构体能让编译器与测试同时看住它。
type successEnvelope struct {
	OK   bool `json:"ok"`
	Data any  `json:"data"`
}

type errorEnvelope struct {
	OK    bool      `json:"ok"`
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeData 写出成功响应：`{ "ok": true, "data": ... }`（[04 §2.4]）。
//
// data 允许为 nil（例如删除记录）：此时序列化为 `"data": null`。
// 刻意**不省略** data 字段——扩展统一按 `data` 取值即可，不必分支判空。
// 序列化失败是编程错误，但**不得**panic 在请求路径上：降级为 500 + 稳定码，
// 并把原因记进本地日志（[12 §3.3] 的 E1 禁止吞错）。
func writeData(w http.ResponseWriter, data any) {
	body, err := json.Marshal(successEnvelope{OK: true, Data: data})
	if err != nil {
		slog.Error("序列化响应失败",
			"component", "api", "event", "encode_failed", "code", CodeInternalError, "err", err)
		writeError(w, http.StatusInternalServerError, CodeInternalError, "")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeError 写出失败响应：`{ "ok": false, "error": { code, message } }`（[04 §2.4]）。
//
// message 为空时按 code 取本包登记的中文文案；而**业务码的 message 由调用方传入**
// ——它来自 Service（`download.Error.Message()` 那类实现），本包不复制那份文案表
// （设计原则 3：一处事实，一处定义）。传进来的 message 一律再过一遍 sanitizeMessage。
func writeError(w http.ResponseWriter, status int, code, message string) {
	message = sanitizeMessage(message)
	if message == "" {
		message = messageFor(code)
	}

	body, err := json.Marshal(errorEnvelope{OK: false, Error: errorBody{Code: code, Message: message}})
	if err != nil {
		// 走到这里说明 code/message 里有无法编码的字符（非法 UTF-8 等）。
		// 绝不能返回半截响应，于是退回一组纯 ASCII 的兜底内容。
		slog.Error("序列化错误响应失败",
			"component", "api", "event", "encode_error_failed", "code", code, "err", err)
		body = []byte(`{"ok":false,"error":{"code":"internal_error","message":"服务内部错误，请稍后重试"}}`)
		status = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// maxMessageRunes 是错误消息的长度上限：1000 字符（[03 §4.5] 的"错误消息"，
// 超出截断）。它挡的是"把上游响应体当消息"这类意外，不是长度校验业务。
const maxMessageRunes = 1000

// pathLike 命中"看起来是文件路径或带凭据的 URL"的片段（[12 §3.3] 的 E3、B-303）。
//
// 为什么要机械检测而不是信任调用方：消息可能**间接**来自 net/http、syscall 或
// 数据库驱动，那些错误天然带绝对路径（实测形态如
// `open C:\Users\x\Downloads\a.mp4: Access is denied.`）。规范把"用户可见消息不含
// 路径"定为硬约束，因此这里做一次兜底，命中即整条替换为通用文案。
var pathLike = regexp.MustCompile(
	`(?i)(` +
		`[a-z]:[\\/]` + // Windows 盘符路径：C:\ 或 C:/
		`|\\\\[^\\\s]+` + // UNC 路径：\\server\share
		`|file://` + // file URL
		`|://[^\s/]*@` + // URL 里的 user:pass@host
		`|/(?:users|home|tmp|var|etc|usr|opt|program|windows)/` + // 典型 Unix 绝对路径
		`)`)

// controlChars 命中换行与制表符：消息必须是**单行短文案**，
// 否则界面会被撑开，日志也会被注入伪造行。
var controlChars = regexp.MustCompile(`[\r\n\t]`)

// sanitizeMessage 把任意来源的文本净化成"可直接展示"的消息（[04 §2.4]）。
//
// 三层：① 清掉控制字符；② 命中路径/凭据特征就整条替换为通用文案
// （不逐段打码——打码后的残句仍然会泄露目录结构，且更难读）；
// ③ 按 [03 §4.5] 截断。
func sanitizeMessage(raw string) string {
	msg := strings.TrimSpace(controlChars.ReplaceAllString(raw, " "))
	if msg == "" {
		return ""
	}
	if pathLike.MatchString(msg) {
		return messageFor(CodeInternalError)
	}
	if utf8.RuneCountInString(msg) > maxMessageRunes {
		runes := []rune(msg)
		msg = string(runes[:maxMessageRunes])
	}
	return msg
}

// errorCoder 是"自带稳定机器码的错误"的形状。
//
// 本包只认这一张面孔，不改写它的语义：码是 Service 的契约（[12 §3.1]），
// Handler 的职责是**转发**（[04 §1.2] 的 G5）。
type errorCoder interface {
	Code() string
}

// errorMessenger 是"自带可安全展示消息的错误"的形状。
type errorMessenger interface {
	Message() string
}

// 实际使用的 `internal/service` 把码与消息放在**导出字段**上（`service.Error`
// 的 `Code` / `Message`），而 `internal/download` 用的是方法。下面的提取逻辑
// 覆盖两种形态，Service 侧改哪一种都不需要动本包。

// codeOfServiceError 取出 Service 错误的稳定码；取不到时返回空串。
//
// 判据只有两条：实现 `Code() string`，或携带一个叫 `Code` 的字符串字段。
// **刻意不从错误文本里猜码**（例如看消息里有没有 "not found"）：[12 §3.3] 的 E5
// 禁止用错误信息做流程控制，而且猜出来的码会让扩展的行为随文案变化而漂移。
func codeOfServiceError(err error) string {
	if err == nil {
		return ""
	}

	// ① Service 的实际形状：导出字段 `Code`。
	var serviceErr *service.Error
	if errors.As(err, &serviceErr) {
		return serviceErr.Code
	}

	// ② 方法形态，便于将来统一成 `Code() string` 或换成别处的错误类型。
	var coder errorCoder
	if errors.As(err, &coder) {
		return coder.Code()
	}
	return ""
}

// messageOfServiceError 取出 Service 错误里的**可安全展示消息**；取不到时返回空串，
// 由调用方回退到通用文案。
//
// 优先用 `Message`（`service.Error` 与 `internal/download` 的 `*Error` 都提供），
// 都没有才退到 `Error()`——那条路径必须过 sanitizeMessage 净化，
// 因为原始错误可能来自 os 或 net/http，天然带绝对路径。
func messageOfServiceError(err error) (message string, raw bool) {
	if err == nil {
		return "", false
	}

	var serviceErr *service.Error
	if errors.As(err, &serviceErr) {
		return serviceErr.Message, false
	}

	var messenger errorMessenger
	if errors.As(err, &messenger) {
		return messenger.Message(), false
	}
	return err.Error(), true
}

// 业务码 → HTTP 状态的分组。
//
// **这不是"业务判断"**：它不产生新的判定分支，也不改变调用顺序——只是把
// Service 已经确定的语义翻译成 HTTP 的表达方式，一处定义、一处维护。
// 判定"计划能不能停 / 能不能重试"仍然在 Service（[04 §1.2] 的 G5）。
//
// 码**全部引用 `internal/service` 的常量**，不在这里写第二份字面量——
// 字面量会随 Service 改名而静默失效（映射漏掉就变成 500，很难定位）。
var (
	// notFoundCodes：资源（或资源对应的文件）不存在。
	notFoundCodes = map[string]struct{}{
		service.CodePlanNotFound:    {},
		service.CodePlanFileMissing: {},
	}

	// conflictCodes：当前状态与请求冲突，语义是"时机不对，不是参数错"
	// （[05 §2.2] 的终态不再自动调度、[05 §4.4.3] 用 `plan_state_changed`
	// 终止浏览器侧上传循环）。
	conflictCodes = map[string]struct{}{
		service.CodePlanNotRetryable:  {},
		service.CodePlanStateChanged:  {},
		service.CodePlanNotImportable: {},
	}

	// forbiddenCodes：文件不归程序管（B-401：用户与 IDM 的文件永不改动）。
	forbiddenCodes = map[string]struct{}{
		service.CodePlanFileNotOwned: {},
	}

	// unavailableCodes：环境原因导致暂时不可用，稍后可再试。
	unavailableCodes = map[string]struct{}{
		service.CodeOpenFolderUnavailable: {},
	}
)

// statusForServiceCode 把 Service 的错误码翻译成 HTTP 状态。
//
// 三类结果：
//
//	已登记且已有明确语义的码 → 404 / 409 / 403 / 503；
//	已登记的**输入或环境类**码（[05 §3.2] 与 [05 §7.2] 的其余码）→ 400；
//	**未登记的码** → 500，并且是刻意的：它说明 Service 返回了一个本包不认识的
//	  新码（[03 §3.4] 要求新码必须登记到主文档）。静默按 400 处理会把实现缺口
//	  伪装成"用户输入错了"，而 500 + 日志里的 `code` 能直接指出该去补哪一处。
//
// 因此本函数**不需要调用点给兜底值**——兜底值会让同一份语义在 7 个 handler 里
// 各写一遍，正是 [12 §2] 的"一处事实，一处定义"要避免的。
func statusForServiceCode(err error) int {
	code := codeOfServiceError(err)
	if code == "" {
		// 错误连码都没有：Service 未按 [12 §3.1] 返回稳定码，属实现缺口。
		return http.StatusInternalServerError
	}

	switch {
	case inSet(notFoundCodes, code):
		return http.StatusNotFound
	case inSet(conflictCodes, code):
		return http.StatusConflict
	case inSet(forbiddenCodes, code):
		return http.StatusForbidden
	case inSet(unavailableCodes, code):
		return http.StatusServiceUnavailable
	case code == service.CodeInternal, code == CodeInternalError:
		return http.StatusInternalServerError
	case inSet(inputCodes, code):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// inputCodes 是 [05 §3.2] 与 [05 §7.2] 的**输入/环境类**码：请求本身有问题，
// 改参数或换时机重试即可。它们**不是** 5xx——那是调用方要能区分的边界。
//
// 同样引用 Service 的常量：本包**不自创码**（[05 §7] 是唯一取值域）。
var inputCodes = map[string]struct{}{
	// [05 §3.2] 创建期校验。
	service.CodeInvalidURL: {}, service.CodeBlockedLocalTarget: {},
	service.CodeBlobNotDownloadable: {}, service.CodeFixedRangeFragment: {},
	service.CodeInvalidMergeMode: {}, service.CodeInvalidContainer: {},
	service.CodeMissingMedia: {}, service.CodeWechatSpecInvalid: {},
	service.CodeWechatKeyInvalid: {}, service.CodeWechatOriginalUnverifiable: {},
	// [05 §7.2] 其余不可重试码。
	service.CodeHeadersTooLarge: {}, service.CodeSubtitleTooLarge: {},
	service.CodeUploadIncomplete: {}, service.CodeUploadAborted: {},
	service.CodeUploadAbandoned: {}, service.CodeBrowserModeSizeExceeded: {},
	service.CodeUploadOffsetMismatch: {}, service.CodeContextExpired: {},
	service.CodeDiskFull: {}, service.CodeOutputNameExhausted: {},
}

// inSet 是集合判定的小助手（map 而非 slice：这里的码表会有几十项，
// 线性扫描在请求路径上是无谓的开销）。
func inSet[T comparable](set map[T]struct{}, value T) bool {
	_, ok := set[value]
	return ok
}

// logEvent 记一条本包的结构化日志（[12 §4.1]：字段固定 component/event/code）。
//
// **禁止记录的字段**（[12 §4.3]、B-303、B-722）：URL 与查询串、Cookie、
// `Authorization`、签名参数、请求体。因此本函数只接受非敏感的上下文值，
// 默认只带 method / route / status / ms / origin_present / code。
//
// 特别地，`route` 必须是**路由模板**（`/api/plan`）而不是 `r.URL.Path`：
// 后者的路径参数来自调用方，可能带签名信息。
func logEvent(ctx context.Context, level slog.Level, event, method, route string, status int, code string, attrs ...any) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			// 请求已被客户端取消：排错价值低，且继续写会让日志噪声掩盖真问题。
			level = slog.LevelDebug
		}
	}

	args := []any{"component", "api", "event", event, "method", method, "route", route}
	if status != 0 {
		args = append(args, "status", status)
	}
	if code != "" {
		args = append(args, "code", code)
	}
	args = append(args, attrs...)

	slog.Log(ctx, level, event, args...)
}
