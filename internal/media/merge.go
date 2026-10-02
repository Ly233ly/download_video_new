package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
)

// 轨道的类型（[03 §2.1.1] 的 `stream_plan.track` 里 P3 会出现的两种）。
const (
	trackKindVideo = "video"
	trackKindAudio = "audio"
)

// trackProgress 汇总各轨的字节数，按 [05 §4.6.3] 的**字节口径**上报。
//
// 为什么需要汇总而不是各报各的：`plans.downloaded_bytes` 是**整条计划**的
// 已下载量，报单轨的数字会让进度条走完两遍。
type trackProgress struct {
	mu          sync.Mutex
	got         []int64
	declared    int64
	allDeclared bool
	emit        func(Progress)
}

func newTrackProgress(count int, declared int64, allDeclared bool, emit func(Progress)) *trackProgress {
	return &trackProgress{got: make([]int64, count), declared: declared, allDeclared: allDeclared, emit: emit}
}

// report 记下某条轨的最新字节数并上报总和。
func (p *trackProgress) report(index int, progress Progress) {
	p.mu.Lock()
	if index >= 0 && index < len(p.got) {
		p.got[index] = progress.Downloaded
	}
	var sum int64
	for _, value := range p.got {
		sum += value
	}
	total := int64(0)
	if p.allDeclared {
		// 任一轨未声明字节数时 total 保持 0 = 未知，**不得**用"已声明之和"充数
		// （[05 §4.6.3] 的 `任意一轨未声明时为 NULL`）。
		total = p.declared
	}
	p.mu.Unlock()

	if p.emit != nil {
		p.emit(Progress{Downloaded: sum, Total: total, Phase: progress.Phase, TimeRatio: progress.TimeRatio})
	}
}

// sum 返回当前各轨已取回之和。
func (p *trackProgress) sum() (int64, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var sum int64
	for _, value := range p.got {
		sum += value
	}
	total := int64(0)
	if p.allDeclared {
		total = p.declared
	}
	return sum, total
}

// executeTracks 是 [05 §4.6.2] 的 P3：分离的音视频轨各自取字节，再 `streamcopy`
// 合并成一个文件。
//
// 阶段顺序是 downloading → merging → validating：**先合并、再校验合并后的成品**
// （T-DL-15）。这与 P1/P2 的 downloading → validating → merging 不同，
// 因为这里 `merging` 是真正的合并步骤，而校验必须看到最终形态——
// 单轨各自的"有没有视频/音频"说明不了成品有没有。
func (d *Downloader) executeTracks(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress),
) (Result, error) {
	if strings.TrimSpace(d.ffmpegPath) == "" {
		return Result{}, d.fail(req, CodeMergeFailed, errors.New("未找到 FFmpeg，无法合并音视频"))
	}

	// 各轨声明字节数之和；任一轨未声明就整体算"未知"（[05 §4.6.3]）。
	var declared int64
	allDeclared := true
	for _, track := range req.Tracks {
		if track.TotalBytes > 0 {
			declared += track.TotalBytes
		} else {
			allDeclared = false
		}
	}
	progress := newTrackProgress(len(req.Tracks), declared, allDeclared, onProgress)

	paths := make([]string, len(req.Tracks))
	// 先把所有轨的地址检查完再动手。理由不是洁癖：下到一半才发现第二条轨
	// 没有地址，等于白下一条轨（还可能白等几分钟）。
	for _, track := range req.Tracks {
		if strings.TrimSpace(track.URL) == "" {
			return Result{}, d.fail(req, CodeDownloadFailed, errors.New("音视频轨缺少地址"))
		}
	}
	for index, track := range req.Tracks {
		paths[index] = trackStagingPath(dirs.planDir, track.Kind, index)
		if err := d.downloadTrack(ctx, req, track, paths[index], index, progress); err != nil {
			return Result{}, err
		}
	}

	merged := dirs
	merged.staging = mergedStagingPath(dirs.planDir, req.Container)

	// 合并本身就是 [05 §2.1] 的 `merging` 阶段。
	sum, total := progress.sum()
	reportPhase(onProgress, PhaseMerging, sum, total)

	if err := d.mergeTracks(ctx, req, paths, merged.staging); err != nil {
		return Result{}, err
	}

	probe, err := d.validate(ctx, req, merged, onProgress)
	if err != nil {
		return Result{}, err
	}

	delivered, err := deliverFile(req.OutputDir, req.OutputName, merged.staging, probe.Size)
	if err != nil {
		return Result{}, err
	}
	return Result{FinalPath: delivered.Path, Bytes: delivered.Bytes, Probe: probe}, nil
}

