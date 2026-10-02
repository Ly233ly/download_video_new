package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ly233ly/download_video_new/internal/store"
)

// 计划业务的测试（[12 §5]：命名 Test<被测>_<场景>，使用临时目录与临时数据库）。
// 全部依赖都可注入：时钟（T4）、DNS 解析（T3：不依赖外网）、下载引擎与事件发布。

// fakeRunner 是可编排的下载引擎替身。
type fakeRunner struct {
	mu       sync.Mutex
	requests []DownloadRequest
	handler  func(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error)
}

func (f *fakeRunner) Run(ctx context.Context, req DownloadRequest, onProgress func(DownloadProgress)) (DownloadResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	handler := f.handler
	f.mu.Unlock()
	if handler == nil {
		return DownloadResult{FinalPath: `C:\已完成\video.mp4`, DownloadedBytes: 10}, nil
	}
	return handler(ctx, req, onProgress)
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeRunner) lastRequest() DownloadRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return DownloadRequest{}
	}
	return f.requests[len(f.requests)-1]
}

// recordingPublisher 记录事件，并实现可选接口 PlanViewPublisher（[04 §4.2] 的完整推送）。
type recordingPublisher struct {
	mu           sync.Mutex
	changed      []string
	changedBatch int
	progress     []PlanView
	notices      []string
}

func (p *recordingPublisher) PlanChanged(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.changed = append(p.changed, id)
}

func (p *recordingPublisher) PlansChanged() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.changedBatch++
}

func (p *recordingPublisher) AppNotice(level, code, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notices = append(p.notices, level+"|"+code+"|"+message)
}

// PlanProgress 让 publishPlan 走"携带完整 PlanView"的分支（[04 §4.2]）。
func (p *recordingPublisher) PlanProgress(view PlanView) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.progress = append(p.progress, view)
	p.changed = append(p.changed, view.ID)
}

func (p *recordingPublisher) lastProgress() (PlanView, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.progress) == 0 {
		return PlanView{}, false
	}
	return p.progress[len(p.progress)-1], true
}

func (p *recordingPublisher) progressCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.progress)
}

// fakeEagle 统计探测次数，用于验证 [02 B-307] 的 single-flight。
type fakeEagle struct {
	mu    sync.Mutex
	calls int
	block chan struct{}
	ok    bool
}

func (e *fakeEagle) Available(ctx context.Context) bool {
	e.mu.Lock()
	e.calls++
	block := e.block
	e.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	return e.ok
}

func (e *fakeEagle) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// fixedResolver 是假的 DNS 解析器：绝不依赖外网（[12 §5.2] 的 T3）。
func fixedResolver(ips ...string) HostResolver {
	return func(ctx context.Context, host string) ([]net.IP, error) {
		out := make([]net.IP, 0, len(ips))
		for _, ip := range ips {
			out = append(out, net.ParseIP(ip))
		}
		return out, nil
	}
}

// failingResolver 模拟 DNS 解析失败（[03 §4.3]：失败即拒绝）。
func failingResolver() HostResolver {
	return func(ctx context.Context, host string) ([]net.IP, error) {
		return nil, errors.New("解析失败")
	}
}

// testClock 是可推进的注入时钟（[12 §5.2] 的 T4：时间相关逻辑必须可注入）。
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type planTestEnv struct {
	svc    *Service
	db     *store.DB
	pub    *recordingPublisher
	runner *fakeRunner
	eagle  *fakeEagle
	clock  *testClock
}

// newPlanTestEnv 装配一套可注入的测试环境。默认一切从简：公网 DNS、无 Eagle、可推进的时钟。
func newPlanTestEnv(t *testing.T, tune func(*Deps)) *planTestEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pub := &recordingPublisher{}
	runner := &fakeRunner{}
	eagle := &fakeEagle{}
	clock := newTestClock()
	deps := Deps{
		Store:     db,
		Publisher: pub,
		Runner:    runner,
		Eagle:     eagle,
		Resolver:  fixedResolver("93.184.216.34"),
		Now:       clock.Now,
		Version:   "test",
	}
	if tune != nil {
		tune(&deps)
	}

	// 输出目录固定到临时目录：调度会解析 `output_dir` 并创建「已完成」，
	// 留空的话就会落到 `%USERPROFILE%\Downloads\留底下载器\`——测试不得污染用户目录（[12 §5.2] 的 T2）。
	outputRoot := t.TempDir()
	encoded, err := json.Marshal(outputRoot)
	if err != nil {
		t.Fatalf("编码临时输出目录失败: %v", err)
	}
	if err := db.SetSetting(context.Background(), settingOutputDir, string(encoded)); err != nil {
		t.Fatalf("写入 output_dir 失败: %v", err)
	}

	svc, err := New(deps)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return &planTestEnv{svc: svc, db: db, pub: pub, runner: runner, eagle: eagle, clock: clock}
}

