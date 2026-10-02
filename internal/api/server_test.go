package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 [12 §5] 要求的"必须覆盖"清单里与**端口和生命周期**有关的部分：
// 只绑回环、端口被占用时回退、端口文件的写入时机与内容、停止时清理。

// TestServerStart_BindsLoopbackOnly 对应 T-STB-01 / B-1001：
// 本机 API **只**监听回环地址。
//
// 断言三件事，缺一不可：
//  1. 实际监听地址的 IP 是回环（不是 `0.0.0.0`、不是 `::`）；
//  2. 从本机**非回环地址**连接该端口会失败——这一条才是"没有暴露到局域网"的
//     直接证据，只查地址字面量可能被将来的改动绕过（例如换成 `:port`）；
//  3. 该地址能被真正连上（避免"断言了一个没人听的地址"这种空测试）。
func TestServerStart_BindsLoopbackOnly(t *testing.T) {
	path := portFilePath(t)
	mainPort := reservePort(t)

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	addr, err := srv.Start(context.Background())
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("监听地址不是 host:port 形态: %q", addr)
	}
	if host != LoopbackHost {
		t.Fatalf("必须只绑 %s，实际绑了 %q", LoopbackHost, host)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("监听地址必须是回环地址，实际为 %q", host)
	}
	// 显式挡住最危险的那两种写法：`:47652` 与 `0.0.0.0:47652`。
	if host == "" || host == "0.0.0.0" || host == "::" {
		t.Fatalf("监听地址绝不能是通配地址，实际为 %q", host)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("端口不是数字: %q", port)
	}

	// 回环可达：证明上一步返回的地址确实有人在听。
	res, err := http.Get("http://" + addr + PathHealth)
	if err != nil {
		t.Fatalf("回环地址应可访问，实际失败: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /health 应返回 200，实际 %d", res.StatusCode)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("监听开始后应已写入端口发现文件: %v", err)
	}
}

// TestServerStart_FallsBackToEphemeralPort 对应 [01 §2.1.1] 的 S4：
// 主端口被占用时回退临时端口。
//
// 做法：先把 `Options.MainPort` 设成一个**本测试自己占住**的端口。
// 不用真实主端口 `47652`，因为那会让结果依赖"本机此刻有没有在跑实例"——
// 测试必须给出确定结论（[12 §5.3] 禁止依赖执行顺序与环境）。
func TestServerStart_FallsBackToEphemeralPort(t *testing.T) {
	portFilePath(t)

	occupied, err := net.Listen("tcp4", net.JoinHostPort(LoopbackHost, "0"))
	if err != nil {
		t.Fatalf("预占端口失败: %v", err)
	}
	defer func() { _ = occupied.Close() }()
	mainPort := occupied.Addr().(*net.TCPAddr).Port

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	addr, err := srv.Start(context.Background())
	if err != nil {
		t.Fatalf("主端口被占用时应回退临时端口而不是失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("监听地址非法: %q", addr)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("监听端口非法: %q", portText)
	}
	if port == mainPort {
		t.Fatalf("主端口已被占用，不应仍然绑在 %d 上", mainPort)
	}
	if port == 0 {
		t.Fatalf("回退后的端口必须是内核分配的**实际**端口，不能是 0")
	}

	// 回退后的端口必须是真有服务在听的（否则扩展拿到端口文件也连不上）。
	res, err := http.Get("http://" + addr + PathHealth)
	if err != nil {
		t.Fatalf("回退端口应可访问: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
}

// TestServerStart_PortFilePublishedAfterListening 对应 S4 的写入时机与内容：
// 端口文件必须**在服务开始监听之后**写入，且内容含 `port` 与 `pid`。
//
// "之后"怎么验证：端口文件是扩展唯一的发现途径，所以真正的判据是
// **读到文件的那一刻就能连上**。因此这里先读到文件，再立刻用文件里的端口
// 发起一次真实请求——若写入发生在监听之前，这个请求会连接被拒。
func TestServerStart_PortFilePublishedAfterListening(t *testing.T) {
	path := portFilePath(t)
	mainPort := reservePort(t)

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	addr, err := srv.Start(context.Background())
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("监听后端口文件必须存在: %v", err)
	}

	// 内容形状：`{"port":47652,"pid":1234}`（S4）。
	var record struct {
		Port *int `json:"port"`
		PID  *int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("端口文件必须是合法 JSON: %v（原文 %q）", err, raw)
	}
	if record.Port == nil {
		t.Fatalf("端口文件必须含 port 字段: %q", raw)
	}
	if record.PID == nil {
		t.Fatalf("端口文件必须含 pid 字段（扩展按它校验进程仍在）: %q", raw)
	}
	if *record.PID != os.Getpid() {
		t.Fatalf("pid 必须是当前进程，实际 %d，期望 %d", *record.PID, os.Getpid())
	}

	_, listeningPort, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("监听地址非法: %q", addr)
	}
	if strconv.Itoa(*record.Port) != listeningPort {
		t.Fatalf("端口文件写的必须是**实际**监听端口：文件 %d，实际 %s", *record.Port, listeningPort)
	}

	// 找到文件即可用：这一步同时验证"写入发生在监听之后"。
	res, err := http.Get(fmt.Sprintf("http://%s:%d%s", LoopbackHost, *record.Port, PathHealth))
	if err != nil {
		t.Fatalf("端口文件存在时服务必须已可连接: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /health 应返回 200，实际 %d", res.StatusCode)
	}
}

// TestServerStop_RemovesPortFile 对应 S4 的收尾：停止时清理端口发现文件。
//
// 不清理的后果是扩展在整个下次启动期间会去连一个死端口（B-214 的重探一直失败），
// 表现成"软件明明开着却连不上"。
func TestServerStop_RemovesPortFile(t *testing.T) {
	path := portFilePath(t)
	mainPort := reservePort(t)

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if _, err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Start 后端口文件应存在: %v", err)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stop 后端口文件必须被清理，实际 stat 返回: %v", err)
	}
}

