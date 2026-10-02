package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/Ly233ly/download_video_new/internal/adapter"
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
//
// pageHints 是站点适配器给页面解析的提示（[14 §5]、[05 §4.2]）；nil 表示没有
// 可用声明，引擎退回通用行为。
func newPlanRunner(tools *media.Toolset, db *store.DB, pageHints media.PageHints) (*planRunner, error) {
	engine, err := media.New(tools, media.Options{
		HTTPClient: systemProxyClient(),
		PageHints:  pageHints,
	})
	if err != nil {
		return nil, err
	}
	return &planRunner{engine: engine, store: db}, nil
}

// adapterDirName 是随程序分发的适配器目录名（[14 §5.3]：扫描它下面的一级子目录）。
const adapterDirName = "adapters"

// loadAdapters 加载程序安装目录下的站点适配器，并把它适配成 media 层的页面提示。
//
// 位置按 [14 §4] 第 114 行——"桌面端（Go）能读**程序安装目录**的 `adapters/`"：
// 以 `os.Executable()` 所在目录为基准（媒体工具用的也是同一个基准，D7）。
//
// **加载失败不终止启动**（[14 §5.3] 的追加条款）：适配器只影响发现与解析
// （A-106），程序仍然应该能打开；失败以可见警告呈现，与媒体工具缺失同口径
// ——只写进 core.warnings，然后由 UI/日志展示（README 设计原则：不静默降级）。
// 返回 nil 表示"没有可用声明"，引擎于是走 [05 §4.2] 的通用失败码。
func (c *Core) loadAdapters(exePath string) media.PageHints {
	hints, warning := loadPageHintsFrom(filepath.Join(filepath.Dir(exePath), adapterDirName))
	if warning != "" {
		c.warnings = append(c.warnings, warning)
	}
	return hints
}

// loadPageHintsFrom 是加载步骤的可测核心：给定适配器根目录，返回提示实现
// 与一句可展示的降级说明（空串 = 一切正常，没有任何降级）。
//
// 为什么把"警告文案"和日志一起在这里产生：A-116 要求加载失败**必须报错并指出
// 文件路径**，路径只出现在 `adapter.Load` 的错误里——这里把它同时交给日志和
// 用户可见的警告，才不会在某一侧丢掉定位信息。
func loadPageHintsFrom(root string) (media.PageHints, string) {
	set, err := adapter.Load(root)
	if err != nil {
		// err 里带着目录/文件路径（A-116），两个出口都用它的原文。
		slog.Warn("站点适配器不可用", "component", "adapter", "event", "adapters_load_failed", "err", err)
		return nil, err.Error()
	}
	// 只报数量：适配器 id 列表会随分发内容变化，逐条铺开没有诊断价值（[12 §3.3]）。
	slog.Info("站点适配器已就绪", "component", "adapter", "event", "adapters_ready", "count", set.Len())
	return adapterHints{set: set}, ""
}

// adapterHints 把站点适配器集合适配成 media 层的页面解析提示（[14 §5] 的
// `resolve` 与 `errors`；桌面侧形态见 [14 §6.2]——声明数据 + 引擎，不引入插件）。
//
// 为什么提示接口带页面地址：适配器要靠**主机名**才能选出声明（[14 §5.3] 的
// 优先级顺序），主机名又只存在于页面地址里——把地址绑进实现体就得为每个页面
// 造一个提示对象，反而把"无状态的声明查询"变成有状态的东西。
type adapterHints struct {
	set *adapter.Set
}

// CanonicalPageURL 用声明的 `identity.canonical` 模板与 `identity.urlRules`
// 拼出规范地址（[14 §10] 的 M12）。
//
// 三道防线，缺一条都可能让坏声明吞掉好地址——真出事时用户看到的是"视频解析
// 失败"，而原因只是一条模板写错了：
//   - 适配器**明确声明了** `resolve.canonicalizePageUrl` 才动手；
//   - `ResolveID` 至少要解出一个 id（解不出就是这套规则认不出这个地址）；
//   - `canonical` 模板里必须有 `{id}`——加载期已按契约 A-119 校验过（模板含
//     `{id}`、主机在 `match.hosts` 之内），这里再挡一次是"坏声明不许吞掉好地址"
//     的第二道防线：本函数是**唯一**能把地址改掉的地方，宁可退回原地址。
//
// 任何一步不成立都原样返回入参：调用方据此把地址原样交给解析工具。
func (h adapterHints) CanonicalPageURL(pageURL string) string {
	if h.set == nil || pageURL == "" {
		return pageURL
	}
	decl := h.set.Match(hostOf(pageURL))
	if decl == nil || decl.Resolve == nil || !decl.Resolve.CanonicalizePageURL {
		return pageURL
	}
	if decl.Identity == nil || !strings.Contains(decl.Identity.Canonical, "{id}") {
		return pageURL
	}
	id, ok := decl.ResolveID(pageURL)
	if !ok || id == "" {
		return pageURL
	}
	return strings.Replace(decl.Identity.Canonical, "{id}", id, 1)
}

// TranslateResolveError 用声明的 `errors` 翻译解析工具的原始输出
// （[05 §4.2] 的追加条款）。
//
// `raw`（工具 stderr 的原文）只在这里被内存匹配一次：声明的 `match` 是短标记
// 串，本层既不记录它、也不把它放进返回值——返回值只有声明里的码与文案。
// 未命中返回 ok = false，调用方仍报 `page_resolve_failed`。
func (h adapterHints) TranslateResolveError(pageURL string, raw string) (string, string, bool) {
	if h.set == nil || raw == "" {
		return "", "", false
	}
	decl := h.set.Match(hostOf(pageURL))
	if decl == nil {
		return "", "", false
	}
	return decl.MapError(raw)
}

// hostOf 取出页面地址的主机名，供 [adapter.Set.Match] 选择声明。
//
// 解析不出来时返回空串：空主机名在匹配里既不是精确域名也不是通配，只会落到
// 兜底的 `generic`——那正是"认不出这个地址"时应有的结果（[14 §5.3]）。
func hostOf(pageURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
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
//
// 网络/工具错误之外的第三类：站点适配器声明翻译出来的错误（[05 §4.2] 的追加
// 条款）。它的码与文案都来自随程序分发的声明数据，必须**原样透传**——退化成
// `download_failed` 会让"需要人工处理"变成"值得自动重试"（[05 §7.1] 的反面）。
func adaptDownloadError(err error) error {
	var coded *media.Error
	if errors.As(err, &coded) {
		return &service.DownloadError{
			Code:    string(coded.Code),
			Message: coded.Code.Message(),
			Cause:   coded,
		}
	}
	var site *media.SiteError
	if errors.As(err, &site) {
		return &service.DownloadError{
			Code:    string(site.Code),
			Message: site.Message,
			Cause:   site,
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
