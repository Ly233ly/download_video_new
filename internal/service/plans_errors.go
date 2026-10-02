package service

import (
	"errors"

	"github.com/Ly233ly/download_video_new/internal/store"
)

// 错误码是**稳定枚举**，一经发布不得改变语义（[12 §3.1]）。
// 取值域共三处：下载类 [05 §7.1]/[05 §7.2]、计划与文件类 [05 §7.3]。
// 这里逐条登记，**不新增、不改名**；新增必须同时登记到 05 的对应表格（[03 §3.4] 的常驻规则）。

const (
	// [05 §7.1] 可重试（自动退避）。
	CodeDownloadFailed         = "download_failed"
	CodePageResolveFailed      = "page_resolve_failed"
	CodeSubtitleDownloadFailed = "subtitle_download_failed"
	CodeWechatDownloadFailed   = "wechat_download_failed"
	CodeEagleImportError       = "eagle_import_error"

	// [05 §7.2] 不可重试（直接 failed）。其中前十个同时是 [05 §3.2] 的创建期校验码。
	CodeInvalidURL                 = "invalid_url"
	CodeBlockedLocalTarget         = "blocked_local_target"
	CodeBlobNotDownloadable        = "blob_not_downloadable"
	CodeFixedRangeFragment         = "fixed_range_fragment"
	CodeInvalidMergeMode           = "invalid_merge_mode"
	CodeInvalidContainer           = "invalid_container"
	CodeMissingMedia               = "missing_media"
	CodeHeadersTooLarge            = "headers_too_large"
	CodeSubtitleTooLarge           = "subtitle_too_large"
	CodeWechatSpecInvalid          = "wechat_spec_invalid"
	CodeWechatKeyInvalid           = "wechat_key_invalid"
	CodeWechatOriginalUnverifiable = "wechat_original_unverifiable"
	CodeOutputNoStreams            = "output_no_streams"
	CodeOutputNoVideo              = "output_no_video"
	CodeOutputNoAudio              = "output_no_audio"
	CodeOutputDurationMismatch     = "output_duration_mismatch"
	// 视频号分辨率与字节数校验（[05 §6.2]）。
	CodeWechatOriginalResolutionMismatch = "wechat_original_resolution_mismatch"
	CodeWechatQualityResolutionMismatch  = "wechat_quality_resolution_mismatch"
	CodeWechatSizeMismatch               = "wechat_size_mismatch"
	CodeWechatIncomplete                 = "wechat_incomplete"
	CodeDiskFull                         = "disk_full"
	// 浏览器中转（P6）专用（[05 §4.4.3]）。
	CodeUploadIncomplete        = "upload_incomplete"
	CodeUploadAborted           = "upload_aborted"
	CodeUploadAbandoned         = "upload_abandoned"
	CodeBrowserModeSizeExceeded = "browser_mode_size_exceeded"
	CodeUploadOffsetMismatch    = "upload_offset_mismatch"
	// CodeContextExpired 与 store 里的常量同一个码，只是从这里引用，避免两处字面量漂移。
	CodeContextExpired = store.CodeContextExpired

	// [05 §7.3] 计划与文件。
	CodePlanNotFound          = "plan_not_found"
	CodePlanNotRetryable      = "plan_not_retryable"
	CodePlanStateChanged      = "plan_state_changed"
	CodePlanFileMissing       = "plan_file_missing"
	CodePlanFileNotOwned      = "plan_file_not_owned"
	CodePlanNotImportable     = "plan_not_importable"
	CodeOutputNameExhausted   = "output_name_exhausted"
	CodeOpenFolderUnavailable = "open_folder_unavailable"

	// CodeInternal 是**兜底码**，不属于 05 的清单：只在"程序自身出错但没有更贴切的码"时使用。
	// 它让绑定层永远能拿到一个机器码，而不是把原始错误或堆栈透出去（[12 §3.3] 的 E2）。
	// 一旦某类内部失败反复出现，应当为它登记一个正式码（[03 §3.4] 的新增流程）。
	CodeInternal = "internal_error"
)

