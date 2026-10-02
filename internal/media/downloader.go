package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Ly233ly/download_video_new/internal/logging"
)

// 计划阶段（`plans.phase`，[05 §2]）。只在 `running` 内推进，不改变 `status`。
const (
	// PhaseDownloading：正在取字节。
	PhaseDownloading = "downloading"
	// PhaseMerging：交付落盘。
	//
	// P1 没有 `streamcopy` 合并步骤，所以本包把它用在"把校验过的字节
	// 原子搬进「已完成」"这一刻（[05 §11]）——那正是"让它成为最终文件"的一步。
	// 真正的合并属阶段 3（P3）；P6 复用合并实现时也会复用这个常量。
	PhaseMerging = "merging"
	// PhaseValidating：正在用 FFprobe 校验。
	PhaseValidating = "validating"
)

// 本包的默认取值。**这里只放阶段 2 必需、且权威文档未定义的上限**；
// 已定义的取值一律由调用方传入或读取，不在这里复制：
//   - 重试上限 `retry_max` → [03 §2.4]（调度侧负责，本包不重试）
//   - 进度节流 `progress_throttle_ms` → [03 §2.4]（调用方经 [Options] 传入）
//   - 中转分片/单轨/单计划上限 → [03 §4.6]
const (
	// DefaultToolTimeout 是**单次**工具调用的默认上限。
	//
	// ⚠️ 这是本包自定的兜底值：**05 未定义媒体工具的超时**（[05 §5.2] 只要求
	// "每次调用有超时"），取值待阶段 3 用真实样本核定（[13 §7.4]）。
	// 取 10 分钟的理由：直链本身的时长由调用方 context 决定，
	// 而校验一个已落盘的文件不应比下载它更慢。
	DefaultToolTimeout = 10 * time.Minute

	// ToolWaitDelay 是"强杀之后等子进程回收"的上限（[05 §5.2] 的"等待回收"）。
	ToolWaitDelay = 10 * time.Second

	// DefaultOutputLimit 是工具 stdout/stderr 的默认捕获上限
	// （[05 §5.2]：输出有上限、超限截断）。
	DefaultOutputLimit = 256 * 1024

	// DefaultProgressInterval 是**进程内回调**的默认最小间隔。
	//
	// 与 [03 §2.4] 的 `progress_throttle_ms`（写库节流）是两件事：
	// 那个管"多久写一次数据库"，这个管"多久打扰一次调用方"。
	// 进程内回调比写库便宜，所以默认更密；调用方可用同一参数覆盖。
	DefaultProgressInterval = 100 * time.Millisecond

	// MaxUniqueNameAttempts 是同名冲突时生成唯一名的最大尝试次数。
	// 超过即报 `output_name_exhausted`（[05 §11]："冲突时生成唯一名，过多则报错"）。
	MaxUniqueNameAttempts = 100

	// MaxRedirects 是重定向跳数上限。
	//
	// 取值与 `net/http` 的默认上限一致；显式写出来是因为本包**自带**
	// 重定向策略（[sameHostRedirectOnly]），替换默认策略时容易连上限一起丢掉。
	MaxRedirects = 16

	// downloadChunkSize 是流式写入的分块大小。
	//
	// 必须**分块流式**而不能整文件读进内存：字节在写盘前只过一块缓冲区。
	downloadChunkSize = 256 * 1024

	// stagingName 是 P1 的临时产物名，位于 `临时\<plan_id>\`（[05 §11]）。
	// `main.bin` 在 [05 §11] 里指定给 P6 的单轨中转，P1 用 `direct.bin`
	// 以免两条路径在同一目录里撞名。
	stagingName = "direct.bin"
)

