package ui

import (
	"bytes"
	"context"
	"net/http"

	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// 注入点在 `<head>` 之后、任何脚本之前，保证 index.html 的内联脚本能读到它。
const headOpenTag = "<head>"

// InitialTheme 读取首帧要用的主题。
//
// 读不出来就用默认值：主题读失败绝不能挡住窗口出现（[03 §2.4]）。
func InitialTheme(ctx context.Context, svc *service.Service) string {
	if svc == nil {
		return ThemeDefault
	}
	theme, err := svc.Setting(ctx, ThemeSettingKey, ThemeDefault)
	if err != nil {
		return ThemeDefault
	}
	return normalizeTheme(theme)
}

// ThemeMiddleware 把权威主题值注入 index.html。
//
// **为什么必须在服务端注入**：B-803 要求首次渲染直接使用持久值，不得先渲染默认色
// 再切换。[09 §4.3] 给的两条路是"由绑定层注入初始值"或"内联脚本读取持久值"——
// Wails 的绑定在页面加载后才可用，所以这里走前者：中间件把值写进 HTML，
// index.html 的同步内联脚本读它并设置 `data-theme`，React 挂载前主题就已确定。
//
// 只处理文档请求；其余静态资源原样透传。
func ThemeMiddleware(theme string) assetserver.Middleware {
	script := []byte(`<script>window.__LIUDI_THEME__="` + normalizeTheme(theme) + `";</script>`)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" && r.URL.Path != "/index.html" {
				next.ServeHTTP(w, r)
				return
			}

			rec := newRecorder()
			next.ServeHTTP(rec, r)

			body := rec.body.Bytes()
			if bytes.Contains(body, []byte(headOpenTag)) {
				body = bytes.Replace(body, []byte(headOpenTag), append([]byte(headOpenTag), script...), 1)
			}

			header := w.Header()
			for key, values := range rec.header {
				for _, value := range values {
					header.Add(key, value)
				}
			}
			// 内容长度变了，必须让 Go 重新计算；否则浏览器会按旧长度截断页面。
			header.Del("Content-Length")

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			_, _ = w.Write(body)
		})
	}
}

// recorder 是最小的响应缓冲：只为了在文档里插一段脚本。
// 缓冲是**有界**的——它只用于 index.html 这类小文档，静态资源不经过这里。
type recorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newRecorder() *recorder {
	return &recorder{header: make(http.Header)}
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }

func (r *recorder) WriteHeader(statusCode int) { r.status = statusCode }