// baseRequest 返回一个能通过全部创建期校验的请求。
func baseRequest() CreatePlanRequest {
	return CreatePlanRequest{
		URL:             "https://cdn.example.com/video.mp4",
		PageURL:         "https://example.com/watch?v=1",
		MediaKind:       store.PlanMediaDirect,
		SourceTitle:     "示例视频",
		OutputName:      "示例视频",
		OutputContainer: store.PlanContainerMP4,
		MergeMode:       store.PlanMergeSingle,
	}
}

func (e *planTestEnv) planRow(t *testing.T, id string) store.Plan {
	t.Helper()
	plan, err := e.db.GetPlan(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPlan(%s) 失败: %v", id, err)
	}
	return plan
}

func (e *planTestEnv) countPlans(t *testing.T) int {
	t.Helper()
	total, err := e.db.CountPlans(context.Background(), nil)
	if err != nil {
		t.Fatalf("CountPlans 失败: %v", err)
	}
	return total
}

// ---------------------------------------------------------------------------
// 创建期校验（[05 §3.2] 的十条，每条对应一个错误码）
// ---------------------------------------------------------------------------

func TestPlanCreate_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name     string
		wantCode string
		mutate   func(*CreatePlanRequest)
		resolver HostResolver
	}{
		{name: "媒体地址格式非法", wantCode: CodeInvalidURL, mutate: func(r *CreatePlanRequest) {
			r.URL = "not a url"
		}},
		{name: "scheme 不受支持", wantCode: CodeInvalidURL, mutate: func(r *CreatePlanRequest) {
			r.URL = "ftp://cdn.example.com/a.mp4"
		}},
		{name: "带用户名密码", wantCode: CodeInvalidURL, mutate: func(r *CreatePlanRequest) {
			r.URL = "https://user:pass@cdn.example.com/a.mp4"
		}},
		{name: "地址超长", wantCode: CodeInvalidURL, mutate: func(r *CreatePlanRequest) {
			r.URL = "https://cdn.example.com/" + strings.Repeat("a", store.MaxSourceURLLen)
		}},
		{name: "DNS 解析失败", wantCode: CodeInvalidURL, resolver: failingResolver()},
		{name: "主机为本机名", wantCode: CodeBlockedLocalTarget, mutate: func(r *CreatePlanRequest) {
			r.URL = "http://localhost:8080/a.mp4"
		}},
		{name: "回环地址", wantCode: CodeBlockedLocalTarget, mutate: func(r *CreatePlanRequest) {
			r.URL = "http://127.0.0.1/a.mp4"
		}},
		{name: "IPv6 回环", wantCode: CodeBlockedLocalTarget, mutate: func(r *CreatePlanRequest) {
			r.URL = "http://[::1]/a.mp4"
		}},
		{name: "私有网段字面量", wantCode: CodeBlockedLocalTarget, mutate: func(r *CreatePlanRequest) {
			r.URL = "http://192.168.1.5/a.mp4"
		}},
		{name: "IPv4-mapped 内网地址", wantCode: CodeBlockedLocalTarget, mutate: func(r *CreatePlanRequest) {
			r.URL = "http://[::ffff:192.168.1.5]/a.mp4"
		}},
		{name: "解析到内网", wantCode: CodeBlockedLocalTarget, resolver: fixedResolver("10.1.2.3")},
		{name: "多地址中任一命中", wantCode: CodeBlockedLocalTarget, resolver: fixedResolver("93.184.216.34", "169.254.1.1")},
		{name: "blob 地址", wantCode: CodeBlobNotDownloadable, mutate: func(r *CreatePlanRequest) {
			r.URL = "blob:https://example.com/9f1c-4a"
		}},
		{name: "时间片段", wantCode: CodeFixedRangeFragment, mutate: func(r *CreatePlanRequest) {
			r.URL = "https://cdn.example.com/a.mp4#t=10,20"
		}},
		{name: "字节范围参数", wantCode: CodeFixedRangeFragment, mutate: func(r *CreatePlanRequest) {
			r.URL = "https://cdn.example.com/a.mp4?range=0-1023"
		}},
		{name: "合并方式不支持", wantCode: CodeInvalidMergeMode, mutate: func(r *CreatePlanRequest) {
			r.MergeMode = "merge"
		}},
		{name: "输出容器不支持", wantCode: CodeInvalidContainer, mutate: func(r *CreatePlanRequest) {
			r.OutputContainer = "avi"
		}},
		{name: "缺少音视频内容", wantCode: CodeMissingMedia, mutate: func(r *CreatePlanRequest) {
			r.Tracks = []StreamTrack{}
		}},
		{name: "双轨合并只给一条轨道", wantCode: CodeMissingMedia, mutate: func(r *CreatePlanRequest) {
			r.MergeMode = store.PlanMergeAV
			r.Tracks = []StreamTrack{{Kind: "video", Index: 0}}
		}},
		{name: "视频号档位非法", wantCode: CodeWechatSpecInvalid, mutate: func(r *CreatePlanRequest) {
			r.MediaKind = store.PlanMediaWechat
			r.QualityLabel = "1080p"
			r.Wechat = &WechatContext{DecodeKey: "k"}
		}},
		{name: "视频号解密键非法", wantCode: CodeWechatKeyInvalid, mutate: func(r *CreatePlanRequest) {
			r.MediaKind = store.PlanMediaWechat
			r.QualityLabel = "xWT111"
		}},
		{name: "视频号原画不可验证", wantCode: CodeWechatOriginalUnverifiable, mutate: func(r *CreatePlanRequest) {
			r.MediaKind = store.PlanMediaWechat
			r.QualityLabel = "original"
			r.Wechat = &WechatContext{DecodeKey: "k", SessionID: "s"}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newPlanTestEnv(t, func(deps *Deps) {
				if tc.resolver != nil {
					deps.Resolver = tc.resolver
				}
			})
			req := baseRequest()
			if tc.mutate != nil {
				tc.mutate(&req)
			}

			_, err := env.svc.PlanCreate(context.Background(), req)
			if err == nil {
				t.Fatal("非法请求被接受了，应当返回错误码")
			}
			if got := CodeOf(err); got != tc.wantCode {
				t.Errorf("错误码 = %s，期望 %s", got, tc.wantCode)
			}
			// [05 §3.2]：拒绝**且不落库**。
			if n := env.countPlans(t); n != 0 {
				t.Errorf("拒绝后库里留下了 %d 条记录，应当为 0", n)
			}
			// 拒绝路径也不得把地址与凭据带进内存注册表。
			if got := env.runner.callCount(); got != 0 {
				t.Errorf("被拒绝的请求不该调用下载引擎，实际 %d 次", got)
			}
		})
	}
}

