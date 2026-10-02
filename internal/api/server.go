package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Ly233ly/download_video_new/internal/platform"
)

// 编译期断言：本包依赖的 Service 方法集（接口定义与断言说明见 handlers.go）。
//
// 方法签名逐条来自交付说明里的清单；`PlanOpen` / `SourceReport` 尚不在清单中，
// 因此 `handlePlanOpen` 与 `handleSource` 目前返回 501（见 handlers.go）。
//
// Options 是本地 API 的装配参数。
type Options struct {
	// Service 是**唯一业务入口**（[04 §1.2]）。为 nil 属装配错误：启动期直接失败，
	// 不留一个所有端点都返回 500 的半成品（[12 §3.2] 的分类：编程错误在启动期暴露）。
	//
	// 类型是方法集接口而不是 `*service.Service`：生产装配传的就是
	// `*service.Service`（隐式满足，见 handlers.go 的断言），测试可以注入替身。
	Service serviceAPI

	// OriginWhitelist 是编译期内置的白名单值（[01 §2.1]、[07 §1.1]）。
	//
	// 留成字段而不是直接读常量，只有两个用途：
	//   - 测试注入（[12 §5] 要求覆盖白名单放行与拒绝，而测试不该依赖某个固定的扩展 ID）；
	//   - 将来若产品改为多 ID（例如 Edge 商店另发一份），只需在上层改一处。
	//
	// 为空时回退到 `DefaultExtensionOrigin`。
	OriginWhitelist string

	// MainPort 覆盖主端口，**仅供测试注入**。
	//
	// 为什么必须可注入：端口回退测试（[12 §5] 要求覆盖"端口被占用时回退临时端口"）
	// 需要先占住一个端口；如果测试一律去占真实的 `47652`，多个测试并行或与
	// 本机正在运行的实例就会互相干扰，测试结果将依赖环境。
	//
	// 0 表示使用编译常量 `MainPort`——生产路径**永远**走默认值。
	MainPort int

	// Logger 允许注入 logger（[12 §4] 的字段约定由本包保证）。
	// 为空时使用 `slog.Default()`——正常路径下由 internal/logging 在启动期装好。
	Logger *slog.Logger
}

// Server 是本地回环 HTTP API。
//
// 生命周期与并发：`New` 只装配不监听；`Start` 监听并写端口文件；
// `Stop` 优雅关闭并清理端口文件。三个方法都可以从不同 goroutine 调用，
// 内部用互斥锁串行化状态迁移。
type Server struct {
	service  serviceAPI
	origin   string
	mainPort int
	logger   *slog.Logger
	handler  http.Handler
	// routeMethods 是"路径 → 该路径接受的方法集合"，由路由表构建一次
	// （见 routes.go）。它服务两件事：日志用的路径白名单，以及 405 的
	// `Allow` 头——两处都不该各自维护一份路由清单。
	routeMethods map[string]map[string]struct{}
	listener     net.Listener
	http         *http.Server

	mu      sync.Mutex
	started bool
	stopped bool
	port    int
	serve   chan error
	// stopOnce 保证 Stop 可重复调用而不重复关闭 listener（[01 §5.2]：
	// 关闭路径可能被正常退出与异常回收两条路同时走到）。
	stopOnce sync.Once
}