// Request 是一次下载的全部输入。
//
// **媒体 URL 与请求头只在内存：绝不落库、绝不进日志**（B-722/B-303）。
type Request struct {
	PlanID     string
	URL        string
	Headers    http.Header // 页面凭据，只用于本次请求
	OutputName string      // 已按 03 §4.4 清洗
	Container  string
	TempDir    string // ...\临时\<plan_id>\
	OutputDir  string // ...\已完成\
	TotalBytes int64  // 0 表示「长度未知」，不是「长度为零」

	// Kind 是调用方给的**路径提示**（[05 §3.1]、[03 §3.3] 的 `media_kind`）。
	//
	// **零值等于 `direct`**：这样阶段 2 已有的调用方与测试不用改动，
	// 而"没给提示"与"提示是直链"在 [05 §4.0] 第一步里本来就等价。
	// 它只是提示：真实形态由 [directHintMismatch] 在连上之后核实。
	Kind Kind

	// Tracks 是**分离的音视频轨**（[05 §4.6.2] 的 P3）。
	//
	// 长度 ≥ 2 即按 P3 处理：每条轨按 P1 取字节，再 `streamcopy` 合并成一条
	// （[05 §4.6] 的判定表把"输入形态"排在提示之前，所以这条判定不消耗降级机会）。
	// 此时 [Request.URL] 允许为空——地址在每条轨上。
	Tracks []Track

	// QualityLabel 是创建计划时声明的画质档位（[03 §2.1] 的 `quality_label`）。
	//
	// 清单里选流**只看它**（[05 §5.3]："选择依据只来自创建入参"）；
	// 为空表示用户没指定，按"分辨率最高、并列取带宽最高"这条确定规则选。
	QualityLabel string

	// sniffDisabled 关闭"提示不成立就改路"的核实（**包内使用**）。
	//
	// 只有 P3 取单条轨时用得上：那时地址是另一条路径的**产物**，
	// 不是在替调用方的提示做验证，所以不该在轨道上再触发一次改路。
	sniffDisabled bool
}

// Track 是 P3 的一条分离轨道。
//
// 与 [Request] 一样：URL 与请求头**只在内存**（B-303）。
type Track struct {
	Kind       string // video / audio（[03 §2.1.1] 的 stream_plan.track）
	URL        string
	Headers    http.Header
	TotalBytes int64 // 0 = 未声明字节数（[04 §3.3.3] 的 bytes 可省略）
}

// kind 返回本次执行的路径提示；零值按 direct 处理（见 [Request.Kind]）。
func (r Request) kind() Kind {
	if r.Kind == "" {
		return KindDirect
	}
	return r.Kind
}

// wantsDirectSniff 判断这次执行要不要在连上之后核实"提示是不是 direct"。
//
// 只有提示是 direct（或没给提示）时才核实：别的提示本来就该走别的路径
// （[05 §4.0] 第一步），而 P3 的每条轨是一次普通取字节，
// 不该在轨道上再触发一次改路。
func (r Request) wantsDirectSniff() bool {
	return !r.sniffDisabled && r.kind() == KindDirect
}

// Progress 是一次进度快照。
//
// **语义约定**（[05 §4.1]，调用方按此决定界面文案）：
//   - `Total > 0`：可以显示 `Downloaded / Total` 的百分比；
//   - `Total == 0`：长度未知，**不得**伪造百分比，改用阶段语义；
//   - `Phase` 离开 `downloading` 之后的两帧只是阶段标记，
//     它们原样重复最后一帧的字节数（便于单调性检查），不再表示新的下载量。
type Progress struct {
	Downloaded int64
	// Total 是**预期总字节**；0 = 未知，不是"长度为 0"。
	Total int64
	Phase string // downloading / merging / validating

	// TimeRatio 是**按时长口径**的完成比例（0..1）；0 表示"这次没有这个口径"。
	//
	// 存在的唯一理由：[05 §4.6.3] 规定 P2（清单）的进度以**媒体时长**为口径，
	// 而它的 `total_bytes` 通常是 `NULL`——不得用估算值填充，于是调用方
	// 拿 `Downloaded / Total` 算不出百分比。那种情况下改用这个值。
	//
	// 其余路径一律留 0：字节口径与时长口径**不得混算**（[05 §4.6.3]）。
	TimeRatio float64
}

// Result 是一次成功下载的产出。
type Result struct {
	FinalPath string
	Bytes     int64
	Probe     ProbeInfo // FFprobe 实测：容器、时长、各流编码与分辨率
}

