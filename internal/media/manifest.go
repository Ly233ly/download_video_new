package media

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxManifestBytes 是主清单文本的读取上限。
//
// 取 8 MiB：主清单只列变体与分段索引，常见在几十 KB；给足余量是因为
// 有些站点会把整份分段索引塞进主清单。**超限直接报错而不是截断解析**——
// 截断后"解析成功但少了一半分段"会让 FFmpeg 下出一个缺内容的文件，
// 而那种文件能通过结构校验（[05 §6] 只校验流与容器）。
const maxManifestBytes = 8 << 20

// manifestChoice 是 [05 §5.3] 的"选择结果"：先探测、再选择、最后把选择结果
// **显式**交给 FFmpeg。
type manifestChoice struct {
	// Input 是交给 FFmpeg 的输入地址。
	//
	// HLS 是**选中变体的绝对地址**（相对地址已按主清单地址解析）；
	// DASH 仍是主清单地址——它的选择靠 `-map` 落实（[05 §5.3]）。
	Input string
	// MapArgs 是显式的 `-map` 参数。
	//
	// **必须显式**（[05 §4.6.1]）：不写时 FFmpeg 默认只取"第一个"，
	// 而清单里变体的排列顺序不保证与画质有关。
	MapArgs []string
	// TotalTime 是清单总时长（秒）；0 = 未知。
	//
	// 它只用于 [05 §4.6.3] 的**时长口径**进度。拿不到时进度退回阶段语义，
	// **不得**伪造百分比——所以 0 是一个合法且常见的取值。
	TotalTime float64
}

