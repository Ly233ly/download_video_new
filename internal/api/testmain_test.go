package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// 测试的整体策略（[12 §5]）：
//
//   - 用 `httptest` 与本地回环，**不依赖外网**（T3）；
//   - 数据目录用 `t.TempDir()` 重定向，**不污染用户目录**（T2）——
//     端口发现文件就落在 `%LOCALAPPDATA%\LiudiDownloader\` 下，
//     不重定向就会真的去动用户机器上的文件；
//   - 命名一律 `Test<被测>_<场景>`（[12 §2]）。
//
// 关于 Service：本包只需要它满足一张方法表。测试里的 stub **只承载测试所需行为**，
// 不含任何业务规则——业务规则在 internal/service，本包不复制也不模拟它们
// （[04 §1.2]：Handler 不含业务逻辑，那么它的测试也不该造一份业务规则）。

// testOrigin 是测试用的白名单值。
//
// 刻意**不复用** `DefaultExtensionOrigin`：白名单是否真的生效，只有在一个
// "不等于默认值"的取值上才能测出来（用默认值测，即使比较逻辑被写成恒真也会通过）。
// 真实 ID 的正确性由 [07 §1.1] 的文档对照与编译常量本身保证。
const testOrigin = "chrome-extension://test-only-origin"

// TestMain 把数据目录重定向到临时目录（T2）。
//
// `platform.DataDir()` 走 `os.UserCacheDir()`，Windows 上即 `%LocalAppData%`。
// 在 Windows 上环境变量名大小写不敏感，Go 的 `os.Getenv` 因此能命中 `LocalAppData`。
func TestMain(m *testing.M) {
	temp, err := os.MkdirTemp("", "liudi-api-test-*")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(temp) }()

	original, had := os.LookupEnv("LocalAppData")
	if err := os.Setenv("LocalAppData", temp); err != nil {
		panic(err)
	}
	code := m.Run()
	if had {
		_ = os.Setenv("LocalAppData", original)
	} else {
		_ = os.Unsetenv("LocalAppData")
	}
	os.Exit(code)
}

// stubService 是 Service 的最小替身：每个方法返回预置的结果。
//
// 它不实现任何业务判断（不校验状态、不决定重试），因为那正是本包**不该有**的东西；
// 测试要验证的是"错误被原样转发成什么格式"，而不是"业务规则对不对"。
type stubService struct {
	// settingValue / settingErr 承载 `settings.extension_origin` 的读取
	// （[04 §2.2] 的开发调试覆盖值）。本包只读这一个键，因此替身只实现读取。
	settingValue string
	settingErr   error

	healthErr    error
	planView     service.PlanView
	planErr      error
	page         service.Paged[service.PlanView]
	plansListErr error
	createErr    error
	removeErr    error
}

// Setting 复刻 `*service.Service` 的读取语义（[03 §2.4]）：
// 键不存在或值不是字符串时返回 fallback，且**不报错**。
func (s *stubService) Setting(_ context.Context, _ string, fallback string) (string, error) {
	if s.settingErr != nil {
		return fallback, s.settingErr
	}
	if s.settingValue == "" {
		return fallback, nil
	}
	return s.settingValue, nil
}

func (s *stubService) Health(context.Context) (service.HealthView, error) {
	if s.healthErr != nil {
		return service.HealthView{}, s.healthErr
	}
	return service.HealthView{}, nil
}

func (s *stubService) PlanCreate(context.Context, service.CreatePlanRequest) (service.PlanView, error) {
	if s.createErr != nil {
		return service.PlanView{}, s.createErr
	}
	return s.planView, nil
}

func (s *stubService) PlanGet(context.Context, string) (service.PlanView, error) {
	if s.planErr != nil {
		return service.PlanView{}, s.planErr
	}
	return s.planView, nil
}

func (s *stubService) PlansList(context.Context, []string, int, int) (service.Paged[service.PlanView], error) {
	if s.plansListErr != nil {
		return service.Paged[service.PlanView]{}, s.plansListErr
	}
	return s.page, nil
}

func (s *stubService) PlanStop(context.Context, string) (service.PlanView, error) {
	if s.planErr != nil {
		return service.PlanView{}, s.planErr
	}
	return s.planView, nil
}

func (s *stubService) PlanRetry(context.Context, string) (service.PlanView, error) {
	if s.planErr != nil {
		return service.PlanView{}, s.planErr
	}
	return s.planView, nil
}