// PageHints 是站点适配器给页面解析（P4，[05 §4.2]）的两点提示。
//
// 为什么是**窄契约**而不是把适配器的类型搬进来：本包不依赖 `internal/adapter`
// （[01 §3] 的包划分、A-106：适配器只影响发现与解析）。引擎需要知道的只有
// 两件事，"声明数据长什么样、怎么按主机选出声明"都是适配层的知识。
//
// 两个方法都带页面地址：适配器要靠**主机名**才能选出声明（[14 §5.3]），
// 而页面是请求级的——把地址绑进实现体就得为每个页面造一个提示对象，
// 反而把"无状态的声明查询"变成有状态的东西。
type PageHints interface {
	// CanonicalPageURL 返回页面地址的规范形态（[14 §10] 的 M12）。
	// 没有可用的规范化规则时**必须原样返回入参**。
	CanonicalPageURL(pageURL string) string
	// TranslateResolveError 用适配器声明的 `errors` 翻译解析工具的原始输出
	// （一般是 stderr 原文）。未命中时返回 `ok == false`，调用方按
	// `page_resolve_failed` 处置（[05 §4.2] 的追加条款）。
	//
	// `raw` 是**敏感内容**：它可能整条带着媒体地址与签名参数（B-722 / [12 §3.3]
	// 的 E2/E3）。实现方只允许在内存里读它，不得写进日志、错误文本或任何
	// 持久化位置。
	TranslateResolveError(pageURL string, raw string) (code string, message string, ok bool)
}

// Options 是下载器的可调项；零值即合理默认（逐字段说明见下）。
type Options struct {
	// HTTPClient 用于出站取字节；nil 时用 [DefaultHTTPClient]。
	HTTPClient *http.Client
	// ToolRunner 执行 FFprobe；nil 时用 [ProcessRunner]。
	ToolRunner ToolRunner
	// PageHints 是站点适配器的页面解析提示（[05 §4.2]、[14 §5]）。
	// nil 表示未接线：页面地址不规范化、解析失败一律报 `page_resolve_failed`，
	// 与没有适配器的行为完全一致。
	PageHints PageHints
	// ToolTimeout 是单次工具调用的上限；0 时用 [DefaultToolTimeout]。
	ToolTimeout time.Duration
	// OutputLimit 是工具输出的捕获上限；0 时用 [DefaultOutputLimit]。
	OutputLimit int
	// ProgressInterval 是进度回调的最小间隔；0 时用 [DefaultProgressInterval]。
	ProgressInterval time.Duration
	// DisableRangeResume 关闭 `Range` 断点续传。
	//
	// **零值是"启用续传"**，所以关闭要用这个正向布尔（取反），
	// 而不是 `RangeResume bool`——后者读代码时极易把零值理解成"默认关闭"。
	DisableRangeResume bool
}

// DefaultHTTPClient 返回直链下载用的 HTTP 客户端。
//
// 刻意**不设 Client.Timeout**：直链可能持续几十分钟，固定超时会把大文件
// 误判为失败。超时与取消由调用方的 `context` 承担（[05 §10]、[12 §3.3] 的 E4）。
//
// 刻意**关闭透明解压**：`Content-Length` 是原始字节长度，
// 若传输层自动解压，落盘字节数就与它对不上，会让"断点续传的偏移"
// 与"长度自检"同时失去意义（[05 §4.1] 的两条要求都依赖这个长度）。
//
// 重定向策略见 [sameHostRedirectOnly]——**不能沿用 `net/http` 的默认策略**：
// 实测它按"主机名"判定（`shouldHeaderBeCopiedOnRedirect` 忽略端口），
// `127.0.0.1:8080 → 127.0.0.1:9090` 会被当成同主机，凭据原样带走。
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DisableCompression: true},
		CheckRedirect: sameHostRedirectOnly,
	}
}

// sameHostRedirectOnly 只允许重定向到**与初始请求完全同一个源**（scheme + host + port）。
//
// 这是 B-304 的落实（"[05 §4.2] 页面解析凭据不得转发给不同主机的媒体 CDN"）：
// 媒体 CDN 与本机服务往往只是端口不同，所以"源"必须含端口，
// 否则"同主机不同端口"的重定向仍会把 Cookie/Authorization 送出去。
//
// 发现跨源时**中止重定向并报错**，而不是"去掉凭据继续跟随"：
// 后者会把"用户给的地址其实指向别处"这件事藏起来，用户只看到下载失败；
// 而它更可能是链接被改写或链路被劫持，值得报错而不是静默降级。
func sameHostRedirectOnly(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return errors.New("重定向次数过多")
	}
	origin := via[0].URL
	if !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
		return errors.New("重定向到不同来源，已中止以保护请求凭据")
	}
	return nil
}