func TestPlanCreate_AcceptsValidRequest(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	view, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("PlanCreate 失败: %v", err)
	}
	if view.ID == "" {
		t.Fatal("返回的视图没有 ID")
	}
	if view.Status != store.PlanStatusQueued {
		t.Errorf("初始状态 = %s，期望 queued（[05 §3.1]）", view.Status)
	}
	if view.CanStop != true {
		t.Error("新建计划应当可以停止")
	}
	if !env.pub.plansChangedFired() {
		t.Error("创建后应发出 plansChanged（[04 §4.1]）")
	}

	plan := env.planRow(t, view.ID)
	if plan.Status != store.PlanStatusQueued || plan.AttemptCount != 0 {
		t.Errorf("落库状态 = %s/%d，期望 queued/0", plan.Status, plan.AttemptCount)
	}
	if plan.NextAttemptAt != nil {
		t.Error("新建计划的 next_attempt_at 应为 NULL（表示首次调度）")
	}
	if plan.Phase != store.PlanPhaseNone {
		t.Errorf("phase = %q，期望空串", plan.Phase)
	}
}

// 媒体地址与会话凭据**只在内存**（[03 §6]、B-722）：库里不得出现签名参数。
func TestPlanCreate_KeepsSecretsOutOfDatabase(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	req := baseRequest()
	req.URL = "https://cdn.example.com/video.mp4?sign=deadbeef&token=secret123&utm_source=x&idx=7"
	req.Headers = map[string]string{"Cookie": "session=abc", "Authorization": "Bearer xyz"}

	view, err := env.svc.PlanCreate(ctx, req)
	if err != nil {
		t.Fatalf("PlanCreate 失败: %v", err)
	}

	stored := env.planRow(t, view.ID).SourceURL
	for _, forbidden := range []string{"sign=", "token=", "idx=", "utm_source", "secret123", "deadbeef"} {
		if strings.Contains(stored, forbidden) {
			t.Errorf("source_url 落盘了 %q：%s", forbidden, stored)
		}
	}
	if stored != "https://example.com/watch?v=1" {
		t.Errorf("source_url = %q，期望页面地址（[03 §2.1]）", stored)
	}

	// 内存里仍然保留完整地址与凭据——下载需要它们。
	if !env.svc.plan.canProvideSource(env.planRow(t, view.ID)) {
		t.Error("内存注册表里应当有可用的下载上下文")
	}

	// 视图里不得带出会话凭据（[04 §3.1] 的 B2：不得返回秘密字段）。
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("序列化视图失败: %v", err)
	}
	if strings.Contains(string(raw), "session=abc") || strings.Contains(string(raw), "deadbeef") {
		t.Errorf("视图泄露了秘密：%s", raw)
	}
}

