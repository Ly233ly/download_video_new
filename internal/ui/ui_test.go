package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ly233ly/download_video_new/internal/service"
	"github.com/Ly233ly/download_video_new/internal/store"
)

func newUI(t *testing.T) *UI {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc, err := service.New(service.Deps{Store: db})
	if err != nil {
		t.Fatalf("service.New 失败: %v", err)
	}
	binding, err := New(svc)
	if err != nil {
		t.Fatalf("ui.New 失败: %v", err)
	}
	return binding
}

func TestNew_RequiresService(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("缺 Service 时应当装配失败")
	}
}

// 没有持久值时返回默认主题，且**不报错**（B-804 首启浅色 + [03 §2.4] 的回退要求）。
func TestGetTheme_DefaultsToLight(t *testing.T) {
	binding := newUI(t)

	got, err := binding.GetTheme(context.Background())
	if err != nil {
		t.Fatalf("GetTheme 不应报错: %v", err)
	}
	if got != ThemeLight {
		t.Errorf("got = %q，期望 %q", got, ThemeLight)
	}
}

func TestSetTheme_RoundTrip(t *testing.T) {
	binding := newUI(t)
	ctx := context.Background()

	if err := binding.SetTheme(ctx, ThemeDark); err != nil {
		t.Fatalf("SetTheme 失败: %v", err)
	}
	got, err := binding.GetTheme(ctx)
	if err != nil {
		t.Fatalf("GetTheme 失败: %v", err)
	}
	if got != ThemeDark {
		t.Errorf("got = %q，期望 %q", got, ThemeDark)
	}
}

// 非法取值属于输入错误：直接拒绝，不写库（[12 §3.2]）。
func TestSetTheme_RejectsUnknownValue(t *testing.T) {
	binding := newUI(t)
	ctx := context.Background()

	if err := binding.SetTheme(ctx, "neon"); err == nil {
		t.Fatal("未知主题取值应当被拒绝")
	}
	if got, _ := binding.GetTheme(ctx); got != ThemeLight {
		t.Errorf("被拒绝的写入不应落库，GetTheme = %q", got)
	}
}

// 库里存了非字符串 JSON 时回退默认值，不报错也不中断启动（[03 §2.4]）。
func TestGetTheme_FallsBackOnTypeMismatch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SetSetting(context.Background(), ThemeSettingKey, `123`); err != nil {
		t.Fatalf("写库失败: %v", err)
	}
	svc, err := service.New(service.Deps{Store: db})
	if err != nil {
		t.Fatalf("service.New 失败: %v", err)
	}
	binding, err := New(svc)
	if err != nil {
		t.Fatalf("ui.New 失败: %v", err)
	}

	got, err := binding.GetTheme(context.Background())
	if err != nil {
		t.Fatalf("类型不符时不应报错: %v", err)
	}
	if got != ThemeLight {
		t.Errorf("got = %q，期望回退为 %q", got, ThemeLight)
	}
}

// 首帧主题注入（B-803）：文档必须带上主题值，且长度变化后不能留旧的 Content-Length。
func TestThemeMiddleware_InjectsIntoDocument(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "64") // 故意设一个旧长度
		_, _ = w.Write([]byte("<html><head><title>x</title></head><body></body></html>"))
	})

	rec := httptest.NewRecorder()
	ThemeMiddleware(ThemeDark)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	if !strings.Contains(body, `window.__LIUDI_THEME__="dark"`) {
		t.Errorf("主题未注入: %s", body)
	}
	if !strings.Contains(body, "<head><script>") {
		t.Errorf("注入点应在 <head> 之后（早于其它脚本）: %s", body)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("内容长度已变，Content-Length 必须删除，实际 %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got == "" {
		t.Error("其它响应头应当保留")
	}
}

func TestThemeMiddleware_PassesAssetsThrough(t *testing.T) {
	const payload = "console.log('asset')"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	})

	rec := httptest.NewRecorder()
	ThemeMiddleware(ThemeDark)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))

	if rec.Body.String() != payload {
		t.Errorf("静态资源不应被改动: %q", rec.Body.String())
	}
}

func TestThemeMiddleware_UnknownThemeNormalizes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<head>"))
	})

	rec := httptest.NewRecorder()
	ThemeMiddleware("neon")(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(rec.Body.String(), `window.__LIUDI_THEME__="light"`) {
		t.Errorf("未知主题应归一化为浅色: %s", rec.Body.String())
	}
}

func TestInitialTheme_WithoutServiceUsesDefault(t *testing.T) {
	if got := InitialTheme(context.Background(), nil); got != ThemeDefault {
		t.Errorf("got = %q，期望 %q", got, ThemeDefault)
	}
}

func TestNormalizeTheme(t *testing.T) {
	cases := map[string]string{
		ThemeDark:  ThemeDark,
		ThemeLight: ThemeLight,
		"":         ThemeLight,
		"NEON":     ThemeLight,
	}
	for in, want := range cases {
		if got := normalizeTheme(in); got != want {
			t.Errorf("normalizeTheme(%q) = %q，期望 %q", in, got, want)
		}
	}
}
