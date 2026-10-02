package adapter

import (
	"strings"
	"testing"
)

// 本文件是抖音适配器的 L2 桌面侧落点（[14 §8] 的 A-113）：旧项目
// `tests/test_media.py` 里三条抖音断言迁到这里，断言数量只增不减。
//
// 与 adapter_test.go 的分工：那边按 `fixtures/*.json` 做数据驱动的批量断言；
// 这里钉住**声明与"页面解析器启动之前"的接缝**——规范地址长什么样、哪些入口
// 形态会收敛到同一个规范地址、错误映射的码与文案。
//
// 真正调用规范化的地方在 P4 路径上（[14 §10] 的 M12：**必须在启动进程之前**），
// 由 `internal/media/page_test.go` 与 `internal/app` 的装配测试覆盖；本文件只
// 保证适配器给出的声明足以支撑那次调用。

// douyinModalURL 是旧用例里的原始地址：在"精选"信息流里点开视频弹层。
const douyinModalURL = "https://www.douyin.com/jingxuan?modal_id=7662692425235828009&from_page=feed"

// douyinCanonicalURL 是声明 `identity.canonical` 展开后的结果。
const douyinCanonicalURL = "https://www.douyin.com/video/7662692425235828009"

// TestDouyin_CanonicalModalURL 迁移自旧 tests/test_media.py:441-446：
// `?modal_id=` 形态的弹层地址必须能变成规范的视频详情地址。
func TestDouyin_CanonicalModalURL(t *testing.T) {
	douyin := loadDouyin(t)

	// 没有 `resolve.canonicalizePageUrl` 就没人会去规范化——它是 M12 的前提。
	if douyin.Resolve == nil || !douyin.Resolve.CanonicalizePageURL {
		t.Fatalf("douyin.resolve.canonicalizePageUrl 必须为 true（[14 §10] 的 M12）：%+v", douyin.Resolve)
	}
	if douyin.Identity == nil || !strings.Contains(douyin.Identity.Canonical, "{id}") {
		t.Fatalf("identity.canonical = %q，必须带 {id} 占位符", douyin.Identity.Canonical)
	}

	id, ok := douyin.ResolveID(douyinModalURL)
	if !ok || id != "7662692425235828009" {
		t.Fatalf("弹层地址取到的 ID = %q（ok=%v），期望 7662692425235828009", id, ok)
	}
	if canonical := canonicalOf(douyin, id); canonical != douyinCanonicalURL {
		t.Fatalf("规范化地址 = %q，期望 %q", canonical, douyinCanonicalURL)
	}

	// 规范地址应当是**不动点**：再喂回同一套规则仍取到同一个 ID。
	// 否则"规范化一次"会随入口形态漂移，解析器又要面对多种地址。
	again, ok := douyin.ResolveID(douyinCanonicalURL)
	if !ok || again != id {
		t.Fatalf("规范地址再解析 = %q（ok=%v），期望仍为 %q", again, ok, id)
	}
}

// TestDouyin_EveryShapeConvergesBeforeToolStart 迁移自旧 tests/test_media.py:665-687：
// 页面解析**在启动工具之前**就把弹层地址规范化。
//
// 在适配器层，这条要求等价于"所有入口形态都收敛到同一个规范地址"：只要有一种
// 形态没能收敛，工具就得认识那种形态——那正是旧实现失败的地方。
func TestDouyin_EveryShapeConvergesBeforeToolStart(t *testing.T) {
	douyin := loadDouyin(t)
	shapes := []struct {
		why string
		url string
	}{
		{"精选信息流里点开的弹层", douyinModalURL},
		{"个人页里点开的弹层", "https://www.douyin.com/user/someone?modal_id=7662692425235828009"},
		{"已经是规范地址", douyinCanonicalURL},
		{"规范地址带无关查询串", "https://www.douyin.com/video/7662692425235828009/?foo=bar"},
		{"文本规则形态（video_<id>）", "https://www.douyin.com/video_7662692425235828009"},
	}
	for _, shape := range shapes {
		t.Run(shape.why, func(t *testing.T) {
			id, ok := douyin.ResolveID(shape.url)
			if !ok {
				t.Fatalf("取不到 ID：%s", shape.url)
			}
			if got := canonicalOf(douyin, id); got != douyinCanonicalURL {
				t.Fatalf("规范化地址 = %q，期望 %q", got, douyinCanonicalURL)
			}
		})
	}

	// 没有声明的站点不得凭空规范化（A-104：行为只来自声明）。generic 连
	// canonical 模板都没有，P4 于是把原地址交给工具。
	generic := loadGeneric(t)
	if generic.Resolve != nil && generic.Resolve.CanonicalizePageURL {
		t.Error("generic 不该声明规范化")
	}
	if generic.Identity != nil && generic.Identity.Canonical != "" {
		t.Errorf("generic 不该有 canonical 模板：%q", generic.Identity.Canonical)
	}
}

// TestDouyin_DeclaredErrorMappings 迁移自旧 tests/test_media.py:690-712。
//
// 码与文案都要断言：码决定界面怎么归类，文案是用户唯一看得到的东西，
// 两者都来自随程序分发的声明（[14 §5] 的 `errors`）。
func TestDouyin_DeclaredErrorMappings(t *testing.T) {
	douyin := loadDouyin(t)

	cases := []struct {
		why      string
		upstream string
		code     string
		message  string
	}{
		{
			why:      "会话过期",
			upstream: "ERROR: [Douyin] Fresh cookies (not necessarily logged in) are needed",
			code:     "douyin_session_expired",
			message:  "抖音需要当前浏览器的新鲜会话，请刷新抖音页面后重试",
		},
		{
			why:      "页面类型不支持",
			upstream: "ERROR: [Douyin] Unsupported URL: https://www.douyin.com/user/someone",
			code:     "douyin_page_unsupported",
			message:  "抖音内容页不是受支持的视频详情地址，请刷新当前视频后重试",
		},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			code, message, ok := douyin.MapError(tc.upstream)
			if !ok {
				t.Fatalf("没有命中任何声明：%s", tc.upstream)
			}
			if code != tc.code || message != tc.message {
				t.Fatalf("翻译结果 = (%q, %q)，期望 (%q, %q)", code, message, tc.code, tc.message)
			}
		})
	}

	// 没有声明的上游原文不得被翻译：把"网络抖动"说成"需要人工处理"会让用户
	// 白折腾一轮（[05 §4.2] 的追加条款只在**命中声明**时改变失败码）。
	if code, _, ok := douyin.MapError("ERROR: unable to download video data: HTTP Error 403"); ok {
		t.Fatalf("未声明的错误被翻译成了 %q", code)
	}
	// 两条声明同时命中时按声明顺序取首个（errors[0] 是 Unsupported URL）。
	combined := "ERROR: Unsupported URL; note: Fresh cookies are needed"
	if code, _, ok := douyin.MapError(combined); !ok || code != "douyin_page_unsupported" {
		t.Fatalf("同时命中时 code = %q（ok=%v），期望按声明顺序取 douyin_page_unsupported", code, ok)
	}
}

// canonicalOf 复现装配层把"ID + canonical 模板"拼成地址的方式
// （internal/app 的 adapterHints.CanonicalPageURL，[14 §10] 的 M12）。
func canonicalOf(a *Adapter, id string) string {
	return strings.Replace(a.Identity.Canonical, "{id}", id, 1)
}
