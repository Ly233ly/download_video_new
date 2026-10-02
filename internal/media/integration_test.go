//go:build integration

package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件是**真实站点实测**，不是单元测试：真的起 yt-dlp 子进程、真的出网、
// 真的落盘并用 FFprobe 校验。
//
// 单独一个 build tag 的理由：单元测试必须离线可跑（[12 §5]），而这里的
// 结果取决于网络出口——同一个二进制在不同网络下结论不同，不能混进
// `go test ./...` 的绿/红判定。默认构建不编译本文件。
//
// 跑法（PowerShell）：
//
//	$env:HTTPS_PROXY='http://127.0.0.1:7890'   # 按本机实际代理填；直连可省略
//	go test -tags integration ./internal/media/ -run TestIntegration -v -timeout 30m
//
// 目标站点的选择理由与实测结论见 docs/phase3-report.md：这里刻意选了
// **不需要登录就能匿名解析**的站点（Wikimedia Commons），因为要验的是
// 本程序调 yt-dlp 的那条链路对不对，而不是某站点今天放不放行。

// integrationTimeout 是单个用例的上限：包住解析 + 取字节 + 合并 + 校验。
const integrationTimeout = 15 * time.Minute

// integrationDownloader 用仓库根 media-tools/ 里的真工具构造下载器。
//
// 工具不全就跳过：那是本机没准备媒体工具，不是这条路径有问题。
func integrationDownloader(t *testing.T) *Downloader {
	t.Helper()

	tools, err := Resolve(filepath.Join("..", ".."))
	if err != nil {
		t.Skipf("本机缺媒体工具，跳过真实站点实测：%v", err)
	}
	d, err := New(tools, Options{})
	if err != nil {
		t.Fatalf("构造下载器失败：%v", err)
	}
	return d
}

// integrationRequest 造一次真实下载所需的目录与入参。
func integrationRequest(t *testing.T, kind Kind, pageURL, quality, container string) Request {
	t.Helper()

	root := t.TempDir()
	tempDir := filepath.Join(root, "临时", "p-int")
	outputDir := filepath.Join(root, "已完成")
	for _, dir := range []string{tempDir, outputDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败：%v", err)
		}
	}
	return Request{
		PlanID:       "p-int",
		URL:          pageURL,
		Kind:         kind,
		QualityLabel: quality,
		Container:    container,
		OutputName:   "真实站点实测",
		TempDir:      tempDir,
		OutputDir:    outputDir,
	}
}

// integrationRun 跑一次下载，并把阶段与字节数记进测试日志。
//
// 日志是有用的产出：出问题时能从"阶段停在哪一步、有没有拿到字节"
// 判断是解析失败还是取字节失败。
func integrationRun(t *testing.T, d *Downloader, req Request) (Result, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	last := Progress{}
	result, err := d.Run(ctx, req, func(p Progress) {
		last = p
		t.Logf("进度 phase=%s downloaded=%d total=%d ratio=%.3f", p.Phase, p.Downloaded, p.Total, p.TimeRatio)
	})
	t.Logf("最后一帧：phase=%s downloaded=%d total=%d", last.Phase, last.Downloaded, last.Total)
	return result, err
}

// TestIntegrationPage_Wikimedia 是 P4 的端到端实测：给一个页面地址，
// 让 yt-dlp 自己找可下的媒体，再走完整的取字节、校验、交付。
//
// 这条源刻意挑了 240p 档（约 24 MiB）：不声明档位时本程序会按
// "分辨率最高"选到 896x504 那条（约 94 MiB），跑一次没有必要地慢。
func TestIntegrationPage_Wikimedia(t *testing.T) {
	d := integrationDownloader(t)
	req := integrationRequest(t, KindPage,
		"https://commons.wikimedia.org/wiki/File:Big_Buck_Bunny_medium.ogv",
		"240p", "webm")

	result, err := integrationRun(t, d, req)
	if err != nil {
		t.Fatalf("页面解析这条路径没能跑通：%v", err)
	}

	info, statErr := os.Stat(result.FinalPath)
	if statErr != nil {
		t.Fatalf("交付的文件不在：%v", statErr)
	}
	if info.Size() == 0 {
		t.Fatal("交付的文件是空的")
	}
	if info.Size() != result.Bytes {
		t.Errorf("文件大小 %d 与结果里的字节数 %d 不一致", info.Size(), result.Bytes)
	}
	if result.Probe.Container == "" {
		t.Error("FFprobe 校验没有给出容器（[05 §1] 要求实测校验后才交付）")
	}
	t.Logf("交付：%s（%d 字节，容器 %s，时长 %.1fs）",
		result.FinalPath, result.Bytes, result.Probe.Container, result.Probe.Duration)
}

// TestIntegrationPage_YouTube 记录 YouTube 在当前网络下的真实结果。
//
// **它不断言"能下"**：YouTube 会按出口 IP、cookie、PO token 决定放不放行，
// 而我们无法保证任何一样（CONTEXT.md 红线 M10：不得声称已修复登录会话问题）。
// 它断言的是**我们这边不撒谎**——放行就交付，不放行就必须如实报成
// `page_resolve_failed`（可重试）或 `page_media_unavailable`，
// 绝不能报成别的错，更不能在没拿到文件时说成功。
func TestIntegrationPage_YouTube(t *testing.T) {
	d := integrationDownloader(t)
	req := integrationRequest(t, KindPage,
		"https://www.youtube.com/watch?v=jNQXAC9IVRw", // Me at the zoo，19 秒
		"", "mp4")

	result, err := integrationRun(t, d, req)
	if err == nil {
		if _, statErr := os.Stat(result.FinalPath); statErr != nil {
			t.Errorf("说成功却没有文件：%v", statErr)
		}
		t.Logf("YouTube 这次放行了，交付 %s（%d 字节）", result.FinalPath, result.Bytes)
		return
	}

	if code, ok := CodeOf(err); ok {
		switch code {
		case CodePageResolveFailed, CodePageMediaUnavailable:
			t.Logf("YouTube 这次没放行，如实报成 %s：%v", code, err)
		default:
			t.Errorf("页面解析失败被报成了 %s，应当是 page_resolve_failed 或 page_media_unavailable", code)
		}
		return
	}
	t.Fatalf("失败必须带错误码（[05 §7]），实际是：%v", err)
}