func TestPlanCreate_NormalizesURL(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	req := baseRequest()
	req.URL = "HTTPS://CDN.Example.COM.:443/a.mp4?utm_medium=x&keep=1#frag"
	req.PageURL = ""

	view, err := env.svc.PlanCreate(ctx, req)
	if err != nil {
		t.Fatalf("PlanCreate 失败: %v", err)
	}
	stored := env.planRow(t, view.ID).SourceURL
	if stored != "https://cdn.example.com/a.mp4?keep=1" {
		t.Errorf("归一化结果 = %q，期望 https://cdn.example.com/a.mp4?keep=1（[03 §4.1]）", stored)
	}
}

func TestPlanCreate_SanitizesOutputName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"去非法字符", `a/b\c:d*e?f"g<h>i|j`, "abcdefghij"},
		{"去首尾空白与点", "  ..标题..  ", "标题"},
		{"保留名加前缀", "CON", "_CON"},
		{"保留名不分大小写", "com1", "_com1"},
		{"超长截断", strings.Repeat("长", 200), strings.Repeat("长", store.MaxOutputNameLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newPlanTestEnv(t, nil)
			req := baseRequest()
			req.OutputName = tc.input

			view, err := env.svc.PlanCreate(context.Background(), req)
			if err != nil {
				t.Fatalf("PlanCreate 失败: %v", err)
			}
			if got := env.planRow(t, view.ID).OutputName; got != tc.want {
				t.Errorf("output_name = %q，期望 %q（[03 §4.4]）", got, tc.want)
			}
		})
	}
}

func TestPlanCreate_CleansOutputNameToFallback(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	req := baseRequest()
	req.OutputName = `\/:*?"<>|`

	view, err := env.svc.PlanCreate(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanCreate 失败: %v", err)
	}
	// [03 §4.4] 第 5 条：清洗后为空则回退到内容 ID 或时间戳。
	if got := env.planRow(t, view.ID).OutputName; got != view.ID {
		t.Errorf("output_name = %q，期望回退为计划 ID %q", got, view.ID)
	}
}

