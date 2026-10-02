package adapter

import (
	"net/url"
	"regexp"
	"strings"
)

// ResolveID 按 `identity.urlRules` 从地址里取出视频 ID（[14 §8] 的 L1 测试项之一）。
//
// 规则按声明顺序尝试，首个命中者胜；取不到返回 false，调用方据此决定
// `identity.requireId` 的后果（丢弃候选）。
//
// 正则无效或取值表达式越界都只是"这条规则不命中"——**声明本身的问题已经由
// 加载期校验拦掉**（Load 会因坏声明启动失败），运行期不必再报错。
func (a *Adapter) ResolveID(rawURL string) (string, bool) {
	if a.Identity == nil {
		return "", false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}

	for _, rule := range a.Identity.URLRules {
		if rule.Path != "" {
			compiled, err := regexp.Compile(rule.Path)
			if err != nil {
				continue
			}
			if groups := compiled.FindStringSubmatch(parsed.Path); groups != nil {
				if id, ok := expandID(rule.ID, groups); ok && id != "" {
					return id, true
				}
			}
		}
		if rule.Query != "" {
			value := parsed.Query().Get(rule.Query)
			if value == "" {
				continue
			}
			groups := []string{value}
			if rule.Pattern != "" {
				compiled, err := regexp.Compile(rule.Pattern)
				if err != nil {
					continue
				}
				found := compiled.FindStringSubmatch(value)
				if found == nil {
					continue
				}
				groups = found
			}
			if id, ok := expandID(rule.ID, groups); ok && id != "" {
				return id, true
			}
		}
		if rule.Path == "" && rule.Query == "" && rule.Pattern != "" {
			// 文本规则（[14 §5.4]）：桌面端只有地址这一份文本，所以作用于整条地址。
			// 扩展侧还会把它作用于 DOM 信号值与类名——那是页面上才有的东西。
			compiled, err := regexp.Compile(rule.Pattern)
			if err != nil {
				continue
			}
			if groups := compiled.FindStringSubmatch(parsed.String()); groups != nil {
				if id, ok := expandID(rule.ID, groups); ok && id != "" {
					return id, true
				}
			}
		}
	}
	return "", false
}

// expandID 展开取值表达式：`$0` 是整串，`$1`..`$9` 是捕获组。
//
// 只支持一位数字（[14 §5] 的示例只用到 `$0` 与 `$1`）：多数字组会让表达式
// 出现歧义，真有需要时应当先改规范，而不是让两端各猜一种解析方式。
func expandID(expr string, groups []string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(expr); i++ {
		if expr[i] != '$' {
			out.WriteByte(expr[i])
			continue
		}
		if i+1 >= len(expr) || expr[i+1] < '0' || expr[i+1] > '9' {
			return "", false
		}
		index := int(expr[i+1] - '0')
		if index >= len(groups) {
			return "", false
		}
		out.WriteString(groups[index])
		i++
	}
	return out.String(), true
}

// MapError 把上游错误翻译成用户提示（[14 §1.2] 的第四类定制原因）：
// 按声明顺序匹配，**首个命中生效**。
//
// yt-dlp 的 `Unsupported URL`、`Fresh cookies` 这类原文对用户没有意义，
// 而它们恰好是"用户下一步该做什么"的唯一线索。
func (a *Adapter) MapError(upstream string) (code, message string, ok bool) {
	for _, rule := range a.Errors {
		if rule.Match == "" {
			continue
		}
		if strings.Contains(upstream, rule.Match) {
			return rule.Code, rule.Message, true
		}
	}
	return "", "", false
}
