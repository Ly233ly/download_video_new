package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/idna"

	"github.com/Ly233ly/download_video_new/internal/store"
)

// 创建期校验（[05 §3.2]）。
//
// [05 §3.2] 的十条校验**必须拒绝且不落库**——所以它们全部发生在 InsertPlan 之前，
// 任何一条不过就直接返回错误码，数据库里不会留下半成品记录。
//
// 本文件只做**输入合法性校验**，目标是防止程序自身出错，不构成权限或身份体系（[03 §4]）。

// HostResolver 把主机名解析为 IP，供 [03 §4.3] 的"本机/内网目标"判定使用。
//
// 它是可注入的依赖：测试不得依赖外网 DNS（[12 §5.2] 的 T3），
// 生产装配用 net.DefaultResolver 即可。
type HostResolver func(ctx context.Context, host string) ([]net.IP, error)

// planSource 是计划的**内存**下载上下文：媒体地址与会话凭据。
//
// [03 §6] 与 B-722 规定：媒体 URL、Cookie、Authorization、签名参数与解密键
// **只能驻留内存**，不得写入日志、诊断或数据库。进程退出即消失——
// 重启后的处置见 [05 §9]（store.RecoverInterrupted）。
type planSource struct {
	// mediaURL 是归一化后的媒体地址，可能带一次性签名参数——正因如此才不落盘。
	mediaURL string
	// headers 是会话上下文（Cookie / Authorization 等），只用于本次下载（[05 §4.2] 的凭据边界）。
	headers map[string]string
	// tracks 是**分离轨道**的地址（P3，[05 §4.6.2]）。
	//
	// 与 mediaURL 同理只在内存：轨道地址常带一次性签名参数。落库的
	// `stream_plan` 投影里没有地址（[03 §2.1.1]），重启后取不回它们——
	// 这正是 [05 §9] 规定"重启后只有能重新解析的路径才继续调度"的原因。
	tracks []StreamTrack
}

// StreamTrack 是 [03 §2.1] 的 stream_plan 元素：描述所选轨道与质量档位。
//
// **不得包含签名 URL 或解密键**——store 层会在写入前机械拒绝（见 store.validateStreamPlan）。
type StreamTrack struct {
	// Track 是轨道角色（[03 §2.1.1]）：`main` / `video` / `audio`。
	Track string `json:"track"`
	// Kind 是流类型：`video` / `audio` / `subtitle`。
	Kind string `json:"kind"`
	// Quality 是可读质量标签。
	Quality string `json:"quality,omitempty"`
	// Container 是该轨的容器（[03 §2.1.1]）。
	Container string `json:"container,omitempty"`
	// URL 是媒体地址，**只在内存**：不落库、不进日志（B-722）。
	// 投影进 `stream_plan` 时被显式丢弃（[03 §2.1.1]），store 层会机械拒绝含它的写入。
	URL string `json:"-"`
	// Index 与 Bitrate 是**阶段 3 的补齐字段**——[03 §2.1.1] 要求补齐必须向后兼容：
	// 新增字段一律可缺省、读方必须容忍缺省。阶段 2 只做直链，不填它们。
	Index   int `json:"index,omitempty"`
	Bitrate int `json:"bitrate,omitempty"`
	// Bytes 是该轨**已声明的**字节数（[04 §3.3.3] 的 `streams[].bytes`）；0 表示未声明。
	//
	// **只在内存**：落库投影里没有它。P3 用它判断"已完整取回的轨不重下"
	// （[05 §4.6.2]）——未声明就必须重下，因为"无法确认时重下"。
	Bytes int64 `json:"-"`
}

// WechatContext 是视频号专用上下文（[05 §3.1]：会话上下文**仅内存**）。
//
// String 被刻意遮蔽：这些字段一旦被 `%v` 打进日志就是 B-722 的违规。
type WechatContext struct {
	// Spec 是画质档位：`original` 或明确转码档（如 `xWT111`，见 [06 §6.1]）。
	Spec string
	// DecodeKey 是解密键。**永不落盘、永不进日志**（[06 §8]、B-722）。
	DecodeKey string
	// DeclaredSize 是 feed 声明的字节数，用于完成后的字节比对（[06 §6.3]）。
	DeclaredSize int64
	// SessionID 标识签发该地址的捕获会话，用于判断上下文能否重建（[05 §9]）。
	SessionID string
}