// executeManifest 是 [05 §4.6.1] 的 P2：FFmpeg 在**同一个子进程**里读清单、
// 取分片、必要时合并、封装成文件。
//
// 因此这里**没有** `merging` 这一步：清单内部的合并不产生新阶段（[05 §2.1]）。
// 末尾那次 `merging` 与 P1 同义——"把已校验的字节原子搬进「已完成」"。
//
// 阶段顺序沿用 P1 的 downloading → validating → merging（理由见 [Downloader.execute]）。
func (d *Downloader) executeManifest(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress),
) (Result, error) {
	if strings.TrimSpace(d.ffmpegPath) == "" {
		return Result{}, d.fail(req, CodeManifestInvalid, errors.New("未找到 FFmpeg，无法下载清单"))
	}
	// [05 §4.2] 的请求头预算：超限立即拒绝，**一个字节都不写**。
	if headerBudgetExceeded(req.Headers) {
		return Result{}, d.fail(req, CodeHeadersTooLarge, errors.New("请求头超出 6 KB 预算"))
	}

	choice, mismatch, err := d.resolveManifest(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if mismatch != nil {
		// 改路信号：尚未写入任何字节（[05 §4.0] 第二步）。
		return Result{}, mismatch
	}

	target := dirs
	target.staging = manifestStagingPath(dirs.planDir, req.Container)

	// 开场一帧零进度：不给它的话，上一轮尝试的旧值会一直挂在界面上
	// （与 P1 的 [Downloader.acquire] 同一条理由）。
	reportProgress(onProgress, Progress{Phase: PhaseDownloading})

	if err := d.runManifestFFmpeg(ctx, req, choice, target, onProgress); err != nil {
		return Result{}, err
	}

	probe, err := d.validate(ctx, req, target, onProgress)
	if err != nil {
		return Result{}, err
	}

	// P2 的 total_bytes 通常是 NULL：FFmpeg 写出多少字节事先不知道，
	// 而 [05 §4.6.3] 明令**不得**用估算值填充。
	reportPhase(onProgress, PhaseMerging, probe.Size, 0)

	delivered, err := deliverFile(req.OutputDir, req.OutputName, target.staging, probe.Size)
	if err != nil {
		return Result{}, err
	}
	return Result{FinalPath: delivered.Path, Bytes: delivered.Bytes, Probe: probe}, nil
}

// manifestStagingPath 给 P2 的产物一个**带正确扩展名**的路径。
//
// 扩展名不是装饰：FFmpeg 靠它推断封装格式。刻意**不传 `-f`**——
// 那需要在本包维护一份"容器名 → FFmpeg 格式名"的对照表（`mkv` 要写
// `matroska`、`m4a` 要写 `ipod`……），而这张表 FFmpeg 自己就有，
// 且它的取值随版本变化。文件名只活在临时目录里，交付时会被换成用户的输出名。
func manifestStagingPath(planDir, container string) string {
	name := normalizeContainer(container)
	if name == "" {
		name = "bin"
	}
	return filepath.Join(planDir, "manifest."+name)
}

// resolveManifest 取主清单、解析它、按 [05 §5.3] 选流，并尽量取得总时长。
//
// 返回的 `*RouteMismatch` 表示"提示说这是清单，但拿回来的不是"——
// 那是 [05 §4.0] 第二步允许的一种改路（改走 P4），由调用方决定要不要降。
// 出现它时**尚未写入任何字节**。
func (d *Downloader) resolveManifest(
	ctx context.Context, req Request,
) (manifestChoice, *RouteMismatch, error) {
	text, contentType, baseURL, err := d.fetchManifest(ctx, req)
	if err != nil {
		return manifestChoice{}, nil, err
	}

	// Content-Type 明说是网页：不必再试解析（[05 §4.0] 第二步第三种情形）。
	if isHTMLContentType(contentType) {
		return manifestChoice{}, newRouteMismatch(KindPage, "内容是一份网页而不是清单"), nil
	}

	variants, parseErr := parseManifestText(text, req.kind(), baseURL)
	if parseErr != nil {
		// 提示的格式解析不了时，看 Content-Type 与文本形态是否指向**另一种**清单。
		// 这是"输入形态"层面的判定，按 [05 §4.0] 第一步**不消耗**降级机会；
		// 与 direct 那条"不嗅探响应体"的纪律不冲突——那次嗅探的代价是把一条能下的
		// 直链推给页面解析，这次只是换一个解析器，选错也无副作用。
		if alt, ok := alternateManifestKind(req.kind(), contentType, text); ok {
			variants, parseErr = parseManifestText(text, alt, baseURL)
		}
	}
	if parseErr != nil {
		// 真解析不了：这是 [05 §4.0] 第二步第三种情形（提示是清单、清单解析不了）。
		// 不报 manifest_invalid——那会让计划直接失败，而用户手里那条页面地址
		// 还有一条路可走（P4）。
		return manifestChoice{}, newRouteMismatch(KindPage, "清单无法解析"), nil
	}

	chosen, ok := selectManifestVariant(variants, req.QualityLabel)
	if !ok {
		// [05 §5.3]：无匹配就报错，**不得**退到相近档位、不得静默选默认流。
		return manifestChoice{}, nil, d.fail(req, CodeManifestNoMatchingStream,
			fmt.Errorf("清单里没有符合档位 %q 的流", req.QualityLabel))
	}

	choice := manifestChoice{Input: chosen.Input, MapArgs: chosen.MapArgs}
	// 总时长只影响进度口径，取不到就退化为阶段语义（[05 §4.6.3] 明确允许）。
	choice.TotalTime = d.probeManifestDuration(ctx, req, chosen.Input)
	return choice, nil, nil
}

// fetchManifest 取回主清单文本与它的最终地址。
//
// 取不回来算**可重试**的下载失败：清单地址抖动与分片抖动是同一类问题
// （[05 §4.6.1]："清单地址失效仍按 §8 计数并最终 failed"）。
func (d *Downloader) fetchManifest(
	ctx context.Context, req Request,
) (text, contentType, finalURL string, err error) {
	httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if reqErr != nil {
		// 刻意不带上原始错误：它的文本含完整 URL（[12 §3.3] 的 E3）。
		return "", "", "", d.fail(req, CodeDownloadFailed, errors.New("构造清单请求失败"))
	}
	for key, values := range req.Headers {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}

	resp, doErr := d.client.Do(httpReq)
	if doErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", "", d.fail(req, CodeDownloadFailed, ctxErr)
		}
		return "", "", "", d.fail(req, CodeDownloadFailed,
			fmt.Errorf("取清单失败: %w", scrubURLError(doErr)))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", d.fail(req, CodeDownloadFailed, statusCause(resp.StatusCode))
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if readErr != nil {
		return "", resp.Header.Get("Content-Type"), "", d.fail(req, CodeDownloadFailed,
			fmt.Errorf("读取清单失败: %w", readErr))
	}
	if len(body) > maxManifestBytes {
		return "", "", "", d.fail(req, CodeManifestInvalid, errors.New("清单文本超出上限"))
	}

	final := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		// 相对地址要按**最终**地址解析：同源重定向（[05 §4.1] 只允许同源）
		// 可能把清单换到另一个目录下。
		final = resp.Request.URL.String()
	}
	return string(body), resp.Header.Get("Content-Type"), final, nil
}

