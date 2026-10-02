package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ly233ly/download_video_new/internal/media"
	"github.com/Ly233ly/download_video_new/internal/service"
)

// 本文件覆盖装配层的站点适配器加载（[14 §5.3] 的追加条款）：
// 加载失败必须**报错并指出路径**（A-116），但**不终止启动**——只留一条可见警告。
//
// 为什么不去直接跑一次 [Bootstrap]：它要 platform.Acquire（Wails 运行时）、
// 真实数据库与端口，属于集成测试的范畴。这里覆盖的是 Bootstrap 那一步调用的
// 全部逻辑——`loadAdapters` 是它唯一的分支点，返回 nil 而不是 error 这一点本身
// 就是"不终止启动"的结构性证据。

const repoAdaptersDir = "../../adapters"

// TestLoadPageHintsFrom_RealAdapters 断言真实声明能被装配成 media 层的提示。
func TestLoadPageHintsFrom_RealAdapters(t *testing.T) {
	hints, warning := loadPageHintsFrom(repoAdaptersDir)
	if warning != "" {
		t.Fatalf("真实适配器目录不该产生降级警告：%s", warning)
	}
	if hints == nil {
		t.Fatal("没有拿到页面提示实现")
	}

	// M12：抖音的弹层地址必须在这里就被规范化（[14 §10]）。
	const modal = "https://www.douyin.com/jingxuan?modal_id=7662692425235828009&from_page=feed"
	if got := hints.CanonicalPageURL(modal); got != "https://www.douyin.com/video/7662692425235828009" {
		t.Fatalf("规范化地址 = %q", got)
	}
	// 没有声明的站点原样返回（A-104：行为只来自声明）。
	const plain = "https://example.com/watch?v=1"
	if got := hints.CanonicalPageURL(plain); got != plain {
		t.Fatalf("未声明的站点被改动了地址：%q", got)
	}

	code, message, ok := hints.TranslateResolveError(modal, "ERROR: [Douyin] Fresh cookies are needed")
	if !ok || code != "douyin_session_expired" || message == "" {
		t.Fatalf("声明翻译 = (%q, %q, %v)", code, message, ok)
	}
	// 未命中不得翻译：否则"网络抖动"会被说成"需要人工处理"。
	if _, _, ok := hints.TranslateResolveError(modal, "ERROR: HTTP Error 403"); ok {
		t.Error("未声明的上游原文被翻译了")
	}
}

// TestLoadPageHintsFrom_BrokenAdapterWarnsWithPath 断言 A-116 的报错必须带路径。
func TestLoadPageHintsFrom_BrokenAdapterWarnsWithPath(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "douyin", "adapter.json")
	writeTestFile(t, broken, `{"id":"douyin","name":`)

	hints, warning := loadPageHintsFrom(root)
	if hints != nil {
		t.Fatal("坏声明却拿到了提示实现")
	}
	if warning == "" {
		t.Fatal("坏声明没有任何降级说明——用户会只看到「解析失败」而不知道原因")
	}
	if !strings.Contains(warning, broken) {
		t.Fatalf("降级说明没有指出文件路径（A-116）：%q", warning)
	}
}

// TestLoadPageHintsFrom_MissingDirectory 断言适配器目录缺失也只降级、不致命。
func TestLoadPageHintsFrom_MissingDirectory(t *testing.T) {
	hints, warning := loadPageHintsFrom(filepath.Join(t.TempDir(), "adapters"))
	if hints != nil || warning == "" {
		t.Fatalf("目录缺失时 = (%v, %q)，期望 (nil, 非空警告)", hints, warning)
	}
}