// String 遮蔽全部字段：视频号上下文含解密键，任何形式的字符串化都不得泄露。
func (w *WechatContext) String() string { return "[视频号上下文已隐藏]" }

// CreatePlanRequest 是创建下载计划的入参（[05 §3.1]）。
type CreatePlanRequest struct {
	// URL 是媒体地址或页面地址，按 MediaKind 解释。
	// **它只进内存注册表**，落库的 source_url 是它的归一化且去敏感版本（[03 §2.1]、[03 §6]）。
	URL string
	// PageURL 是可选的来源页面地址（扩展上报的页面 URL）。
	// 提供时 source_url 落它——[03 §2.1] 规定 source_url 是"归一化后的页面地址"。
	PageURL string

	MediaKind string
	// SourceTitle 是展示用标题，上限 500 字符（[03 §4.5]）。
	SourceTitle string
	// OutputName 是输出名，按 [03 §4.4] 清洗。
	OutputName      string
	OutputContainer string
	MergeMode       string
	// QualityLabel 是可读画质档位；视频号专用（[05 §3.1]）。
	QualityLabel string
	// Tracks 是轨道选择。[05 §3.1] 要求"至少一个可用媒体流"：
	// nil 表示由下载引擎自行解析；显式给空数组表示"没有可用媒体流"，按 missing_media 拒绝。
	Tracks []StreamTrack

	ImportToEagle     bool
	DeleteAfterImport bool

	// Headers 是会话上下文（Cookie / Authorization 等），**仅内存**（[05 §3.1]、B-303）。
	Headers map[string]string
	// Wechat 是视频号专用上下文，**仅内存**（[05 §3.1]）。
	Wechat *WechatContext
}

// String 遮蔽会话凭据：CreatePlanRequest 会被日志与错误路径碰到，
// 而 Headers 里可能有 Cookie、URL 里可能有签名参数（[12 §4.3] 的 C4）。
func (r CreatePlanRequest) String() string {
	return fmt.Sprintf("CreatePlanRequest{kind:%s, container:%s, merge:%s, tracks:%d}",
		r.MediaKind, r.OutputContainer, r.MergeMode, len(r.Tracks))
}

// planDraft 是通过校验后的落库草稿与内存上下文。
type planDraft struct {
	plan   store.Plan
	source planSource
}

// 页面 URL 上限见 [03 §4.5]（2048 字符）。
const maxTitleLen = 500 // [03 §4.5]：标题 500 字符

var (
	// 视频号明确转码档的形状：`xWT` + 三位数字（[06 §6.1] 的样本为 xWT111 ~ xWT128）。
	wechatSpecPattern = regexp.MustCompile(`^xWT\d{3}$`)
	// 固定字节分片的时间片段标记，如 `#t=12.5,30`（[05 §3.2] 的 fixed_range_fragment）。
	timeFragmentPattern = regexp.MustCompile(`^t=\d+(\.\d+)?(,\d+(\.\d+)?)?$`)
	// 固定字节分片的范围参数，如 `?range=0-1023`。
	byteRangePattern = regexp.MustCompile(`^\d*-\d*$`)
)

// dropQueryParams 是 [03 §4.1] 第 6 条要求丢弃的追踪参数。
var dropQueryParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true,
	"mc_cid": true, "mc_eid": true, "igshid": true, "yclid": true,
	"_ga": true, "_gl": true,
}

// secretQueryParams 是 [03 §6] 列出的签名类参数：**不得落盘**（B-722）。
// 它们可以留在内存地址里（下载需要），但落库的 source_url 必须去掉。
var secretQueryParams = map[string]bool{
	"sign": true, "token": true, "encfilekey": true,
	"hy": true, "idx": true, "basedata": true,
}