// B-220 / B-221：模式在创建时决定并写入计划记录；B-226：视频号不适用该模式。
func TestPlanCreate_BrowserModeSelectsMediaKind(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	// 直接写库：browser_download_mode 是 int 类型（[03 §2.4]），
	// 而 Service.SetSetting 只处理字符串设置，这里要的是原始 JSON 数字。
	if err := env.db.SetSetting(ctx, settingBrowserDownloadMode, "1"); err != nil {
		t.Fatalf("写入模式配置失败: %v", err)
	}

	view, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("PlanCreate 失败: %v", err)
	}
	if got := env.planRow(t, view.ID).MediaKind; got != store.PlanMediaBrowser {
		t.Errorf("media_kind = %s，期望 browser（[04 §2.5]）", got)
	}

	wechat := baseRequest()
	wechat.MediaKind = store.PlanMediaWechat
	wechat.QualityLabel = "xWT111"
	wechat.Wechat = &WechatContext{DecodeKey: "k"}
	wechatView, err := env.svc.PlanCreate(ctx, wechat)
	if err != nil {
		t.Fatalf("视频号创建失败: %v", err)
	}
	if got := env.planRow(t, wechatView.ID).MediaKind; got != store.PlanMediaWechat {
		t.Errorf("视频号 media_kind = %s，期望保持 wechat（B-226）", got)
	}
}

func TestPlanCreate_DeleteAfterImportRequiresImport(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	req := baseRequest()
	req.ImportToEagle = false
	req.DeleteAfterImport = true

	view, err := env.svc.PlanCreate(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanCreate 失败: %v", err)
	}
	if env.planRow(t, view.ID).DeleteAfterImport {
		t.Error("import_to_eagle = 0 时 delete_after_import 必须为 0（[05 §3.1]）")
	}
}

// ---------------------------------------------------------------------------
// 查询与列表
// ---------------------------------------------------------------------------

func TestPlanGet_NotFound(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	_, err := env.svc.PlanGet(context.Background(), "missing")
	if !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("返回 %v，期望 plan_not_found", err)
	}
	if CodeOf(err) != CodePlanNotFound {
		t.Errorf("错误码 = %s", CodeOf(err))
	}
}

func TestPlansList_ReportsEffectivePaging(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		req := baseRequest()
		req.OutputName = "视频" + string(rune('A'+i))
		if _, err := env.svc.PlanCreate(ctx, req); err != nil {
			t.Fatalf("创建失败: %v", err)
		}
	}

	page, err := env.svc.PlansList(ctx, nil, -5, 10_000)
	if err != nil {
		t.Fatalf("PlansList 失败: %v", err)
	}
	if page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("返回 %d/%d 条，期望 3/3", len(page.Items), page.Total)
	}
	// 回显的是**实际生效**的分页参数（[03 §4.5] 的上限 200）。
	if page.Limit != store.MaxPlanListLimit {
		t.Errorf("limit = %d，期望被夹到 %d", page.Limit, store.MaxPlanListLimit)
	}
	if page.Offset != 0 {
		t.Errorf("offset = %d，期望被夹到 0", page.Offset)
	}
}

func TestPlansList_RejectsInvalidStatus(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	if _, err := env.svc.PlansList(context.Background(), []string{"bogus"}, 0, 10); err == nil {
		t.Fatal("非法状态筛选值被接受，应当拒绝")
	}
}

// ---------------------------------------------------------------------------
// 停止 / 重试 / 删除
// ---------------------------------------------------------------------------

func TestPlanStop_CanceledIsIdempotent(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	first, err := env.svc.PlanStop(ctx, created.ID)
	if err != nil {
		t.Fatalf("第一次停止失败: %v", err)
	}
	if first.Status != store.PlanStatusCanceled {
		t.Fatalf("状态 = %s，期望 canceled", first.Status)
	}

	second, err := env.svc.PlanStop(ctx, created.ID)
	if err != nil {
		t.Fatalf("重复停止不应报错: %v", err)
	}
	if second.Status != store.PlanStatusCanceled {
		t.Errorf("状态 = %s，期望 canceled", second.Status)
	}
}

func TestPlanStop_RejectsCompletedPlan(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()
	id := env.createAndComplete(t)

	if _, err := env.svc.PlanStop(ctx, id); !errors.Is(err, ErrPlanStateChanged) {
		t.Fatalf("停止已完成计划返回 %v，期望 plan_state_changed（[05 §10]）", err)
	}
	// 已交付的文件与状态都不受影响。
	if got := env.planRow(t, id); got.Status != store.PlanStatusCompleted || got.FinalPath == "" {
		t.Errorf("已完成计划被改动：%+v", got)
	}
}

