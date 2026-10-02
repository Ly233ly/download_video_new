package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Ly233ly/download_video_new/internal/service"
)

// 本文件覆盖端点层的行为：成功信封、错误转发、稳定码、未实现端点，
// 以及"参数解析只做合法性校验"这条边界。

// TestPlansList_ReturnsSuccessEnvelope：成功响应必须是 `{ "ok": true, "data": ... }`
// （[04 §2.4]）。
func TestPlansList_ReturnsSuccessEnvelope(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, PathPlans, testOrigin, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d：%s", res.StatusCode, payload)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type 必须是 JSON，实际 %q", got)
	}
	data := decodeSuccess(t, payload)
	if string(data) == "" || string(data) == "null" {
		t.Fatalf("列表接口的 data 不得为空，实际 %q", data)
	}
}

// TestPlanGet_ForwardsServiceError：端点必须**原样转发** Service 的错误码与消息
// （[04 §1.2] 的 G5：Handler 只做转发）。
//
// 这是本包最重要的边界之一：如果 Handler 把码改写成自己的码，
// 扩展就会看到一套与桌面端 UI 不一致的错误语义。
func TestPlanGet_ForwardsServiceError(t *testing.T) {
	const message = "任务不存在"
	stub := &stubService{planErr: errCoded("plan_not_found", message)}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodGet, PathPlan+"?id=p-1", testOrigin, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("[05 §7.3] 的 plan_not_found 应映射为 404，实际 %d：%s", res.StatusCode, payload)
	}
	body := decodeError(t, payload)
	if body.Code != "plan_not_found" {
		t.Fatalf("必须原样转发 Service 的错误码，实际 %s", body.Code)
	}
	if body.Message != message {
		t.Fatalf("必须原样转发 Service 的可展示消息，实际 %q", body.Message)
	}
	assertNoPathLike(t, body.Message)
}

// TestPlanGet_MissingIDIsBadRequest：缺 `id` 属**参数不合法**，
// 是 400 而不是 404——404 会让调用方以为"这条计划不存在"。
func TestPlanGet_MissingIDIsBadRequest(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, PathPlan, testOrigin, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 id 应返回 400，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != CodeInvalidRequest {
		t.Fatalf("错误码应为 %s，实际 %s", CodeInvalidRequest, code)
	}
}

// TestPlanGet_AcceptsIDInBody：`id` 也允许放在请求体里（POST 兼容形式）。
func TestPlanGet_AcceptsIDInBody(t *testing.T) {
	stub := &stubService{planErr: errCoded("plan_not_found", "任务不存在")}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodGet, PathPlan, testOrigin, []byte(`{"id":"p-9"}`))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("带 id 的请求应进入 Service 并被判为不存在，实际 %d：%s", res.StatusCode, payload)
	}
}

// TestPlanCreate_ForwardsInputCode：创建期校验失败（[05 §3.2]）应映射为 400，
// 且错误码保持 Service 给的那个。
func TestPlanCreate_ForwardsInputCode(t *testing.T) {
	stub := &stubService{createErr: errCoded("invalid_url", "媒体地址无效")}
	_, handler := newServer(t, stub)

	body := []byte(`{"url":"not-a-url","mediaKind":"direct","outputName":"x","outputContainer":"mp4","mergeMode":"single"}`)
	res, payload := doRequest(t, handler, http.MethodPost, PathPlan, testOrigin, body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("创建期校验失败应返回 400，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != "invalid_url" {
		t.Fatalf("必须转发 Service 的码 invalid_url，实际 %s", code)
	}
}

// TestPlanCreate_AcceptsSourceURLAlias：`sourceUrl` 与 `url` 都是合法写法
// （[03 §2.1] 的列名是 `source_url`）。
func TestPlanCreate_AcceptsSourceURLAlias(t *testing.T) {
	stub := &stubService{createErr: errCoded("invalid_url", "媒体地址无效")}
	_, handler := newServer(t, stub)

	body := []byte(`{"sourceUrl":"https://example.com/v","mediaKind":"direct","outputName":"x","outputContainer":"mp4","mergeMode":"single"}`)
	res, payload := doRequest(t, handler, http.MethodPost, PathPlan, testOrigin, body)
	// 判据：请求进到了 Service（返回了它的码），而不是在本包被拒。
	if res.StatusCode == http.StatusBadRequest && decodeError(t, payload).Code == CodeInvalidRequest {
		t.Fatalf("sourceUrl 别名应被接受并转发给 Service：%s", payload)
	}
}

// TestPlanCreate_EmptyBodyIsAccepted：空请求体合法（字段校验是 Service 的事）。
func TestPlanCreate_EmptyBodyIsAccepted(t *testing.T) {
	stub := &stubService{createErr: errCoded("missing_media", "缺少音视频内容")}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodPost, PathPlan, testOrigin, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("空体应被交给 Service 判定，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != "missing_media" {
		t.Fatalf("应转发 Service 的码，实际 %s", code)
	}
}

// TestPlanCreate_MalformedJSONIsBadRequest：非法 JSON 是 400，
// 且**不得回显**原始解析错误（它含字段名与偏移，属堆栈类信息）。
func TestPlanCreate_MalformedJSONIsBadRequest(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodPost, PathPlan, testOrigin, []byte(`{"url":`))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d：%s", res.StatusCode, payload)
	}
	body := decodeError(t, payload)
	if body.Code != CodeInvalidRequest {
		t.Fatalf("错误码应为 %s，实际 %s", CodeInvalidRequest, body.Code)
	}
	assertNoPathLike(t, body.Message)
	// Go 的解析错误里会出现 "unexpected EOF" 这类字眼；不得出现在响应里。
	if strings.Contains(strings.ToLower(string(payload)), "unexpected") ||
		strings.Contains(strings.ToLower(string(payload)), "eof") {
		t.Fatalf("响应不得包含原始解析错误：%s", payload)
	}
}

