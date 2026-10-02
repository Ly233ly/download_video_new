package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 用临时目录搭一个假的 media-tools（[12 §5.2] 的 T2：不污染用户目录）。
func buildFakeToolset(t *testing.T, base string, exes []string, ffmpegVersion, ytdlpVersion, denoVersion string) {
	t.Helper()
	dir := filepath.Join(base, ToolsetDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	for _, name := range exes {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stub"), 0o600); err != nil {
			t.Fatalf("写 %s 失败: %v", name, err)
		}
	}
	writeJSON := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("写 %s 失败: %v", name, err)
		}
	}
	if ffmpegVersion != "" {
		writeJSON(ffmpegVersionFile, `{"version":"`+ffmpegVersion+`"}`)
	}
	if ytdlpVersion != "" || denoVersion != "" {
		writeJSON(resolverVersionFile,
			`{"ytDlpVersion":"`+ytdlpVersion+`","denoVersion":"`+denoVersion+`"}`)
	}
}

var allExes = []string{"ffmpeg.exe", "ffprobe.exe", "yt-dlp.exe", "deno.exe"}

func TestResolve_AllPresent(t *testing.T) {
	base := t.TempDir()
	buildFakeToolset(t, base, allExes, "8.1.2", "2026.06.09", "2.8.1")

	set, err := Resolve(base)
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}

	got := set.All()
	wantNames := []string{ToolFFmpeg, ToolFFprobe, ToolYtDlp, ToolDeno}
	wantVersions := []string{"8.1.2", "8.1.2", "2026.06.09", "2.8.1"}
	for i, tool := range got {
		if tool.Name != wantNames[i] {
			t.Errorf("工具[%d] 名称 = %q，期望 %q", i, tool.Name, wantNames[i])
		}
		if tool.Path == "" {
			t.Errorf("工具 %s 缺路径", tool.Name)
		}
		if tool.Version != wantVersions[i] {
			t.Errorf("工具 %s 版本 = %q，期望 %q", tool.Name, tool.Version, wantVersions[i])
		}
	}
}

// 缺失必须被报出来，且**已找到的工具仍然可用**（阶段 1 不阻塞启动，但不静默降级）。
func TestResolve_MissingReported(t *testing.T) {
	base := t.TempDir()
	buildFakeToolset(t, base, []string{"ffmpeg.exe"}, "8.1.2", "", "")

	set, err := Resolve(base)
	if err == nil {
		t.Fatal("缺三个工具时应当报错")
	}
	for _, name := range []string{"ffprobe.exe", "yt-dlp.exe", "deno.exe"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("错误信息里缺少 %s：%v", name, err)
		}
	}
	if set.FFmpeg.Path == "" {
		t.Error("已找到的 ffmpeg 应当带路径")
	}
	if set.Deno.Path != "" {
		t.Error("缺失的工具不应带路径")
	}
}

// 版本文件缺失或字段为空都**不算错误**：版本只是展示信息。
func TestResolve_VersionMissingIsNotAnError(t *testing.T) {
	base := t.TempDir()
	buildFakeToolset(t, base, allExes, "", "", "")

	set, err := Resolve(base)
	if err != nil {
		t.Fatalf("缺版本文件不应报错: %v", err)
	}
	for _, tool := range set.All() {
		if tool.Version != "" {
			t.Errorf("工具 %s 版本应为空，实际 %q", tool.Name, tool.Version)
		}
		if tool.Path == "" {
			t.Errorf("工具 %s 仍应有路径", tool.Name)
		}
	}
}

// 版本文件带 UTF-8 BOM 时仍应解析（编辑器/脚本写过的文件常带 BOM）。
func TestResolve_ToleratesBOMInVersionFile(t *testing.T) {
	base := t.TempDir()
	buildFakeToolset(t, base, allExes, "", "", "")

	dir := filepath.Join(base, ToolsetDirName)
	bom := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"version":"9.9.9"}`)...)
	if err := os.WriteFile(filepath.Join(dir, ffmpegVersionFile), bom, 0o600); err != nil {
		t.Fatalf("写带 BOM 的版本文件失败: %v", err)
	}

	set, err := Resolve(base)
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if set.FFmpeg.Version != "9.9.9" {
		t.Errorf("带 BOM 的版本未解析: %q", set.FFmpeg.Version)
	}
}

// 环境变量优先，且**不做向上级搜索**（避免工具缺失时悄悄用上别处的副本）。
func TestResolveDefault_UsesEnvDirAndDoesNotSearchUpwards(t *testing.T) {
	base := t.TempDir()
	buildFakeToolset(t, base, allExes, "1.0", "2.0", "3.0")

	t.Setenv(ToolsDirEnv, base)
	exePath := filepath.Join(t.TempDir(), "build", "bin", "留底下载器.exe")
	if _, err := ResolveDefault(exePath); err != nil {
		t.Fatalf("设置了 %s 时应解析成功: %v", ToolsDirEnv, err)
	}

	t.Setenv(ToolsDirEnv, "")
	if _, err := ResolveDefault(exePath); err == nil {
		t.Error("exe 同级的上级目录里有 media-tools，但不设环境变量时不应向上搜索")
	}
}
