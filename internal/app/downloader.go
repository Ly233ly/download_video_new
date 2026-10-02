package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Ly233ly/download_video_new/internal/media"
	"github.com/Ly233ly/download_video_new/internal/platform"
	"github.com/Ly233ly/download_video_new/internal/proxy"
	"github.com/Ly233ly/download_video_new/internal/service"
	"github.com/Ly233ly/download_video_new/internal/store"
)

// planRunner 把 service 的下载契约适配到 internal/media 的下载引擎。
//
// 为什么需要这一层：service **刻意不 import 下载引擎**（[04 §1.2]）——它只在
// `service.Deps.Runner` 上声明"能跑一个计划"这个最小契约，具体引擎由装配层注入。
// 这样业务层不依赖引擎实现，而包划分仍服从 [01 §3]（下载引擎在 internal/media）。
type planRunner struct {
	engine *media.Downloader
	store  *store.DB
}

// newPlanRunner 构造适配器。
//
// 系统代理在这里注入（B-314 前半句："桌面下载按任务读取 Windows 系统代理"）。
// 读一次系统设置、构造一次客户端——程序运行期间代理若被别的软件改动，由
// [proxy] 的恢复/快照流程负责，不在每个下载任务里重读。
func newPlanRunner(tools *media.Toolset, db *store.DB) (*planRunner, error) {
	engine, err := media.New(tools, media.Options{HTTPClient: systemProxyClient()})
	if err != nil {
		return nil, err
	}
	return &planRunner{engine: engine, store: db}, nil
}

// systemProxyClient 在默认客户端上接好系统代理。
//
// 读不到系统代理**不是致命错误**：直连是安全退化（[12 §3.2] 的环境错误 →
// 降级并记录），而 B-314 要求的是"读取"，没有要求读不到就拒绝下载。
func systemProxyClient() *http.Client {
	client := media.DefaultHTTPClient()

	settings, err := proxy.RegistryBackend{}.Read()
	if err != nil {
		slog.Warn("读取系统代理失败，下载将直连",
			"component", "app", "event", "proxy_read_failed", "err", err)
		return client
	}
	// DefaultHTTPClient 的 Transport 就是 *http.Transport；断言失败时保持默认
	// （即直连），不 panic——装配层不该因为一个可选优化而让进程起不来。
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.Proxy = proxy.ProxyFunc(settings)
	}
	return client
}

// resolveDirs 解析输出目录。
//
// `output_dir` 为空表示默认目录，这正是 [03 §2.4] 的配置语义；值坏掉时同样
// 回退默认值且**不中断**——与该节"解码失败必须回退、不得中断启动"一致。
func (r *planRunner) resolveDirs(ctx context.Context) (platform.OutputDirs, error) {
	raw, ok, err := r.store.GetSetting(ctx, "output_dir")
	if err != nil || !ok {
		return platform.ResolveOutputDirs("")
	}
	var configured string
	if json.Unmarshal([]byte(raw), &configured) != nil {
		return platform.ResolveOutputDirs("")
	}
	return platform.ResolveOutputDirs(configured)
}

// Run 实现 service.Deps.Runner。
func (r *planRunner) Run(
	ctx context.Context,
	req service.DownloadRequest,
	onProgress func(service.DownloadProgress),
) (service.DownloadResult, error) {
	dirs, err := r.resolveDirs(ctx)
	if err != nil {
		return service.DownloadResult{}, &service.DownloadError{
			Code: "download_failed", Message: "无法确定输出目录", Cause: err,
		}
	}
	tempDir, err := dirs.EnsurePlanTemp(req.PlanID)
	if err != nil {
		return service.DownloadResult{}, &service.DownloadError{
			Code: "download_failed", Message: "无法创建任务临时目录", Cause: err,
		}
	}

	engineReq := media.Request{
		PlanID:     req.PlanID,
		URL:        req.MediaURL,
		Headers:    headerFromMap(req.Headers),
		OutputName: req.OutputName,
		Container:  req.OutputContainer,
		TempDir:    tempDir,
		OutputDir:  dirs.Completed,
		// 长度未知：适配层**不猜** total（[03 §2.1]：`0` 表示未知，不是"长度为 0"）。
		// 真实总长由引擎从响应头得到并经 Progress 回报。
	}

	// 引擎的 Total 用 `0 = 未知`，而 [03 §2.1] 要求库里是 NULL——转换在出口做。
	var lastTotal int64
	result, err := r.engine.Run(ctx, engineReq, func(p media.Progress) {
		lastTotal = p.Total
		if onProgress == nil {
			return
		}
		onProgress(service.DownloadProgress{
			DownloadedBytes: p.Downloaded,
			TotalBytes:      nullableBytes(p.Total),
			Phase:           p.Phase,
		})
	})
	if err != nil {
		return service.DownloadResult{}, adaptDownloadError(err)
	}

	return service.DownloadResult{
		FinalPath:       result.FinalPath,
		DownloadedBytes: result.Bytes,
		TotalBytes:      nullableBytes(lastTotal),
	}, nil
}

// adaptDownloadError 把引擎的稳定错误码转成 service 的错误类型（[05 §7]）。
//
// 引擎之外的错误（如建目录失败）按 `download_failed` 归类——它是 [05 §7.1]
// 的可重试默认，也是"未知失败"最安全的落点。
func adaptDownloadError(err error) error {
	var coded *media.Error
	if errors.As(err, &coded) {
		return &service.DownloadError{
			Code:    string(coded.Code),
			Message: coded.Code.Message(),
			Cause:   coded,
		}
	}
	return &service.DownloadError{Code: "download_failed", Cause: err}
}

// headerFromMap 把 service 侧的会话头转成 http.Header（只在内存流转）。
func headerFromMap(in map[string]string) http.Header {
	if len(in) == 0 {
		return nil
	}
	out := make(http.Header, len(in))
	for key, value := range in {
		out.Set(key, value)
	}
	return out
}

// nullableBytes 把"0 = 未知"转成 [03 §2.1] 要求的 NULL 语义。
func nullableBytes(total int64) *int64 {
	if total <= 0 {
		return nil
	}
	return &total
}
