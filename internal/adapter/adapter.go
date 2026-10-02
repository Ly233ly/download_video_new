// Package adapter 加载并匹配站点适配器声明（[14](../docs/14-SITE-ADAPTERS.md)）。
//
// 本包只做三件事：**扫描** `adapters/` 的一级子目录、**解析并校验** `adapter.json`、
// 按主机名**匹配**出一个适配器。它不解析媒体地址（那是 yt-dlp 的职责，[14 §1.1]），
// 也不决定下载路径（[14 §5.2] 契约 A-118）。
//
// 校验刻意从严：`adapter.json` 是随程序分发的**内置**内容，拼错一个字段名、
// 目录名与 id 不一致、两个适配器抢同一个域名，都属于**程序自己的错误**——
// 必须在启动时立刻看得见，而不是变成"这个站点忽然抓不到了"（设计原则 2）。
package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FileName 是每个适配器目录里声明文件的固定名字（[14 §3]）。
const FileName = "adapter.json"

// GenericID 是通用兜底适配器的 id（[14 §5.3] 第 3 条：都不匹配时使用它）。
const GenericID = "generic"

// allowedEntries 是适配器目录里允许出现的一级条目（[14 §3] 契约 A-103）。
// 多出来的文件会让"这个适配器到底带不带代码"变得说不清，因此直接拒绝。
var allowedEntries = map[string]bool{
	FileName:    true,
	"README.md": true,
	// L2 才有的两个：代码与夹具。
	"extension.js": true,
	"fixtures":     true,
}

// Adapter 是一份适配器声明的解析结果，字段与 `adapter.json` 一一对应。
type Adapter struct {
	// ID 必须等于其目录名（契约 A-114）。
	ID string `json:"id"`
	// Name 是界面展示名。
	Name string `json:"name"`
	// Version 是该适配器自身的修订号，与产品版本**不绑定**（[14 §12] SA3）。
	Version int `json:"version"`
	// UpdatedFor 是最后按真实页面核对的时间（`YYYY-MM`）；从未核对为 null。
	UpdatedFor *string `json:"updatedFor"`
	// Match 决定"这个适配器管哪些地址"。
	Match Match `json:"match"`
	// Identity 描述"页面上的视频身份怎么取"。
	Identity *Identity `json:"identity"`
	// Capture 描述"候选在页面哪里、选哪个播放器"。
	Capture *Capture `json:"capture"`
	// Title 描述候选标题怎么拼。
	Title *Title `json:"title"`
	// Resolve 描述"页面地址交给谁解析"。
	Resolve *Resolve `json:"resolve"`
	// Errors 是上游错误到用户提示的映射，按顺序匹配、首个命中生效。
	Errors []ErrorRule `json:"errors"`
	// CodeReason 是 L2 的理由；有 extension.js 时必填（契约 A-101）。
	CodeReason *string `json:"codeReason"`
}

// Match 是主机匹配规则。
type Match struct {
	// Hosts 支持 `*.` 前缀通配；不写协议、不写路径。
	Hosts []string `json:"hosts"`
	// Priority 越大越优先（generic 为 0）。
	Priority int `json:"priority"`
}

// Identity 是页面身份规则。
type Identity struct {
	URLRules   []URLRule `json:"urlRules"`
	DOMSignals []string  `json:"domSignals"`
	// Canonical 是 `{id}` 占位符形式的规范地址。
	Canonical string `json:"canonical"`
	// RequireID 为真时取不到 ID 就丢弃候选。
	RequireID bool `json:"requireId"`
}

// URLRule 是一条身份规则，三种形态之一：
//
//   - 路径规则：`path` 匹配 `location.pathname`；
//   - 查询参数规则：取 `query` 命名的参数，可选 `pattern` 校验其取值；
//   - 文本规则：只有 `pattern`，作用于**任意字符串信号**（页面地址、DOM 信号值、
//     类名等），用于「整串就是 ID」或「ID 藏在类名里」这类页面形态。
//
// 三者都按声明顺序尝试，先命中者胜（[14 §5.4]）。
type URLRule struct {
	// Path 是匹配 `location.pathname` 的正则。
	Path string `json:"path"`
	// Query 是查询参数名。
	Query string `json:"query"`
	// Pattern 是 Query 取值的正则；没有 `path`/`query` 时是文本规则的正则。
	Pattern string `json:"pattern"`
	// ID 是 ID 的取值表达式（`$1`、`$0`）。
	ID string `json:"id"`
}