// codeMessages 是错误码 → **可安全展示**的中文消息（[05 §7.4]）。
//
// 全部文案都不含路径、URL 或秘密（[12 §3.3] 的 E3）——它们是给用户看的，
// 排查信息走日志，不走这里。
var codeMessages = map[string]string{
	CodeDownloadFailed:         "下载失败",
	CodePageResolveFailed:      "页面解析失败，未能找到可下载的媒体",
	CodeSubtitleDownloadFailed: "字幕下载失败",
	CodeWechatDownloadFailed:   "视频号下载失败",
	CodeEagleImportError:       "导入 Eagle 失败",

	CodeInvalidURL:                       "媒体地址无效",
	CodeBlockedLocalTarget:               "该地址指向本机或内网，已拒绝",
	CodeBlobNotDownloadable:              "该地址是浏览器内部地址，无法下载",
	CodeFixedRangeFragment:               "这是一段固定字节分片，不是完整媒体",
	CodeInvalidMergeMode:                 "不支持的合并方式",
	CodeInvalidContainer:                 "不支持的输出格式",
	CodeMissingMedia:                     "没有可下载的音视频内容",
	CodeHeadersTooLarge:                  "请求头超过大小上限",
	CodeSubtitleTooLarge:                 "字幕文件超过大小上限",
	CodeWechatSpecInvalid:                "视频号画质档位无效",
	CodeWechatKeyInvalid:                 "视频号解密信息无效",
	CodeWechatOriginalUnverifiable:       "无法验证原画，请改选明确档位",
	CodeOutputNoStreams:                  "输出文件没有可用的媒体流",
	CodeOutputNoVideo:                    "输出文件缺少所选视频流",
	CodeOutputNoAudio:                    "输出文件缺少所选音频流",
	CodeOutputDurationMismatch:           "输出时长与预期不符",
	CodeWechatOriginalResolutionMismatch: "原画分辨率校验未通过",
	CodeWechatQualityResolutionMismatch:  "分辨率与所选档位不符",
	CodeWechatSizeMismatch:               "文件大小与声明不符",
	CodeWechatIncomplete:                 "视频号文件不完整",
	CodeDiskFull:                         "磁盘空间不足，请释放空间后重试",
	CodeUploadIncomplete:                 "上传的字节数不完整",
	CodeUploadAborted:                    "浏览器侧已中止上传",
	CodeUploadAbandoned:                  "上传会话已超时",
	CodeBrowserModeSizeExceeded:          "文件超过浏览器模式的体积上限，请改用软件下载",
	CodeUploadOffsetMismatch:             "上传偏移不匹配，请从返回位置继续",
	CodeContextExpired:                   "任务上下文已失效，请重新创建任务",

	CodePlanNotFound:          "任务不存在",
	CodePlanNotRetryable:      "当前状态不支持重试",
	CodePlanStateChanged:      "任务状态已变化，请刷新后重试",
	CodePlanFileMissing:       "文件不存在",
	CodePlanFileNotOwned:      "该文件不属于本程序管理，已保留",
	CodePlanNotImportable:     "当前状态不支持补导",
	CodeOutputNameExhausted:   "无法生成可用的输出文件名",
	CodeOpenFolderUnavailable: "无法打开所在文件夹",

	CodeInternal: "程序内部错误",
}

// MessageForCode 返回错误码对应的可展示消息。未知码回退为通用文案——
// **不回显原始错误**（[12 §3.3] 的 E2）。
func MessageForCode(code string) string {
	if message, ok := codeMessages[code]; ok {
		return message
	}
	return codeMessages[CodeInternal]
}

// retryableCodes 是 [05 §7.1] 的自动退避重试集合。其余错误码一律直接 failed（[05 §7.2]）。
var retryableCodes = map[string]bool{
	CodeDownloadFailed:         true,
	CodePageResolveFailed:      true,
	CodeSubtitleDownloadFailed: true,
	CodeWechatDownloadFailed:   true,
	CodeEagleImportError:       true,
}

// IsRetryableCode 报告错误码是否参与自动退避重试（[05 §7.1]）。
func IsRetryableCode(code string) bool { return retryableCodes[code] }

// manuallyRetryable 报告失败的记录能否被用户手动重试（[05 §2.2]：
// "failed 只能手动重试"）。
//
// 两类必须排除：
//   - 浏览器中转（P6）的所有失败码：字节在浏览器侧，桌面端没有取字节的途径，
//     送它回 queued 只会空转（[05 §4.4.3]）；
//   - context_expired：会话上下文已失效，只能重新创建任务（[05 §9]）。
func manuallyRetryable(plan store.Plan, code string) bool {
	if plan.Status != store.PlanStatusFailed {
		return false
	}
	if plan.MediaKind == store.PlanMediaBrowser {
		return false
	}
	switch code {
	case CodeContextExpired,
		CodeUploadAborted, CodeUploadAbandoned, CodeUploadIncomplete,
		CodeUploadOffsetMismatch, CodeBrowserModeSizeExceeded:
		return false
	default:
		return true
	}
}

// Error 是 Service 返回给绑定层与 HTTP handler 的稳定错误（[04 §1.1] 的 G2）。
//
// 它只有两样东西能跨边界：**机器码**与**可安全展示的消息**。
// 原始错误与堆栈留在 cause 里，供日志使用，**绝不进入消息**（[12 §3.3] 的 E2/E3）。
type Error struct {
	Code    string
	Message string
	cause   error
}

// Error 实现 error。字符串形式含码与消息，便于日志定位；两者都不含秘密。
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Unwrap 暴露内部原因，供 errors.Is/As 与日志使用。
func (e *Error) Unwrap() error { return e.cause }

// Is 让 errors.Is 按**错误码**比较，而不是按实例：
// 这样调用方写 errors.Is(err, ErrPlanNotFound) 就够，不必关心内部构造。
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return other.Code == e.Code
}

// newError 按码构造错误，消息从码表取——调用方无法顺手把路径或 URL 塞进用户可见消息。
func newError(code string, cause error) *Error {
	return &Error{Code: code, Message: MessageForCode(code), cause: cause}
}

// CodeOf 返回错误的机器码；非 Service 错误返回 CodeInternal。
// 绑定层与 HTTP handler 用它填 [04 §2.4] 的错误响应。
func CodeOf(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return CodeInternal
}

// 常用哨兵错误。内部判断一律用 errors.Is(err, 哨兵)，不比较字符串。
var (
	ErrPlanNotFound     = newError(CodePlanNotFound, store.ErrPlanNotFound)
	ErrPlanNotRetryable = newError(CodePlanNotRetryable, nil)
	ErrPlanStateChanged = newError(CodePlanStateChanged, store.ErrPlanStateChanged)
)
