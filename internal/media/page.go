package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// [05 §4.2] P4 页面解析：用固定版本的 yt-dlp（+ Deno 解站点脚本挑战）把页面
// 解析成**输入形态**——一条直链、一份清单，或两条各自独立的轨道——
// 之后复用 P1 / P2 / P3 的取字节实现。
//
// 本文件刻意**不做**的两件事：
//
//  1. **不解析媒体地址**。那是 yt-dlp 的职责（[14 §1.1]：能只靠一个 URL 完成的
//     判断就交给它）。本文件只做"在它的输出里挑一条"和"把凭据用对地方"。
//  2. **不把页面的凭据交给媒体 CDN**（B-304）。跨主机时只带 yt-dlp 自己给出的
//     请求头（Referer / Origin / User-Agent 之类），页面 Cookie 一律留在解析阶段。

// pageResolveOutputLimit 是 yt-dlp `-J` 输出的上限。
//
// 刻意**远大于** [DefaultOutputLimit]：`-J` 吐的是整份 info JSON
// （formats + thumbnails + 章节，实测数百 KB），用 256 KB 的默认上限会把它截断，
// 而截断的 JSON 解析必然失败——那会把"解析成功但输出太大"误报成解析失败。
const pageResolveOutputLimit = 8 << 20

// resolveConfigName 是传给 yt-dlp 的受限配置文件名（[05 §4.2] 的"经受限临时文件传递"）。
//
// 为什么走配置文件而不是命令行：同一行规定"**禁止**放命令行"，
// 而 yt-dlp 读请求头的唯一入口是 `--add-headers`。配置文件既满足"不放命令行"，
// 又比 stdin 多一层好处——它是**文件**，可以在用完时删掉（stdin 的内容进了进程就没了踪迹）。
const resolveConfigName = "yt-dlp.conf"

// deniedHeaderChars 是请求头里**必须拒绝**的字符：换行会让一个头变成两个头
// （HTTP 头注入），而这份内容会被写进交给 yt-dlp 的文件。
const deniedHeaderChars = "\r\n"

// pageChoice 是解析出来的**一条**可下载来源。
type pageChoice struct {
	kind    Kind
	url     string
	headers http.Header
	total   int64
	// track 是这条来源在 [Request.Tracks] 里的角色（`video` / `audio`）。
	track string
}

// pageResolution 是 P4 的解析结论，形状与 [Request] 的三个输入形态一一对应。
type pageResolution struct {
	kind    Kind
	url     string
	headers http.Header
	tracks  []Track
	total   int64
}

// executePage 是 [05 §4.2] 的 P4：解析页面 → 按解析出的形态复用 P1/P2/P3。
func (d *Downloader) executePage(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress),
) (Result, error) {
	if strings.TrimSpace(d.ytdlpPath) == "" {
		return Result{}, ErrorOf(CodePageResolveFailed, errors.New("未找到 yt-dlp，无法解析页面"))
	}

	resolution, err := d.resolvePage(ctx, req, dirs)
	if err != nil {
		return Result{}, err
	}

	next := req
	next.URL = resolution.url
	next.Headers = resolution.headers
	next.Tracks = resolution.tracks
	next.Kind = resolution.kind
	next.TotalBytes = resolution.total
	// 解析出来的地址**不再改路**：它已经是 yt-dlp 的结论，再"降级"只会在
	// page → page 之间打转（[05 §4.0] 禁止链式降级）。
	next.sniffDisabled = true

	switch {
	case len(next.Tracks) >= 2:
		return d.executeTracks(ctx, next, dirs, onProgress)
	case next.kind() == KindHLS || next.kind() == KindDASH:
		return d.executeManifest(ctx, next, dirs, onProgress)
	default:
		return d.execute(ctx, next, dirs, onProgress)
	}
}