// alternateManifestKind 判断"这份文本其实是另一种清单"。
func alternateManifestKind(hint Kind, contentType, text string) (Kind, bool) {
	if kind, ok := manifestContentTypes[normalizeContentType(contentType)]; ok && kind != hint {
		return kind, true
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "#EXTM3U") && hint != KindHLS {
		return KindHLS, true
	}
	if strings.Contains(trimmed, "<MPD") && hint != KindDASH {
		return KindDASH, true
	}
	return "", false
}

// manifestVariant 是清单里的一条可选流（HLS 的一个变体，或 DASH 的一个 Representation）。
type manifestVariant struct {
	Width     int
	Height    int
	Bandwidth int
	// Input 与 MapArgs 是"选中它时交给 FFmpeg 的东西"，在解析阶段就算好，
	// 免得选择逻辑里再掺一次格式判断。
	Input   string
	MapArgs []string
}

// hlsStreamInf 匹配 `#EXT-X-STREAM-INF:` 行。
var hlsStreamInf = regexp.MustCompile(`(?i)^#EXT-X-STREAM-INF:`)

// hlsResolution 匹配 `RESOLUTION=1920x1080`。
var hlsResolution = regexp.MustCompile(`(?i)RESOLUTION=(\d+)x(\d+)`)

// hlsBandwidth 匹配 `BANDWIDTH=5000000`（也接受 `AVERAGE-BANDWIDTH`）。
var hlsBandwidth = regexp.MustCompile(`(?i)(?:AVERAGE-)?BANDWIDTH=(\d+)`)

// qualityHeight 从档位标签里取高度（`1080p` / `1080P` / `1080` → 1080）。
var qualityHeight = regexp.MustCompile(`(\d{3,4})`)

// parseManifestText 按指定的清单格式解析出可选流。
//
// HLS 的单流清单（没有 `#EXT-X-STREAM-INF`）与 DASH 的单 Representation
// 清单都返回**一条**变体——那正是 [05 §5.3] 的"不必选择的情形"：
// 不做选择，直接把它唯一的轨道用 `-map` 显式交给 FFmpeg。
func parseManifestText(text string, kind Kind, baseURL string) ([]manifestVariant, error) {
	switch kind {
	case KindDASH:
		return parseDASHManifest(text, baseURL)
	default:
		return parseHLSManifest(text, baseURL)
	}
}

