package proxy

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ProxyFunc 把系统代理设置转换成 `http.Transport` 可用的代理选择函数（B-314）。
//
// 三条规则，书写顺序即优先级：
//
//  1. **回环与本机地址一律直连**。B-314 明确"Eagle、本机 API 与回环通信**始终**直连"——
//     本机 API 走代理会直接失败，Eagle 也在本机。这是硬规则，不受系统设置影响。
//  2. 系统未启用代理（`ProxyEnable != 1`）或解析不出地址 → 直连。
//  3. 其余按系统代理走——这正是 B-314 前半句"桌面下载按任务读取 Windows 系统代理"。
//
// **刻意不解析 `ProxyOverride`**：它是 Windows 的绕过列表语义（含 `<local>`、通配主机）。
// 阶段 2 只需满足 B-314 的两半，引入一套通配匹配会让"什么该绕过"出现第二处定义
// （设计原则 3）。真要支持时先改 [01 §2.1] 再动代码（[12 §7]）。
func ProxyFunc(s Settings) func(*http.Request) (*url.URL, error) {
	endpoint := parseProxyServer(s.ProxyServer)
	enabled := s.ProxyEnable == 1 && endpoint != nil

	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return nil, nil
		}
		if isLocalAddress(req.URL.Hostname()) {
			return nil, nil
		}
		if !enabled {
			return nil, nil
		}
		return endpoint, nil
	}
}

// parseProxyServer 把 Windows 的 `ProxyServer` 值解析成代理 URL。
//
// 只认两种常见形态：
//
//	127.0.0.1:7890                            所有协议共用
//	http=127.0.0.1:7890;https=127.0.0.1:7891  按协议分段
//
// 返回 nil 表示"没有可用地址"（空值或解析失败）。**解析失败不报错**：
// 一个写坏的代理设置不该让下载完全起不来，直连是更安全的退化。
func parseProxyServer(value string) *url.URL {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if strings.Contains(value, "=") {
		var httpPart, httpsPart string
		for _, segment := range strings.Split(value, ";") {
			segment = strings.TrimSpace(segment)
			lower := strings.ToLower(segment)
			switch {
			case strings.HasPrefix(lower, "http="):
				httpPart = segment[len("http="):]
			case strings.HasPrefix(lower, "https="):
				httpsPart = segment[len("https="):]
			}
		}
		if httpPart == "" {
			httpPart = httpsPart
		}
		value = strings.TrimSpace(httpPart)
		if value == "" {
			return nil
		}
	}

	// 回环代理不使用 TLS，缺 scheme 时补 http。
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return nil
	}
	return parsed
}

// isLocalAddress 判断主机名是否指向本机（回环或未指定地址）。
//
// 只认字面量，**不做 DNS 解析**：解析会引入延迟与失败路径，而本项目里
// "本机 API 与回环"始终是字面量（`127.0.0.1` / `localhost` / `::1`）。
func isLocalAddress(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}
