// Package download 是下载引擎：取字节、FFprobe 校验、原子交付（[01 §3]、[05]）。
//
// 已实现的路径（[05 §4]）：
//
//	P1 受控 HTTP 直链（[05 §4.1]）、P2 清单（[05 §4.6.1]）、
//	P3 分离音视频合并（[05 §4.6.2]）、P4 页面解析（[05 §4.2]）。
//
// 视频号（P5）与浏览器中转（P6）不在本包范围内。
//
// 三条贯穿全包的原则（[05 §1]）：
//
//  1. **未通过校验的输出永不交付**——校验是交付的唯一门槛，不通过就删掉临时产物；
//  2. **无法证明归属的文件永不删除**——本包只删自己这次创建的路径；
//  3. **媒体 URL 与请求头只在内存**——不落库、不进日志（B-722 / B-303）。
package media

import (
	"errors"
	"log/slog"
)

// Code 是稳定错误码（[12 §3.1]：一经发布不得改变语义）。
//
// **取值域只有 [05 §7] 与 [05 §7.3] 已定义的那些，本包不得自创**——
// 新增错误码必须同时登记到 05 的对应表（[03 §3.4] 的新增流程、[13 §7] 的 `05 N6`）。
type Code string

// [05 §7.2] 不可重试：校验类。
const (
	// CodeOutputNoStreams：FFprobe 看不到任何可用流。
	CodeOutputNoStreams Code = "output_no_streams"
	// CodeOutputNoVideo：缺少所选视频流。
	CodeOutputNoVideo Code = "output_no_video"
	// CodeOutputNoAudio：缺少所选音频流。
	CodeOutputNoAudio Code = "output_no_audio"
	// CodeOutputDurationMismatch：时长与预期不符。**本阶段只保留接口**（[05 §6.1]）。
	CodeOutputDurationMismatch Code = "output_duration_mismatch"
)

// [05 §7.2] 不可重试：清单与合并类（[05 §4.6]）。
const (
	// CodeManifestInvalid：清单本身无法解析（[05 §4.6.1]）。
	//
	// 与 CodeDownloadFailed 的区别是**重试有没有意义**：取清单失败可能是网络抖动，
	// 清单解析不了则是内容本身的问题，重试只会重复失败。
	CodeManifestInvalid Code = "manifest_invalid"
	// CodeManifestNoMatchingStream：清单里没有匹配所选档位的流（[05 §5.3]）。
	//
	// 刻意**不退到相近档位**：那会让用户拿到一个不是自己选的东西（[05 §5.3]）。
	CodeManifestNoMatchingStream Code = "manifest_no_matching_stream"
	// CodeMergeFailed：分离音视频合并失败（[05 §4.6.2]）。
	CodeMergeFailed Code = "merge_failed"
)

// [05 §7.2] 不可重试：下载与环境类。
const (
	// CodeDownloadFailed：网络或响应异常导致本次下载未完成。它同时是 [05 §7.1]
	// 的**可重试**码——重试次数由调度侧按 `retry_max` 决定（[03 §2.4]），本包不重试。
	CodeDownloadFailed Code = "download_failed"
	// CodeDiskFull：磁盘满。清理本次临时产物后转 `failed`，**不自动重试**（[05 §4.1]）。
	CodeDiskFull Code = "disk_full"
)

// [05 §7.1] 可重试：页面解析类（[05 §4.2]）。
const (
	// CodePageResolveFailed：页面解析失败（yt-dlp 退出非零、输出看不懂、超时）。
	//
	// 可重试的理由与 CodeDownloadFailed 相同：多数失败是临时的（站点抖动、
	// 解析器刚好需要重取一次会话）。页面**确实没有媒体**是另一回事，见下面那个码。
	CodePageResolveFailed Code = "page_resolve_failed"
)

// [05 §7.2] 不可重试：页面解析与请求头预算类（[05 §4.2]）。
const (
	// CodePageMediaUnavailable：页面解析成功，但里面确实没有可下载的媒体。
	//
	// 不可重试：重试会得到同样的页面、同样的结论，只会白等一轮退避。
	CodePageMediaUnavailable Code = "page_media_unavailable"
	// CodeHeadersTooLarge：请求头总量超过 [05 §4.2] 的 6 KB 预算。
	//
	// 不可重试：[05 §7.2] 把它列为不可重试——超预算是输入本身的问题，
	// 重试不会让它变小（要下这类内容得先缩小要带的凭据）。
	CodeHeadersTooLarge Code = "headers_too_large"
)

// [05 §7.3] 计划与文件类。
const (
	// CodeOutputNameExhausted：「已完成」目录里同名冲突太多，无法生成唯一名（[05 §11]）。
	CodeOutputNameExhausted Code = "output_name_exhausted"
)

// codeMessages 是错误码的**可安全展示的中文消息**（[05 §7.4]）。
//
// 硬性要求：**不得包含路径、URL 或秘密**（[12 §3.3] 的 E3）。
// 因此这些消息刻意写得笼统——排错需要的细节走本包的结构化日志，不走用户可见文案。
var codeMessages = map[Code]string{
	CodeDownloadFailed:           "下载未完成，请稍后重试",
	CodeDiskFull:                 "磁盘空间不足，请释放空间后重试",
	CodeOutputNoStreams:          "文件校验未通过：未找到可用的音视频内容",
	CodeOutputNoVideo:            "文件校验未通过：缺少视频内容",
	CodeOutputNoAudio:            "文件校验未通过：缺少音频内容",
	CodeOutputDurationMismatch:   "文件校验未通过：媒体时长与预期不符",
	CodeOutputNameExhausted:      "同名文件过多，无法生成新的文件名",
	CodeManifestInvalid:          "内容清单无法解析，本次没有下载",
	CodeManifestNoMatchingStream: "内容清单里没有符合所选画质的内容",
	CodeMergeFailed:              "音视频合并失败，本次没有下载",
	CodePageResolveFailed:        "无法解析这个页面，请稍后重试",
	CodePageMediaUnavailable:     "这个页面上没有找到可下载的内容",
	CodeHeadersTooLarge:          "登录信息过大，无法用于这次下载",
}

