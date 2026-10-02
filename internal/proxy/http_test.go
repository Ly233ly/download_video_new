package proxy

import (
	"net/http"
	"net/url"
	"testing"
)

func requestTo(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	return req
}

// B-314 后半句：本机 API 与回环**始终直连**，哪怕系统开着代理。
func TestProxyFunc_LoopbackAlwaysDirect(t *testing.T) {
	enabled := Settings{ProxyEnable: 1, ProxyServer: "127.0.0.1:7890"}
	fn := ProxyFunc(enabled)

	for _, target := range []string{
		"http://127.0.0.1:47652/health",
		"http://localhost:47652/health",
		"http://[::1]:47652/health",
		"https://127.0.0.2/any",
	} {
		got, err := fn(requestTo(t, target))
		if err != nil {
			t.Fatalf("%s 返回错误: %v", target, err)
		}
		if got != nil {
			t.Errorf("%s 应当直连，实际走了 %v", target, got)
		}
	}
}

// B-314 前半句：普通下载目标要按系统代理走。
func TestProxyFunc_RemoteUsesSystemProxy(t *testing.T) {
	fn := ProxyFunc(Settings{ProxyEnable: 1, ProxyServer: "127.0.0.1:7890"})
	got, err := fn(requestTo(t, "https://cdn.example.com/video.mp4"))
	if err != nil {
		t.Fatalf("返回错误: %v", err)
	}
	if got == nil {
		t.Fatal("远程目标应当走系统代理，实际直连")
	}
	if got.String() != "http://127.0.0.1:7890" {
		t.Fatalf("代理地址 = %q", got.String())
	}
}

func TestProxyFunc_DisabledOrEmptyGoesDirect(t *testing.T) {
	cases := map[string]Settings{
		"未启用":     {ProxyEnable: 0, ProxyServer: "127.0.0.1:7890"},
		"地址为空":    {ProxyEnable: 1, ProxyServer: ""},
		"地址解析不出来": {ProxyEnable: 1, ProxyServer: "http://"},
	}
	for name, settings := range cases {
		fn := ProxyFunc(settings)
		got, err := fn(requestTo(t, "https://cdn.example.com/a.mp4"))
		if err != nil {
			t.Fatalf("%s：返回错误 %v", name, err)
		}
		if got != nil {
			t.Errorf("%s：应当直连，实际走了 %v", name, got)
		}
	}
}

func TestParseProxyServer_PerProtocolForm(t *testing.T) {
	got := parseProxyServer("http=127.0.0.1:7890;https=127.0.0.1:7891")
	if got == nil {
		t.Fatal("按协议分段的取值应当解析成功")
	}
	// 取 http 段（回环代理不用 TLS）。
	want, _ := url.Parse("http://127.0.0.1:7890")
	if got.String() != want.String() {
		t.Fatalf("解析结果 = %q，期望 %q", got.String(), want.String())
	}
}

func TestParseProxyServer_KeepsExplicitScheme(t *testing.T) {
	got := parseProxyServer("socks5://127.0.0.1:1080")
	if got == nil || got.Scheme != "socks5" {
		t.Fatalf("显式 scheme 应当保留，实际 = %v", got)
	}
}
