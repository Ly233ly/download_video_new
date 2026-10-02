package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoAdapters 是仓库里的唯一事实源目录（契约 A-104）。
// 测试的工作目录是本包目录，所以向上两级。
const repoAdapters = "../../adapters"

func TestLoad_RealAdapters(t *testing.T) {
	set, err := Load(repoAdapters)
	if err != nil {
		t.Fatalf("加载 %s 失败：%v", repoAdapters, err)
	}
	if set.Len() != 2 {
		t.Fatalf("适配器数量 = %d，期望 2（douyin、generic）", set.Len())
	}
	// 顺序按目录名排序，是稳定的。
	all := set.All()
	if all[0].ID != "douyin" || all[1].ID != GenericID {
		t.Fatalf("加载顺序 = %q, %q，期望 douyin, generic", all[0].ID, all[1].ID)
	}

	douyin, ok := set.ByID("douyin")
	if !ok {
		t.Fatal("找不到 douyin 适配器")
	}
	if douyin.Name != "抖音" {
		t.Errorf("douyin.name = %q，期望 抖音", douyin.Name)
	}
	if douyin.Match.Priority != 100 {
		t.Errorf("douyin.match.priority = %d，期望 100", douyin.Match.Priority)
	}
	if len(douyin.Match.Hosts) != 2 {
		t.Errorf("douyin.match.hosts = %v，期望两条（精确 + 通配）", douyin.Match.Hosts)
	}
	if douyin.Capture == nil || douyin.Capture.PrimarySelection != "current-player" {
		t.Errorf("douyin.capture.primarySelection 不是 current-player：%+v", douyin.Capture)
	}
	if douyin.Resolve == nil || douyin.Resolve.Engine != "yt-dlp" {
		t.Errorf("douyin.resolve.engine 不是 yt-dlp：%+v", douyin.Resolve)
	}
	if douyin.CodeReason != nil {
		t.Errorf("douyin 是 L1（[14 §10]），不得有 codeReason：%q", *douyin.CodeReason)
	}
	if _, err := os.Stat(filepath.Join(repoAdapters, "douyin", "extension.js")); err == nil {
		t.Error("douyin 是 L1，目录里不得有 extension.js（A-101：能用 L1 表达就禁止写 L2）")
	}
	if len(douyin.Identity.URLRules) != 3 {
		t.Errorf("douyin.identity.urlRules 数量 = %d，期望 3（路径、查询参数、文本规则，[14 §5.4]）",
			len(douyin.Identity.URLRules))
	}
	if douyin.UpdatedFor != nil {
		t.Errorf("douyin.updatedFor = %v，期望 null（从未按新架构核对，[14 §10]）", *douyin.UpdatedFor)
	}
	if len(douyin.Errors) != 2 {
		t.Errorf("douyin.errors 数量 = %d，期望 2", len(douyin.Errors))
	}

	generic, ok := set.ByID(GenericID)
	if !ok {
		t.Fatal("找不到 generic 适配器")
	}
	if generic.Match.Priority != 0 {
		t.Errorf("generic.match.priority = %d，期望 0（[14 §5]）", generic.Match.Priority)
	}
	if generic.Capture == nil || !generic.Capture.RequireBlobSource || generic.Capture.AllowDirectStream {
		t.Errorf("generic 的通用约束不对：%+v", generic.Capture)
	}
}

// ---------------------------------------------------------------------------
// 匹配（[14 §5.3]）
// ---------------------------------------------------------------------------

func TestMatch_RealSet(t *testing.T) {
	set, err := Load(repoAdapters)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}

	cases := []struct {
		host string
		want string
		why  string
	}{
		{"www.douyin.com", "douyin", "精确域名"},
		{"douyin.com", "douyin", "精确域名（裸域在 hosts 里显式列出）"},
		{"v.douyin.com", "douyin", "子域命中 *.douyin.com"},
		{"WWW.DOUYIN.COM", "douyin", "大小写不敏感"},
		{"www.douyin.com:443", "douyin", "带端口也要命中"},
		{"www.douyin.com.", "douyin", "末尾点归一化"},
		{"example.com", GenericID, "没有适配器时用 generic 兜底"},
		{"sub.example.com", GenericID, "generic 的 hosts 是 *"},
		{"", "", "空主机名不匹配任何适配器"},
	}
	for _, tc := range cases {
		t.Run(tc.host+"/"+tc.why, func(t *testing.T) {
			got := set.Match(tc.host)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("匹配到 %q，期望 nil", got.ID)
				}
				return
			}
			if got == nil {
				t.Fatalf("没有匹配到适配器，期望 %q", tc.want)
			}
			if got.ID != tc.want {
				t.Fatalf("匹配到 %q，期望 %q", got.ID, tc.want)
			}
		})
	}
}