// validateCreatePlan 执行 [05 §3.2] 的全部创建期校验，通过后返回落库草稿。
//
// 校验顺序刻意从"便宜且确定"到"需要 IO"：格式 → 取值域 → 输出名 → 轨道 → 视频号 → DNS。
// 这样绝大多数非法请求不会触发 DNS 查询（[04 §1.1] 的 G1：跨边界调用要有超时，更要少发生）。
func (s *Service) validateCreatePlan(ctx context.Context, req CreatePlanRequest) (planDraft, error) {
	mediaKind, err := s.resolveMediaKind(ctx, req)
	if err != nil {
		return planDraft{}, err
	}
	if !validMediaKind(mediaKind) {
		return planDraft{}, newError(CodeInvalidURL, fmt.Errorf("非法媒体类型 %q", mediaKind))
	}

	target, err := normalizeTargetURL(req.URL)
	if err != nil {
		return planDraft{}, err
	}
	if err := s.checkDownloadTarget(ctx, target.Hostname()); err != nil {
		return planDraft{}, err
	}

	if !validMergeMode(req.MergeMode) {
		return planDraft{}, newError(CodeInvalidMergeMode, fmt.Errorf("非法合并方式 %q", req.MergeMode))
	}
	if !validContainer(req.OutputContainer) {
		return planDraft{}, newError(CodeInvalidContainer, fmt.Errorf("非法输出容器 %q", req.OutputContainer))
	}
	if err := validateTracks(req); err != nil {
		return planDraft{}, err
	}
	if err := validateWechatRequest(req, mediaKind); err != nil {
		return planDraft{}, err
	}

	outputName := sanitizeOutputName(req.OutputName, "")
	streamPlan, err := buildStreamPlan(req.Tracks)
	if err != nil {
		return planDraft{}, err
	}

	sourceURL, err := sourceURLForStorage(req, target)
	if err != nil {
		return planDraft{}, err
	}

	plan := store.Plan{
		SourceURL:       sourceURL,
		SourceTitle:     truncateRunes(strings.TrimSpace(req.SourceTitle), maxTitleLen),
		MediaKind:       mediaKind,
		OutputName:      outputName,
		OutputContainer: req.OutputContainer,
		MergeMode:       req.MergeMode,
		QualityLabel:    strings.TrimSpace(req.QualityLabel),
		StreamPlan:      streamPlan,
		// [05 §3.1]：delete_after_import = 1 只在 import_to_eagle = 1 时有意义。
		ImportToEagle:     req.ImportToEagle,
		DeleteAfterImport: req.ImportToEagle && req.DeleteAfterImport,
		Status:            store.PlanStatusQueued,
	}

	return planDraft{
		plan: plan,
		source: planSource{
			mediaURL: target.String(),
			headers:  cloneHeaders(req.Headers),
			tracks:   cloneTracks(req.Tracks),
		},
	}, nil
}

// resolveMediaKind 处理浏览器下载模式对媒体类型的影响（[04 §2.5]、B-220/B-221）。
//
// 模式在**创建计划时**决定并写入计划记录，运行中改开关不影响已创建的计划；
// 视频号不适用该模式（B-226），保持原类型。
func (s *Service) resolveMediaKind(ctx context.Context, req CreatePlanRequest) (string, error) {
	kind := strings.TrimSpace(req.MediaKind)
	if kind != "" && kind != store.PlanMediaBrowser {
		// 显式指定了非 browser 的类型：只有视频号能豁免模式改写。
		if kind == store.PlanMediaWechat {
			return kind, nil
		}
	}
	mode, err := s.settingInt(ctx, settingBrowserDownloadMode, 0)
	if err != nil {
		return "", err
	}
	if mode != 0 && kind != store.PlanMediaWechat {
		return store.PlanMediaBrowser, nil
	}
	return kind, nil
}