// TestCore_LoadAdaptersKeepsWarningWithoutAborting 覆盖 Bootstrap 那一步的装配：
// 目录按"程序安装目录下的 adapters/"解析（[14 §4] 第 114 行），失败写进
// core.warnings，函数照常返回。
func TestCore_LoadAdaptersKeepsWarningWithoutAborting(t *testing.T) {
	t.Run("失败只记警告", func(t *testing.T) {
		core := &Core{}
		hints := core.loadAdapters(filepath.Join(t.TempDir(), "app.exe"))
		if hints != nil {
			t.Fatal("没有适配器却拿到了提示实现")
		}
		if len(core.warnings) != 1 {
			t.Fatalf("core.warnings = %v，期望恰好一条", core.warnings)
		}
	})

	t.Run("成功不记警告", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "adapters", "demo", "adapter.json"),
			`{"id":"demo","name":"示例","version":1,"updatedFor":null,
			  "match":{"hosts":["demo.test"],"priority":10},
			  "identity":{"urlRules":[{"path":"^/v/(\\d+)$","id":"$1"}],
			              "canonical":"https://demo.test/v/{id}","requireId":true},
			  "resolve":{"engine":"yt-dlp","canonicalizePageUrl":true}}`)

		core := &Core{}
		hints := core.loadAdapters(filepath.Join(root, "app.exe"))
		if len(core.warnings) != 0 {
			t.Fatalf("加载成功却记了警告：%v", core.warnings)
		}
		if hints == nil {
			t.Fatal("加载成功却没有提示实现")
		}
		if got := hints.CanonicalPageURL("https://demo.test/v/42?from=feed"); got != "https://demo.test/v/42" {
			t.Fatalf("规范化地址 = %q，期望 https://demo.test/v/42", got)
		}
		// 同一份提示必须按**页面主机**选声明：另一个主机不该被这条规则碰到。
		const other = "https://other.test/v/42"
		if got := hints.CanonicalPageURL(other); got != other {
			t.Fatalf("别的主机被改动了地址：%q", got)
		}
	})
}

// writeTestFile 写一个测试用文件（自动建父目录）。
func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
}

// TestAdaptDownloadError_PassesSiteDeclarationThrough 覆盖**生产路径**上的站点错误透传。
//
// 真正跑下载的 runner 就是这里装配的 [planRunner]（注入 service.Deps.Runner），
// 它返回的 `*media.SiteError` 必须原样变成 service 的码与文案：退化成
// `download_failed` 会让声明白写，而且它落在 [05 §7.1] 的可重试一侧——用户
// 重试多少次都不会成功（[05 §4.2] 的追加条款要求按不可重试处理）。
func TestAdaptDownloadError_PassesSiteDeclarationThrough(t *testing.T) {
	const (
		code = "douyin_session_expired"
		msg  = "抖音需要当前浏览器的新鲜会话，请刷新抖音页面后重试"
	)
	site := &media.SiteError{Code: code, Message: msg}

	adapted := adaptDownloadError(site)

	var downloadErr *service.DownloadError
	if !errors.As(adapted, &downloadErr) {
		t.Fatalf("适配结果不是 *service.DownloadError：%T", adapted)
	}
	if downloadErr.Code != code || downloadErr.Message != msg {
		t.Fatalf("适配结果 = (%q, %q)，期望 (%q, %q)",
			downloadErr.Code, downloadErr.Message, code, msg)
	}
	// 判定用的是 service 自己的分类器（plans_errors.go 的 IsRetryableCode）：
	// 未知码不在 [05 §7.1] 的可重试表里 → 不可重试。
	if service.IsRetryableCode(downloadErr.Code) {
		t.Error("站点声明的码被判成了可重试")
	}
	// 原因链保留 SiteError：它只带不含原文的退出码事实，可以进日志。
	if !errors.Is(adapted, site) {
		t.Error("原因链丢了 SiteError")
	}
	for _, got := range []string{adapted.Error(), downloadErr.Message} {
		if strings.Contains(got, "://") || strings.Contains(got, "signature=") {
			t.Fatalf("展示文案含地址或签名参数：%q", got)
		}
	}
}