// New 装配服务器但**不监听**（[01 §5.1] 的第 5 步才启动本地 API）。
//
// 白名单在这里解析一次：Origin 是**进程级**的配置（[01 §2.1]：临时调试时才覆盖），
// settings 表又是配置的唯一权威源（[03 §2.4]）。启动时读一次的好处是
// 认证路径完全不依赖数据库——数据库暂时不可用时，Origin 判定仍然成立。
//
// 解析覆盖值的失败**不终止启动**：回退到编译常量并记 Warn。
// 理由与 [03 §2.4] 的"解码失败必须回退到默认值，不得中断启动"一致——
// 一个调试用的覆盖值写坏了，不该让整个程序起不来。
func New(opts Options) (*Server, error) {
	if opts.Service == nil {
		return nil, errors.New("本地 API 需要 Service 依赖")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	origin := strings.TrimSpace(opts.OriginWhitelist)
	if origin == "" {
		origin = DefaultExtensionOrigin
	}

	mainPort := opts.MainPort
	if mainPort <= 0 {
		mainPort = MainPort
	}

	s := &Server{
		service:  opts.Service,
		origin:   resolveOrigin(opts.Service, origin, logger),
		mainPort: mainPort,
		logger:   logger,
	}
	s.handler = s.build()
	return s, nil
}

// resolveOrigin 处理 `settings.extension_origin` 的开发调试覆盖（[04 §2.2]：
// "白名单值默认来自编译常量；`settings.extension_origin` 仅作为开发调试的覆盖值"）。
//
// 空值表示"没有覆盖"，此时用编译常量——[03 §2.4] 里该键的默认值就是空串。
func resolveOrigin(svc serviceAPI, fallback string, logger *slog.Logger) string {
	// 启动期读设置必须有自己的预算，不能靠调用方的 context（此处还没有）。
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	override, err := svc.Setting(ctx, ExtensionOriginSettingKey, "")
	if err != nil {
		logger.Warn("读取扩展 Origin 覆盖值失败，使用编译常量",
			"component", "api", "event", "origin_override_failed", "code", CodeInternalError)
		return fallback
	}
	if strings.TrimSpace(override) == "" {
		return fallback
	}
	return strings.TrimSpace(override)
}

// Start 开始监听，并按 [01 §2.1.1] 的 S4 写入端口发现文件；返回实际监听地址。
//
// 顺序**不可调换**：
//
//  1. 绑 `127.0.0.1:47652`（S1：不得是 `:47652`，也不得是 `localhost`）；
//  2. 被占用则回退**临时端口**（S4）；
//  3. 拿到实际端口后**立刻**把监听放进服务循环；
//  4. 最后才写 `platform.APIPortPath()`——S4 明确"写入时机是服务开始监听之后"。
//     早写会让扩展拿到一个还没人在听的端口。
//
// 第 3 步与第 4 步之间不需要额外同步：`net.Listener` 一被创建，内核就已经把
// 连接排进 backlog，此后 `Serve` 是"取走"而不是"开始接收"。
func (s *Server) Start(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return "", errors.New("本地 API 已停止，不能重新启动")
	}
	if s.started {
		s.mu.Unlock()
		return "", errors.New("本地 API 已在监听")
	}
	s.mu.Unlock()

	listener, fallback, err := listen(s.mainPort)
	if err != nil {
		s.logger.Error("本地 API 监听失败",
			"component", "api", "event", "listen_failed", "code", CodeInternalError)
		return "", err
	}

	addr := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port

	httpServer := &http.Server{
		Handler: s.handler,
		// 超时按 [01 §2.1.1] 的 S5 配置：读头 5–10 s、空闲 60–120 s，
		// 而 `ReadTimeout` / `WriteTimeout` **保持 0**——需要闲置上限时用
		// `http.ResponseController` 续期，而不是给整条连接设死写超时。
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
		// 显式置 0 并写明理由：这两个字段将来被"顺手补上"就会破坏 S5。
		ReadTimeout:  0,
		WriteTimeout: 0,
		ErrorLog:     nil, // 用 net/http 的默认 stderr 日志，不接管
	}

	serve := make(chan error, 1)
	s.mu.Lock()
	if s.stopped {
		// 极端交错：Start 与 Stop 并发。此时还没写入任何状态，直接收摊。
		s.mu.Unlock()
		_ = listener.Close()
		return "", errors.New("本地 API 已停止，不能重新启动")
	}
	s.listener = listener
	s.http = httpServer
	s.port = port
	s.serve = serve
	s.started = true
	s.mu.Unlock()

	// 服务循环是**有主**的 goroutine：错误经带缓冲的 channel 回收（[12 §9] 的 C1/C3），
	// 不吞错也不泄漏。
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serve <- err
		close(serve)
	}()

	if err := s.writePortFile(port); err != nil {
		// 端口文件是扩展发现本 API 的**唯一**途径（S4）。写不进去就没人找得到它，
		// 于是一边监听一边不可用——保留一个没人能连的服务没有意义，直接回收。
		_ = httpServer.Close()
		<-serve
		s.mu.Lock()
		s.started = false
		s.listener, s.http, s.serve = nil, nil, nil
		s.mu.Unlock()
		s.logger.Error("写入端口发现文件失败",
			"component", "api", "event", "port_file_failed", "code", CodeInternalError)
		return "", err
	}

	s.logger.Info("本地 API 已监听",
		"component", "api", "event", "api_listening",
		"addr", addr, "port", port, "fallback", fallback)

	if ctx != nil {
		// 上层若提前取消（例如启动期间决定退出），跟着收摊。
		go func() {
			<-ctx.Done()
			stopCtx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
			defer cancel()
			_ = s.Stop(stopCtx)
		}()
	}

	return addr, nil
}

