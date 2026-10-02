package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件覆盖 [12 §5] 要求的"必须覆盖"清单里与**认证、跨源、载荷上限、消息净化**
// 有关的部分。

// TestOriginGate_AllowsWhitelistedOrigin 对应 [04 §2.2]：
// `Origin` 等于白名单 → 放行。
func TestOriginGate_AllowsWhitelistedOrigin(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, PathPlans, testOrigin, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("白名单 Origin 必须放行，实际 %d：%s", res.StatusCode, payload)
	}
	decodeSuccess(t, payload)
}

// TestOriginGate_RejectsMissingOrigin 对应 [04 §2.2]：
// **不带** `Origin` 一律 403。
//
// 这不是"顺手加严"：扩展的跨源请求由浏览器自动带上 `Origin`，因此没有它
// 只能说明调用方不是扩展。这条用例同时是"未带 Origin 不得被当成同源放行"的回归。
func TestOriginGate_RejectsMissingOrigin(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, PathPlans, "", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("不带 Origin 必须 403，实际 %d：%s", res.StatusCode, payload)
	}
	body := decodeError(t, payload)
	if body.Code != CodeForbiddenOrigin {
		t.Fatalf("错误码应为 %s，实际 %s", CodeForbiddenOrigin, body.Code)
	}
	assertNoPathLike(t, body.Message)
	// 不泄露白名单内容：拒绝响应里不得出现白名单值。
	if strings.Contains(string(payload), testOrigin) {
		t.Fatalf("拒绝响应不得泄露白名单值：%s", payload)
	}
}

// TestOriginGate_RejectsWrongOrigin：其他 Origin 值一律 403。
func TestOriginGate_RejectsWrongOrigin(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	cases := []struct {
		name   string
		origin string
	}{
		{"其他扩展 ID", "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"网页来源", "https://example.com"},
		{"空字符串 Origin", " "},
		{"带尾斜杠的近似值", testOrigin + "/"},
		{"前缀相同但不相等", testOrigin + "x"},
		{"大小写不同", strings.ToUpper(testOrigin)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, payload := doRequest(t, handler, http.MethodGet, PathPlans, tc.origin, nil)
			if res.StatusCode != http.StatusForbidden {
				t.Fatalf("Origin=%q 必须 403，实际 %d：%s", tc.origin, res.StatusCode, payload)
			}
			if code := decodeError(t, payload).Code; code != CodeForbiddenOrigin {
				t.Fatalf("错误码应为 %s，实际 %s", CodeForbiddenOrigin, code)
			}
		})
	}
}

// TestOriginGate_RejectionDoesNotEchoOrigin：拒绝时**不得**回显 Origin
// （[04 §2.1] 只规定白名单通过后回显）。
//
// 回显会让浏览器把响应交给一个不被允许的源，等于把 CORS 放行写成了恒真。
func TestOriginGate_RejectionDoesNotEchoOrigin(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, _ := doRequest(t, handler, http.MethodGet, PathPlans, "https://evil.example", nil)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("拒绝时不得回显 Origin，实际 %q", got)
	}
}

// TestOriginGate_CoversAllEndpointsExceptHealth 对应 [04 §2.2] 的
// "除 `GET /health` 外全部端点都需要 Origin 校验"。
//
// 用**逐端点表**而不是抽查：漏一个端点就是一个无认证入口，
// 将来新增端点时这张表也会在评审时提醒补一行。
func TestOriginGate_CoversAllEndpointsExceptHealth(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	endpoints := []struct{ method, path string }{
		{http.MethodGet, PathPlans},
		{http.MethodGet, PathPlan},
		{http.MethodPost, PathPlan},
		{http.MethodPost, PathPlanStop},
		{http.MethodPost, PathPlanRetry},
		{http.MethodPost, PathPlanRemove},
		{http.MethodPost, PathPlanOpen},
		{http.MethodPost, PathSource},
		// 阶段 2 之外、当前返回 501 的端点同样要有闸门：
		// "尚未实现"不等于"可以匿名访问"。
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
			res, payload := doRequest(t, handler, ep.method, ep.path, "", nil)
			if res.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s 不带 Origin 必须 403，实际 %d：%s", ep.method, ep.path, res.StatusCode, payload)
			}
			if code := decodeError(t, payload).Code; code != CodeForbiddenOrigin {
				t.Fatalf("错误码应为 %s，实际 %s", CodeForbiddenOrigin, code)
			}
		})
	}
}