// parseHLSManifest 解析 HLS 主清单（[05 §5.3] 指定的探测方式：
// 读 `#EXT-X-STREAM-INF` 的 `BANDWIDTH` 与 `RESOLUTION`）。
func parseHLSManifest(text, baseURL string) ([]manifestVariant, error) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "#EXTM3U") {
		return nil, errors.New("不是 HLS 清单")
	}

	variants := make([]manifestVariant, 0, 8)
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !hlsStreamInf.MatchString(line) {
			continue
		}
		// URI 是这一行的**下一条非空且非注释**行（HLS 规范允许夹注释行）。
		uri := ""
		for j := i + 1; j < len(lines); j++ {
			candidate := strings.TrimSpace(lines[j])
			if candidate == "" || strings.HasPrefix(candidate, "#") {
				continue
			}
			uri = candidate
			break
		}
		if uri == "" {
			return nil, errors.New("HLS 变体缺少地址")
		}

		variant := manifestVariant{
			Input:   resolveManifestURL(baseURL, uri),
			MapArgs: []string{"-map", "0:v:0?", "-map", "0:a:0?"},
		}
		if match := hlsResolution.FindStringSubmatch(line); match != nil {
			variant.Width, _ = strconv.Atoi(match[1])
			variant.Height, _ = strconv.Atoi(match[2])
		}
		if match := hlsBandwidth.FindStringSubmatch(line); match != nil {
			variant.Bandwidth, _ = strconv.Atoi(match[1])
		}
		variants = append(variants, variant)
	}

	if len(variants) == 0 {
		// 单流清单：没有变体可选，它就是唯一的那条流。地址是清单自己。
		return []manifestVariant{{
			Input:   baseURL,
			MapArgs: []string{"-map", "0:v:0?", "-map", "0:a:0?"},
		}}, nil
	}
	return variants, nil
}

// mpdDocument 是 DASH 清单里我们真正要用的那几层。
//
// 只声明需要的字段：`encoding/xml` 会忽略未声明的元素，
// 于是这份结构对 MPD 规范的演进不敏感（多出来的属性/元素不构成失败）。
type mpdDocument struct {
	Periods []mpdPeriod `xml:"Period"`
}

type mpdPeriod struct {
	AdaptationSets []mpdAdaptationSet `xml:"AdaptationSet"`
}

type mpdAdaptationSet struct {
	ContentType     string              `xml:"contentType,attr"`
	MimeType        string              `xml:"mimeType,attr"`
	Width           int                 `xml:"width,attr"`
	Height          int                 `xml:"height,attr"`
	Representations []mpdRepresentation `xml:"Representation"`
}

type mpdRepresentation struct {
	Bandwidth int    `xml:"bandwidth,attr"`
	Width     int    `xml:"width,attr"`
	Height    int    `xml:"height,attr"`
	MimeType  string `xml:"mimeType,attr"`
}

// parseDASHManifest 解析 DASH 清单（[05 §5.3]：读 `AdaptationSet` 与
// `Representation` 的 `bandwidth` / `width` / `height`）。
//
// **流序号按"该 Representation 在同类型 Representation 里的出现次序"编**：
// FFmpeg 的 dash 解复用器为每个 Representation 建一条流，而 `-map 0:v:N`
// 里的 N 就是这个次序。这条假设在阶段 3 用真实 DASH 样本校核
// （[05 §12] 的待确认项）；选错时 FFmpeg 会以"映射不到流"退出，
// 我们把它报成 `manifest_no_matching_stream`，不会交付一个错的档位。
func parseDASHManifest(text, baseURL string) ([]manifestVariant, error) {
	var doc mpdDocument
	if err := xml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("解析 DASH 清单失败: %w", err)
	}

	variants := make([]manifestVariant, 0, 8)
	videoIndex, audioIndex := 0, 0
	for _, period := range doc.Periods {
		for _, set := range period.AdaptationSets {
			for _, rep := range set.Representations {
				switch adaptationKind(set, rep) {
				case "video":
					variant := manifestVariant{
						Width:     firstPositive(rep.Width, set.Width),
						Height:    firstPositive(rep.Height, set.Height),
						Bandwidth: rep.Bandwidth,
						Input:     baseURL,
						// 视频用选中的序号，音频取第一条：画质才是"档位"
						// （[05 §5.3] 的选择依据只有 `kind` 与 `quality`）。
						MapArgs: []string{
							"-map", "0:v:" + strconv.Itoa(videoIndex),
							"-map", "0:a:0?",
						},
					}
					videoIndex++
					variants = append(variants, variant)
				case "audio":
					audioIndex++
				}
			}
		}
	}

	if videoIndex == 0 {
		return nil, errors.New("DASH 清单里没有视频轨")
	}
	return variants, nil
}