// downloadTrack 取一条轨的字节——**复用 P1 的取字节实现**（[05 §4.6.4]：
// "P4 解析出的清单/分离轨复用 §4.6.1/§4.6.2 实现"，而 §4.6.2 的每条轨
// 就是按 §4.1 取的）。
//
// **刻意不做单轨校验**：[05 §4.6.2] 要求"先合并、再校验合并后的成品"，
// 而单轨多半是纯视频或纯音频，拿成品那套"容器要不要视频/音频"的判据去卡它没有意义。
func (d *Downloader) downloadTrack(
	ctx context.Context, req Request, track Track, staging string, index int, progress *trackProgress,
) error {
	sub := req
	sub.URL = track.URL
	sub.Headers = track.Headers
	sub.TotalBytes = track.TotalBytes
	sub.Tracks = nil
	// 轨道不做"提示不成立就改路"的核实：它本来就是某条路径的**产物**，
	// 不是在替调用方的提示做验证（[05 §4.0] 的改路发生在路径选择那一层）。
	sub.sniffDisabled = true

	dirs := workDirs{planDir: filepath.Dir(staging), staging: staging}
	report := func(p Progress) { progress.report(index, p) }

	existing := existingBytes(staging)
	// 已完整取回的轨道**不重下**（[05 §4.6.2]）。"完整"必须有依据：
	// 磁盘上的长度与**声明的字节数**一致。没有声明（`TotalBytes == 0`）时
	// 无法确认，按同一条要求**重下**。
	if existing > 0 && track.TotalBytes > 0 && existing == track.TotalBytes {
		slog.Info("轨道已完整，跳过下载",
			"component", "download", "event", "track_resume_complete",
			"plan_id", req.PlanID, "track", track.Kind, "bytes", existing)
		report(Progress{Downloaded: existing, Total: track.TotalBytes, Phase: PhaseDownloading})
		return nil
	}

	report(Progress{Total: track.TotalBytes, Phase: PhaseDownloading})
	return d.downloadDirect(ctx, sub, dirs, existing, report)
}

// mergeTracks 用 FFmpeg 把各轨 `streamcopy` 合并成一个文件（B-302）。
//
// 两条硬要求都写在 [05 §4.6.2] 里，且都不是"保险起见"：
//
//   - **必须逐条 `-map`**：不写时 FFmpeg 默认只从**第一个**输入取视频和音频，
//     第二条输入的音轨会**整个丢掉**——这是它的默认行为，不是偶发。
//   - **不加 `-shortest`**：它会按最短的那条轨截断，把合法的长轨切掉。
//
// 合并只对**已取回本地的轨道文件**调用 FFmpeg，因此这一步**不出站、不需要凭据**
// （[05 §4.6.2]）。
func (d *Downloader) mergeTracks(
	ctx context.Context, req Request, paths []string, output string,
) error {
	args := []string{"-hide_banner", "-y"}
	for _, path := range paths {
		args = append(args, "-i", path)
	}

	mapped := 0
	for index, track := range req.Tracks {
		switch trackKindOf(track, index) {
		case trackKindVideo:
			args = append(args, "-map", fmt.Sprintf("%d:v:0", index))
			mapped++
		case trackKindAudio:
			args = append(args, "-map", fmt.Sprintf("%d:a:0", index))
			mapped++
		}
	}
	if mapped == 0 {
		return d.fail(req, CodeMergeFailed, errors.New("没有可合并的音视频轨"))
	}

	// streamcopy：不重编码（B-302）。刻意**不加** `-shortest`（见上）。
	args = append(args, "-c", "copy")
	args = append(args, output)

	out, err := d.runner.Run(ctx, ToolCall{
		Path:        d.ffmpegPath,
		Args:        args,
		Timeout:     d.toolWait,
		OutputLimit: d.outLimit,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return d.fail(req, CodeDownloadFailed, ctxErr)
		}
		if out.TimedOut {
			return d.fail(req, CodeMergeFailed, errors.New("音视频合并超出单次调用上限"))
		}
		return d.fail(req, CodeMergeFailed, fmt.Errorf("音视频合并未能执行: %w", err))
	}
	if out.ExitFailure {
		// 合并失败**不可重试**（[05 §7.2]）：输入是本地文件，重试只会重复失败；
		// 真的读不了就是字节坏了，那要重新下载而不是原地重试。
		return d.fail(req, CodeMergeFailed,
			fmt.Errorf("音视频合并失败（FFmpeg 退出码 %d）", out.ExitCode))
	}
	return nil
}

// trackKindOf 取轨道类型；没声明时按位置推断（第一条当视频、第二条当音频）。
//
// 推断只在调用方漏填时兜底：[03 §2.1.1] 的 `track` 是必填的，
// 而 `-map` 必须显式（[05 §4.6.2]），没有类型就无从映射。
func trackKindOf(track Track, index int) string {
	switch track.Kind {
	case trackKindVideo, trackKindAudio:
		return track.Kind
	}
	if index == 0 {
		return trackKindVideo
	}
	return trackKindAudio
}

// trackStagingPath 是某条轨在 `临时\<plan_id>\` 里的落点。
//
// 名字带上轨的类型：它只活在临时目录里，而"哪个文件是哪条轨"是排错时
// 第一件要看的事（[05 §11]）。
func trackStagingPath(planDir, kind string, index int) string {
	name := fmt.Sprintf("track%d", index)
	switch kind {
	case trackKindVideo:
		name = trackKindVideo
	case trackKindAudio:
		name = trackKindAudio
	}
	return filepath.Join(planDir, name+".bin")
}

// mergedStagingPath 是合并产物的落点。
//
// 扩展名必须对：FFmpeg 靠它推断封装格式（与 [manifestStagingPath] 同理）。
func mergedStagingPath(planDir, container string) string {
	name := normalizeContainer(container)
	if name == "" {
		name = "bin"
	}
	return filepath.Join(planDir, "merged."+name)
}