// TestPlanStop_MapsConflictCode：`plan_state_changed` 是"时机不对"，
// 映射为 409（[05 §4.4.3] 用它终止浏览器侧上传循环）。
func TestPlanStop_MapsConflictCode(t *testing.T) {
	stub := &stubService{planErr: errCoded("plan_state_changed", "任务状态已变化")}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodPost, PathPlanStop+"?id=p-1", testOrigin, nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("plan_state_changed 应映射为 409，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != "plan_state_changed" {
		t.Fatalf("必须转发原始码，实际 %s", code)
	}
}

// TestPlanRetry_MapsNotRetryable：[05 §2.2] 的终态不可重试 → 409。
func TestPlanRetry_MapsNotRetryable(t *testing.T) {
	stub := &stubService{planErr: errCoded("plan_not_retryable", "当前状态不可重试")}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodPost, PathPlanRetry+"?id=p-1", testOrigin, nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("plan_not_retryable 应映射为 409，实际 %d：%s", res.StatusCode, payload)
	}
}

// TestPlanRemove_SuccessHasNullData：删除成功时 `data` 为 `null`，但 `ok` 为 true。
//
// 明确固定这个形状：扩展只需判 `ok`，不必为"没有 data"分支。
func TestPlanRemove_SuccessHasNullData(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodPost, PathPlanRemove+"?id=p-1", testOrigin, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("删除成功应返回 200，实际 %d：%s", res.StatusCode, payload)
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("成功响应 ok 应为 true：%s", payload)
	}
	if string(envelope.Data) != "null" {
		t.Fatalf("删除成功时 data 应为 null，实际 %s", envelope.Data)
	}
}

// TestPlanRemove_ForwardsNotFound：删除不存在的记录转发 404 + `plan_not_found`。
func TestPlanRemove_ForwardsNotFound(t *testing.T) {
	stub := &stubService{removeErr: errCoded("plan_not_found", "任务不存在")}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodPost, PathPlanRemove+"?id=none", testOrigin, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("应返回 404，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != "plan_not_found" {
		t.Fatalf("应转发原始码，实际 %s", code)
	}
}

// TestServiceError_NoCodeBecomesInternalError：Service 返回**不带稳定码**的错误时，
// 兜底为 500 + `internal_error`，而不是猜一个业务码。
//
// 猜码会让扩展的行为随错误文案变化而漂移（[12 §3.3] 的 E5 禁止用错误信息做流程控制）。
func TestServiceError_NoCodeBecomesInternalError(t *testing.T) {
	stub := &stubService{planErr: errPlain("database is locked")}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodGet, PathPlan+"?id=p-1", testOrigin, nil)
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("无码错误应返回 500，实际 %d：%s", res.StatusCode, payload)
	}
	body := decodeError(t, payload)
	if body.Code != CodeInternalError {
		t.Fatalf("错误码应为 %s，实际 %s", CodeInternalError, body.Code)
	}
}