// Stop 优雅关闭并清理端口发现文件（[01 §5.2] 的第 4 步、[01 §5.3] 的 3 s 预算）。
//
// 三件事的顺序：先关监听（不再接新请求）→ 等 `Shutdown` 收完在途请求 →
// 删端口文件。端口文件**最后**删：先删会让扩展在关机过程中"发现不到"，
// 而删完再关又会让它连上一个正在关闭的端口。
//
// 可重复调用（`sync.Once`），并且即使 `Shutdown` 超预算也继续删文件——
// 留着端口文件会让扩展在整个下次启动期间连一个死端口（B-214 的重探会一直失败）。
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	started := s.started
	httpServer := s.http
	serve := s.serve
	s.stopped = true
	s.started = false
	s.listener, s.http, s.serve = nil, nil, nil
	s.mu.Unlock()

	var err error
	s.stopOnce.Do(func() {
		if ctx == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), ShutdownTimeout)
			defer cancel()
		}

		if httpServer != nil {
			if shutdownErr := httpServer.Shutdown(ctx); shutdownErr != nil {
				// 超预算即关闭连接，不阻塞退出（[01 §5.3]）。
				err = shutdownErr
				s.logger.Warn("本地 API 未在预算内优雅关闭",
					"component", "api", "event", "shutdown_timeout",
					"code", CodeInternalError, "err", shutdownErr)
				_ = httpServer.Close()
			}
			if serve != nil {
				<-serve // 等 Serve 返回，确保没有残留的连接处理 goroutine
			}
		}

		if cleanupErr := removePortFile(); cleanupErr != nil {
			// 删不掉只记日志：退出流程不得被它拖住（[01 §5.2]）。
			s.logger.Warn("清理端口发现文件失败",
				"component", "api", "event", "port_file_cleanup_failed",
				"code", CodeInternalError)
		}
	})

	if started {
		s.logger.Info("本地 API 已停止", "component", "api", "event", "api_stopped")
	}
	return err
}

// Addr 返回实际监听地址；未监听时为空串。
//
// 单独暴露而不复用 `Start` 的返回值：上层装配代码可能先记下 Server，
// 稍后再取地址（[01 §2.1.1] 的 S2 要求 `*http.Server` 存入 app 结构体）。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// portFile 是端口发现文件的内容（[01 §2.1.1] 的 S4）。
//
// 字段名与顺序照规范写死：`{"port":47652,"pid":1234}`。
// `pid` 不是装饰——扩展按它校验进程仍在（S4），因此它必须是**当前进程**的 pid。
type portFile struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