// normalizeTargetURL 按 [03 §4.1] 归一化媒体地址。
func normalizeTargetURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, newError(CodeInvalidURL, nil)
	}
	if len(trimmed) > store.MaxSourceURLLen {
		return nil, newError(CodeInvalidURL, fmt.Errorf("地址超过 %d 字符", store.MaxSourceURLLen))
	}
	// blob: 单独判：它没有 hostname，走通用规则会被归为"格式非法"，
	// 而 [05 §3.2] 要求给它自己的码（B-205：浏览器内部地址不可提交）。
	if strings.HasPrefix(strings.ToLower(trimmed), "blob:") {
		return nil, newError(CodeBlobNotDownloadable, nil)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, newError(CodeInvalidURL, err)
	}

	// 固定字节分片必须在**丢弃 fragment 之前**判定（[05 §3.2] 的 fixed_range_fragment），
	// 否则 `#t=10,20` 这类标记会先被 [03 §4.1] 第 7 条抹掉，永远判不出来。
	if looksLikeFixedRangeFragment(parsed) {
		return nil, newError(CodeFixedRangeFragment, nil)
	}

	// 1. scheme 必须是 http / https。
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, newError(CodeInvalidURL, fmt.Errorf("scheme %q 不受支持", scheme))
	}
	// 3. 禁止 username / password。
	if parsed.User != nil {
		return nil, newError(CodeInvalidURL, fmt.Errorf("地址不得包含用户名或密码"))
	}
	// 2. 必须有 hostname。
	host := parsed.Hostname()
	if host == "" {
		return nil, newError(CodeInvalidURL, fmt.Errorf("地址缺少主机名"))
	}
	// 4. 主机名转小写、去末尾点、IDNA 转 ASCII。
	asciiHost, err := normalizeHostname(host)
	if err != nil {
		return nil, newError(CodeInvalidURL, err)
	}
	// 5. 默认端口必须省略。
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}

	parsed.Scheme = scheme
	parsed.Host = hostPort(asciiHost, port)
	// 6. 丢弃全部追踪参数（保留签名参数——下载要用）。
	parsed.RawQuery = stripTrackingParams(parsed.RawQuery)
	// 3. 禁止 username / password（前面已判，这里再清一遍以免下游用到）。
	parsed.User = nil
	// 7. 丢弃 fragment。固定字节分片的判定已在丢弃**之前**完成（见 validateCreatePlan）。
	parsed.Fragment = ""
	parsed.RawFragment = ""
	// 8. path 为空时取 "/"。
	if parsed.Path == "" && parsed.Opaque == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

// normalizeHostname 执行 [03 §4.1] 第 4 条与 [03 §4.2] 的域名归一化（构造播出站地址时）。
func normalizeHostname(host string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if trimmed == "" {
		return "", fmt.Errorf("主机名为空")
	}
	// 合法 IP 字面量原样返回（[03 §4.2] 第 5 条）。
	if ip := net.ParseIP(trimmed); ip != nil {
		return trimmed, nil
	}
	ascii, err := idnaASCII(trimmed)
	if err != nil {
		return "", err
	}
	if err := validateHostLabels(ascii); err != nil {
		return "", err
	}
	return ascii, nil
}

// validateHostLabels 落实 [03 §4.2] 的第 7、8 条：任一 label 为空、超长或以 `-` 开头/结尾都拒绝。
func validateHostLabels(host string) error {
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return fmt.Errorf("域名含空标签")
		}
		if len(label) > 63 {
			return fmt.Errorf("域名标签超过 63 字符")
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("域名标签不得以连字符开头或结尾")
		}
	}
	return nil
}

// stripTrackingParams 丢弃 [03 §4.1] 第 6 条列出的追踪参数（含全部 `utm_*`），
// 保留其余参数——媒体地址的签名参数必须原样带下去，否则下载会被拒。
func stripTrackingParams(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		// 解析不了的 query 原样保留：**不因为清理追踪参数而破坏可用的地址**。
		return rawQuery
	}
	for key := range values {
		lower := strings.ToLower(key)
		if dropQueryParams[lower] || strings.HasPrefix(lower, "utm_") {
			values.Del(key)
		}
	}
	return values.Encode()
}