// TestServiceError_PathInMessageIsSanitized：Service 的消息若带路径，
// 响应里必须被净化（[04 §2.4]、[12 §3.3] 的 E3、B-303）。
//
// 这一条覆盖的是**最容易漏的路径**：本包自己的文案是干净的，
// 但透传来的消息可能来自 os 或 net/http，天然带绝对路径。
func TestServiceError_PathInMessageIsSanitized(t *testing.T) {
	stub := &stubService{
		planErr: errCoded("plan_file_missing", `open C:\Users\alice\Downloads\已完成\video.mp4: The system cannot find the file`),
	}
	_, handler := newServer(t, stub)

	res, payload := doRequest(t, handler, http.MethodGet, PathPlan+"?id=p-1", testOrigin, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("plan_file_missing 应映射为 404，实际 %d", res.StatusCode)
	}
	body := decodeError(t, payload)
	assertNoPathLike(t, body.Message)
	if strings.Contains(body.Message, "alice") {
		t.Fatalf("消息不得包含用户名或路径片段：%q", body.Message)
	}
}

// TestPlanOpen_NotImplemented：[04 §2.3] 定义了该端点，但 `internal/service`
// 尚未提供打开能力（见 handlers.go 的说明）。此时必须**明确**返回 501，
// 而不是 404——后者会让扩展以为地址写错了。
func TestPlanOpen_NotImplemented(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodPost, PathPlanOpen+"?id=p-1", testOrigin, nil)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未接线的端点应返回 501，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != CodeNotImplemented {
		t.Fatalf("错误码应为 %s，实际 %s", CodeNotImplemented, code)
	}
}

// TestSource_NotImplemented：`/api/source` 同上（事件类型全集是待确认项 `04 I4`）。
func TestSource_NotImplemented(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodPost, PathSource, testOrigin, []byte(`{"event":"page_view"}`))
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未接线的端点应返回 501，实际 %d：%s", res.StatusCode, payload)
	}
}

// TestUnimplementedEndpoints_Return501：[13 §5.1] 与 [13 §6] 把其余端点划在
// 阶段 2.5/3/4，它们必须注册并返回 501（而不是 404）。
func TestUnimplementedEndpoints_Return501(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	endpoints := []struct{ method, path string }{
		{http.MethodGet, PathJobs},
		{http.MethodGet, PathSites},
		{http.MethodPost, PathSites},
		{http.MethodGet, PathPreview},
		{http.MethodGet, PathMode},
		{http.MethodPost, PathMode},
		{http.MethodPost, PathPlanImport},
		{http.MethodPut, PathUpload},
		{http.MethodPost, PathUpload},
		{http.MethodDelete, PathUpload},
	}
	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			res, payload := doRequest(t, handler, ep.method, ep.path, testOrigin, nil)
			if res.StatusCode != http.StatusNotImplemented {
				t.Fatalf("应返回 501，实际 %d：%s", res.StatusCode, payload)
			}
			if code := decodeError(t, payload).Code; code != CodeNotImplemented {
				t.Fatalf("错误码应为 %s，实际 %s", CodeNotImplemented, code)
			}
		})
	}
}

// TestUnknownPath_ReturnsJSON404：未注册路径必须给 [04 §2.4] 的 JSON 错误格式，
// 而不是 net/http 默认的纯文本 "404 page not found"（扩展无法按 code 分支）。
func TestUnknownPath_ReturnsJSON404(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, "/api/definitely-not-here", testOrigin, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("未知路径应返回 404，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != CodeNotFound {
		t.Fatalf("错误码应为 %s，实际 %s", CodeNotFound, code)
	}
}

// TestWrongMethod_ReturnsJSON405：路径已注册但方法不对 → 405，
// 且必须**也是 JSON 信封**（net/http 默认写纯文本）。
func TestWrongMethod_ReturnsJSON405(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, PathPlanStop, testOrigin, nil)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("方法不匹配应返回 405，实际 %d：%s", res.StatusCode, payload)
	}
	body := decodeError(t, payload)
	if body.Code != CodeMethodNotAllowed {
		t.Fatalf("错误码应为 %s，实际 %s", CodeMethodNotAllowed, body.Code)
	}
	if allow := res.Header.Get("Allow"); !strings.Contains(allow, http.MethodPost) {
		t.Fatalf("405 的 Allow 头应含 POST，实际 %q", allow)
	}
}