// resolvePage 跑一次 yt-dlp 并把它的输出变成 [pageResolution]。
func (d *Downloader) resolvePage(
	ctx context.Context, req Request, dirs workDirs,
) (pageResolution, error) {
	// **先规范化地址，再启动解析工具**（[14 §10] 的 M12）：站点把同一个视频表达成
	// 多种链接形态（抖音的 `?modal_id=` 弹层地址就是一例），而解析器只认其中一种。
	// 规范化之后的地址才是交给 yt-dlp 的地址。
	pageURL := d.canonicalPageURL(strings.TrimSpace(req.URL))
	if pageURL == "" {
		return pageResolution{}, ErrorOf(CodePageResolveFailed, errors.New("缺少页面地址"))
	}

	info, err := d.fetchPageInfo(ctx, req, dirs, pageURL)
	if err != nil {
		return pageResolution{}, err
	}

	choices, err := selectPageChoices(info, req.Container, req.QualityLabel)
	if err != nil {
		return pageResolution{}, err
	}

	for i := range choices {
		choices[i].headers = scopedMediaHeaders(choices[i].headers, pageURL, choices[i].url, req.Headers)
	}

	first := choices[0]
	resolution := pageResolution{kind: first.kind, url: first.url, headers: first.headers}
	if len(choices) == 1 {
		resolution.total = first.total
		return resolution, nil
	}

	// 两条独立轨：地址各归各的，凭据也各归各的（[05 §4.6.2] 的 B-304）。
	resolution.tracks = make([]Track, 0, len(choices))
	for _, choice := range choices {
		resolution.tracks = append(resolution.tracks, Track{
			Kind:       choice.track,
			URL:        choice.url,
			Headers:    choice.headers,
			TotalBytes: choice.total,
		})
	}
	return resolution, nil
}

// fetchPageInfo 调用 yt-dlp 取回 info JSON。
//
// 会话凭据经**受限临时文件**传入（[05 §4.2]），文件权限 0600 且用完即删——
// 它含 Cookie，比 info JSON 本身更敏感（B-722）。
func (d *Downloader) fetchPageInfo(
	ctx context.Context, req Request, dirs workDirs, pageURL string,
) (ytdlpInfo, error) {
	args := []string{"-J", "--no-playlist", "--no-warnings"}

	if len(req.Headers) > 0 {
		configPath := filepath.Join(dirs.planDir, resolveConfigName)
		if err := writeResolveConfig(configPath, req.Headers); err != nil {
			return ytdlpInfo{}, ErrorOf(CodePageResolveFailed, err)
		}
		defer removeOwnedFile(configPath)
		// 放在靠前的位置：yt-dlp 按出现顺序解析，靠前可以确保它先于任何
		// 会被配置文件影响的选项生效。
		args = append([]string{"--config-locations", configPath}, args...)
	}

	// Deno 是解站点脚本挑战用的 JS 运行时。**显式给出路径**：
	// 它随程序分发在 media-tools/ 下，不在 PATH 里，靠 yt-dlp 自己找是找不到的。
	if path := strings.TrimSpace(d.denoPath); path != "" {
		args = append(args, "--js-runtimes", "deno:"+path)
	}
	args = append(args, pageURL)

	if ctx.Err() != nil {
		return ytdlpInfo{}, ctx.Err()
	}

	out, err := d.runner.Run(ctx, ToolCall{
		Path:        d.ytdlpPath,
		Args:        args,
		Timeout:     d.toolWait,
		OutputLimit: pageResolveOutputLimit,
	})
	if err != nil {
		if ctx.Err() != nil {
			return ytdlpInfo{}, ctx.Err()
		}
		// 超时与启动失败都归为"解析没成功"：对用户而言两者都是"这次没解析出来，
		// 稍后重试"，而 `page_resolve_failed` 在 [05 §7.1] 里是可重试的。
		return ytdlpInfo{}, ErrorOf(CodePageResolveFailed, err)
	}
	if out.ExitFailure {
		// 站点适配器可以为这条原始输出声明一条更准确的失败码（[05 §4.2] 的
		// 追加条款、[14 §5] 的 `errors`）。stderr 原文**只在内存里过一遍**。
		if siteErr := d.translateResolveError(pageURL, out); siteErr != nil {
			return ytdlpInfo{}, siteErr
		}
		// **刻意不把 yt-dlp 的 stderr 放进 cause**：它经常整条带上媒体地址与
		// 查询串（含签名参数），那是 [12 §3.3] 的 E2/E3 与 B-722 禁止进日志的内容。
		return ytdlpInfo{}, ErrorOf(CodePageResolveFailed,
			fmt.Errorf("页面解析工具退出码 %d", out.ExitCode))
	}
	if out.Truncated {
		return ytdlpInfo{}, ErrorOf(CodePageResolveFailed, errors.New("页面解析输出超出上限"))
	}

	info, err := parsePageInfo(out.Stdout)
	if err != nil {
		return ytdlpInfo{}, ErrorOf(CodePageResolveFailed, err)
	}
	return info, nil
}