// listen 按 S4 绑定端口：先试主端口，**失败则回退临时端口**。
//
// 返回的第二个值表示是否发生了回退（只用于日志）。
//
// 网络类型写死 `"tcp4"`：字面量已经是 `127.0.0.1`（IPv4），显式限定协议族
// 能保证**绝无可能**落到 `::1` 或 `0.0.0.0`——T-STB-01 验收看的就是这一点，
// 不让它依赖系统对 `127.0.0.1` 的解析行为。
//
// **为什么不去精确判定"端口被占用"**：直觉写法是
// `errors.Is(err, syscall.EADDRINUSE)`，但它在 Windows 上**恒为 false**
// （实测：Winsock 报的是 `WSAEADDRINUSE` = 10048，而 Go 的
// `syscall.EADDRINUSE` = 0x20000002，两者数值不同），按它判定会让回退
// **永不触发**——这个 bug 正是被本包的测试捕获的。
//
// 精确判定需要引 `golang.org/x/sys/windows` 并对错误号特判，而收益只是
// "区分失败原因"。改为**无条件回退**：
//
//   - 端口被占用 → 正是要回退的情形；
//   - 其他绑定失败（权限、地址不可用）→ 回退到临时端口同样会失败，
//     此时返回的错误同时带上两次失败的信息，诊断信息**更多**而不是更少。
//
// 失败的那次尝试会由调用方记 Warn 日志，原因不会被吞掉（[12 §3.3] 的 E1）。
func listen(mainPort int) (net.Listener, bool, error) {
	primary := net.JoinHostPort(LoopbackHost, strconv.Itoa(mainPort))
	listener, primaryErr := net.Listen("tcp4", primary)
	if primaryErr == nil {
		return listener, false, nil
	}

	// 回退：端口 0 让内核分配一个空闲的临时端口（S4 的"回退临时端口"）。
	fallbackAddr := net.JoinHostPort(LoopbackHost, "0")
	listener, fallbackErr := net.Listen("tcp4", fallbackAddr)
	if fallbackErr != nil {
		return nil, false, fmt.Errorf("绑定主端口失败（%v），回退临时端口亦失败: %w", primaryErr, fallbackErr)
	}
	return listener, true, nil
}

// writePortFile 写入端口发现文件（[01 §2.1.1] 的 S4）。
//
// 路径来自 `platform.APIPortPath()`，**不在本包拼路径**（[01 §8] 的布局是
// platform 包的职责，且 `%LOCALAPPDATA%` 的解析在 Windows 上有细节）。
//
// 目录不存在时**由本函数创建**，不假设别人建过：数据目录在首次启动、
// 用户清理过 `%LOCALAPPDATA%`、或测试注入临时目录时都可能缺失，
// 而"文件写不进 → 本地 API 无可发现"是启动期硬失败。这与 `store.Open`
// 自己 `MkdirAll` 的做法一致。
//
// 先写临时文件再改名：扩展可能恰好在写入过程中读取，读到半截 JSON 会让它
// 直接放弃发现（B-214）。`os.Rename` 在同目录内是原子的。
func (s *Server) writePortFile(port int) error {
	path, err := platform.APIPortPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}

	payload, err := json.Marshal(portFile{Port: port, PID: os.Getpid()})
	if err != nil {
		return fmt.Errorf("编码端口发现文件失败: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, portFileMode); err != nil {
		return fmt.Errorf("写入端口发现文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // 改名失败时不留垃圾
		return fmt.Errorf("提交端口发现文件失败: %w", err)
	}
	return nil
}

// removePortFile 删除端口发现文件。
//
// **只在文件属于本进程时才删**：`pid` 不匹配说明那是另一个实例写的文件，
// 删它会破坏那个实例的发现（[01 §9] 的 N7：不得删除或修改归属不明的文件）。
// 读不出来（文件不存在、内容损坏）时按"属于本进程"处理——残留的坏文件
// 比"扩展连不上"更糟。
func removePortFile() error {
	path, err := platform.APIPortPath()
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var record portFile
	if err := json.Unmarshal(raw, &record); err == nil && record.PID != 0 && record.PID != os.Getpid() {
		// 归属他人：保留。
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