// Downloader 执行 P1 直链下载。**必须用 [New] 构造**（依赖在构造期定死）。
//
// 同一个实例可被多个计划并发使用（`http.Client` 本身并发安全）；
// 但**同一个 `plan_id` 的执行体会被串行化**，见 [Downloader.Run]。
type Downloader struct {
	tools      *Toolset
	probePath  string
	ffmpegPath string
	// ytdlpPath 与 denoPath 是 [05 §4.2] 的页面解析工具：yt-dlp 负责解析，
	// Deno 是它解站点脚本挑战时需要的 JS 运行时。
	ytdlpPath string
	denoPath  string
	client    *http.Client
	runner    ToolRunner
	toolWait  time.Duration
	outLimit  int
	progress  time.Duration
	rangeOn   bool
	// pageHints 是站点适配器的页面解析提示；nil = 未接线（见 [Options.PageHints]）。
	pageHints PageHints

	// plans 是每计划的串行锁，见 [Downloader.Run]。用 `sync.Map` 是因为
	// 它的 `LoadOrStore` 不会出现"先查后插"的窗口——那两个窗口之间
	// 恰好能让两个执行体同时进来，正是要防的情况。
	plans sync.Map // planID -> *sync.Mutex
}

// New 构造下载器。
//
// `tools` 允许为 nil——阶段 1 起"缺工具不阻塞启动"（[internal/media] 的 `Resolve`），
// 但 [Downloader.Run] 在需要 FFprobe 时会报错并**拒绝交付**：
// 缺工具不能成为绕开校验（[05 §1]）的理由。
func New(tools *Toolset, opts Options) (*Downloader, error) {
	if opts.ProgressInterval < 0 || opts.ToolTimeout < 0 || opts.OutputLimit < 0 {
		return nil, ErrorOf(CodeDownloadFailed, errors.New("下载器选项不能为负数"))
	}

	client := opts.HTTPClient
	if client == nil {
		client = DefaultHTTPClient()
	}
	runner := opts.ToolRunner
	if runner == nil {
		runner = ProcessRunner{}
	}
	toolTimeout := opts.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = DefaultToolTimeout
	}
	outLimit := opts.OutputLimit
	if outLimit <= 0 {
		outLimit = DefaultOutputLimit
	}
	interval := opts.ProgressInterval
	if interval <= 0 {
		interval = DefaultProgressInterval
	}

	d := &Downloader{
		tools:     tools,
		client:    client,
		runner:    runner,
		toolWait:  toolTimeout,
		outLimit:  outLimit,
		progress:  interval,
		pageHints: opts.PageHints,
		// 零值 = 启用续传（见 Options.DisableRangeResume 的说明）。
		rangeOn: !opts.DisableRangeResume,
	}
	if tools != nil {
		d.probePath = tools.FFprobe.Path
		d.ffmpegPath = tools.FFmpeg.Path
		d.ytdlpPath = tools.YtDlp.Path
		d.denoPath = tools.Deno.Path
	}
	return d, nil
}