func TestMatch_PriorityBeatsPrecision(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "generic", `{"id":"generic","name":"通用","version":1,"updatedFor":null,
		"match":{"hosts":["*"],"priority":0},
		"capture":{"primarySelection":"all","allowDirectStream":false,"requireBlobSource":true}}`)
	writeAdapter(t, root, "precise", `{"id":"precise","name":"精确","version":1,"updatedFor":null,
		"match":{"hosts":["video.example.com"],"priority":10}}`)
	writeAdapter(t, root, "wild", `{"id":"wild","name":"通配","version":1,"updatedFor":null,
		"match":{"hosts":["*.example.com"],"priority":50}}`)

	set, err := Load(root)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := set.Match("video.example.com"); got == nil || got.ID != "wild" {
		t.Fatalf("匹配到 %v，期望 wild——优先级高的先胜（[14 §5.3] 第 1 条）", got)
	}
	if got := set.Match("other.example.com"); got == nil || got.ID != "wild" {
		t.Fatalf("匹配到 %v，期望 wild", got)
	}
}

func TestMatch_ExactBeatsWildcardAtSamePriority(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "generic", `{"id":"generic","name":"通用","version":1,"updatedFor":null,
		"match":{"hosts":["*"],"priority":0},
		"capture":{"primarySelection":"all","allowDirectStream":false,"requireBlobSource":true}}`)
	writeAdapter(t, root, "exact", `{"id":"exact","name":"精确","version":1,"updatedFor":null,
		"match":{"hosts":["video.example.com"],"priority":100}}`)
	writeAdapter(t, root, "wild", `{"id":"wild","name":"通配","version":1,"updatedFor":null,
		"match":{"hosts":["*.example.com"],"priority":100}}`)

	set, err := Load(root)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := set.Match("video.example.com"); got == nil || got.ID != "exact" {
		t.Fatalf("匹配到 %v，期望 exact——同优先级下精确域名优先（[14 §5.3] 第 2 条）", got)
	}
	if got := set.Match("other.example.com"); got == nil || got.ID != "wild" {
		t.Fatalf("匹配到 %v，期望 wild", got)
	}
}

func TestMatch_WildcardDoesNotCoverBareDomain(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "generic", `{"id":"generic","name":"通用","version":1,"updatedFor":null,
		"match":{"hosts":["*"],"priority":0},
		"capture":{"primarySelection":"all","allowDirectStream":false,"requireBlobSource":true}}`)
	writeAdapter(t, root, "wild", `{"id":"wild","name":"通配","version":1,"updatedFor":null,
		"match":{"hosts":["*.example.com"],"priority":100}}`)

	set, err := Load(root)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	// `*.example.com` 只覆盖子域；裸域要写一条精确规则。这条语义必须在
	// adapters/<id>/README.md 的「匹配范围」里说明（[14 §7.2]）。
	if got := set.Match("example.com"); got == nil || got.ID != GenericID {
		t.Fatalf("裸域匹配到 %v，期望 generic（通配不覆盖裸域）", got)
	}
	if got := set.Match("a.example.com"); got == nil || got.ID != "wild" {
		t.Fatalf("子域匹配到 %v，期望 wild", got)
	}
}

// ---------------------------------------------------------------------------
// 加载期校验（A-101 / A-103 / A-114 / A-115 / A-116）
// ---------------------------------------------------------------------------