// sourceURLForStorage 产出落库的 source_url。
//
// [03 §2.1]：它是"归一化后的页面地址"；[03 §6] 又规定签名查询参数不得落盘。
// 因此优先级是：显式页面地址 → 媒体地址去掉敏感参数后的形式。
func sourceURLForStorage(req CreatePlanRequest, target *url.URL) (string, error) {
	if strings.TrimSpace(req.PageURL) != "" {
		page, err := normalizeTargetURL(req.PageURL)
		if err != nil {
			// 页面地址非法**不阻塞创建**：它只是展示与站点规则判定的输入，
			// 真正决定能否下载的是媒体地址，而那条已经在前面校验过了。
			return sanitizeForStorage(target), nil
		}
		return sanitizeForStorage(page), nil
	}
	return sanitizeForStorage(target), nil
}

// sanitizeForStorage 去掉 [03 §6] 明令不得落盘的签名类参数。
func sanitizeForStorage(u *url.URL) string {
	if u == nil {
		return ""
	}
	clone := *u
	values, err := url.ParseQuery(clone.RawQuery)
	if err != nil {
		clone.RawQuery = ""
		return clone.String()
	}
	for key := range values {
		if secretQueryParams[strings.ToLower(key)] {
			values.Del(key)
		}
	}
	clone.RawQuery = values.Encode()
	return clone.String()
}

// looksLikeFixedRangeFragment 判定"固定字节分片"（[05 §3.2] 的 fixed_range_fragment）。
//
// [02 B-205] 把固定字节分片与预加载资源并列为"默认隐藏且不可提交"的候选：它是媒体的
// 一段固定字节范围，不是完整媒体。可机械判定的两种形状：时间片段 fragment 与字节范围参数。
func looksLikeFixedRangeFragment(u *url.URL) bool {
	fragment := strings.ToLower(u.Fragment)
	if timeFragmentPattern.MatchString(fragment) {
		return true
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for _, key := range []string{"range", "bytes"} {
		for _, value := range values[key] {
			if byteRangePattern.MatchString(strings.TrimSpace(value)) {
				return true
			}
		}
	}
	return false
}

// blockedTargetNetworks 是 [03 §4.3] 要求拒绝的地址集合。
//
// **刻意逐条列出而不是用 Go 的 IsGlobalUnicast()**：[03 §4.3] 明确警告后者对
// 10/8、172.16/12、192.168/16 返回 true，照字面实现会漏掉整个内网。
//
// **刻意不含 `::ffff:0:0/96`**：文档要求"IPv4-mapped IPv6 必须先还原为 IPv4 再按上表判定"，
// 而 isBlockedTargetIP 已经用 To4() 做了这次还原。若再把这个网段列进来，Go 的 IPNet.Contains
// 会把**任何** 4 字节 IPv4 都映射成 ::ffff:a.b.c.d 去比较，于是 93.184.216.34 这种正常公网
// 地址也会被判为内网——本实现的第一版就是这么错的，创建期校验的测试当场抓住。
var blockedTargetNetworks = mustParseCIDRs(
	"127.0.0.0/8",        // 回环
	"::1/128",            //
	"10.0.0.0/8",         // 私有
	"172.16.0.0/12",      //
	"192.168.0.0/16",     //
	"fc00::/7",           //
	"169.254.0.0/16",     // 链路本地
	"fe80::/10",          //
	"100.64.0.0/10",      // 运营商级 NAT
	"0.0.0.0/32",         // 未指定 / 广播 / 组播
	"::/128",             //
	"255.255.255.255/32", //
	"224.0.0.0/4",        //
	"ff00::/8",           //
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			// 常量写错属于编程错误，启动期直接失败（[12 §3.2]）。
			panic("非法的拒绝网段常量: " + cidr)
		}
		networks = append(networks, network)
	}
	return networks
}

