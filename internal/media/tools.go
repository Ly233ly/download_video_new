// Package media 是下载引擎（[01 §3]）。本阶段（D7）实现随包媒体工具的解析。
//
// 工具是**运行时资产**：开发时在仓库的 `media-tools/`，发行时由安装器复制到安装根的
// `media-tools/`（[10 §5.2]）。二进制不入库（`.gitignore` 的 `*.exe`），
// 版本与校验和记在同目录的 `*-VERSION.json` 里。
package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ToolsetDirName 是随包工具目录名（[10 §5.2]）。
const ToolsetDirName = "media-tools"

// ToolsDirEnv 允许显式指定**包含** media-tools 的目录。
//
// 开发时必需：`wails build` 的产物在 `build/bin/`，而工具在仓库根，
// 两者不同级。发行布局下不需要它（exe 与 media-tools 同在安装根）。
const ToolsDirEnv = "LIUDI_TOOLS_DIR"

// 工具逻辑名。
const (
	ToolFFmpeg  = "ffmpeg"
	ToolFFprobe = "ffprobe"
	ToolYtDlp   = "yt-dlp"
	ToolDeno    = "deno"
)

// 版本记录文件名（沿用旧项目 media-tools/ 的既有命名，见其 README）。
const (
	ffmpegVersionFile   = "FFMPEG-VERSION.json"
	resolverVersionFile = "YOUTUBE-RESOLVER-VERSION.json"
)

// Tool 是一个外部媒体工具。
type Tool struct {
	Name    string // 逻辑名
	Path    string // 可执行文件绝对路径
	Version string // 版本号；版本文件缺失或字段为空时为空串
}

// Toolset 是随包的四个工具。
type Toolset struct {
	FFmpeg  Tool
	FFprobe Tool
	YtDlp   Tool
	Deno    Tool
}

// All 按固定顺序返回四个工具（供界面与诊断展示）。
func (t *Toolset) All() []Tool {
	if t == nil {
		return nil
	}
	return []Tool{t.FFmpeg, t.FFprobe, t.YtDlp, t.Deno}
}

// Resolve 在 baseDir 下解析四个工具（即找 `baseDir/media-tools/`）。
//
// 返回的 Toolset 里**已找到的工具仍然带路径**，err 只说明缺了哪些——
// 调用方既能报出缺失，也能继续用现有的。阶段 1 缺工具不阻塞启动，
// 但**缺失必须可见**（README 设计原则：不静默降级）。
func Resolve(baseDir string) (*Toolset, error) {
	dir := filepath.Join(baseDir, ToolsetDirName)
	ffmpegVersion := readFFmpegVersion(dir)
	ytdlpVersion, denoVersion := readResolverVersions(dir)

	set := &Toolset{
		FFmpeg:  Tool{Name: ToolFFmpeg, Version: ffmpegVersion},
		FFprobe: Tool{Name: ToolFFprobe, Version: ffmpegVersion},
		YtDlp:   Tool{Name: ToolYtDlp, Version: ytdlpVersion},
		Deno:    Tool{Name: ToolDeno, Version: denoVersion},
	}

	var missing []string
	for _, item := range []struct {
		tool *Tool
		exe  string
	}{
		{&set.FFmpeg, "ffmpeg.exe"},
		{&set.FFprobe, "ffprobe.exe"},
		{&set.YtDlp, "yt-dlp.exe"},
		{&set.Deno, "deno.exe"},
	} {
		path := filepath.Join(dir, item.exe)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			missing = append(missing, item.exe)
			continue
		}
		item.tool.Path = path
	}

	if len(missing) > 0 {
		return set, fmt.Errorf("缺少媒体工具 %s（应在 %s 目录下）",
			strings.Join(missing, "、"), ToolsetDirName)
	}
	return set, nil
}

// ResolveDefault 按发行布局解析：优先环境变量 ToolsDirEnv 指定的目录，
// 否则在可执行文件所在目录下查找。
//
// **刻意不向上级目录搜索**：那会在工具缺失时悄悄用上别处的副本，
// 属于静默降级。开发时请显式设置 `LIUDI_TOOLS_DIR`。
func ResolveDefault(exePath string) (*Toolset, error) {
	if dir := os.Getenv(ToolsDirEnv); dir != "" {
		return Resolve(dir)
	}
	return Resolve(filepath.Dir(exePath))
}

// readFFmpegVersion 读 FFmpeg 版本；ffmpeg 与 ffprobe 同属一个发布包，版本相同。
func readFFmpegVersion(dir string) string {
	var info struct {
		Version string `json:"version"`
	}
	if !readVersionFile(filepath.Join(dir, ffmpegVersionFile), &info) {
		return ""
	}
	return info.Version
}

func readResolverVersions(dir string) (ytdlp, deno string) {
	var info struct {
		YtDlpVersion string `json:"ytDlpVersion"`
		DenoVersion  string `json:"denoVersion"`
	}
	if !readVersionFile(filepath.Join(dir, resolverVersionFile), &info) {
		return "", ""
	}
	return info.YtDlpVersion, info.DenoVersion
}

// readVersionFile 读取并解析版本文件；任何失败都返回 false。
//
// 版本只是展示信息，**缺版本不算错误**——工具本身在不在才是。
// 容忍 UTF-8 BOM：这类文件被编辑器写过就会带 BOM（[internal/proxy] 踩过同一个坑）。
func readVersionFile(path string, dst any) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	return json.Unmarshal(raw, dst) == nil
}