// TestParseListQuery_Bounds：分页参数的边界是**参数合法性**，
// 与"status 取值是否合法"（属业务）分开。
func TestParseListQuery_Bounds(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	cases := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"默认值", "", http.StatusOK},
		{"正常范围", "?status=queued,running&offset=10&limit=20", http.StatusOK},
		{"重复 status 参数", "?status=queued&status=failed", http.StatusOK},
		{"limit 等于上限", "?limit=200", http.StatusOK},
		{"limit 超上限", "?limit=201", http.StatusBadRequest},
		{"limit 为零", "?limit=0", http.StatusBadRequest},
		{"limit 非数字", "?limit=abc", http.StatusBadRequest},
		{"offset 为负", "?offset=-1", http.StatusBadRequest},
		{"offset 非数字", "?offset=x", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, payload := doRequest(t, handler, http.MethodGet, PathPlans+tc.query, testOrigin, nil)
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("期望 %d，实际 %d：%s", tc.wantStatus, res.StatusCode, payload)
			}
			if tc.wantStatus != http.StatusOK {
				if code := decodeError(t, payload).Code; code != CodeInvalidRequest {
					t.Fatalf("错误码应为 %s，实际 %s", CodeInvalidRequest, code)
				}
			}
		})
	}
}

// TestParseListQuery_StatusValueIsNotValidatedHere：
// **未知 status 取值不得在本包被拒**——取值域是 [03 §3.1] 的业务定义，
// 由 Service 判定（[04 §1.2] 的 G5）。
//
// 这条用例是"Handler 不含业务判断"的机械护栏：一旦有人在这里加上状态白名单，
// 它就会失败。
func TestParseListQuery_StatusValueIsNotValidatedHere(t *testing.T) {
	// stub 不判定 status，因此未知取值也会走成功路径。
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, PathPlans+"?status=definitely-not-a-status", testOrigin, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status 取值域判定属 Service，本包不得拒绝：%d %s", res.StatusCode, payload)
	}
}

// TestStatusForServiceCode_MapsRegisteredCodes：[05 §7] 的码 → HTTP 状态。
//
// 表驱动覆盖"码表里出现过的每一类"，其中未登记的码必须是 500——
// 静默按 400 处理会把实现缺口伪装成"用户输入错了"。
//
// 输入用 `codedError`（**方法形态**：`Code()` / `Message()`），与其余用例用的
// `*service.Error`（**字段形态**）形成互补：两种错误形态都要能被识别，
// 否则某天 Service 统一成方法形态时，全部业务码会静默变成 500。
func TestStatusForServiceCode_MapsRegisteredCodes(t *testing.T) {
	cases := []struct {
		code       string
		wantStatus int
	}{
		{"plan_not_found", http.StatusNotFound},
		{"plan_file_missing", http.StatusNotFound},
		{"plan_not_retryable", http.StatusConflict},
		{"plan_state_changed", http.StatusConflict},
		{"plan_not_importable", http.StatusConflict},
		{"plan_file_not_owned", http.StatusForbidden},
		{"open_folder_unavailable", http.StatusServiceUnavailable},
		{"invalid_url", http.StatusBadRequest},
		{"blocked_local_target", http.StatusBadRequest},
		{"missing_media", http.StatusBadRequest},
		{"browser_mode_size_exceeded", http.StatusBadRequest},
		{"disk_full", http.StatusBadRequest},
		{"context_expired", http.StatusBadRequest},
		// 未登记的码：说明要么本包的映射漏了，要么 Service 新增了码没进文档。
		{"some_brand_new_code", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := statusForServiceCode(&codedError{code: tc.code, message: "消息"})
			if got != tc.wantStatus {
				t.Fatalf("%s 应映射为 %d，实际 %d", tc.code, tc.wantStatus, got)
			}
		})
	}
}

// TestCodeOfServiceError_FieldAndMethodForms：两种错误形态都能提取出码与消息。
//
// 这不是"为兼容而兼容"：实际使用的 `service.Error` 是**字段形态**，
// 而 `internal/download` 的错误是**方法形态**，两条路径在生产里都会遇到。
func TestCodeOfServiceError_FieldAndMethodForms(t *testing.T) {
	fieldForm := &service.Error{Code: "plan_not_found", Message: "任务不存在"}
	if got := codeOfServiceError(fieldForm); got != "plan_not_found" {
		t.Fatalf("字段形态应提取出码，实际 %q", got)
	}
	if msg, raw := messageOfServiceError(fieldForm); raw || msg != "任务不存在" {
		t.Fatalf("字段形态应直接给出可展示消息，实际 %q raw=%v", msg, raw)
	}

	methodForm := &codedError{code: "plan_not_retryable", message: "当前状态不可重试"}
	if got := codeOfServiceError(methodForm); got != "plan_not_retryable" {
		t.Fatalf("方法形态应提取出码，实际 %q", got)
	}
	if msg, raw := messageOfServiceError(methodForm); raw || msg != "当前状态不可重试" {
		t.Fatalf("方法形态应直接给出可展示消息，实际 %q raw=%v", msg, raw)
	}

	// 两种形态都没有：码为空（由调用方兜底为 internal_error），消息标记为**原始**，
	// 必须过净化才能展示。
	plain := errPlain(`open C:\Users\alice\a.mp4: denied`)
	if got := codeOfServiceError(plain); got != "" {
		t.Fatalf("无码错误的码应为空串，实际 %q", got)
	}
	if msg, raw := messageOfServiceError(plain); !raw || msg == "" {
		t.Fatalf("无码错误的消息应标记为原始文本，实际 %q raw=%v", msg, raw)
	}
}