func (s *stubService) PlanRemove(context.Context, string) error {
	return s.removeErr
}

// codedError 复刻"方法形态"的错误（`Code()` / `Message()`）。
//
// 它存在的意义是覆盖本包错误提取的**第二条路径**：实际使用的 `service.Error`
// 把码与消息放在导出字段上，而 `internal/download` 的错误用的是方法。
// 两条路径都要有测试，将来统一成哪一种都不会静默失效。
type codedError struct {
	code    string
	message string
	cause   error
}

func (e *codedError) Error() string   { return e.message }
func (e *codedError) Code() string    { return e.code }
func (e *codedError) Message() string { return e.message }
func (e *codedError) Unwrap() error   { return e.cause }

// errCoded 造一个带稳定码的错误。
//
// 直接用 `*service.Error`（**生产路径的形状**）而不是本包的复刻品：
// 测试要验证的是"真实的 Service 错误被转发成什么"，用自造类型测出来的结果
// 不能代表生产行为。
func errCoded(code, message string) error {
	return &service.Error{Code: code, Message: message}
}

// noOriginService 返回一个"不会被调用"的 Server：这些用例连 Service 的
// 入口都到不了（端口、Origin、体积上限、路由），因此传零值替身即可。
func noOriginServer(t *testing.T, origin string) *Server {
	t.Helper()
	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: origin})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv
}

// newServer 造一个只跑路由的 Server，返回它与 handler。
//
// 不调用 `Start`：绝大多数用例要测的是协议层行为，绑定真实端口只会引入
// 端口占用与清理的噪声（那部分由 server_test.go 单独覆盖）。
func newServer(t *testing.T, svc serviceAPI) (*Server, http.Handler) {
	t.Helper()
	srv, err := New(Options{Service: svc, OriginWhitelist: testOrigin})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return srv, srv.handler
}

// doRequest 发一个请求到给定的 handler，返回响应与响应体。
//
// origin 为空表示**不带** `Origin` 头——这正是 [04 §2.2] 要求被 403 的情形之一，
// 因此不能给它一个默认值。
func doRequest(t *testing.T, handler http.Handler, method, target, origin string, body []byte) (*http.Response, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return res, payload
}

// decodeError 解析错误信封，并顺带断言它符合 [04 §2.4] 的形状。
func decodeError(t *testing.T, payload []byte) errorBody {
	t.Helper()

	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v（原文 %q）", err, payload)
	}
	if envelope.OK {
		t.Fatalf("失败响应的 ok 必须为 false，实际为 true：%q", payload)
	}
	if envelope.Error == nil {
		t.Fatalf("失败响应缺少 error 对象：%q", payload)
	}
	if envelope.Error.Code == "" {
		t.Fatalf("失败响应缺少稳定错误码：%q", payload)
	}
	if envelope.Error.Message == "" {
		t.Fatalf("失败响应缺少可展示消息：%q", payload)
	}
	return errorBody{Code: envelope.Error.Code, Message: envelope.Error.Message}
}

// decodeSuccess 解析成功信封，返回 data 的原始 JSON。
func decodeSuccess(t *testing.T, payload []byte) json.RawMessage {
	t.Helper()

	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("成功响应不是合法 JSON: %v（原文 %q）", err, payload)
	}
	if !envelope.OK {
		t.Fatalf("成功响应的 ok 必须为 true：%q", payload)
	}
	return envelope.Data
}

// assertNoPathLike 断言消息里没有路径或凭据痕迹（[04 §2.4]、[12 §3.3] 的 E3、B-303）。
//
// 这是**机械检查**而不是措辞检查：它同时覆盖"本包自己写的消息"与
// "从 Service 透传过来的消息"——后者才是容易漏的那一类。
func assertNoPathLike(t *testing.T, message string) {
	t.Helper()
	if pathLike.MatchString(message) {
		t.Fatalf("消息不得包含路径或凭据形态的内容：%q", message)
	}
	if strings.ContainsAny(message, "\r\n\t") {
		t.Fatalf("消息必须是单行短文案：%q", message)
	}
	lower := strings.ToLower(message)
	for _, forbidden := range []string{"cookie", "authorization", "bearer ", "decode_key", "token="} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("消息不得包含秘密类关键词 %q：%q", forbidden, message)
		}
	}
}