// Message 返回错误码的可安全展示消息（[05 §7.4]）。
//
// 未登记的码返回兜底文案而**不是空串**：错误码是要落库并展示的，
// 空消息会让界面显示一片空白（[03 §3.4]：`failed` 时 `error_code` 必须非空）。
func (c Code) Message() string {
	if msg, ok := codeMessages[c]; ok {
		return msg
	}
	return "下载失败，请稍后重试"
}

// Error 把错误码与可安全展示的中文消息绑在一起。
//
// `cause` 只用于日志与 `errors.Is` 判定，**绝不进入 `Error()` 的输出**——
// 原始错误里可能带 URL、路径或响应体，那是 [12 §3.3] 的 E2/E3 与 B-303 禁止的。
type Error struct {
	Code  Code
	cause error
}

// ErrorOf 构造一个带码错误；cause 可为 nil。
func ErrorOf(code Code, cause error) *Error {
	return &Error{Code: code, cause: cause}
}

// Error 只返回可安全展示的中文消息，**不含 cause**。
func (e *Error) Error() string { return e.Code.Message() }

// Unwrap 暴露原因供 `errors.Is` / `errors.As` 判定与日志使用。
func (e *Error) Unwrap() error { return e.cause }

// SiteError 是站点适配器声明翻译出来的错误（[14 §5] 的 `errors`）。
//
// 码与文案都来自**随程序分发的声明数据**，不是本包自创的码——本包 `Code` 的
// 取值域仍只有 [05 §7] 与 [05 §7.3] 定义的那些（见文件头），这里只是透传，
// 所以它**不登记进 codeMessages**：`Code.Message()` 对它没有意义，
// 可展示文案在 `Message` 里。
//
// 声明是随程序分发的公开内容（A-111 禁止里面出现 Cookie、签名 URL 与抓包原文），
// 因此 `Message` 可以展示给用户。`cause` 里只放不含原文的事实（如退出码）：
// 解析工具的 stderr 常常整条带着媒体地址与签名参数（B-722 / [12 §3.3] 的 E2/E3），
// 它**只允许在内存里过一遍**——不进日志、不进 `Error()`、不进 cause。
type SiteError struct {
	// Code 是声明里的失败码；它不属于 [05 §7] 的码表。
	Code Code
	// Message 是声明里的中文文案，可直接展示。
	Message string
	cause   error
}

// Error 只返回声明里的中文文案，**不含 cause**。
func (e *SiteError) Error() string { return e.Message }

// Unwrap 暴露原因供 `errors.Is` / `errors.As` 判定与日志使用。
func (e *SiteError) Unwrap() error { return e.cause }

// CodeOf 取出错误链上的稳定错误码。
//
// 认两种错误：`*Error`（本包自有的码）与 `*SiteError`（[14 §5] 的声明码，
// 由站点适配器透传）。后者若不被认出来，上层就只能看见一个不带码的错误，
// 那条声明也就白写了。其余错误（New 阶段的装配错误等）仍然没有码——
// 那种错误不落库，由调用方在启动期处理。
func CodeOf(err error) (Code, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target.Code, true
	}
	var site *SiteError
	if errors.As(err, &site) {
		return site.Code, true
	}
	return "", false
}

// Retryable 判断错误码是否属于 [05 §7.1] 的可重试集合。
//
// **本包不实现重试**：退避与 `attempt_count` 的持久化属调度侧（[05 §8]、B-315）。
// 这个函数只把"该不该重试"这个判定留在错误码的归属地，
// 免得调用方各自维护一份码表副本（[12 §2] 的"一处事实，一处定义"）。
func Retryable(code Code) bool {
	switch code {
	case CodeDownloadFailed, CodePageResolveFailed:
		return true
	default:
		return false
	}
}

// RetryableError 判断**一个具体错误**能否参与自动退避重试（[05 §7.1]）。
//
// 与 [Retryable] 的分工：后者只看码，前者还看错误的种类。站点声明的码
// （[SiteError]）**一律不可重试**——它存在的意义就是告诉用户需要人工处理
// （[05 §4.2] 的追加条款），重试只会拿到同样的页面、同样的结论。
// 这条判定不能只靠码表：声明是数据，它完全可以声明一个恰好与 [05 §7.1]
// 重名的码，那时码表会答错，而错误种类不会。
func RetryableError(err error) bool {
	var site *SiteError
	if errors.As(err, &site) {
		return false
	}
	code, ok := CodeOf(err)
	if !ok {
		return false
	}
	return Retryable(code)
}

// logFailure 记一条失败事件（[12 §4.1] 的固定字段）。
//
// **刻意只记 plan_id 与码，不记 URL、路径、请求头**（B-303 / B-722）。
// cause 可能来自 net/http，本包调用方需保证它不带秘密——本包自己构造的 cause
// 只用状态码与本地错误，不含 URL。
func logFailure(code Code, planID string, cause error) {
	attrs := []any{"component", "download", "event", "download_failed", "code", string(code)}
	if planID != "" {
		attrs = append(attrs, "plan_id", planID)
	}
	if cause != nil {
		attrs = append(attrs, "cause", cause.Error())
	}
	slog.Warn("下载未完成", attrs...)
}