// TestHealth_NoOriginRequired 对应 [04 §2.3]：`/health` **免 Origin 校验**。
//
// 它是唯一没有闸门的端点，因此这条用例同时固定了"只有它"这个事实：
// 任何把闸门加到链上的改动都会让这里失败。
func TestHealth_NoOriginRequired(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodGet, PathHealth, "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /health 不带 Origin 也必须放行，实际 %d：%s", res.StatusCode, payload)
	}
	data := decodeSuccess(t, payload)

	// 响应必须是 JSON 对象（[04 §2.3] 说它返回"健康与能力"）。
	var view map[string]json.RawMessage
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatalf("data 应是 JSON 对象: %v（原文 %q）", err, data)
	}

	// `eagleAvailable` 的**字段名**是硬约束（[07 §3] 的 AD-4 与 [04 §2.3] 都点名它）。
	// 它的值由 Service 判定，本包只转发，因此这里只断言字段存在性这一层契约。
	if _, ok := view["eagleAvailable"]; !ok {
		t.Fatalf("health 的 data 必须含 eagleAvailable 字段，实际 %q", data)
	}
}

// TestHealth_RejectsNonGet：`/health` 只接受 GET。
func TestHealth_RejectsNonGet(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, payload := doRequest(t, handler, http.MethodPost, PathHealth, testOrigin, nil)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health 必须 405，实际 %d：%s", res.StatusCode, payload)
	}
	if code := decodeError(t, payload).Code; code != CodeMethodNotAllowed {
		t.Fatalf("405 也必须是 JSON 错误信封，实际 %s", payload)
	}
	if allow := res.Header.Get("Allow"); allow == "" {
		t.Fatalf("405 必须带 Allow 头")
	}
}

// TestCorsPreflight_OnlyReturnsHeaders：[04 §2.1] 规定预检
// "只回 CORS 头、**不进入业务处理**"。
//
// 判据：预检命中一个**业务端点**时也不能触发 Service——因此给一个"任何方法
// 都会失败"的 stub，预检仍必须是 204。
func TestCorsPreflight_OnlyReturnsHeaders(t *testing.T) {
	stub := &stubService{planErr: errCoded("plan_not_found", "任务不存在")}
	_, handler := newServer(t, stub)

	req := httptest.NewRequest(http.MethodOptions, PathPlan, nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-liudi-trace")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("预检应返回 204，实际 %d", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Fatalf("预检必须回显白名单 Origin，实际 %q", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("预检必须允许凭据（与回显 Origin 成对），实际 %q", got)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
		if !strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), method) {
			t.Fatalf("Allow-Methods 应含 %s，实际 %q", method, res.Header.Get("Access-Control-Allow-Methods"))
		}
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "x-liudi-trace") {
		t.Fatalf("预检应回显请求的自定义头，实际 %q", got)
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("预检不得有响应体（不进入业务处理），实际 %q", body)
	}
}

// TestCorsPreflight_WrongOriginDoesNotEcho：非白名单来源的预检不回显任何放行头。
func TestCorsPreflight_WrongOriginDoesNotEcho(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	req := httptest.NewRequest(http.MethodOptions, PathPlans, nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("非白名单来源的预检不得回显 Origin，实际 %q", got)
	}
}

// TestCors_WhitelistedResponseEchoesOrigin 对应 [04 §2.1]：
// 白名单通过后**回显该 Origin**（而不是写 `*`）。
func TestCors_WhitelistedResponseEchoesOrigin(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	res, _ := doRequest(t, handler, http.MethodGet, PathPlans, testOrigin, nil)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Fatalf("必须回显白名单 Origin，实际 %q", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatalf("不得用通配符放行")
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("回显 Origin 时必须同时允许凭据，实际 %q", got)
	}
	if !strings.Contains(res.Header.Get("Vary"), "Origin") {
		t.Fatalf("响应必须带 Vary: Origin，实际 %q", res.Header.Get("Vary"))
	}
}