func TestPlanStop_ReleasesInMemorySecret(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := env.svc.PlanStop(ctx, created.ID); err != nil {
		// 取消是合法转换，这里不该失败。
		t.Fatalf("停止失败: %v", err)
	}
	if _, ok := env.svc.plan.lookupSource(created.ID); ok {
		t.Error("停止后内存里不该继续保留媒体地址与凭据（[03 §6]）")
	}
}

func TestPlanRetry_RejectsNonFailedPlan(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := env.svc.PlanRetry(ctx, created.ID); !errors.Is(err, ErrPlanNotRetryable) {
		t.Fatalf("重试 queued 计划返回 %v，期望 plan_not_retryable（[05 §2.2]）", err)
	}
}

// P6 与 context_expired 的失败**不可手动重试**（[05 §4.4.3]、[05 §9]）。
func TestPlanRetry_RejectsUnrecoverableFailures(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	browser := baseRequest()
	browser.MediaKind = store.PlanMediaBrowser
	created, err := env.svc.PlanCreate(ctx, browser)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	env.failPlanDirectly(t, created.ID, CodeUploadAborted)

	if _, err := env.svc.PlanRetry(ctx, created.ID); !errors.Is(err, ErrPlanNotRetryable) {
		t.Errorf("浏览器中转失败的重试返回 %v，期望 plan_not_retryable", err)
	}

	direct, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	env.failPlanDirectly(t, direct.ID, CodeContextExpired)
	if _, err := env.svc.PlanRetry(ctx, direct.ID); !errors.Is(err, ErrPlanNotRetryable) {
		t.Errorf("上下文失效的重试返回 %v，期望 plan_not_retryable", err)
	}
}

func TestPlanRetry_RequeuesAndResetsAttempts(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	env.failPlanDirectly(t, created.ID, CodeDownloadFailed)

	view, err := env.svc.PlanRetry(ctx, created.ID)
	if err != nil {
		t.Fatalf("PlanRetry 失败: %v", err)
	}
	if view.Status != store.PlanStatusQueued {
		t.Fatalf("状态 = %s，期望 queued", view.Status)
	}
	if view.AttemptCount != 0 {
		t.Errorf("attempt_count = %d，期望归零（重新获得自动重试预算）", view.AttemptCount)
	}
	if view.NextAttemptAt == nil {
		t.Error("next_attempt_at 应当被写入（立即调度）")
	}
	if !view.CanStop {
		t.Error("重新排队的计划应当可以停止")
	}
}

func TestPlanRemove_RejectsRunningPlan(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := env.db.MarkPlanRunning(ctx, created.ID, store.PlanPhaseDownloading); err != nil {
		t.Fatalf("置运行失败: %v", err)
	}

	if err := env.svc.PlanRemove(ctx, created.ID); !errors.Is(err, ErrPlanStateChanged) {
		t.Fatalf("删除运行中的计划返回 %v，期望 plan_state_changed", err)
	}
	if n := env.countPlans(t); n != 1 {
		t.Errorf("记录数 = %d，期望仍为 1", n)
	}
}

func TestPlanRemove_DeletesTerminalPlan(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()
	id := env.createAndComplete(t)

	if err := env.svc.PlanRemove(ctx, id); err != nil {
		t.Fatalf("PlanRemove 失败: %v", err)
	}
	if n := env.countPlans(t); n != 0 {
		t.Errorf("记录数 = %d，期望 0", n)
	}
	if err := env.svc.PlanRemove(ctx, id); !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("重复删除返回 %v，期望 plan_not_found", err)
	}
}

// ---------------------------------------------------------------------------
// 视图投影与派生布尔
// ---------------------------------------------------------------------------

func TestPlanView_DerivedFlagsFollowRealState(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()
	id := env.createAndComplete(t)

	view, err := env.svc.PlanGet(ctx, id)
	if err != nil {
		t.Fatalf("PlanGet 失败: %v", err)
	}
	if view.CanStop {
		t.Error("已完成的计划不该能停止（B-313：只投影真实状态）")
	}
	if !view.CanOpen {
		t.Error("已完成的计划应当能打开所在文件夹（B-309）")
	}
	if view.CanRetry {
		t.Error("已完成的计划不该能重试")
	}
	// Eagle 未被探测（fake 返回 false）→ 补导入口必须禁用（B-211）。
	if view.CanImport {
		t.Error("Eagle 不可用时补导入口必须禁用（B-211）")
	}
}