// Run 执行一次 P1 直链下载：取字节 → FFprobe 校验 → 原子交付。
//
// 返回的错误一定是带稳定错误码的错误（[*Error]，或站点适配器声明翻译出来的
// [*SiteError]；目录参数非法除外），其 `Error()` 是**可安全展示的中文消息**（[05 §7.4]）。
//
// 失败时的产物处置遵循"无法证明归属的文件永不删除"（[05 §1]）：
//   - `disk_full`、调用方取消、**校验不通过**：清理本次临时产物
//     （[05 §4.1]、[05 §10]；校验不通过的字节已证明是坏的，留着会被下次续传误用）；
//   - 其他失败（网络中断等）：**保留**已下载的分片供重试续传（[05 §4.1]）。
//     它的归属明确（`临时\<plan_id>\`），由启动恢复流程统一清理（[05 §9] 第 3 步）。
//
// **同一个 `plan_id` 会被串行化**：两次并发执行会同时往
// `临时\<plan_id>\direct.bin` 追加，拼出一个两边都不认识的坏文件——
// 而它**能通过结构校验**（容器头还在），于是坏文件被交付。这个后果太重，
// 不能只靠"上层会按信号量调度"的约定（[05 §1] 的并发约束在别的模块）。
// 调用方即使重复提交同一个计划，这里也只会顺序执行两次。
func (d *Downloader) Run(ctx context.Context, req Request, onProgress func(Progress)) (Result, error) {
	if req.PlanID != "" {
		lock := d.planLock(req.PlanID)
		lock.Lock()
		defer lock.Unlock()
	}

	// 关键操作记录耗时，阈值与字段格式复用 [internal/logging] 的既有实现
	// （[12 §4.4]），免得本包再造一套阈值。
	defer logging.Slow("download", "download_run", time.Now())()

	dirs, err := resolveDirs(req)
	if err != nil {
		return Result{}, err
	}

	// 只在创建前记一次"目录是否已存在"，用于结束后回收自己造出来的空目录（[05 §11]）。
	planDirExisted := dirExists(dirs.planDir)
	if mkErr := os.MkdirAll(dirs.planDir, 0o700); mkErr != nil {
		return Result{}, d.fail(req, CodeDownloadFailed, fmt.Errorf("创建任务临时目录失败: %w", mkErr))
	}

	// "失败时删什么"集中在一处判定，比在每个返回点各写一遍更难漏。
	defer func() {
		if err != nil {
			d.cleanupAfterFailure(req, dirs, err, planDirExisted)
		}
	}()

	result, err := d.dispatch(ctx, req, dirs, onProgress)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// dispatch 按**输入形态**选一条路径（[05 §4.0] 第一步 + [05 §4.6] 的判定表）。
//
// 判定顺序不是随意的：先看"是不是两条各自独立的轨道"（[Request.Tracks]），
// 再看提示。因为两条独立轨是**输入形态**，它比提示更硬——[05 §4.0] 明确
// 这条改判**不消耗**那次降级机会。
//
// 这里选出的只是**本次执行的路径**。真去连了之后发现提示不成立，
// 由各路径返回 [RouteMismatch]（P1 见 [directHintMismatch]，P2 见
// [Downloader.resolveManifest]），改不改路、能不能改由调用方按 [05 §4.0]
// 第二步决定——本包不替它降级。
func (d *Downloader) dispatch(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress),
) (Result, error) {
	switch {
	case len(req.Tracks) >= 2:
		return d.executeTracks(ctx, req, dirs, onProgress)
	case req.kind() == KindHLS || req.kind() == KindDASH:
		return d.executeManifest(ctx, req, dirs, onProgress)
	case req.kind() == KindPage:
		return d.executePage(ctx, req, dirs, onProgress)
	default:
		return d.execute(ctx, req, dirs, onProgress)
	}
}

