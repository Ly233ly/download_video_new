package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// 纯音频输出容器。P1 的直链只有一条流，靠容器判断"该有什么轨道"：
// `m4a` / `mp3` 里没有视频流是**正常**的，不能报 `output_no_video`
// （取值域见 [03 §3.3] 的 `output_container`）。
var audioOnlyContainers = map[string]bool{"m4a": true, "mp3": true}

// wantsVideo 判断该容器是否要求视频流。
func wantsVideo(container string) bool {
	return !audioOnlyContainers[normalizeContainer(container)]
}

// wantsAudio 判断该容器是否要求音频流。
//
// 只有纯音频容器**必须**有音频；视频容器里没有音轨是合法媒体
// （无声视频、纯画面录屏），此时报 `output_no_audio` 会把好文件判死。
func wantsAudio(container string) bool {
	return audioOnlyContainers[normalizeContainer(container)]
}

// normalizeContainer 归一化容器名：去空白、去点、转小写。
func normalizeContainer(container string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(container), "."))
}

// StreamInfo 是 FFprobe 实测的单条流。
type StreamInfo struct {
	Index     int    `json:"index"`
	Type      string `json:"type"` // video / audio / subtitle / data …
	Codec     string `json:"codec"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Duration  string `json:"duration"`
	FrameRate string `json:"frame_rate"`
	Channels  int    `json:"channels"`
	SampleHz  int    `json:"sample_rate"`
	BitRate   string `json:"bit_rate"`
}

// ProbeInfo 是 FFprobe 对产物的**实测**结果（[05 §6]）。
//
// 它同时承担两个职责：校验的输入，以及 [Result] 里给界面/去重用的媒体事实。
// 每个字段都来自 FFprobe 的实际输出或本地文件系统的实际长度——
// **没有任何推断值**：推断出来的分辨率或时长会在去重（B-506）与展示上变成假事实。
type ProbeInfo struct {
	Container   string       `json:"container"`
	Duration    float64      `json:"duration"`
	StreamCount int          `json:"stream_count"`
	HasVideo    bool         `json:"has_video"`
	HasAudio    bool         `json:"has_audio"`
	Streams     []StreamInfo `json:"streams"`
	// Size 是产物在磁盘上的字节数（本包自己 stat 得来，不是 FFprobe 的输出）。
	// 交付前用它复核"校验过的字节"与"要搬走的字节"是同一份。
	Size int64 `json:"size"`
}

// probe 调用 FFprobe 读取媒体事实；失败时返回的错误**已脱敏**
// （不含路径与原始 stderr，[05 §5.2]：退出码映射为稳定原因，原始 stderr 不给用户）。
func (d *Downloader) probe(ctx context.Context, path string) (info ProbeInfo, err error) {
	if strings.TrimSpace(d.probePath) == "" {
		// 缺工具不是"跳过校验"的理由：校验是交付的唯一门槛（[05 §1]）。
		return ProbeInfo{}, errors.New("未找到 FFprobe，无法校验输出")
	}

	// 校验对象是**下载来的字节**，也就是不可信输入；解析它的工具因此也可能崩。
	// panic 必须在包内停下（[12 §3.2]：编程错误只允许在启动期 panic，
	// 运行期的 panic 会把整个后台调度循环带走——B-306 明确禁止单计划异常终止循环）。
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("媒体校验器发生 panic",
				"component", "download", "event", "probe_panicked", "cause", fmt.Sprint(recovered))
			info = ProbeInfo{}
			err = errors.New("媒体校验工具异常退出")
		}
	}()

	// 参数数组，绝不拼命令行字符串（[05 §5.2]）。
	out, err := d.runner.Run(ctx, ToolCall{
		Path: d.probePath,
		Args: []string{
			"-hide_banner",
			"-v", "error",
			"-print_format", "json",
			"-show_format",
			"-show_streams",
			path,
		},
		Timeout:     d.toolWait,
		OutputLimit: d.outLimit,
	})
	if err != nil {
		// 调用方取消要原样上抛：它决定上层是 `canceled` 而不是 `failed`（[05 §10]）。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProbeInfo{}, ctxErr
		}
		if errors.Is(err, ErrToolTimeout) {
			return ProbeInfo{}, errors.New("校验超时：媒体探测未在上限内返回")
		}
		return ProbeInfo{}, errors.New("无法启动媒体校验工具")
	}
	if out.TimedOut {
		return ProbeInfo{}, errors.New("校验超时：媒体探测未在上限内返回")
	}
	if out.ExitFailure {
		// 原始 stderr 只进本地日志的 Debug（[12 §4.2]），不进用户可见消息。
		return ProbeInfo{}, fmt.Errorf("媒体探测失败（退出码 %d）", out.ExitCode)
	}
	if out.Truncated {
		// 输出被截断说明 JSON 可能不完整，继续解析会得到"看起来能解析"的半个对象。
		// 这里**宁可失败也不猜**：半个对象照样能解析出流列表，却可能丢掉
		// 恰好缺的那条音频流——那会让校验放行一个残缺文件。
		return ProbeInfo{}, errors.New("媒体探测输出超出上限")
	}

	parsed, err := parseProbeOutput(out.Stdout)
	if err != nil {
		return ProbeInfo{}, err
	}
	size, statErr := fileSizeOf(path)
	if statErr != nil {
		return ProbeInfo{}, statErr
	}
	parsed.Size = size
	return parsed, nil
}

// probeResult 是 FFprobe `-print_format json` 的载荷（只取本阶段用到的字段）。
type probeResult struct {
	Streams []struct {
		Index     int    `json:"index"`
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Duration  string `json:"duration"`
		Channels  int    `json:"channels"`
		SampleHz  string `json:"sample_rate"`
		FrameRate string `json:"r_frame_rate"`
		BitRate   string `json:"bit_rate"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
}

