package media

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// httptestServer 是本包测试里 `httptest` 服务的短别名——
// 让测试代码行不被重复的包名塞满。
type httptestServer = httptest.Server

// newHTTPTestServer 起一个本地假服务并登记清理（[12 §5.2] 的 T3：不依赖外网）。
func newHTTPTestServer(t *testing.T, handler http.Handler) *httptestServer {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}