// TestStatusForServiceCode_NoCodeIsInternalError：没有码的错误是 500。
func TestStatusForServiceCode_NoCodeIsInternalError(t *testing.T) {
	if got := statusForServiceCode(errPlain("boom")); got != http.StatusInternalServerError {
		t.Fatalf("无码错误应映射为 500，实际 %d", got)
	}
}

// TestNew_OriginOverrideFromSettings 对应 [04 §2.2]：
// `settings.extension_origin` 非空时作为**开发调试的覆盖值**。
func TestNew_OriginOverrideFromSettings(t *testing.T) {
	const override = "chrome-extension://dev-override-origin"
	stub := &stubService{settingValue: override}

	srv, err := New(Options{Service: stub})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	res, payload := doRequest(t, srv.handler, http.MethodGet, PathPlans, override, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("覆盖值必须被接受，实际 %d：%s", res.StatusCode, payload)
	}
	// 覆盖之后，编译常量不再生效。
	res, payload = doRequest(t, srv.handler, http.MethodGet, PathPlans, DefaultExtensionOrigin, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("覆盖生效后编译常量必须不再放行，实际 %d：%s", res.StatusCode, payload)
	}
}

// TestNew_EmptySettingFallsBackToCompiledConstant：空覆盖值表示"没有覆盖"
// （[03 §2.4]：该键默认就是空串），此时用编译常量。
func TestNew_EmptySettingFallsBackToCompiledConstant(t *testing.T) {
	srv, err := New(Options{Service: &stubService{}})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	res, payload := doRequest(t, srv.handler, http.MethodGet, PathPlans, DefaultExtensionOrigin, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("空覆盖值时应回退到编译常量，实际 %d：%s", res.StatusCode, payload)
	}
}

// TestNew_SettingErrorDoesNotBlockStartup：读取覆盖值失败**不得**中断装配
// （[03 §2.4]：解码失败必须回退到默认值，不得中断启动）。
func TestNew_SettingErrorDoesNotBlockStartup(t *testing.T) {
	stub := &stubService{settingErr: errPlain("database is locked")}

	srv, err := New(Options{Service: stub})
	if err != nil {
		t.Fatalf("读取覆盖值失败不得导致装配失败: %v", err)
	}
	res, payload := doRequest(t, srv.handler, http.MethodGet, PathPlans, DefaultExtensionOrigin, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("应回退到编译常量，实际 %d：%s", res.StatusCode, payload)
	}
}

// TestDefaultExtensionOrigin_MatchesExtensionIdentity：编译常量与 [07 §1.1]
// 的固定身份一致。
//
// 这是"同一事实的三处落点"（docs/07 §1.1 的值、`extension/manifest.json` 的 key、
// 本常量）里本包能自查的那一处：改扩展 ID 时若漏改这里，这条用例会失败。
func TestDefaultExtensionOrigin_MatchesExtensionIdentity(t *testing.T) {
	const wantID = "cfefnmhhollflbhgbdmphgnpeaeipfil"
	if !strings.HasSuffix(DefaultExtensionOrigin, wantID) {
		t.Fatalf("白名单常量必须含 [07 §1.1] 的扩展 ID，实际 %q", DefaultExtensionOrigin)
	}
	if !strings.HasPrefix(DefaultExtensionOrigin, "chrome-extension://") {
		t.Fatalf("Chrome 扩展 Origin 必须以 chrome-extension:// 开头，实际 %q", DefaultExtensionOrigin)
	}
}

// errPlain 造一个**不带**稳定码的普通错误，用来验证兜底路径。
type plainError struct{ text string }

func (e *plainError) Error() string { return e.text }

func errPlain(text string) error { return &plainError{text: text} }
