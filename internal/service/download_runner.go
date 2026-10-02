package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/Ly233ly/download_video_new/internal/media"
)

// 下载引擎的适配（[05 §4]）。
//
// [internal/media] 的下载器只负责"跑一次直链：取字节 → FFprobe 校验 → 原子交付"，
// 它不知道计划、状态机、重试与事件；那些都在本包。这一层因此**只做字段搬运**，
// 不含任何业务判断（[04 §1.2] 的 G5）。

// NewDownloadRunner 用 [internal/media] 的下载器构造一个 Runner。
//
// 装配层通常不需要显式调用它——[New] 在 Deps.Runner 为空时会用同一路径
// 自建默认引擎。它存在是为了让"想换引擎参数"的装配层有正式入口：
//
//	runner, err := service.NewDownloadRunner(tools, media.Options{DisableRangeResume: true})
//	svc, err := service.New(service.Deps{Store: db, Tools: tools, Runner: runner})
func NewDownloadRunner(tools *media.Toolset, opts media.Options) (Runner, error) {
	engine, err := media.New(tools, opts)
	if err != nil {
		return nil, err
	}
	return &downloadRunner{engine: engine}, nil
}

// downloadRunner 把 Service 的请求/进度/结果与引擎的类型互转。
type downloadRunner struct {
	engine *media.Downloader
}

// Run 执行一次下载并把引擎的进度与错误翻译成本包的形状。
func (r *downloadRunner) Run(
	ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress),
) (DownloadResult, error) {
	engineReq := toEngineRequest(req)

	result, err := r.engine.Run(ctx, engineReq, func(progress media.Progress) {
		if onProgress == nil {
			return
		}
		onProgress(fromEngineProgress(progress))
	})
	if err != nil {
		return DownloadResult{}, translateEngineError(err)
	}

	// 交付完成后文件的实际大小就是它的总长度：这是**已知事实**，不是"长度未知"，
	// 所以这里给出非 nil 的 TotalBytes（[03 §2.1] 的三态里选"已知"那一支）。
	var total *int64
	if result.Bytes > 0 {
		bytes := result.Bytes
		total = &bytes
	}
	return DownloadResult{
		FinalPath:       result.FinalPath,
		DownloadedBytes: result.Bytes,
		TotalBytes:      total,
	}, nil
}

// toEngineRequest 把本包的请求映射成引擎的请求。
func toEngineRequest(req DownloadRequest) media.Request {
	return media.Request{
		PlanID:     req.PlanID,
		URL:        req.MediaURL,
		Headers:    toHTTPHeader(req.Headers),
		OutputName: req.OutputName,
		Container:  req.OutputContainer,
		TempDir:    req.TempDir,
		OutputDir:  req.OutputDir,
		// 0 在引擎侧表示"长度未知"，与本包的三态语义一致（[03 §2.1]）。
		TotalBytes: req.KnownTotalBytes,
	}
}

// toHTTPHeader 把会话上下文交给引擎。它只在本次请求内使用，
// 引擎不持久化、不记录（B-303 / B-722）。
func toHTTPHeader(headers map[string]string) http.Header {
	if len(headers) == 0 {
		return nil
	}
	out := make(http.Header, len(headers))
	for key, value := range headers {
		out.Set(key, value)
	}
	return out
}

// fromEngineProgress 把引擎进度映射成本包的进度。
//
// 引擎用 `Total == 0` 表示"长度未知"，本包用 `TotalBytes == nil`——
// 两者是同一件事的两种写法，这里做唯一一次转换（[03 §2.1]：**不得用 0 表示未知**）。
func fromEngineProgress(progress media.Progress) DownloadProgress {
	out := DownloadProgress{
		DownloadedBytes: progress.Downloaded,
		Phase:           progress.Phase,
		PhaseDetail:     phaseDetailFor(progress.Phase),
	}
	if progress.Total > 0 {
		total := progress.Total
		out.TotalBytes = &total
	}
	return out
}

// phaseDetailFor 给阶段配一句**可直接展示**的短文案（[03 §2.1]）。
//
// 这是 phase_detail 的唯一来源：文案在这里写死，引擎与本包的其他部分都不生成它，
// 因此不可能夹带路径、URL 或秘密（store 层还会再机械校验一次）。
func phaseDetailFor(phase string) string {
	switch phase {
	case media.PhaseMerging:
		return "正在合并音视频"
	case media.PhaseValidating:
		return "正在校验"
	default:
		return "正在下载"
	}
}

// translateEngineError 把引擎的带码错误翻译成本包的 *DownloadError。
//
// 引擎的错误码就是 [05 §7] 的码，两者**不做映射表**——多一张表就多一处漂移源。
// 消息取引擎码表的中文文案（[05 §7.4]：可安全展示，不含路径与 URL）。
func translateEngineError(err error) error {
	var coded *media.Error
	if errors.As(err, &coded) {
		return &DownloadError{
			Code:    string(coded.Code),
			Message: coded.Code.Message(),
			Cause:   err,
		}
	}
	// 不带码的错误（目录参数非法、装配问题）原样上抛：本包的分类逻辑会把它
	// 归为可重试的 download_failed，绝不复用它的文案展示给用户（[12 §3.3] 的 E2）。
	return err
}