// Capture 是候选发现规则。
type Capture struct {
	Containers       []string `json:"containers"`
	PrimarySelection string   `json:"primarySelection"`
	// AllowDirectStream 与 RequireBlobSource 是**放宽或收紧通用约束的唯一入口**。
	AllowDirectStream bool `json:"allowDirectStream"`
	RequireBlobSource bool `json:"requireBlobSource"`
}

// Title 是候选标题规则。
type Title struct {
	Template string `json:"template"`
	Fallback string `json:"fallback"`
}

// Resolve 是解析引擎声明。
type Resolve struct {
	// Engine 目前只允许 `yt-dlp` 与 `builtin`。
	Engine string `json:"engine"`
	// Builtin 在 Engine 为 `builtin` 时必填（[14 §6.2]）。
	Builtin              string `json:"builtin"`
	RequiresFreshCookies bool   `json:"requiresFreshCookies"`
	CanonicalizePageURL  bool   `json:"canonicalizePageUrl"`
}

// ErrorRule 把上游错误片段翻译成面向用户的提示。
type ErrorRule struct {
	Match   string `json:"match"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Set 是加载完成、已校验过的适配器集合。
type Set struct {
	adapters []Adapter
}

// Load 扫描 root 下的一级子目录并加载全部适配器。
//
// 任何一份声明坏掉都返回错误并**指出路径**（契约 A-116）——静默跳过会表现成
// "这个站点突然抓不到了"，用户与开发者都无从排查。
func Load(root string) (*Set, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("读取适配器目录失败：%w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	// 固定顺序：冲突报错与匹配结果不该随文件系统的枚举顺序变化。
	sort.Strings(names)

	set := &Set{}
	for _, name := range names {
		loaded, err := loadOne(filepath.Join(root, name), name)
		if err != nil {
			return nil, err
		}
		set.adapters = append(set.adapters, *loaded)
	}
	if len(set.adapters) == 0 {
		return nil, fmt.Errorf("适配器目录 %s 下没有任何适配器", root)
	}
	if err := checkHostConflicts(set.adapters); err != nil {
		return nil, err
	}
	return set, nil
}

func loadOne(dir, name string) (*Adapter, error) {
	if err := checkEntries(dir, name); err != nil {
		return nil, err
	}

	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("适配器 %s 无法读取 %s：%w", name, path, err)
	}

	var a Adapter
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// 禁用未知字段：拼错 `prority` 这类错字必须当场报错，而不是安静地按默认值生效。
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a); err != nil {
		return nil, fmt.Errorf("适配器声明解析失败 %s：%w", path, err)
	}
	if err := a.validate(dir, name); err != nil {
		return nil, err
	}
	return &a, nil
}

// checkEntries 执行契约 A-103：目录里只允许出现约定的文件名。
func checkEntries(dir, name string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("适配器 %s 目录无法读取：%w", dir, err)
	}
	for _, entry := range entries {
		if !allowedEntries[entry.Name()] {
			return fmt.Errorf("适配器 %s 目录里出现了不允许的条目 %s（[14 §3] A-103 只允许 %s、README.md、extension.js、fixtures/）",
				name, filepath.Join(dir, entry.Name()), FileName)
		}
	}
	return nil
}

// validate 校验一份声明。dir 是完整目录路径，dirName 是它的名字（id 必须等于它）。
func (a *Adapter) validate(dir, dirName string) error {
	if a.ID == "" {
		return fmt.Errorf("适配器 %s 的 %s 缺少 id 字段（契约 A-114：id 必须等于目录名）", dirName, FileName)
	}
	if a.ID != dirName {
		return fmt.Errorf("适配器声明 %s 的 id 是 %q，而目录名是 %q——两者必须一致（契约 A-114）",
			filepath.Join(dir, FileName), a.ID, dirName)
	}
	if a.Name == "" {
		return fmt.Errorf("适配器 %s 缺少 name 字段", a.ID)
	}
	if a.Version < 1 {
		return fmt.Errorf("适配器 %s 的 version 必须是正整数，实际 %d", a.ID, a.Version)
	}
	if len(a.Match.Hosts) == 0 {
		return fmt.Errorf("适配器 %s 的 match.hosts 为空——至少要有一条匹配规则", a.ID)
	}
	if a.Match.Priority < 0 {
		return fmt.Errorf("适配器 %s 的 match.priority 不得为负，实际 %d", a.ID, a.Match.Priority)
	}
	for _, host := range a.Match.Hosts {
		if err := checkHostPattern(a.ID, host); err != nil {
			return err
		}
	}
	if a.Identity != nil {
		for _, rule := range a.Identity.URLRules {
			if rule.ID == "" {
				return fmt.Errorf("适配器 %s 的某条 identity.urlRules 缺少 id 取值表达式", a.ID)
			}
			if rule.Path == "" && rule.Query == "" && rule.Pattern == "" {
				return fmt.Errorf("适配器 %s 的某条 identity.urlRules 既没有 path、query，也没有 pattern（[14 §5.4]）", a.ID)
			}
			if rule.Path != "" && rule.Query != "" {
				return fmt.Errorf("适配器 %s 的某条 identity.urlRules 同时声明了 path 与 query（[14 §5.4]）", a.ID)
			}
			// 正则的方言必须是两端都能接受的：桌面端是 Go 的 RE2，扩展端是 JS 的 RegExp。
			// 用不了 lookahead、反向引用这类只有一边支持的东西，所以**在这里编译一次**，
			// 编不过就启动失败——否则会变成"只有浏览器里抓不到"这种看不见的错。
			for _, pattern := range []string{rule.Path, rule.Pattern} {
				if pattern == "" {
					continue
				}
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("适配器 %s 的 identity.urlRules 里正则 %q 无法编译（两端共用方言，[14 §5.4]）：%w",
						a.ID, pattern, err)
				}
			}
		}
	}
	if a.Capture != nil {
		switch a.Capture.PrimarySelection {
		case "", "current-player", "first", "all":
		default:
			return fmt.Errorf("适配器 %s 的 capture.primarySelection 取值 %q 不在 current-player / first / all 之内（[14 §5.1]）",
				a.ID, a.Capture.PrimarySelection)
		}
	}
	if a.Resolve != nil {
		switch a.Resolve.Engine {
		case "", "yt-dlp":
		case "builtin":
			if a.Resolve.Builtin == "" {
				return fmt.Errorf("适配器 %s 的 resolve.engine 是 builtin，但没有写 resolve.builtin（[14 §5]）", a.ID)
			}
		default:
			return fmt.Errorf("适配器 %s 的 resolve.engine 取值 %q 不在 yt-dlp / builtin 之内（[14 §5]）", a.ID, a.Resolve.Engine)
		}
	}
	// L2 的两条硬要求：有代码就必须说明为什么不能用声明表达（A-101），
	// 且代码只能待在这个目录里（A-102，由 A-103 的文件白名单保证）。
	if hasExtension(dir) && (a.CodeReason == nil || strings.TrimSpace(*a.CodeReason) == "") {
		return fmt.Errorf("适配器 %s 带 extension.js，但没有填 codeReason（契约 A-101：能用声明表达的禁止写代码）", a.ID)
	}
	// 契约 A-119：声明必须**写了就起作用**。下面三条都是"写了却不起作用"的形态，
	// 它们不会报错、只会静默失效，正是最难查的那种问题。
	for _, rule := range a.Errors {
		if strings.TrimSpace(rule.Code) == "" || strings.TrimSpace(rule.Message) == "" {
			return fmt.Errorf("适配器 %s 的 errors 里有一条缺少 code 或 message（契约 A-119）：%s",
				a.ID, describeErrorRule(rule))
		}
	}
	if a.Identity != nil && strings.TrimSpace(a.Identity.Canonical) != "" {
		if err := checkCanonicalTemplate(a.ID, a.Identity.Canonical, a.Match.Hosts); err != nil {
			return err
		}
	}
	return nil
}

// describeErrorRule 给出一条错误映射的可读摘要——报错时要说清是哪一条，
// 只写"有一条不合规"会让人把整份声明从头翻一遍。
func describeErrorRule(rule ErrorRule) string {
	match := strings.TrimSpace(rule.Match)
	if len(match) > 40 {
		match = match[:40] + "…"
	}
	if match == "" {
		match = "(没有 match 片段)"
	}
	return fmt.Sprintf("match=%q code=%q message=%q", match, rule.Code, rule.Message)
}

// checkCanonicalTemplate 执行契约 A-119 的后两条：`identity.canonical` 必须真的能替换
// （含 `{id}`），且它的主机名必须落在 `match.hosts` 之内。
//
// 主机名这一条是安全要求，不是洁癖：规范化之后的地址会带着**本页凭据**去请求
// （[05 §4.2] 的 B-304，`internal/media/page.go` 的 `scopedMediaHeaders`），
// 跨站规范化等于把用户的 Cookie 送到适配器自己都没声明的站点——而适配器是公开内容，
// 一份写错的声明不该有这个能力。
func checkCanonicalTemplate(id, canonical string, hosts []string) error {
	template := strings.TrimSpace(canonical)
	if !strings.Contains(template, "{id}") {
		return fmt.Errorf("适配器 %s 的 identity.canonical 缺少 {id} 占位符（契约 A-119）：%q 无法替换出规范地址",
			id, template)
	}
	probe, err := url.Parse(strings.ReplaceAll(template, "{id}", "1"))
	if err != nil {
		return fmt.Errorf("适配器 %s 的 identity.canonical 不是合法地址（契约 A-119）：%w", id, err)
	}
	if probe.Scheme != "http" && probe.Scheme != "https" {
		return fmt.Errorf("适配器 %s 的 identity.canonical 必须以 http:// 或 https:// 开头（契约 A-119）：%q",
			id, template)
	}
	host := normalizeHost(probe.Host)
	if host == "" {
		return fmt.Errorf("适配器 %s 的 identity.canonical 没有主机名（契约 A-119）：%q", id, template)
	}
	for _, pattern := range hosts {
		if _, ok := (&Adapter{Match: Match{Hosts: []string{pattern}}}).matches(host); ok {
			return nil
		}
	}
	return fmt.Errorf("适配器 %s 的 identity.canonical 主机 %q 不在 match.hosts %v 之内（契约 A-119）——"+
		"规范化会把本页凭据带到适配器未声明的站点（[05 §4.2] B-304）", id, host, hosts)
}

func hasExtension(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "extension.js"))
	return err == nil
}

// checkHostPattern 校验一条主机规则：不支持协议、路径与端口，只支持 `*`、`*.` 前缀通配。
func checkHostPattern(id, host string) error {
	trimmed := strings.TrimSpace(host)
	switch {
	case trimmed == "":
		return fmt.Errorf("适配器 %s 的 match.hosts 里有空规则", id)
	case trimmed == "*":
		return nil
	case strings.HasPrefix(trimmed, "*."):
		if strings.ContainsAny(trimmed[2:], "*/:") {
			return fmt.Errorf("适配器 %s 的主机规则 %q 不合法：只支持 *.example.com 这一种通配（[14 §5]）", id, host)
		}
		return nil
	case strings.ContainsAny(trimmed, "*/:"):
		return fmt.Errorf("适配器 %s 的主机规则 %q 不合法：不得包含协议、路径或端口（[14 §5]）", id, host)
	default:
		return nil
	}
}

// checkHostConflicts 执行契约 A-115：同一域名被两个适配器以相同优先级声明时启动失败。
//
// 禁止静默取其一——那会让"某站点行为忽然变了"变成无法定位的问题。
func checkHostConflicts(adapters []Adapter) error {
	owner := make(map[string]string)
	for _, a := range adapters {
		for _, host := range a.Match.Hosts {
			key := strings.ToLower(strings.TrimSpace(host)) + "|" + strconv.Itoa(a.Match.Priority)
			if previous, ok := owner[key]; ok && previous != a.ID {
				return fmt.Errorf("适配器 %s 与 %s 以相同优先级 %d 声明了同一个域名 %q（契约 A-115：同一域名不得同优先级重复声明）",
					previous, a.ID, a.Match.Priority, host)
			}
			owner[key] = a.ID
		}
	}
	return nil
}

// All 返回全部适配器（按目录名排序，顺序稳定）。
func (s *Set) All() []Adapter {
	out := make([]Adapter, len(s.adapters))
	copy(out, s.adapters)
	return out
}

// Len 是适配器数量。
func (s *Set) Len() int { return len(s.adapters) }

// ByID 按 id 取一个适配器。
func (s *Set) ByID(id string) (*Adapter, bool) {
	for i := range s.adapters {
		if s.adapters[i].ID == id {
			return &s.adapters[i], true
		}
	}
	return nil, false
}
