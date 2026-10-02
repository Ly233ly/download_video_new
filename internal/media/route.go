package media

import (
	"net/http"
	"path"
	"strings"
)

// Kind 是一条下载路径（[05 §4] 的 P1~P6）。
//
// 取值域与 plans.media_kind / plans.resolved_kind 完全相同（[03 §3.3]）：
// 同一个词表在这里是第三次使用，不另立新词。
type Kind string

const (
	KindDirect  Kind = "direct"  // P1 受控 HTTP 直链
	KindHLS     Kind = "hls"     // P2 HLS 清单
	KindDASH    Kind = "dash"    // P2 DASH 清单
	KindPage    Kind = "page"    // P4 页面解析
	KindWechat  Kind = "wechat"  // P5 视频号
	KindBrowser Kind = "browser" // P6 浏览器中转
)

// manifestContentTypes 是 [05 §4.6] 清单判定集合里 Content-Type 的那一半。
var manifestContentTypes = map[string]Kind{
	"application/vnd.apple.mpegurl": KindHLS,
	"application/x-mpegurl":         KindHLS,
	"audio/mpegurl":                 KindHLS,
	"audio/x-mpegurl":               KindHLS,
	"application/dash+xml":          KindDASH,
}

// manifestExtensions 是同一个判定集合里扩展名的那一半（[03 §3.3]：m3u8 / m3u / mpd）。
var manifestExtensions = map[string]Kind{
	".m3u8": KindHLS,
	".m3u":  KindHLS,
	".mpd":  KindDASH,
}

// RouteMismatch 报告"提示不成立"——[05 §4.0] 第二步允许改路的那三种情形。
//
// 它是**控制信号，不是失败**：返回它时保证**尚未写入任何字节**，
// 因此调用方可以安全地在同一次执行内换一条路。
//
// 调用方必须自己守住 §4.0 的两条纪律：只降一次（禁止链式降级），
// 且提示是 wechat/browser、source_url 为空时不得降级。
// 本包只如实报告观察到的输入形态，不替调用方决定要不要降。
type RouteMismatch struct {
	// Actual 是实际观察到的输入形态。
	Actual Kind
	// Address 是换路之后**应当使用**的地址：同源重定向发生时为最终地址，
	// 否则就是请求时的地址。空字符串表示"沿用调用方原来给的地址"。
	//
	// 它**不进 Error()**，也不进日志：地址可能带一次性签名参数（[03 §6]、B-722）。
	Address string

	reason string
}

func (e *RouteMismatch) Error() string { return e.reason }

// newRouteMismatch 构造改路信号。reason 必须**可安全展示**：不含地址与请求头。
func newRouteMismatch(actual Kind, reason string) *RouteMismatch {
	return &RouteMismatch{Actual: actual, reason: reason}
}

// newRouteMismatchAt 在 newRouteMismatch 之上带上换路后要用的地址。
func newRouteMismatchAt(actual Kind, address, reason string) *RouteMismatch {
	return &RouteMismatch{Actual: actual, Address: address, reason: reason}
}

// responseAddress 是响应最终落到的地址（跟随同源重定向之后）。
//
// [05 §4.0] 第二步要求"发生重定向时用重定向后的地址"，取值就在这里。
func responseAddress(resp *http.Response) string {
	if resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	return resp.Request.URL.String()
}

// normalizeContentType 取 `;` 之前的部分，并做大小写与空白归一化。
func normalizeContentType(value string) string {
	if idx := strings.IndexByte(value, ';'); idx >= 0 {
		value = value[:idx]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// isHTMLContentType 判断响应是不是网页（[05 §4.0] 第二步的一种降级情形）。
func isHTMLContentType(value string) bool {
	return normalizeContentType(value) == "text/html"
}

// manifestKindByURL 按地址扩展名判断清单类型（扩展名取值见 [03 §3.3]）。
func manifestKindByURL(rawURL string) (Kind, bool) {
	trimmed := rawURL
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	kind, ok := manifestExtensions[strings.ToLower(path.Ext(trimmed))]
	return kind, ok
}

// directHintMismatch 判定"提示说 direct，但真去连的时候给的并不是单个媒体文件"。
//
// 判据**只有 [05 §4.0] 第二步列出的三种**：Content-Type 是清单类型、
// Content-Type 是 text/html、同源重定向后的地址落在清单扩展名上。
//
// 刻意**不嗅探响应体内容**：文档没有授权这条判据，而它会在
// "某个文件恰好以 `#EXTM3U` 开头"这类情况下误判，代价是把一条本来能下的
// 直链推给页面解析（多跑一次 yt-dlp，还可能失败）。
//
// 返回非 nil 时保证尚未写入任何字节。
func directHintMismatch(resp *http.Response) *RouteMismatch {
	contentType := resp.Header.Get("Content-Type")
	if kind, ok := manifestContentTypes[normalizeContentType(contentType)]; ok {
		return newRouteMismatchAt(kind, responseAddress(resp), "响应是清单而不是单个媒体文件")
	}
	if isHTMLContentType(contentType) {
		return newRouteMismatchAt(KindPage, responseAddress(resp), "响应是网页而不是媒体文件")
	}
	// 同源重定向后的地址：跨源重定向已由 sameHostRedirectOnly 中止（§4.1），
	// 走到这里的重定向一定同源。
	if resp.Request != nil && resp.Request.URL != nil {
		if kind, ok := manifestKindByURL(resp.Request.URL.Path); ok {
			return newRouteMismatchAt(kind, resp.Request.URL.String(), "重定向后的地址是一份清单")
		}
	}
	return nil
}
