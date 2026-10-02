package adapter

import (
	"net"
	"strings"
)

// Match 按 [14 §5.3] 的顺序选出一个适配器：
//
//  1. `match.priority` 降序
//  2. 同优先级下，精确域名优先于通配域名
//  3. 都不匹配时使用 `generic`
//
// host 可以是带端口的 `www.example.com:443`（会去掉端口），大小写不敏感。
// 返回 nil 只发生在集合里没有 generic 时——正常分发的集合一定带它。
func (s *Set) Match(host string) *Adapter {
	name := normalizeHost(host)
	if name == "" {
		return nil
	}

	var best *Adapter
	bestExact := false
	for i := range s.adapters {
		candidate := &s.adapters[i]
		exact, ok := candidate.matches(name)
		if !ok {
			continue
		}
		switch {
		case best == nil:
		case candidate.Match.Priority > best.Match.Priority:
		case candidate.Match.Priority < best.Match.Priority:
			continue
		case exact && !bestExact:
		default:
			// 同优先级、同等精确度：**保留先遇到的**（适配器已按目录名排序，
			// 因此这个结果与文件系统的枚举顺序无关，是可复现的）。
			continue
		}
		best, bestExact = candidate, exact
	}

	if best != nil {
		return best
	}
	if generic, ok := s.ByID(GenericID); ok {
		return generic
	}
	return nil
}

// matches 报告该适配器是否覆盖 host；exact 表示命中的是精确域名而非通配。
func (a *Adapter) matches(host string) (exact bool, ok bool) {
	for _, pattern := range a.Match.Hosts {
		switch {
		case strings.EqualFold(pattern, host):
			return true, true
		case pattern == "*":
			return false, true
		case strings.HasPrefix(pattern, "*."):
			// `*.example.com` 只覆盖**子域**，不含裸域——裸域要写一条精确规则。
			// 这条语义写在 [14 §7.2] 要求每个适配器的「匹配范围」小节里说明。
			suffix := pattern[1:]
			if len(host) > len(suffix) && strings.HasSuffix(host, strings.ToLower(suffix)) {
				return false, true
			}
		}
	}
	return false, false
}

// normalizeHost 把主机名归一化：小写、去空白与末尾点、去端口。
func normalizeHost(host string) string {
	name := strings.ToLower(strings.TrimSpace(host))
	if name == "" {
		return ""
	}
	if strings.Contains(name, ":") {
		if split, _, err := net.SplitHostPort(name); err == nil {
			name = split
		} else if index := strings.LastIndexByte(name, ':'); index >= 0 && !strings.Contains(name, "]") {
			// 形如 `example.com:` 的残缺写法：去掉冒号之后的部分。
			name = name[:index]
		}
	}
	return strings.TrimSuffix(name, ".")
}