// adaptationKind 判断一个 Representation 属于视频还是音频。
func adaptationKind(set mpdAdaptationSet, rep mpdRepresentation) string {
	for _, value := range []string{set.ContentType, set.MimeType, rep.MimeType} {
		lower := strings.ToLower(value)
		switch {
		case strings.HasPrefix(lower, "video"):
			return "video"
		case strings.HasPrefix(lower, "audio"):
			return "audio"
		}
	}
	if firstPositive(rep.Width, set.Width) > 0 {
		return "video"
	}
	return ""
}

// firstPositive 返回第一个大于 0 的值。
func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// resolveManifestURL 把清单里的相对地址按清单地址解析成绝对地址。
func resolveManifestURL(baseURL, reference string) string {
	if baseURL == "" {
		return reference
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return reference
	}
	ref, err := url.Parse(reference)
	if err != nil {
		return reference
	}
	return base.ResolveReference(ref).String()
}

// selectManifestVariant 按 [05 §5.3] 选一条流。
//
// 规则是**确定的**，不是"静默选默认"：
//   - 声明了档位：按高度匹配（`1080p` → 1080），多个匹配取带宽最高；
//   - 没声明档位：取分辨率最高，并列取带宽最高；
//   - 没有任何分辨率信息（单流清单）：不做选择，就用那一条；
//   - 声明的档位匹配不到：**报错**，不退到相近档位。
func selectManifestVariant(variants []manifestVariant, qualityLabel string) (manifestVariant, bool) {
	if len(variants) == 0 {
		return manifestVariant{}, false
	}

	if target, ok := parseQualityHeight(qualityLabel); ok {
		best := manifestVariant{}
		found := false
		for _, variant := range variants {
			if variant.Height != target {
				continue
			}
			if !found || variant.Bandwidth > best.Bandwidth {
				best, found = variant, true
			}
		}
		return best, found
	}

	// 没声明档位：分辨率最高，并列取带宽最高。没有任何分辨率信息时
	// （HLS 单流清单、DASH 未写 width/height）保持原顺序取第一条——
	// 那时"最高"没有意义，清单里也通常只有一条。
	best := variants[0]
	for _, variant := range variants[1:] {
		switch {
		case variant.Height > best.Height:
			best = variant
		case variant.Height == best.Height && variant.Bandwidth > best.Bandwidth:
			best = variant
		}
	}
	return best, true
}

// parseQualityHeight 从档位标签里取高度；标签里没有数字时返回 false。
func parseQualityHeight(label string) (int, bool) {
	match := qualityHeight.FindStringSubmatch(label)
	if match == nil {
		return 0, false
	}
	height, err := strconv.Atoi(match[1])
	if err != nil || height <= 0 {
		return 0, false
	}
	return height, true
}

// probeManifestDuration 用 FFprobe 探测清单总时长（[05 §4.6.3] 的时长口径需要它）。
//
// 刻意**不把探测失败当错误**：总时长只影响"能不能显示百分比"，
// 而 [05 §4.6.3] 明确"拿不到总时长时 P2 退回阶段语义、不伪造百分比"。
// 所以这里所有失败路径都安静地返回 0。
//
// ⚠️ 探测的是**清单地址**而不是选中变体的地址：FFprobe 不支持对 HLS 变体
// 单独取时长（它会自己去读主清单）。同一条清单下各变体时长相同，
// 所以这个近似不影响百分比。
func (d *Downloader) probeManifestDuration(ctx context.Context, req Request, target string) float64 {
	if strings.TrimSpace(d.probePath) == "" {
		return 0
	}
	call := ToolCall{
		Path: d.probePath,
		Args: []string{
			"-hide_banner", "-v", "error",
			"-print_format", "json", "-show_format",
		},
		Timeout:     d.toolWait,
		OutputLimit: d.outLimit,
	}
	if headers := ffmpegHeaders(req.Headers); headers != "" {
		// FFprobe 与 FFmpeg 共用 http 协议选项，因此这条参数名相同。
		// 万一某个版本的 FFprobe 不认它，探测会失败——那只是"没有百分比"，
		// 不影响下载本身（见上面的说明）。
		call.Args = append(call.Args, "-headers", headers)
	}
	call.Args = append(call.Args, target)

	out, err := d.runner.Run(ctx, call)
	if err != nil || out.ExitFailure {
		return 0
	}
	info, parseErr := parseProbeOutput(out.Stdout)
	if parseErr != nil || info.Duration <= 0 {
		return 0
	}
	return info.Duration
}