func TestPlanView_CanImportRequiresEagleAndIntent(t *testing.T) {
	env := newPlanTestEnv(t, func(deps *Deps) {
		deps.Eagle = &fakeEagle{ok: true}
	})
	ctx := context.Background()

	req := baseRequest()
	req.ImportToEagle = true
	view, err := env.svc.PlanCreate(ctx, req)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// 先完成它。
	env.completePlanDirectly(t, view.ID)

	// 还没探测过 Eagle → 仍然禁用。
	got, err := env.svc.PlanGet(ctx, view.ID)
	if err != nil {
		t.Fatalf("PlanGet 失败: %v", err)
	}
	if got.CanImport {
		t.Error("未探测 Eagle 前补导入口必须禁用（宁可禁用，不假装可用）")
	}

	// 探测一次之后才允许。
	if _, err := env.svc.Health(ctx); err != nil {
		t.Fatalf("Health 失败: %v", err)
	}
	got, err = env.svc.PlanGet(ctx, view.ID)
	if err != nil {
		t.Fatalf("PlanGet 失败: %v", err)
	}
	if !got.CanImport {
		t.Error("Eagle 可用且勾选了导入时补导入口应当可用")
	}
}

// total_bytes 的三态必须穿过视图保持区分（[03 §2.1]）。
func TestPlanView_KeepsUnknownTotalDistinctFromZero(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	unknown, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	zero := int64(0)
	env.completeWith(t, unknown.ID, 0, nil)

	view, err := env.svc.PlanGet(ctx, unknown.ID)
	if err != nil {
		t.Fatalf("PlanGet 失败: %v", err)
	}
	if view.TotalBytes != nil {
		t.Errorf("total_bytes = %v，期望 null（长度未知）", *view.TotalBytes)
	}

	second, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	env.completeWith(t, second.ID, 0, &zero)

	view, err = env.svc.PlanGet(ctx, second.ID)
	if err != nil {
		t.Fatalf("PlanGet 失败: %v", err)
	}
	if view.TotalBytes == nil || *view.TotalBytes != 0 {
		t.Errorf("total_bytes = %v，期望 0（长度为零，与未知不同）", view.TotalBytes)
	}
}