// planLock 返回该计划的串行锁；同一 planID 永远拿到同一把。
//
// 锁**刻意不从表里删除**：`plan_id` 的数量以用户自己的计划数为界
// （[03 §2.1] 的计划是有终态的有限集合），一把 `sync.Mutex` 只有 8 字节，
// 而"删锁"必然引入"释放时另一个执行体正好在等这把锁"的窗口。
func (d *Downloader) planLock(planID string) *sync.Mutex {
	lock, _ := d.plans.LoadOrStore(planID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// execute 是 [Downloader.Run] 的主体：拆出来是为了让上面的清理 defer 只盯一个 err。
//
// 阶段顺序刻意是 downloading → validating → merging，而不是 [05 §2] 列出的
// downloading → merging → validating：本包没有 `streamcopy`，硬凑顺序只会让
// 阶段的含义变成假话。这里 merging 表示"把**已校验**的字节原子搬进「已完成」"，
// 正好落在 [05 §6] 的"校验是交付的唯一门槛"之后——**校验没过就绝不会碰交付目录**。
func (d *Downloader) execute(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress),
) (Result, error) {
	probe, err := d.acquire(ctx, req, dirs, onProgress)
	if err != nil {
		return Result{}, err
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		// 校验期间用户按了停止。此刻还**没有任何东西**落到「已完成」目录，
		// 直接按取消返回最干净：既不会交付（[05 §1]），
		// 也不会把 `canceled` 覆盖成完成（B-308）。
		return Result{}, d.fail(req, CodeDownloadFailed, ctxErr)
	}

	reportPhase(onProgress, PhaseMerging, probe.Size, req.TotalBytes)

	delivered, err := deliverFile(req.OutputDir, req.OutputName, dirs.staging, probe.Size)
	if err != nil {
		// 交付失败时**保留**临时文件：字节已校验通过，重试可直接复用。
		return Result{}, err
	}

	return Result{FinalPath: delivered.Path, Bytes: delivered.Bytes, Probe: probe}, nil
}

// acquire 拿到一个**已通过校验**的本地文件，返回它的实测信息。
func (d *Downloader) acquire(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress),
) (ProbeInfo, error) {
	existing := existingBytes(dirs.staging)

	// 已有分片且长度正好等于预期总长：不重复取字节，直接校验。
	// 这条分支对应**续传的最后一跳**——上次在 `Content-Length` 恰好读满时被中断。
	// 它不构成"跳过校验"：下面仍旧走 [05 §6]。
	if existing > 0 && req.TotalBytes > 0 && existing == req.TotalBytes {
		slog.Info("临时分片已完整，跳过下载",
			"component", "download", "event", "resume_complete", "plan_id", req.PlanID)
		reportProgress(onProgress, Progress{
			Downloaded: existing, Total: req.TotalBytes, Phase: PhaseDownloading,
		})
	} else {
		// 每次尝试都以一帧零进度开场：调用方按进度写库（节流见 [03 §2.4] 的
		// `progress_throttle_ms`），不给它这一帧的话，上一轮尝试的旧值会一直挂着。
		reportProgress(onProgress, Progress{Total: req.TotalBytes, Phase: PhaseDownloading})
		if err := d.downloadDirect(ctx, req, dirs, existing, onProgress); err != nil {
			return ProbeInfo{}, err
		}
	}

	probe, err := d.validate(ctx, req, dirs, onProgress)
	if err != nil {
		return ProbeInfo{}, err
	}
	return probe, nil
}