func TestLoad_RejectsIDMismatch(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "douyin", `{"id":"douyin-video","name":"抖音","version":1,"updatedFor":null,
		"match":{"hosts":["douyin.com"],"priority":100}}`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("id 与目录名不一致却加载成功（契约 A-114）")
	}
	if !strings.Contains(err.Error(), "douyin-video") || !strings.Contains(err.Error(), "douyin") {
		t.Errorf("错误里应同时指出两个值，实际：%v", err)
	}
}

func TestLoad_RejectsUnknownField(t *testing.T) {
	root := t.TempDir()
	// `prority` 是拼错的 priority：必须当场报错，而不是安静地按 0 生效。
	writeAdapter(t, root, "typo", `{"id":"typo","name":"错字","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"prority":100}}`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("字段名拼错却加载成功")
	}
	if !strings.Contains(err.Error(), "prority") {
		t.Errorf("错误里应指出拼错的字段，实际：%v", err)
	}
}

func TestLoad_RejectsUncompilableRegex(t *testing.T) {
	root := t.TempDir()
	// 两端共用正则方言（Go RE2 + JS RegExp），lookahead 只有 JS 支持：
	// 这类声明必须在启动时就报错，而不是变成"只有浏览器里抓不到"。
	writeAdapter(t, root, "lookahead", `{"id":"lookahead","name":"前瞻","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":10},
		"identity":{"urlRules":[{"path":"^/v/(\\d+)(?=/x)","id":"$1"}]}}`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("正则用了 lookahead（RE2 不支持）却加载成功（[14 §5.4]）")
	}
	if !strings.Contains(err.Error(), "lookahead") {
		t.Errorf("错误里应指出适配器 id，实际：%v", err)
	}
}

// 下面三个用例执行契约 A-119：声明必须**写了就起作用**。
func TestLoad_RejectsErrorRuleWithoutCodeOrMessage(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "缺少 code",
			body: `"errors":[{"match":"Fresh cookies","message":"请刷新页面后重试"}]`,
		},
		{
			name: "缺少 message",
			body: `"errors":[{"match":"Fresh cookies","code":"example_session_expired"}]`,
		},
		{
			name: "message 只有空白",
			body: `"errors":[{"match":"Fresh cookies","code":"example_session_expired","message":"  "}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeAdapter(t, root, "example", `{"id":"example","name":"示例","version":1,"updatedFor":null,
				"match":{"hosts":["example.com"],"priority":10},
				`+tc.body+`}`)

			_, err := Load(root)
			if err == nil {
				t.Fatalf("错误映射缺字段却加载成功（契约 A-119）：%s", tc.body)
			}
			// 报错要能定位到具体是哪一条，否则整份声明得从头翻一遍。
			if !strings.Contains(err.Error(), "errors") {
				t.Errorf("错误里应指出是 errors 里的问题，实际：%v", err)
			}
		})
	}
}

func TestLoad_RejectsCanonicalWithoutID(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "example", `{"id":"example","name":"示例","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":10},
		"identity":{"canonical":"https://example.com/video/"}}`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("identity.canonical 没有 {id} 占位符却加载成功（契约 A-119）")
	}
	if !strings.Contains(err.Error(), "{id}") {
		t.Errorf("错误里应指出缺 {id}，实际：%v", err)
	}
}

func TestLoad_RejectsCanonicalOnUndeclaredHost(t *testing.T) {
	// 规范化之后的地址会带着本页凭据去请求（B-304）：跨站规范化等于把用户的
	// Cookie 送到适配器自己都没声明的站点，必须在加载期拦住。
	root := t.TempDir()
	writeAdapter(t, root, "example", `{"id":"example","name":"示例","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":10},
		"identity":{"canonical":"https://cdn.example.net/video/{id}"}}`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("canonical 指向未声明主机却加载成功（契约 A-119）")
	}
	if !strings.Contains(err.Error(), "cdn.example.net") {
		t.Errorf("错误里应指出越界的主机，实际：%v", err)
	}
}

func TestLoad_AcceptsCanonicalInsideDeclaredHosts(t *testing.T) {
	cases := []struct {
		name      string
		hosts     string
		canonical string
	}{
		{name: "精确域名", hosts: `["example.com"]`, canonical: "https://example.com/video/{id}"},
		{name: "通配域名的子域", hosts: `["*.example.com"]`, canonical: "https://www.example.com/video/{id}"},
		{name: "同站换路径与查询串", hosts: `["example.com"]`, canonical: "https://example.com/v/{id}?from=web"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeAdapter(t, root, "example", `{"id":"example","name":"示例","version":1,"updatedFor":null,
				"match":{"hosts":`+tc.hosts+`,"priority":10},
				"identity":{"canonical":"`+tc.canonical+`"},
				"resolve":{"engine":"yt-dlp","canonicalizePageUrl":true}}`)

			set, err := Load(root)
			if err != nil {
				t.Fatalf("合法声明加载失败（契约 A-119 不该误伤）：%v", err)
			}
			if _, ok := set.ByID("example"); !ok {
				t.Fatal("加载成功但拿不到适配器")
			}
		})
	}
}

func TestLoad_RejectsBrokenJSONWithPath(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "broken", `{"id":"broken","name":`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("坏 JSON 却加载成功（契约 A-116）")
	}
	// 契约 A-116：报错必须**指出文件路径**，否则"这个站点突然抓不到了"无从排查。
	if !strings.Contains(err.Error(), FileName) {
		t.Errorf("错误里应指出文件名，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("错误里应指出是哪个适配器，实际：%v", err)
	}
}

func TestLoad_RejectsHostConflict(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "alpha", `{"id":"alpha","name":"甲","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100}}`)
	writeAdapter(t, root, "beta", `{"id":"beta","name":"乙","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100}}`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("同域名同优先级却加载成功（契约 A-115）")
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("错误里应列出两个目录名，实际：%v", err)
	}
}

func TestLoad_AllowsSameHostDifferentPriority(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "alpha", `{"id":"alpha","name":"甲","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100}}`)
	writeAdapter(t, root, "beta", `{"id":"beta","name":"乙","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":10}}`)

	set, err := Load(root)
	if err != nil {
		t.Fatalf("不同优先级不该冲突：%v", err)
	}
	if got := set.Match("example.com"); got == nil || got.ID != "alpha" {
		t.Fatalf("匹配到 %v，期望 alpha", got)
	}
}