// canonicalPageURL 在启动解析工具**之前**把页面地址规范化（[14 §10] 的 M12）。
//
// 三种情况原样返回，它们是同一件事——"没有可用的规范化声明"：
// 未接线（PageHints 为 nil）、适配器没声明 `resolve.canonicalizePageUrl`、
// 或声明给不出结果。**让一个坏声明把好地址吞掉，比不做规范化更糟**，
// 所以这里只接受非空的结果。
func (d *Downloader) canonicalPageURL(pageURL string) string {
	if d.pageHints == nil || pageURL == "" {
		return pageURL
	}
	if canonical := strings.TrimSpace(d.pageHints.CanonicalPageURL(pageURL)); canonical != "" {
		return canonical
	}
	return pageURL
}

// translateResolveError 让站点适配器把解析工具的原始输出（stderr）翻译成它自己
// 声明的失败码（[05 §4.2] 的追加条款、[14 §5] 的 `errors`）。
//
// 四条约束：
//   - **原文只在内存里过一遍**：不进日志、不进任何 `Error()`、不进 cause（B-722）；
//   - 只在**工具确实跑完并报了非零退出**时才翻译。超时与启动失败是可重试的
//     临时故障（[05 §7.1]），把它们翻译成声明码会把"重试一下就好"变成
//     "需要人工处理"，那是错的；
//   - 未接线、未命中、或声明给不出码/文案时返回 nil，调用方仍报
//     `page_resolve_failed`。缺文案的声明**不能**顶替这个码：`DownloadError`
//     的消息为空时会回落到通用文案（[05 §7.4]），那等于把声明的信息丢掉；
//   - cause 里只放不含原文的事实（退出码），它是唯一能进日志的部分。
func (d *Downloader) translateResolveError(pageURL string, out ToolOutput) error {
	if d.pageHints == nil {
		return nil
	}
	if out.TimedOut {
		// 超时是临时故障，不是站点状态：即使工具同时报非零退出与超时，
		// 也要留在可重试的 `page_resolve_failed` 上。
		return nil
	}
	code, message, ok := d.pageHints.TranslateResolveError(pageURL, string(out.Stderr))
	if !ok || strings.TrimSpace(code) == "" || strings.TrimSpace(message) == "" {
		return nil
	}
	return &SiteError{
		Code:    Code(code),
		Message: message,
		cause:   fmt.Errorf("页面解析工具退出码 %d", out.ExitCode),
	}
}

// writeResolveConfig 把会话凭据写成 yt-dlp 能读的配置文件。
//
// 里面**只有** `--add-headers`：这份文件是"凭据的唯一出口"，
// 加任何别的选项都会让它悄悄改变解析行为（那属于代码的职责，不属于数据的）。
func writeResolveConfig(path string, headers http.Header) error {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	// 排序只为让同一份输入永远产出同一份文件（测试可复现），与 HTTP 语义无关。
	sort.Strings(keys)

	var buf strings.Builder
	for _, key := range keys {
		value := headers.Get(key)
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key+value, deniedHeaderChars) {
			return errors.New("请求头含无法安全传递的字符")
		}
		// strconv.Quote 产出的是双引号包裹、反斜杠转义的字符串；
		// yt-dlp 的配置文件按 shell 规则切分，两者对这两个字符的处理一致。
		buf.WriteString("--add-headers ")
		buf.WriteString(strconv.Quote(key + ": " + value))
		buf.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		return errors.New("无法写入页面解析所需的临时文件")
	}
	return nil
}

// parsePageInfo 解析 yt-dlp 的 `-J` 输出。
func parsePageInfo(raw []byte) (ytdlpInfo, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ytdlpInfo{}, errors.New("页面解析没有给出结果")
	}
	var info ytdlpInfo
	if err := json.Unmarshal([]byte(trimmed), &info); err != nil {
		return ytdlpInfo{}, errors.New("页面解析的输出无法理解")
	}
	return info, nil
}

