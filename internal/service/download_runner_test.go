package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/Ly233ly/download_video_new/internal/media"
)

// 下载引擎适配的测试（[05 §4]）。
//
// 这里只验证**字段搬运**：真实引擎的行为由 [internal/media] 自己的测试覆盖，
// 本包要保证的是"送进去的东西对、拿出来的东西没被曲解"。

func TestToEngineRequest_MapsEveryField(t *testing.T) {
	req := DownloadRequest{
		PlanID:          "p_1",
		MediaURL:        "https://cdn.example.com/a.mp4?sign=x",
		Headers:         map[string]string{"Cookie": "session=abc"},
		MediaKind:       "direct",
		OutputName:      "示例",
		OutputContainer: "mp4",
		MergeMode:       "single",
		TempDir:         `C:\out\临时`,
		OutputDir:       `C:\out\已完成`,
		KnownTotalBytes: 2048,
	}

	got := toEngineRequest(req)
	if got.PlanID != req.PlanID || got.URL != req.MediaURL {
		t.Errorf("计划标识或媒体地址未搬运：%+v", got)
	}
	if got.OutputName != req.OutputName || got.Container != req.OutputContainer {
		t.Errorf("输出名或容器未搬运：%+v", got)
	}
	if got.TempDir != req.TempDir || got.OutputDir != req.OutputDir {
		t.Errorf("目录未搬运：%+v", got)
	}
	if got.TotalBytes != req.KnownTotalBytes {
		t.Errorf("已知总长度未搬运：%d", got.TotalBytes)
	}
	if got.Headers.Get("Cookie") != "session=abc" {
		t.Errorf("会话上下文未搬运：%v", got.Headers)
	}
}

func TestToEngineRequest_UnknownTotalStaysZero(t *testing.T) {
	req := DownloadRequest{PlanID: "p_1", MediaURL: "https://x/a.mp4", KnownTotalBytes: knownTotalBytes(nil)}
	if got := toEngineRequest(req); got.TotalBytes != 0 {
		t.Errorf("长度未知时引擎侧应为 0，实得 %d", got.TotalBytes)
	}
}

// 引擎用 0 表示"长度未知"，本包用 nil——这是 [03 §2.1] 三态在边界上的唯一一次转换。
func TestFromEngineProgress_KeepsUnknownDistinctFromZero(t *testing.T) {
	unknown := fromEngineProgress(media.Progress{Downloaded: 10, Total: 0, Phase: media.PhaseDownloading})
	if unknown.TotalBytes != nil {
		t.Errorf("Total = 0 应当映射为 nil（长度未知），实得 %v", *unknown.TotalBytes)
	}
	if unknown.DownloadedBytes != 10 {
		t.Errorf("已下载字节数 = %d", unknown.DownloadedBytes)
	}

	known := fromEngineProgress(media.Progress{Downloaded: 10, Total: 100, Phase: media.PhaseDownloading})
	if known.TotalBytes == nil || *known.TotalBytes != 100 {
		t.Errorf("Total = 100 应当映射为非 nil 的 100，实得 %v", known.TotalBytes)
	}
}

func TestPhaseDetailFor_IsDirectlyDisplayable(t *testing.T) {
	cases := map[string]string{
		media.PhaseDownloading: "正在下载",
		media.PhaseMerging:     "正在合并音视频",
		media.PhaseValidating:  "正在校验",
		"未知阶段":                 "正在下载",
	}
	for phase, want := range cases {
		detail := phaseDetailFor(phase)
		if detail != want {
			t.Errorf("phaseDetailFor(%q) = %q，期望 %q", phase, detail, want)
		}
		// [03 §2.1]：phase_detail 只允许可直接展示的短文案，不得含路径、URL 或秘密。
		if strings.Contains(detail, "://") || strings.ContainsAny(detail, `\/`) {
			t.Errorf("阶段文案含路径或 URL：%q", detail)
		}
	}
}

func TestTranslateEngineError_KeepsCodeAndSafeMessage(t *testing.T) {
	raw := media.ErrorOf(media.CodeDiskFull, errors.New(`写入 C:\out\临时\a.bin 失败`))
	translated := translateEngineError(raw)

	var downloadErr *DownloadError
	if !errors.As(translated, &downloadErr) {
		t.Fatalf("翻译结果不是 *DownloadError：%T", translated)
	}
	if downloadErr.Code != string(media.CodeDiskFull) {
		t.Errorf("错误码 = %q，期望 %q", downloadErr.Code, media.CodeDiskFull)
	}
	if downloadErr.Message == "" {
		t.Error("翻译后必须带可展示消息")
	}
	if strings.Contains(downloadErr.Message, `\`) || strings.Contains(downloadErr.Message, "://") {
		t.Errorf("消息含路径或 URL：%q", downloadErr.Message)
	}

	// 不带码的错误原样上抛：本包的分类逻辑会兜住它，且绝不复用它的文案（[12 §3.3] 的 E2）。
	plain := errors.New("目录参数非法")
	if got := translateEngineError(plain); !errors.Is(got, plain) {
		t.Errorf("未带码的错误应当原样上抛，实得 %v", got)
	}
}

// 站点适配器声明的错误（[14 §5.2]）走的是**码与文案都由声明给出**这条路：
// 生产路径必须逐字透传，既不能套用码表的通用文案，也不能把解析工具的原始
// 输出（可能带签名 URL）带出去（[05 §4.2]、B-722）。
func TestTranslateEngineError_PassesSiteDeclarationThrough(t *testing.T) {
	site := &media.SiteError{
		Code:    "douyin_session_expired",
		Message: "抖音需要当前浏览器的新鲜会话，请刷新抖音页面后重试",
	}
	translated := translateEngineError(site)

	var downloadErr *DownloadError
	if !errors.As(translated, &downloadErr) {
		t.Fatalf("翻译结果不是 *DownloadError：%T", translated)
	}
	if downloadErr.Code != "douyin_session_expired" {
		t.Errorf("错误码 = %q，期望声明给出的 douyin_session_expired", downloadErr.Code)
	}
	if downloadErr.Message != "抖音需要当前浏览器的新鲜会话，请刷新抖音页面后重试" {
		t.Errorf("消息 = %q，期望逐字等于声明文案", downloadErr.Message)
	}
	// 站点声明的码不在 [05 §7.1~§7.3] 的固定表里，因此**不可自动重试**：
	// 重试一次还是同样的会话问题，只会让用户多等一轮。
	if IsRetryableCode(downloadErr.Code) {
		t.Errorf("站点声明的码 %q 不应被判为可自动重试", downloadErr.Code)
	}
}