func TestLoad_RejectsExtraEntries(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "extra", `{"id":"extra","name":"多余文件","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100}}`)
	if err := os.WriteFile(filepath.Join(root, "extra", "helper.js"), []byte("// 不该在这里\n"), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}

	_, err := Load(root)
	if err == nil {
		t.Fatal("目录里出现约定之外的文件却加载成功（契约 A-103）")
	}
	if !strings.Contains(err.Error(), "helper.js") {
		t.Errorf("错误里应指出多出来的文件，实际：%v", err)
	}
}

func TestLoad_RejectsExtensionWithoutCodeReason(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "coded", `{"id":"coded","name":"带代码","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100}}`)
	if err := os.WriteFile(filepath.Join(root, "coded", "extension.js"), []byte("export function videoIdFromSignals() { return ''; }\n"), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}

	_, err := Load(root)
	if err == nil {
		t.Fatal("带 extension.js 却没写 codeReason 却加载成功（契约 A-101）")
	}
	if !strings.Contains(err.Error(), "codeReason") {
		t.Errorf("错误里应指出缺 codeReason，实际：%v", err)
	}
}

func TestLoad_RejectsEmptyRoot(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("空目录却加载成功——那样任何站点都抓不到，且原因不可见")
	}
}

func TestLoad_RejectsBadPrimarySelection(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "bad", `{"id":"bad","name":"坏取值","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100},
		"capture":{"primarySelection":"newest"}}`)

	if _, err := Load(root); err == nil {
		t.Fatal("primarySelection 取值非法却加载成功（[14 §5.1]）")
	}
}

func TestLoad_RejectsBuiltinWithoutName(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "builtin", `{"id":"builtin","name":"内置","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100},
		"resolve":{"engine":"builtin"}}`)

	if _, err := Load(root); err == nil {
		t.Fatal("engine 是 builtin 却没写 builtin 名却加载成功（[14 §5]）")
	}
}

// ---------------------------------------------------------------------------
// 身份规则与错误映射：用抖音的离线夹具（A-112）
// ---------------------------------------------------------------------------