// parseProbeOutput 把 FFprobe 的 JSON 转成 [ProbeInfo]。
//
// 无法解析时返回"输出不可解析"这一类错误，由调用方映射为稳定错误码——
// **不猜**容器、时长或流（推断出来的媒体事实会在去重与展示上变成假事实）。
func parseProbeOutput(raw []byte) (ProbeInfo, error) {
	var payload probeResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ProbeInfo{}, errors.New("媒体探测输出不可解析")
	}

	info := ProbeInfo{
		Container: normalizeContainer(payload.Format.FormatName),
		Duration:  parseSeconds(payload.Format.Duration),
		Streams:   make([]StreamInfo, 0, len(payload.Streams)),
	}
	for _, stream := range payload.Streams {
		kind := strings.ToLower(strings.TrimSpace(stream.CodecType))
		info.Streams = append(info.Streams, StreamInfo{
			Index:     stream.Index,
			Type:      kind,
			Codec:     stream.CodecName,
			Width:     stream.Width,
			Height:    stream.Height,
			Duration:  stream.Duration,
			FrameRate: stream.FrameRate,
			Channels:  stream.Channels,
			SampleHz:  parseRate(stream.SampleHz),
			BitRate:   stream.BitRate,
		})
		switch kind {
		case "video":
			info.HasVideo = true
		case "audio":
			info.HasAudio = true
		}
	}

	info.StreamCount = len(info.Streams)
	if info.Duration <= 0 {
		// 容器级时长缺失时退回第一条带时长的流；两者都没有就保持 0，
		// 由时长校验（阶段 3）去判定，本阶段不据此拦交付。
		for _, stream := range info.Streams {
			if d := parseSeconds(stream.Duration); d > 0 {
				info.Duration = d
				break
			}
		}
	}
	return info, nil
}

// parseSeconds 把 FFprobe 的时长字符串转成秒。
//
// 解析失败返回 0 而不是错误：`N/A` 是 FFprobe 的正常输出之一
// （流级时长常常缺失），把它当错误会让好文件无法交付。
func parseSeconds(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return seconds
}

// parseRate 解析采样率这类整数字段，失败返回 0。
func parseRate(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	rate, err := strconv.Atoi(value)
	if err != nil || rate < 0 {
		return 0
	}
	return rate
}
