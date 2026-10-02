// Package api 是本地回环 HTTP API（[01 §3] 的 `internal/api/`，IF-1）。
//
// 职责边界（这条边界是本包存在的全部理由）：
//
//	浏览器扩展 → 本包（解析参数 / 调用 Service / 序列化结果）→ internal/service
//
// 本包**只做**参数解析、调用 Service、序列化结果；**禁止**在这里写业务判断
// （状态校验、重试决策、降级逻辑）——[04 §1.2] 的 G5 与 [01 §9] 的 `N9`。
// 所有业务错误码都由 Service 返回，本包只负责**转发**，不改写其语义。
//
// 三条硬性约束落在本包：
//
//	S1 只监听 127.0.0.1（B-1001、T-STB-01）——不得绑 :端口，也不得写 localhost；
//	S4 端口被占用时回退临时端口，监听成功后把实际端口写进 platform.APIPortPath()；
//	N10 自建 net/http（[01 §2.1.1]）——**复用 Wails AssetServer 是禁止项**。
package api

import "time"

const (
	// MainPort 是本地 API 的主端口（[01 §2.1.1] 的 S4、[04 §2.1]）。
	//
	// 取值只在 [01 §2.1.1] 定义，本包不复制第二份语义，只引用。
	// 被占用时**必须**回退临时端口——不得在这里静默换一个固定端口，
	// 否则扩展的发现文件与实际端口会各说一套。
	MainPort = 47652

	// LoopbackHost 是**唯一**允许的绑定地址（[01 §2.1.1] 的 S1）。
	//
	// 刻意不是 `localhost`：IPv4/IPv6 解析有歧义，可能落到 `::1` 而让扩展
	// 按 `127.0.0.1` 找不到；也刻意不是空 host（`:47652`）——那会监听全部
	// 网卡并触发 Windows 防火墙弹窗（B-1001 的反例）。
	LoopbackHost = "127.0.0.1"

	// DefaultExtensionOrigin 是编译期内置的扩展 Origin 白名单（[07 §1.1]、[01 §2.1]）。
	//
	// **这是常量，不是配置**：扩展 manifest 固定 `key`，ID 因此在开发者模式与
	// 打包后一致（Chrome 官方机制），所以桌面端可以把白名单编译进去，
	// 安装器**不写入任何 Origin**（[04 §2.2]）。
	//
	// 三处同源的落点：本常量、`docs/07-EXTENSION.md` §1.1、`extension/manifest.json`
	// 的 `key`。**换 key 就换 ID**，改一处必须同步另两处。
	DefaultExtensionOrigin = "chrome-extension://cfefnmhhollflbhgbdmphgnpeaeipfil"

	// ExtensionOriginSettingKey 是 `settings.extension_origin`（[03 §2.4]）。
	// 规范出处是 [01 §2.1]："若需要临时用其他 ID 调试，可通过设置项覆盖"。
	ExtensionOriginSettingKey = "extension_origin"
)

// 上限与预算。**取值都只在本文件的这一处**，别处引用不复制。
const (
	// MaxRequestBodyBytes 是本地 API 的请求体上限：256 KB（[03 §4.5]、[04 §2.1]）。
	//
	// 唯一例外是中转上传端点（[04 §2.5]，阶段 2.5），其分片上限定义在
	// [03 §4.6]——那个端点**不得**复用本常量设限，否则 1 MB 分片会被拒。
	MaxRequestBodyBytes = 256 << 10

	// ReadHeaderTimeout 是读请求头的上限（[01 §2.1.1] 的 S5：5–10 s）。
	// 它是"慢速请求头"这类连接占用攻击的唯一有效防线。
	ReadHeaderTimeout = 10 * time.Second

	// IdleTimeout 是 keep-alive 空闲上限（[01 §2.1.1] 的 S5：60–120 s）。
	IdleTimeout = 120 * time.Second

	// RequestTimeout 是**单个请求**内 Service 调用与序列化的预算（[04 §1.1] 的 G1、
	// [01 §9] 的 N3/N4：无 context 的调用是禁止项）。
	//
	// 为什么不是 `http.Server.ReadTimeout` / `WriteTimeout`：S5 明确规定这两个
	// **设 0**，理由是需要闲置上限时应使用 `http.ResponseController` 续期，
	// 而不是给整条连接设死超时。因此"每请求读写超时有界"（[04 §2.1]）在本包里
	// 由 ReadHeaderTimeout + 本预算共同保证，两者都是有界值。
	//
	// 取值 10 s：本阶段所有端点的 Service 调用都是本地数据库操作，正常在毫秒级；
	// 阶段 2.5 的中转上传是长事务，由该端点的 handler 自行续期，**不继承**本预算。
	RequestTimeout = 10 * time.Second

	// ShutdownTimeout 是优雅关闭的预算（[01 §5.3]：`srv.Shutdown(ctx)` 3 s）。
	// 超预算就关闭连接，**不阻塞退出**——[01 §5.2] 的第 4 步不允许拖住整个退出流程。
	ShutdownTimeout = 3 * time.Second

	// portFileMode 是端口发现文件的权限：仅当前用户可读写。
	// 它不是秘密（只含端口与 pid），但没有任何理由让同机其他用户改写它。
	portFileMode = 0o600
)

// 列表查询的边界（[03 §4.5]、[03 §5] 的 Q1：列表必须命中索引且有 `LIMIT`）。
//
// 本包只做**参数合法性**校验（是不是正整数、有没有超过上限），
// 不判断"这个状态值是否合法"——那是 Service 的业务判断（[04 §1.2] 的 G5）。
const (
	// DefaultListLimit 是调用方未给 `limit` 时的取值。
	DefaultListLimit = 50

	// MaxListLimit 是单次返回条数的上限：200（[03 §4.5] 的"单次返回的计划数"）。
	// 超过**拒绝**而不是截断：静默截断会让扩展以为已经拿全，从而漏显示计划。
	MaxListLimit = 200

	// MaxStatusFilter 是状态过滤值的个数上限（[03 §3.1] 只有 5 个状态）。
	// 给到 8 是为了容纳将来的状态值而不必改这里，同时仍是有界值。
	MaxStatusFilter = 8
)