// isBlockedTargetIP 判定单个 IP 是否属于 [03 §4.3] 的拒绝集合。
func isBlockedTargetIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 必须先还原为 IPv4，否则 ::ffff:192.168.1.1 会绕过判定（[03 §4.3]）。
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, network := range blockedTargetNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// checkDownloadTarget 落实 [03 §4.3]：拒绝本机与内网目标。
//
// 判定只针对**提交的那一条 URL 的主机**。这是防"程序被页面误导去访问本机服务"的 bug，
// 不是完整的 SSRF 防护体系（[03 §4.3] 的原话）。
func (s *Service) checkDownloadTarget(ctx context.Context, host string) error {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || lower == "localhost.localdomain" {
		return newError(CodeBlockedLocalTarget, nil)
	}
	if ip := net.ParseIP(lower); ip != nil {
		if isBlockedTargetIP(ip) {
			return newError(CodeBlockedLocalTarget, nil)
		}
		return nil
	}

	// DNS 解析失败或超时 → **拒绝**，不放行（[03 §4.3] 的处置表）。
	lookupCtx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()

	addrs, err := s.plan.resolve(lookupCtx, lower)
	if err != nil {
		return newError(CodeInvalidURL, fmt.Errorf("解析主机名失败: %w", err))
	}
	if len(addrs) == 0 {
		return newError(CodeInvalidURL, fmt.Errorf("主机名未解析出地址"))
	}
	// 解析出多个地址时，**任一**命中拒绝集合即拒绝。
	for _, ip := range addrs {
		if isBlockedTargetIP(ip) {
			return newError(CodeBlockedLocalTarget, nil)
		}
	}
	return nil
}

// validateTracks 落实 [05 §3.1] 的"至少一个可用媒体流"（missing_media）。
func validateTracks(req CreatePlanRequest) error {
	if req.Tracks == nil {
		// nil 表示"交给下载引擎自行解析"（阶段 2 的直链就是这种情况）。
		return nil
	}
	if len(req.Tracks) == 0 {
		return newError(CodeMissingMedia, nil)
	}
	if req.MergeMode == store.PlanMergeAV && len(req.Tracks) < 2 {
		// 双轨合并至少要两条轨道，否则合不出来（[05 §4] 的 P3）。
		return newError(CodeMissingMedia, nil)
	}
	return nil
}

// validateWechatRequest 落实视频号的三条创建期校验（[05 §3.2]）。
//
// 这里只校验**结构与存在性**：真正的解密与分辨率判定在阶段 5 落地（[06 §8]）。
// 但"没有解密键就必然拿不到可交付文件"是确定的，所以必须在创建期拦住。
func validateWechatRequest(req CreatePlanRequest, mediaKind string) error {
	if mediaKind != store.PlanMediaWechat {
		return nil
	}
	label := strings.TrimSpace(req.QualityLabel)
	if label != "original" && !wechatSpecPattern.MatchString(label) {
		return newError(CodeWechatSpecInvalid, nil)
	}
	// [06 §8.1]：视频号媒体是加密 MP4，没有解密信息就下不出可交付的文件。
	if req.Wechat == nil || strings.TrimSpace(req.Wechat.DecodeKey) == "" {
		return newError(CodeWechatKeyInvalid, nil)
	}
	// [06 §6.3]：原画必须由"URL + decode_key + feed 声明大小同响应配对"证明（B-715）。
	// 缺任何一项都无法验证，因此不允许声明式原画（[06 §6.6] 的教训 1）。
	if label == "original" && (req.Wechat.DeclaredSize <= 0 || strings.TrimSpace(req.Wechat.SessionID) == "") {
		return newError(CodeWechatOriginalUnverifiable, nil)
	}
	return nil
}

// sanitizeOutputName 按 [03 §4.4] 清洗输出名。
//
// 五条规则依次是：去非法字符 → 去首尾空白与点号 → 避开 Windows 保留名 →
// 截到 150 字符 → 为空则回退。fallback 由调用方给（计划 ID 或时间戳）。
func sanitizeOutputName(raw, fallback string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1 // 控制字符
		}
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return -1
		}
		return r
	}, raw)

	cleaned = strings.Trim(cleaned, " \t.")
	if cleaned == "" {
		cleaned = strings.TrimSpace(fallback)
	}
	if cleaned == "" {
		return ""
	}
	// Windows 保留名不分大小写；前缀下划线是**最小惊讶**的处置——
	// 直接拒绝会让一个合法标题（例如 "CON"）永远无法下载。
	if windowsReservedNames[strings.ToUpper(cleaned)] {
		cleaned = "_" + cleaned
	}
	return truncateRunes(cleaned, store.MaxOutputNameLen)
}