// selectPageChoices 按 [05 §5.3] 的同一套规则挑出要下载的来源。
//
// 顺序是固定的：先看要不要视频（由输出容器决定），再按档位/分辨率选视频，
// 最后按"视频是否自带音频"决定是一条轨还是两条。
func selectPageChoices(info ytdlpInfo, container, qualityLabel string) ([]pageChoice, error) {
	formats := pageFormats(info)
	if len(formats) == 0 {
		return nil, ErrorOf(CodePageMediaUnavailable, errors.New("页面里没有可下载的媒体"))
	}

	if wantsAudio(container) && !wantsVideo(container) {
		audio := pickAudioFormat(formats)
		if audio == nil {
			return nil, ErrorOf(CodePageMediaUnavailable, errors.New("页面里没有音频流"))
		}
		return []pageChoice{choiceOf(*audio, trackKindAudio)}, nil
	}

	video, err := pickVideoFormat(formats, qualityLabel)
	if err != nil {
		return nil, err
	}
	if video == nil {
		return nil, ErrorOf(CodePageMediaUnavailable, errors.New("页面里没有视频流"))
	}

	if hasAudioStream(*video) {
		// 自带音频：一条轨就够了，合并那一步不需要发生（[05 §4.6.2] 的前提是"分离"）。
		return []pageChoice{choiceOf(*video, trackKindVideo)}, nil
	}

	audio := pickAudioFormat(formats)
	if audio == nil {
		return nil, ErrorOf(CodePageMediaUnavailable, errors.New("页面里没有音频流"))
	}
	return []pageChoice{
		choiceOf(*video, trackKindVideo),
		choiceOf(*audio, trackKindAudio),
	}, nil
}

// pageFormats 取出可用的格式列表。
//
// 三种形状都要容忍：`-J` 通常给 `formats`；单格式的提取器只给顶层字段；
// 而 `--no-playlist` 被提取器忽略时会给出 `entries`（取第一条，
// 总比拿一个没有格式的播放列表去报"没有媒体"更接近用户的意图）。
func pageFormats(info ytdlpInfo) []ytdlpFormat {
	if len(info.Formats) > 0 {
		return info.Formats
	}
	if len(info.Entries) > 0 {
		return pageFormats(info.Entries[0])
	}
	if strings.TrimSpace(info.URL) != "" {
		return []ytdlpFormat{info.asFormat()}
	}
	return nil
}