// downloadDirect 是 [05 §4.1] 的 P1 主流程。
func (d *Downloader) downloadDirect(
	ctx context.Context, req Request, dirs workDirs, existing int64, onProgress func(Progress),
) error {
	// 只有"分片严格短于预期总长"才敢续传。分片比预期还长说明这次的预期值
	// 与生成分片时的那次不是同一个文件（远端换了内容），此时必须从头取。
	resumeAt := int64(0)
	if d.rangeOn && existing > 0 && (req.TotalBytes <= 0 || existing < req.TotalBytes) {
		resumeAt = existing
	}

	resp, err := d.send(ctx, req, resumeAt)
	if err != nil {
		return d.fail(req, CodeDownloadFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// total 是**本次结束后分片应有的总长**；0 表示未知（[05 §4.1] 的进度语义）。
	total := req.TotalBytes
	appendMode := false

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// 206：只有确实要了 Range 才接受，且必须从请求的偏移开始——
		// 偏移不符说明服务端给了另一段字节，追加会拼出一个坏文件。
		if resumeAt <= 0 {
			return d.fail(req, CodeDownloadFailed, errors.New("未请求分段却收到 206 响应"))
		}
		start, size, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != resumeAt {
			return d.fail(req, CodeDownloadFailed, errors.New("206 响应的 Content-Range 与请求偏移不符"))
		}
		appendMode = true
		switch {
		case size > 0:
			total = size
		case resp.ContentLength >= 0:
			total = resumeAt + resp.ContentLength
		default:
			total = req.TotalBytes
		}

	case http.StatusOK:
		// 200：全量。首次请求与服务端**不支持/忽略 Range** 都走这里，
		// 两种情况都从头写（[05 §4.1] 的"服务器不支持时从头下载"）。
		if resp.ContentLength >= 0 {
			total = resp.ContentLength
		} else {
			// 无 `Content-Length`：预期总长回到"未知"，进度只报阶段语义。
			total = 0
		}

	case http.StatusRequestedRangeNotSatisfiable:
		// 416：范围无效，回退全量（[05 §4.1]）。
		return d.restartFromScratch(ctx, req, dirs, onProgress,
			errors.New("服务端拒绝了请求的字节范围（416）"))

	default:
		return d.fail(req, CodeDownloadFailed, statusCause(resp.StatusCode))
	}

	if !appendMode {
		resumeAt = 0
	}

	// [05 §4.0] 第一步：调用方说"这是一条自足媒体地址"。真去连的时候，形态可能不是。
	//
	// 只在**磁盘上还没有字节**时核实（`existing == 0 && resumeAt == 0`）——这正是
	// §4.0 第二步那条"必须在尚未写入任何字节之前"的纪律。
	//
	// 为什么连上次剩下的分片也算数：那些字节说明**这条直链本来能下**，
	// 此时改路等于把已经下到的部分当成新路径的分片。宁可这次失败去走 §8 的重试，
	// 也不要在有进展的情况下换路。
	//
	// 返回 [*RouteMismatch] 时**不记失败日志**：它不是失败，是同一次执行内的
	// 改路信号（B-315：降级不增加 `attempt_count`）。改不改、能不能改由调用方按
	// §4.0 第二步决定，本包只如实报告观察到的形态。
	if existing == 0 && resumeAt == 0 && req.wantsDirectSniff() {
		if mismatch := directHintMismatch(resp); mismatch != nil {
			return mismatch
		}
	}

	// 打开临时文件：206 走追加，其余截断重写。
	flags := os.O_WRONLY | os.O_CREATE
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(dirs.staging, flags, 0o600)
	if err != nil {
		return d.fail(req, CodeDownloadFailed, fmt.Errorf("打开临时文件失败: %w", err))
	}

	written, copyErr := streamTo(resp.Body, file, resumeAt, total, d.progress, onProgress)
	closeErr := file.Close()
	if copyErr != nil {
		return d.mapWriteError(req, copyErr)
	}
	if closeErr != nil {
		return d.mapWriteError(req, fmt.Errorf("关闭临时文件失败: %w", closeErr))
	}

	// 响应声明了长度、实际却读少了：说明连接被截断。
	// 此时**保留**分片——它就是下次续传的起点（[05 §4.1]）。
	//
	// 只在"读少了"时报错：`resp.Uncompressed` 为真表示传输层做过透明解压，
	// 那时 `Content-Length` 说的是压缩后长度，"读多了"是正常的。
	// 反向的要求（必须读满 `Content-Length`）不成立——服务端本来就允许发更少。
	if resp.ContentLength >= 0 && written < resp.ContentLength {
		return d.fail(req, CodeDownloadFailed,
			fmt.Errorf("响应体长度不足：声明 %d 字节，实得 %d 字节", resp.ContentLength, written))
	}

	slog.Info("直链下载完成",
		"component", "download", "event", "direct_downloaded",
		"plan_id", req.PlanID, "bytes", written)
	return nil
}

// restartFromScratch 是 416 的回退：丢掉已下载的分片，无条件全量重取
// （[05 §4.1] 的"416 范围无效时回退全量"）。
//
// **必须先删掉分片再重发**：只把 Range 去掉的话，磁盘上那段字节仍会被
// 后续逻辑当作续传起点。
func (d *Downloader) restartFromScratch(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress), cause error,
) error {
	slog.Info("范围请求无效，回退全量下载",
		"component", "download", "event", "range_fallback_full",
		"plan_id", req.PlanID, "cause", cause.Error())

	removeOwnedFile(dirs.staging)

	fallback := req
	// 回退后预期长度回到"未知"：把上一轮的预期值留着会误导进度
	// （服务端可能连 `Content-Length` 都不给）。真正的值从新响应的头里取。
	fallback.TotalBytes = 0
	return d.downloadDirect(ctx, fallback, dirs, 0, onProgress)
}

// send 发一次可能带 Range 的请求（resumeAt <= 0 时不带 Range）。
//
// 请求头来自 [Request.Headers]（页面凭据）**只用于本次请求**；
// 本函数不记录、不返回、不持久化它们（B-303 / B-722）。
func (d *Downloader) send(ctx context.Context, req Request, resumeAt int64) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		// 刻意不把 err 往上带：它的文本含完整 URL（[12 §3.3] 的 E3）。
		return nil, errors.New("构造下载请求失败")
	}
	for key, values := range req.Headers {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}
	if resumeAt > 0 {
		httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeAt))
	}

	resp, err := d.client.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// `net/http` 的错误文本内嵌完整 URL（常含签名参数），
		// 因此这里只保留错误的**类型**，绝不带上原始文本（B-303）。
		return nil, fmt.Errorf("网络请求失败: %w", scrubURLError(err))
	}
	return resp, nil
}