// maxHeaderBudget 是 [05 §4.2] 的请求头预算：**6 KB**（沿用旧项目实测值）。
//
// 预算按 `-headers` 的实际形态（每行 `Key: Value\r\n`）计量，而不是按
// "原始 map 有多长"：进命令行的就是那个形态，超的是它。
const maxHeaderBudget = 6 << 10

// headerBudgetExceeded 判断请求头是否超出预算。
//
// 必须**在启动 FFmpeg 之前**判定：`-headers` 是命令行的一部分，而 Windows 的
// 命令行有约 32 KB 的硬上限——超了会以"启动工具失败"这种看不懂的方式失败，
// 既不可诊断，也不符合 [05 §7.2] 对 `headers_too_large` 的规定。
func headerBudgetExceeded(headers http.Header) bool {
	return len(headers) > 0 && len(ffmpegHeaders(headers)) > maxHeaderBudget
}

// ffmpegHeaders 把请求头拼成 FFmpeg `-headers` 需要的格式。
//
// 这是 [05 §4.2]"请求头不得放命令行"的**唯一例外**，理由写在 [05 §4.6.1]：
// FFmpeg 没有从文件或 stdin 读请求头的选项。附带的硬要求是
// **命令行不得进日志**（[12 §4.1]）——本包从不记录 `ToolCall.Args`。
func ffmpegHeaders(headers http.Header) string {
	if len(headers) == 0 {
		return ""
	}
	var builder strings.Builder
	for key, values := range headers {
		for _, value := range values {
			builder.WriteString(key)
			builder.WriteString(": ")
			builder.WriteString(value)
			// FFmpeg 要求每个头以 CRLF 结尾。
			builder.WriteString("\r\n")
		}
	}
	return builder.String()
}

// runManifestFFmpeg 调 FFmpeg 完成 P2 的取字节与封装。
func (d *Downloader) runManifestFFmpeg(
	ctx context.Context, req Request, choice manifestChoice, dirs workDirs, onProgress func(Progress),
) error {
	args := []string{"-hide_banner", "-y"}
	// `-headers` 只在请求头非空时添加（[05 §4.6.1]）。
	if headers := ffmpegHeaders(req.Headers); headers != "" {
		args = append(args, "-headers", headers)
	}
	args = append(args, "-i", choice.Input)
	args = append(args, choice.MapArgs...)
	// streamcopy：不重编码（B-302）。
	args = append(args, "-c", "copy")
	// `-progress pipe:1` 让 FFmpeg 把结构化进度写到 stdout（[05 §4.6.3]）。
	// `-nostats` 关掉它默认那行会不断刷新的统计——那行是给人看的，
	// 而我们要的是可解析的键值行。
	args = append(args, "-progress", "pipe:1", "-nostats")
	args = append(args, dirs.staging)

	call := ToolCall{
		Path:        d.ffmpegPath,
		Args:        args,
		Timeout:     d.toolWait,
		OutputLimit: d.outLimit,
	}

	progress := newFFmpegProgress(choice.TotalTime, d.progress, onProgress)
	reporter := progress.feed

	var out ToolOutput
	var err error
	if streamer := streamingRunner(d.runner); streamer != nil {
		out, err = streamer.RunStreaming(ctx, call, reporter)
	} else {
		// 执行器不支持流式（只可能是测试假件）：退回一次性返回，
		// 进度只剩阶段语义——[05 §4.6.3] 允许"拿不到就不伪造百分比"。
		out, err = d.runner.Run(ctx, call)
	}

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return d.fail(req, CodeDownloadFailed, ctxErr)
		}
		if out.TimedOut {
			return d.fail(req, CodeDownloadFailed, errors.New("清单下载超出单次调用上限"))
		}
		return d.fail(req, CodeDownloadFailed, fmt.Errorf("清单下载未能执行: %w", err))
	}
	if out.ExitFailure {
		// FFmpeg 非零退出：清单读不下来、分片取不回、封装不了都落在这里。
		// 归为**可重试**的 download_failed——[05 §4.6.1] 明确"清单地址失效
		// 仍按 §8 计数并最终 failed"，而不是立刻定案。
		return d.fail(req, CodeDownloadFailed,
			fmt.Errorf("清单下载失败（FFmpeg 退出码 %d）", out.ExitCode))
	}
	return nil
}