func TestPlanView_ErrorCodeSurvivesFailure(t *testing.T) {
	env := newPlanTestEnv(t, nil)
	ctx := context.Background()

	created, err := env.svc.PlanCreate(ctx, baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	env.failPlanDirectly(t, created.ID, CodeDiskFull)

	view, err := env.svc.PlanGet(ctx, created.ID)
	if err != nil {
		t.Fatalf("PlanGet 失败: %v", err)
	}
	if view.ErrorCode != CodeDiskFull {
		t.Errorf("error_code = %q，期望 %q", view.ErrorCode, CodeDiskFull)
	}
	// [05 §7.4]：错误消息必须可安全展示，不得含路径或 URL。
	if view.ErrorMessage == "" || strings.Contains(view.ErrorMessage, `\`) || strings.Contains(view.ErrorMessage, "://") {
		t.Errorf("错误消息不可安全展示：%q", view.ErrorMessage)
	}
	if !view.CanRetry {
		t.Error("disk_full 释放空间后可以手动重试（[05 §4.1]）")
	}
}

// ---------------------------------------------------------------------------
// 健康接口
// ---------------------------------------------------------------------------

// B-307：健康探测的并发刷新必须复用同一轮结果（single-flight）。
func TestHealth_SingleFlight(t *testing.T) {
	eagle := &fakeEagle{block: make(chan struct{}), ok: true}
	env := newPlanTestEnv(t, func(deps *Deps) { deps.Eagle = eagle })

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if _, err := env.svc.Health(context.Background()); err != nil {
				t.Errorf("Health 失败: %v", err)
			}
		}()
	}

	// 等所有调用者进入探测等待，再放行。
	waitForCondition(t, time.Second, func() bool { return eagle.callCount() >= 1 })
	time.Sleep(50 * time.Millisecond)
	close(eagle.block)
	wg.Wait()

	if got := eagle.callCount(); got != 1 {
		t.Errorf("Eagle 探测次数 = %d，期望 1（single-flight）", got)
	}
}

func TestHealth_ReportsCapabilities(t *testing.T) {
	env := newPlanTestEnv(t, func(deps *Deps) { deps.Eagle = &fakeEagle{ok: true} })
	ctx := context.Background()

	// 原始 JSON 数字，见 TestPlanCreate_BrowserModeSelectsMediaKind 的说明。
	if err := env.db.SetSetting(ctx, settingBrowserDownloadMode, "1"); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	view, err := env.svc.Health(ctx)
	if err != nil {
		t.Fatalf("Health 失败: %v", err)
	}
	if !view.OK || !view.DatabaseOK {
		t.Errorf("健康结果 = %+v，期望数据库可读", view)
	}
	if !view.EagleAvailable {
		t.Error("B-503：健康接口必须发布 eagleAvailable")
	}
	if !view.BrowserDownloadMode {
		t.Error("浏览器下载模式未如实反映")
	}
	if view.Version != "test" {
		t.Errorf("版本 = %q，期望注入值", view.Version)
	}
}

// ---------------------------------------------------------------------------
// 错误映射
// ---------------------------------------------------------------------------

func TestCodeOf_MapsServiceErrors(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrPlanNotFound, CodePlanNotFound},
		{ErrPlanNotRetryable, CodePlanNotRetryable},
		{ErrPlanStateChanged, CodePlanStateChanged},
		{newError(CodeBlockedLocalTarget, nil), CodeBlockedLocalTarget},
		{errors.New("随便一个未分类错误"), CodeInternal},
		{nil, CodeInternal},
	}
	for _, tc := range cases {
		if got := CodeOf(tc.err); got != tc.want {
			t.Errorf("CodeOf(%v) = %s，期望 %s", tc.err, got, tc.want)
		}
	}
}

func TestMessageForCode_NeverLeaksInternalDetail(t *testing.T) {
	for code := range codeMessages {
		message := MessageForCode(code)
		if message == "" {
			t.Errorf("错误码 %s 没有可展示消息（[05 §7.4]）", code)
		}
		for _, forbidden := range []string{"://", `\`, "/"} {
			if strings.Contains(message, forbidden) {
				t.Errorf("错误码 %s 的消息含路径或 URL：%q", code, message)
			}
		}
	}
	if got := MessageForCode("never_registered"); got != codeMessages[CodeInternal] {
		t.Errorf("未知码回退 = %q，期望通用文案", got)
	}
}

// ---------------------------------------------------------------------------
// 测试环境辅助
// ---------------------------------------------------------------------------

// createAndComplete 建一条计划并直接写成完成态（不经过调度器）。
func (e *planTestEnv) createAndComplete(t *testing.T) string {
	t.Helper()
	created, err := e.svc.PlanCreate(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	e.completePlanDirectly(t, created.ID)
	return created.ID
}

func (e *planTestEnv) completePlanDirectly(t *testing.T, id string) {
	t.Helper()
	total := int64(2048)
	e.completeWith(t, id, 2048, &total)
}

func (e *planTestEnv) completeWith(t *testing.T, id string, downloaded int64, total *int64) {
	t.Helper()
	ctx := context.Background()
	if err := e.db.MarkPlanRunning(ctx, id, store.PlanPhaseValidating); err != nil {
		t.Fatalf("置运行失败: %v", err)
	}
	if err := e.db.MarkPlanCompleted(ctx, id, `C:\已完成\video.mp4`, downloaded, total); err != nil {
		t.Fatalf("置完成失败: %v", err)
	}
}

func (e *planTestEnv) failPlanDirectly(t *testing.T, id, code string) {
	t.Helper()
	ctx := context.Background()
	if err := e.db.MarkPlanRunning(ctx, id, store.PlanPhaseDownloading); err != nil {
		t.Fatalf("置运行失败: %v", err)
	}
	if err := e.db.MarkPlanFailed(ctx, id, code, MessageForCode(code)); err != nil {
		t.Fatalf("置失败失败: %v", err)
	}
}

func (p *recordingPublisher) plansChangedFired() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.changedBatch > 0
}

// waitForCondition 轮询等待条件成立；超时返回 false（调用方决定怎么报错）。
func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