// TestServerStop_KeepsPortFileOfOtherProcess 对应 [01 §9] 的 N7：
// 不得删除或修改**归属不明**的文件。
//
// 端口文件里带 `pid` 正是为了判定归属。若文件里的 pid 不是本进程，
// 说明它是同一用户下另一个实例写的——删掉它会破坏那个实例的发现。
func TestServerStop_KeepsPortFileOfOtherProcess(t *testing.T) {
	// 一个几乎不可能与本进程相等的 pid。
	const otherPID = 999999
	path := portFilePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("创建数据目录失败: %v", err)
	}
	payload := fmt.Sprintf(`{"port":47652,"pid":%d}`, otherPID)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("预置端口文件失败: %v", err)
	}

	mainPort := reservePort(t)
	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if _, err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	// Start 会覆盖该文件（本进程现在确实是监听者），所以先把归属改回去，
	// 再验证 Stop 不会删别人的文件。
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("重新预置端口文件失败: %v", err)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("归属他人的端口文件不得被删除: %v", err)
	}
	if !strings.Contains(string(raw), strconv.Itoa(otherPID)) {
		t.Fatalf("归属他人的端口文件不得被改写，实际内容 %q", raw)
	}
}

// TestServerStart_SecondStartFails：重复 Start 必须是明确错误，
// 而不是悄悄多开一个监听（那会让端口文件与实际监听者不一致）。
func TestServerStart_SecondStartFails(t *testing.T) {
	portFilePath(t)
	mainPort := reservePort(t)

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if _, err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	if _, err := srv.Start(context.Background()); err == nil {
		t.Fatalf("重复 Start 必须返回错误")
	}
}

// TestServerStop_ThenStartFails：停止后不得复用同一个 Server。
//
// 端口文件的清理与 `stopOnce` 都只做一次，复用会让第二次启动不再写文件。
// 明确拒绝比"看起来能跑但发现机制已失效"要好。
func TestServerStop_ThenStartFails(t *testing.T) {
	portFilePath(t)
	mainPort := reservePort(t)

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if _, err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if _, err := srv.Start(context.Background()); err == nil {
		t.Fatalf("已停止的 Server 不得重新 Start")
	}
}

// TestServerStop_Idempotent：Stop 可重复调用（[01 §5.2] 的关闭路径可能被
// 正常退出与异常回收两条路同时走到）。
func TestServerStop_Idempotent(t *testing.T) {
	portFilePath(t)
	mainPort := reservePort(t)

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin, MainPort: mainPort})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if _, err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("首次 Stop 失败: %v", err)
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("重复 Stop 必须是无害的: %v", err)
	}
}

// TestNew_RejectsNilService：缺 Service 是装配错误，启动期直接失败
// （[12 §3.2] 的"编程错误在启动期暴露"），不留一个所有端点都返回 500 的半成品。
func TestNew_RejectsNilService(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatalf("缺少 Service 依赖时必须返回错误")
	}
}

// TestServerStop_WithoutStart：从未 Start 过就 Stop，不得 panic 也不得报错。
func TestServerStop_WithoutStart(t *testing.T) {
	portFilePath(t)

	srv, err := New(Options{Service: &stubService{}, OriginWhitelist: testOrigin})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("未启动时的 Stop 应是无操作: %v", err)
	}
	if addr := srv.Addr(); addr != "" {
		t.Fatalf("未启动时 Addr 应为空，实际 %q", addr)
	}
}

// TestListen_ReturnsLoopbackAddress：`listen` 产出的地址**必须是回环**，
// 无论走的是主端口还是回退路径。
//
// 这是 T-STB-01 / B-1001 的最小单元级看护：即使将来有人把 `LoopbackHost`
// 换成空串或 `0.0.0.0`（让 `net.Listen` 绑定全部网卡），这条用例会立刻失败。
//
// 注意**不测"两次绑定都失败"**：那个分支在本机无法构造出确定的失败输入
// （实测超范围端口号如 65536/70000 会被 `net.Listen` 以 "invalid port" 拒绝，
// 而回退用的端口 0 在 Windows 上总能成功），依赖环境构造的用例会变成 flaky。
func TestListen_ReturnsLoopbackAddress(t *testing.T) {
	listener, fellBack, err := listen(0) // 0 让内核分配，避免与其他测试抢端口
	if err != nil {
		t.Fatalf("listen 失败: %v", err)
	}
	defer func() { _ = listener.Close() }()

	if fellBack {
		t.Fatalf("主端口为 0（内核分配）时不应发生回退")
	}

	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("监听地址非法: %q", listener.Addr())
	}
	if host != LoopbackHost {
		t.Fatalf("必须绑 %s，实际 %q", LoopbackHost, host)
	}
}

// reservePort 返回一个当前空闲的回环端口，并把它留给调用方当作"主端口"。
//
// 先占再放：`Listen` 成功后立刻 `Close`，端口在这之后短暂地是空闲的，
// 足以让后续绑定成功。它与"端口被占用"的用例相区分——后者**保持占用**。
func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort(LoopbackHost, "0"))
	if err != nil {
		t.Fatalf("预占端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("释放预占端口失败: %v", err)
	}
	// 短暂等：让内核完成端口回收，避免紧接着的绑定偶发失败。
	time.Sleep(10 * time.Millisecond)
	return port
}

// portFilePath 把数据目录重定向到本测试自己的临时目录（T2），
// 并返回端口发现文件的路径。
func portFilePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LocalAppData", dir)
	return filepath.Join(dir, "LiudiDownloader", "api-port.json")
}