// ffmpegProgress 解析 FFmpeg 的 `-progress` 键值行，并把它折算成 [Progress]。
//
// 它按**行**缓冲：`onChunk` 给的增量不保证落在行边界上（[StreamingRunner]）。
type ffmpegProgress struct {
	pending   []byte
	totalTime float64
	interval  time.Duration
	last      time.Time
	emit      func(Progress)

	bytes    int64
	position float64
}

func newFFmpegProgress(totalTime float64, interval time.Duration, emit func(Progress)) *ffmpegProgress {
	return &ffmpegProgress{totalTime: totalTime, interval: interval, emit: emit}
}

// feed 接收一段 stdout 增量。
func (p *ffmpegProgress) feed(chunk []byte) {
	p.pending = append(p.pending, chunk...)
	for {
		idx := bytes.IndexByte(p.pending, '\n')
		if idx < 0 {
			return
		}
		line := strings.TrimSpace(string(p.pending[:idx]))
		p.pending = p.pending[idx+1:]
		p.handleLine(line)
	}
}

// handleLine 处理一行 `key=value`。
//
// 只认两个键：
//   - `total_size` → 已写出字节（[05 §4.6.3] 的 `downloaded_bytes`）；
//   - `out_time` → 已处理到的时间点。**刻意不用 `out_time_ms`**：它的单位
//     在 FFmpeg 历史上有过不一致（名为毫秒、实为微秒），而 `out_time` 的
//     `HH:MM:SS.ffffff` 没有歧义。
//
// 在 `progress=` 行上报（FFmpeg 每完成一块进度就写一行 `progress=continue`），
// 这样一次刷新只回调一次，而不是每个键都回调。
func (p *ffmpegProgress) handleLine(line string) {
	switch {
	case strings.HasPrefix(line, "total_size="):
		if value, err := strconv.ParseInt(strings.TrimPrefix(line, "total_size="), 10, 64); err == nil && value >= 0 {
			p.bytes = value
		}
	case strings.HasPrefix(line, "out_time="):
		if seconds, ok := parseFFmpegTime(strings.TrimPrefix(line, "out_time=")); ok {
			p.position = seconds
		}
	case strings.HasPrefix(line, "progress="):
		p.report()
	}
}

// report 按节流间隔回调一帧。
func (p *ffmpegProgress) report() {
	if p.emit == nil {
		return
	}
	if p.interval > 0 && !p.last.IsZero() && time.Since(p.last) < p.interval {
		return
	}
	p.last = time.Now()

	snapshot := Progress{Downloaded: p.bytes, Phase: PhaseDownloading}
	// 时长口径只在真的有总时长时给：拿不到就不给百分比（[05 §4.6.3]）。
	if p.totalTime > 0 && p.position > 0 {
		ratio := p.position / p.totalTime
		if ratio > 1 {
			ratio = 1
		}
		snapshot.TimeRatio = ratio
	}
	p.emit(snapshot)
}

// parseFFmpegTime 解析 `HH:MM:SS.ffffff`（也接受 `N/A` 这种占位值）。
func parseFFmpegTime(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "N/A") {
		return 0, false
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, false
	}
	hours, err1 := strconv.ParseFloat(parts[0], 64)
	minutes, err2 := strconv.ParseFloat(parts[1], 64)
	seconds, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	total := hours*3600 + minutes*60 + seconds
	if total < 0 {
		return 0, false
	}
	return total, true
}