// mapWriteError 把写入/关闭失败映射成错误码：磁盘满是**独立错误码**，
// 且不自动重试（[05 §4.1]）；其余算可重试的下载失败。
func (d *Downloader) mapWriteError(req Request, err error) error {
	if isDiskFull(err) {
		return d.fail(req, CodeDiskFull, err)
	}
	return d.fail(req, CodeDownloadFailed, err)
}

// validate 用 FFprobe 校验已落盘的临时文件（[05 §6]）。
//
// **本阶段只做结构校验**（有无可用流、所选轨道是否齐备）。时长容差阈值
// 必须在阶段 3 用真实样本测定（[05 §6.1] 的 `N1`），所以本阶段**不写死任何阈值**，
// 只保留 [Downloader.validateDuration] 接口与 `output_duration_mismatch` 码。
func (d *Downloader) validate(
	ctx context.Context, req Request, dirs workDirs, onProgress func(Progress),
) (ProbeInfo, error) {
	// 校验阶段的进度帧沿用最后一帧的字节数（见 [reportPhase]）。
	reportPhase(onProgress, PhaseValidating, existingBytes(dirs.staging), req.TotalBytes)

	info, err := d.probe(ctx, dirs.staging)
	if err != nil {
		return ProbeInfo{}, d.fail(req, CodeDownloadFailed, err)
	}

	// 到了这一步容器里已经有可解析的内容，但可能不是我们承诺交付的轨道组合。
	if info.StreamCount == 0 || (!info.HasVideo && !info.HasAudio) {
		return ProbeInfo{}, d.fail(req, CodeOutputNoStreams,
			errors.New("FFprobe 未在输出中找到任何音视频流"))
	}

	// P1 交付的是"用户所选的那一条流"。是否必须有视频/音频由**输出容器**决定：
	// 纯音频容器（m4a/mp3）要求音频流，其余要求视频流；
	// 视频容器里没有音轨不算失败（无声视频是合法媒体）。
	if wantsVideo(req.Container) && !info.HasVideo {
		return ProbeInfo{}, d.fail(req, CodeOutputNoVideo, errors.New("输出中没有视频流"))
	}
	if wantsAudio(req.Container) && !info.HasAudio {
		return ProbeInfo{}, d.fail(req, CodeOutputNoAudio, errors.New("输出中没有音频流"))
	}

	// 时长校验：接口已就位，阈值等 `N1`（[05 §6.1]）。
	if err := d.validateDuration(req, info); err != nil {
		return ProbeInfo{}, err
	}
	return info, nil
}

// validateDuration 是时长校验的**预留接口**（[05 §6.1]）。
//
// 现在恒返回 nil，且刻意**不在代码里写任何阈值**：`N1` 明确要求阈值在阶段 3
// 用真实样本测定、"不得凭猜测写死"。等 `N1` 落定后在这里用实测容差
// 对比预期时长，不符即返回 `output_duration_mismatch`。
//
// 参数已经够用（`req` 将来承载预期时长、`actual` 是 FFprobe 实测），
// 阶段 3 只需补一个来源与一次比较，不用改签名。
func (d *Downloader) validateDuration(_ Request, _ ProbeInfo) error { return nil }

// fail 记一条失败事件并返回带码错误。
func (d *Downloader) fail(req Request, code Code, cause error) error {
	logFailure(code, req.PlanID, cause)
	return ErrorOf(code, cause)
}

// reportProgress 安全地回调进度：`onProgress` 允许为 nil（调用方不关心进度时）。
func reportProgress(onProgress func(Progress), p Progress) {
	if onProgress != nil {
		onProgress(p)
	}
}

// reportPhase 报一帧纯阶段标记。
//
// 刻意**重复最后一帧的字节数**而不清零：`Downloaded` 对调用方是单调量
// （它直接写进 `plans.downloaded_bytes`），中途归零会让界面进度条倒退。
// 也刻意**不改写 Total**：总长只由服务端的响应决定，阶段推进不产生新的长度事实。
func reportPhase(onProgress func(Progress), phase string, downloaded, total int64) {
	reportProgress(onProgress, Progress{Downloaded: downloaded, Total: total, Phase: phase})
}
