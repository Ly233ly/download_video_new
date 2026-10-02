package api

import (
	"github.com/Ly233ly/download_video_new/internal/service"
)

// 本文件是**本包的请求体契约**：HTTP JSON 字段 → `service` 请求类型。
//
// 为什么单独成文件、而不是在 handler 里直接解析成 `service.*Request`：
//
//   - [04 §7] 的 `I1`（每端点的请求/响应 schema）正由文档侧回填。在文档定稿前
//     让传输格式直接绑定 Service 的字段名，会把"扩展发什么"与"Service 怎么存"
//     绑成同一个事实；分开之后，文档回填只需改这一个文件。
//   - 这里是唯一的**字段名落点**：扩展、文档、Service 三方的字段名在此对齐，
//     拼错一个字母只需要改一处。
//
// 字段取值域（容器、合并方式、media_kind 等）**不在本文件校验**：
// 它们是 [03 §3.3] 的业务枚举，判定与错误码归属 Service（[04 §1.2] 的 G5）。
//
// **schema 待回填对齐**：本文件的 JSON 字段名取自 [04 §2.3] 的端点用途、
// [03 §2.1] 的 `plans` 列与 [05 §3.1] 的入参表；`I1` 回填后以此处为对照点收敛。

// createPlanBody 是 `POST /api/plan` 的请求体。
//
// 布尔字段用**指针**是刻意的：`importToEagle` 缺省时必须是"未给"而不是 `false`，
// 否则调用方漏传字段与显式传 `false` 无法区分。区分它们不是为了业务判断，
// 而是为了在映射到 Service 时保留原始语义（[05 §3.1] 的两个布尔都有默认值 0，
// 由 Service 落实默认值）。
type createPlanBody struct {
	// URL 是媒体地址或页面地址，按 mediaKind 解释（[05 §3.1]）。
	URL string `json:"url"`

	// SourceURL 是来源页面地址（扩展上报的页面 URL）。
	//
	// 它对应 [05 §3.1] 的 `PageURL`，而不是 `URL`：`service.CreatePlanRequest`
	// 用 `PageURL` 承载"可落库的归一化页面地址"（[03 §2.1]），
	// 用 `URL` 承载只进内存的媒体地址（[03 §6]、B-722）。
	// 把两者混为一谈会让签名 URL 有机会落库。
	SourceURL string `json:"sourceUrl"`

	// MediaKind 取 [03 §3.3] 的 `direct` / `hls` / `dash` / `page` / `wechat` / `browser`。
	MediaKind string `json:"mediaKind"`

	// SourceTitle 是页面标题（[03 §2.1]），可选。
	SourceTitle string `json:"sourceTitle"`

	// OutputName / OutputContainer / MergeMode 是 [05 §3.1] 的三个必填项。
	OutputName      string `json:"outputName"`
	OutputContainer string `json:"outputContainer"`
	MergeMode       string `json:"mergeMode"`

	// QualityLabel 是画质档位，可选（视频号专用）。
	QualityLabel string `json:"qualityLabel"`

	// ImportToEagle / DeleteAfterImport 见上方关于指针的说明。
	ImportToEagle     *bool `json:"importToEagle"`
	DeleteAfterImport *bool `json:"deleteAfterImport"`
}

// toServiceRequest 把传输结构映射成 Service 的创建入参。
//
// 只做字段搬运，**不做任何取舍**：
//
//   - 不校验取值域（容器、合并方式、media_kind）——那是 [05 §3.2] 的创建期校验；
//   - 不因为 `deleteAfterImport` 为真而自动把 `importToEagle` 置真
//     （[05 §3.1] 的"`import_to_eagle = 0` 时 `delete_after_import` 必须为 0"
//     属业务校验，归 Service 返回 `invalid_*` 码）；
//   - 不预检地址是不是本机/内网（[05 §3.2] 的 `blocked_local_target` 归 Service）。
//
// URL / PageURL 的分工见字段注释：`url` → 内存中的媒体地址，
// `sourceUrl` → 可落库的来源页面地址。
func (b createPlanBody) toServiceRequest() service.CreatePlanRequest {
	req := service.CreatePlanRequest{
		URL:             b.URL,
		PageURL:         b.SourceURL,
		MediaKind:       b.MediaKind,
		SourceTitle:     b.SourceTitle,
		OutputName:      b.OutputName,
		OutputContainer: b.OutputContainer,
		MergeMode:       b.MergeMode,
		QualityLabel:    b.QualityLabel,
	}
	if b.ImportToEagle != nil {
		req.ImportToEagle = *b.ImportToEagle
	}
	if b.DeleteAfterImport != nil {
		req.DeleteAfterImport = *b.DeleteAfterImport
	}
	return req
}