// TestBodyLimit_RejectsOversizedBody 对应 [03 §4.5]（256 KB 上限）与
// [01 §2.1.1] 的 S7（读到超限即刻返回）。
//
// 断言三层：状态码 413、稳定错误码、以及**响应不得回显请求体内容**
// （请求体可能含媒体地址等敏感信息）。
func TestBodyLimit_RejectsOversizedBody(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	// 造一个超过上限的合法 JSON：`{"url":"<填充>"}`。
	oversized := fmt.Sprintf(`{"url":"%s"}`, strings.Repeat("a", MaxRequestBodyBytes+1024))

	res, payload := doRequest(t, handler, http.MethodPost, PathPlan, testOrigin, []byte(oversized))
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限请求体必须 413，实际 %d：%s", res.StatusCode, payload)
	}
	body := decodeError(t, payload)
	if body.Code != CodeRequestTooLarge {
		t.Fatalf("错误码应为 %s，实际 %s", CodeRequestTooLarge, body.Code)
	}
	assertNoPathLike(t, body.Message)
	if bytes.Contains(payload, bytes.Repeat([]byte("a"), 64)) {
		t.Fatalf("响应不得回显请求体内容")
	}
}

// TestBodyLimit_AllowsBodyAtLimit：刚好在限内的请求体必须被接受
// （上限是 256 KB，不是"小于 256 KB"）。
//
// 边界值用例的意义：`MaxBytesReader` 的语义是"超过才拒"，
// 若实现写成 `>=` 就会在这里失败。
func TestBodyLimit_AllowsBodyAtLimit(t *testing.T) {
	_, handler := newServer(t, &stubService{})

	// 构造恰好等于上限的 JSON 体。
	const prefix = `{"url":"`
	const suffix = `"}`
	filler := strings.Repeat("a", MaxRequestBodyBytes-len(prefix)-len(suffix))
	body := prefix + filler + suffix
	if len(body) != MaxRequestBodyBytes {
		t.Fatalf("测试数据构造错误：%d != %d", len(body), MaxRequestBodyBytes)
	}

	res, payload := doRequest(t, handler, http.MethodPost, PathPlan, testOrigin, []byte(body))
	if res.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("恰好等于上限的请求体不得被拒：%s", payload)
	}
}

// TestSanitizeMessage_StripsPaths 覆盖 [04 §2.4] 的硬要求：
// message 必须可直接展示，**不含路径、堆栈或秘密**（[12 §3.3] 的 E3、B-303）。
//
// 输入刻意取真实世界里会出现的形态：Go 的 os 错误与 net/http 错误都自带绝对路径。
func TestSanitizeMessage_StripsPaths(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"Windows 绝对路径", `open C:\Users\alice\Downloads\video.mp4: Access is denied.`},
		{"Windows 正斜杠路径", "open C:/Users/alice/Downloads/video.mp4: no such file"},
		{"UNC 路径", `open \\fileserver\share\video.mp4: The network path was not found`},
		{"Unix 绝对路径", "open /home/alice/Downloads/video.mp4: no such file or directory"},
		{"file URL", "file:///C:/Users/alice/video.mp4 无法访问"},
		{"带凭据的 URL", "https://user:pass@media.example.com/a.m3u8 请求失败"},
		{"换行注入", "第一行\n第二行\t制表"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeMessage(tc.raw)
			if got == "" {
				t.Fatalf("净化后不得为空串（界面会显示一片空白）")
			}
			assertNoPathLike(t, got)
		})
	}
}

// TestSanitizeMessage_TruncatesLongMessage 对应 [03 §4.5]：
// 错误消息上限 **1000 字符**（超出截断）。
func TestSanitizeMessage_TruncatesLongMessage(t *testing.T) {
	got := sanitizeMessage(strings.Repeat("错", maxMessageRunes+500))
	if len([]rune(got)) != maxMessageRunes {
		t.Fatalf("消息应被截断到 %d 个字符，实际 %d", maxMessageRunes, len([]rune(got)))
	}
}

// TestSanitizeMessage_KeepsReadableText：普通中文文案不得被误伤
// （净化只该命中路径类内容，不能把正常消息也替换掉）。
func TestSanitizeMessage_KeepsReadableText(t *testing.T) {
	const raw = "任务不存在"
	if got := sanitizeMessage(raw); got != raw {
		t.Fatalf("正常文案必须原样保留，实际 %q", got)
	}
}

// TestEqualOrigin_TrimsWhitespace：比较前去除空白。
//
// 理由：`Origin` 头理论上不含空白，但中间设备会加；
// "因为多了一个空格就 403" 是极难排查的失败。
func TestEqualOrigin_TrimsWhitespace(t *testing.T) {
	if !equalOrigin(" "+testOrigin+" ", testOrigin) {
		t.Fatalf("两侧空白必须被忽略")
	}
	if equalOrigin(testOrigin, testOrigin+"x") {
		t.Fatalf("前缀相同不等于相等")
	}
}