// pickVideoFormat 选视频轨。
//
// 规则与 [05 §5.3] 完全一致：声明了档位就按档位匹配（多个取码率最高），
// **匹配不到不退档**；没有声明就取分辨率最高、并列取码率最高。
func pickVideoFormat(formats []ytdlpFormat, qualityLabel string) (*ytdlpFormat, error) {
	candidates := make([]ytdlpFormat, 0, len(formats))
	for _, format := range formats {
		if hasVideoStream(format) {
			candidates = append(candidates, format)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	if height, ok := parseQualityHeight(qualityLabel); ok {
		matched := make([]ytdlpFormat, 0, len(candidates))
		for _, format := range candidates {
			if format.Height == height {
				matched = append(matched, format)
			}
		}
		if len(matched) == 0 {
			// 不退到相近档位、不静默选默认流（[05 §5.3]）。
			return nil, ErrorOf(CodeManifestNoMatchingStream,
				fmt.Errorf("页面里没有 %dp 的内容", height))
		}
		return &matched[bestVideoIndex(matched)], nil
	}

	return &candidates[bestVideoIndex(candidates)], nil
}

// bestVideoIndex 返回"分辨率最高、并列时码率最高"的那条。
//
// 判据是确定的，不是"取第一个"：清单与格式列表的顺序由站点决定，
// 把顺序当默认就是把画质交给运气（[05 §5.3] 的同一理由）。
func bestVideoIndex(formats []ytdlpFormat) int {
	best := 0
	for i := 1; i < len(formats); i++ {
		switch {
		case formats[i].Height != formats[best].Height:
			if formats[i].Height > formats[best].Height {
				best = i
			}
		case formats[i].TBR > formats[best].TBR:
			best = i
		}
	}
	return best
}

// pickAudioFormat 选音频轨：先看码率，再看文件大小。
func pickAudioFormat(formats []ytdlpFormat) *ytdlpFormat {
	best := -1
	for i, format := range formats {
		if !hasAudioStream(format) {
			continue
		}
		if best < 0 || format.ABR > formats[best].ABR ||
			(format.ABR == formats[best].ABR && format.TBR > formats[best].TBR) {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	return &formats[best]
}

// choiceOf 把一条格式变成本次要下载的来源。
func choiceOf(format ytdlpFormat, track string) pageChoice {
	return pageChoice{
		kind:    formatKind(format.Protocol),
		url:     format.URL,
		headers: formatHeaders(format),
		total:   formatBytes(format),
		track:   track,
	}
}

// formatKind 按协议判断这条来源是直链还是清单。
//
// 用协议而不是扩展名：同一个 `https` 地址既可能是 mp4 也可能是分片清单，
// 而 yt-dlp 的 `protocol` 字段正是它自己下载时会走的那条路（[05 §4.0] 第一步）。
func formatKind(protocol string) Kind {
	lowered := strings.ToLower(strings.TrimSpace(protocol))
	switch {
	case strings.HasPrefix(lowered, "m3u8"):
		return KindHLS
	case strings.HasPrefix(lowered, "dash"), lowered == "http_dash_segments":
		return KindDASH
	default:
		return KindDirect
	}
}

// formatHeaders 取 yt-dlp 为这条媒体请求准备的请求头。
func formatHeaders(format ytdlpFormat) http.Header {
	if len(format.HTTPHeaders) == 0 {
		return nil
	}
	headers := make(http.Header, len(format.HTTPHeaders))
	for key, value := range format.HTTPHeaders {
		headers.Set(key, value)
	}
	return headers
}

// formatBytes 取声明字节数；两个字段都没有时返回 0（= 未知，不猜）。
func formatBytes(format ytdlpFormat) int64 {
	if format.Filesize > 0 {
		return format.Filesize
	}
	if format.FilesizeApprox > 0 {
		return format.FilesizeApprox
	}
	return 0
}

func hasVideoStream(format ytdlpFormat) bool {
	codec := strings.TrimSpace(format.VCodec)
	return codec != "" && !strings.EqualFold(codec, "none")
}

func hasAudioStream(format ytdlpFormat) bool {
	codec := strings.TrimSpace(format.ACodec)
	return codec != "" && !strings.EqualFold(codec, "none")
}

// scopedMediaHeaders 决定这条媒体请求能带哪些凭据（B-304、[05 §4.2]）。
//
// 规则只有一条：**媒体地址与页面地址不同主机时，一个页面凭据都不带**。
// 页面凭据（Cookie / Authorization / 任何自定义会话头）是给页面用的，
// 媒体 CDN 需要的是它自己的签名与 Referer——那些已经由 yt-dlp 放在
// `http_headers` 里了（`base`）。同主机时不剥离：那本来就是同一个站点，
// 剥离只会让"解析得到、下载 403"这种最难查的失败出现。
func scopedMediaHeaders(base http.Header, pageURL, mediaURL string, session http.Header) http.Header {
	if len(session) == 0 || !sameHost(pageURL, mediaURL) {
		return base
	}
	if base == nil {
		base = make(http.Header, len(session))
	}
	for key := range session {
		value := session.Get(key)
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key+value, deniedHeaderChars) {
			continue
		}
		base.Set(key, value)
	}
	return base
}

// sameHost 比较两个地址的主机名（不含端口）。
//
// 判据是**主机名**而不是 [sameHostRedirectOnly] 的"源"（scheme + host + port）：
// [05 §4.2] 的措辞是"不同主机"，而重定向那条要防的是"凭据被带到别的端口上的服务"。
// 两处要防的东西不同，所以刻意不共用同一个函数。
func sameHost(left, right string) bool {
	leftHost := hostOf(left)
	if leftHost == "" {
		return false
	}
	return leftHost == hostOf(right)
}

func hostOf(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// ytdlpFormat 是 `-J` 输出里 formats[] 的**用到的字段**。
//
// 只声明用得到的：整份 JSON 有上百个字段，全列出来既没人维护也无法验证。
type ytdlpFormat struct {
	FormatID       string            `json:"format_id"`
	URL            string            `json:"url"`
	Ext            string            `json:"ext"`
	VCodec         string            `json:"vcodec"`
	ACodec         string            `json:"acodec"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	TBR            float64           `json:"tbr"`
	ABR            float64           `json:"abr"`
	Protocol       string            `json:"protocol"`
	Filesize       int64             `json:"filesize"`
	FilesizeApprox int64             `json:"filesize_approx"`
	HTTPHeaders    map[string]string `json:"http_headers"`
}

// ytdlpInfo 是 `-J` 输出的顶层结构。
type ytdlpInfo struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	URL      string            `json:"url"`
	Ext      string            `json:"ext"`
	VCodec   string            `json:"vcodec"`
	ACodec   string            `json:"acodec"`
	Protocol string            `json:"protocol"`
	Formats  []ytdlpFormat     `json:"formats"`
	Entries  []ytdlpInfo       `json:"entries"`
	Duration float64           `json:"duration"`
	Headers  map[string]string `json:"http_headers"`
}

// asFormat 把"只有顶层字段"的单格式输出补成一条格式记录。
func (i ytdlpInfo) asFormat() ytdlpFormat {
	return ytdlpFormat{
		URL:         i.URL,
		Ext:         i.Ext,
		VCodec:      i.VCodec,
		ACodec:      i.ACodec,
		Protocol:    i.Protocol,
		HTTPHeaders: i.Headers,
	}
}