type identityFixture struct {
	Note  string `json:"note"`
	Cases []struct {
		URL string `json:"url"`
		ID  string `json:"id"`
		Why string `json:"why"`
	} `json:"cases"`
}

type errorFixture struct {
	Note  string `json:"note"`
	Cases []struct {
		Upstream string `json:"upstream"`
		Code     string `json:"code"`
		Why      string `json:"why"`
	} `json:"cases"`
}

func TestResolveID_DouyinFixtures(t *testing.T) {
	douyin := loadDouyin(t)

	var fixture identityFixture
	readFixture(t, filepath.Join(repoAdapters, "douyin", "fixtures", "identity.json"), &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("夹具是空的——那样这条测试什么都没验")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.URL+"/"+tc.Why, func(t *testing.T) {
			got, ok := douyin.ResolveID(tc.URL)
			if tc.ID == "" {
				if ok {
					t.Fatalf("取到 ID %q，期望取不到（%s）", got, tc.Why)
				}
				return
			}
			if !ok {
				t.Fatalf("取不到 ID，期望 %q（%s）", tc.ID, tc.Why)
			}
			if got != tc.ID {
				t.Fatalf("ID = %q，期望 %q", got, tc.ID)
			}
		})
	}
}

func TestResolveID_NoIdentityDeclared(t *testing.T) {
	generic := loadGeneric(t)
	// generic 没有 identity 段：取不到 ID 是正常结果，不是错误。
	if id, ok := generic.ResolveID("https://example.com/video/123"); ok {
		t.Fatalf("通用适配器不该解析出 ID，实际 %q", id)
	}
}

func TestMapError_DouyinFixtures(t *testing.T) {
	douyin := loadDouyin(t)

	var fixture errorFixture
	readFixture(t, filepath.Join(repoAdapters, "douyin", "fixtures", "errors.json"), &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("夹具是空的——那样这条测试什么都没验")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.Why, func(t *testing.T) {
			code, message, ok := douyin.MapError(tc.Upstream)
			if tc.Code == "" {
				if ok {
					t.Fatalf("未声明的错误被翻译成了 %q", code)
				}
				return
			}
			if !ok {
				t.Fatalf("没有命中任何规则，期望 %q", tc.Code)
			}
			if code != tc.Code {
				t.Fatalf("code = %q，期望 %q", code, tc.Code)
			}
			if message == "" {
				t.Error("翻译出来的提示不得为空——用户看到空提示等于没有提示")
			}
		})
	}
}

func TestMapError_FirstMatchWins(t *testing.T) {
	root := t.TempDir()
	writeAdapter(t, root, "ordered", `{"id":"ordered","name":"顺序","version":1,"updatedFor":null,
		"match":{"hosts":["example.com"],"priority":100},
		"errors":[
			{"match":"cookie","code":"first","message":"第一条"},
			{"match":"Fresh cookies","code":"second","message":"第二条"}
		]}`)

	set, err := Load(root)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	ordered, _ := set.ByID("ordered")
	code, _, ok := ordered.MapError("ERROR: Fresh cookies are needed")
	if !ok || code != "first" {
		t.Fatalf("code = %q（ok=%v），期望 first——按声明顺序首个命中生效（[14 §5]）", code, ok)
	}
}

// ---------------------------------------------------------------------------

func loadDouyin(t *testing.T) *Adapter {
	t.Helper()
	set, err := Load(repoAdapters)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	douyin, ok := set.ByID("douyin")
	if !ok {
		t.Fatal("找不到 douyin 适配器")
	}
	return douyin
}

func loadGeneric(t *testing.T) *Adapter {
	t.Helper()
	set, err := Load(repoAdapters)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	generic, ok := set.ByID(GenericID)
	if !ok {
		t.Fatal("找不到 generic 适配器")
	}
	return generic
}

func writeAdapter(t *testing.T, root, dir, body string) {
	t.Helper()
	dirPath := filepath.Join(root, dir)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, FileName), []byte(body), 0o644); err != nil {
		t.Fatalf("写声明失败：%v", err)
	}
}

func readFixture(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读夹具失败：%v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("夹具解析失败：%v", err)
	}
}