// windowsReservedNames 是 [03 §4.4] 第 3 条列出的保留名。
var windowsReservedNames = func() map[string]bool {
	names := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
	for i := 1; i <= 9; i++ {
		names[fmt.Sprintf("COM%d", i)] = true
		names[fmt.Sprintf("LPT%d", i)] = true
	}
	return names
}()

// buildStreamPlan 把轨道选择序列化成 [03 §2.1] 要求的 JSON 数组。
//
// 结构里**只有轨道描述**：没有 URL、没有解密键（store 层还会再机械校验一次）。
func buildStreamPlan(tracks []StreamTrack) (string, error) {
	if len(tracks) == 0 {
		return "[]", nil
	}
	encoded, err := jsonMarshal(tracks)
	if err != nil {
		return "", newError(CodeInternal, err)
	}
	return encoded, nil
}

// cloneHeaders 复制会话上下文：调用方之后修改原 map 不得影响已创建的计划。
func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}

// cloneTracks 复制轨道选择：调用方之后改原切片不得影响已创建的计划。
//
// 这里的 URL 与会话凭据同级敏感（B-722），所以是**复制**而不是共享底层数组。
func cloneTracks(tracks []StreamTrack) []StreamTrack {
	if len(tracks) == 0 {
		return nil
	}
	cloned := make([]StreamTrack, len(tracks))
	copy(cloned, tracks)
	return cloned
}

func validMediaKind(kind string) bool {
	switch kind {
	case store.PlanMediaDirect, store.PlanMediaHLS, store.PlanMediaDASH,
		store.PlanMediaPage, store.PlanMediaWechat, store.PlanMediaBrowser:
		return true
	default:
		return false
	}
}

func validContainer(container string) bool {
	switch container {
	case store.PlanContainerMP4, store.PlanContainerMKV, store.PlanContainerWebM,
		store.PlanContainerM4A, store.PlanContainerMP3, store.PlanContainerTS:
		return true
	default:
		return false
	}
}

func validMergeMode(mode string) bool {
	switch mode {
	case store.PlanMergeSingle, store.PlanMergeAV, store.PlanMergeSubtitles:
		return true
	default:
		return false
	}
}

func hostPort(host, port string) string {
	// IPv6 字面量在 URL 的 Host 里**必须带方括号**，否则 url.Hostname() 会把最后一段
	// 当作端口解析（"::1" 会变成 host=":"、port="1"），后续的内网判定就全部失效。
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" {
		return host
	}
	return host + ":" + port
}

// idnaASCII 把主机名按 IDNA 编码为 ASCII（[03 §4.1] 第 4 条、[03 §4.2] 第 6 条）。
//
// 用 idna.Lookup 而不是 idna.Strict：输入来自浏览器页面的候选地址，
// 需要的是"按浏览器同样的规则理解它"，而不是按注册局的严格规则拒绝它。
func idnaASCII(host string) (string, error) {
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("域名无法编码为 ASCII: %w", err)
	}
	if ascii == "" {
		return "", fmt.Errorf("域名为空")
	}
	return ascii, nil
}

// truncateRunes 按字符截断，避免把中文截成半个字（[03 §4.5] 的长度上限按字符计）。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// jsonMarshal / jsonUnmarshal 只服务于 stream_plan 的编解码；
// 独立成函数便于替换实现而不影响调用点。
func jsonMarshal(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func jsonUnmarshal(raw string, dst any) error {
	return json.Unmarshal([]byte(raw), dst)
}

// defaultHostResolver 是生产环境的解析器（[03 §4.3] 的主机名判定）。
// 测试必须注入假的解析器，不得依赖外网 DNS（[12 §5.2] 的 T3）。
func defaultHostResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}
