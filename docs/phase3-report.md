# 阶段 3 · 验收记录（更多下载路径与站点适配器）

本文记录阶段 3 的**实际完成度与逐项证据**。性质与 [`phase1-report.md`](phase1-report.md)、[`phase2-report.md`](phase2-report.md) 一致：**不是规范**，判据一律以 [11](11-ACCEPTANCE.md) 为准，本文只记录实测与判定。

> **与其它记录的分工**：性能数值在 [`baseline.md`](baseline.md)（旧版）与 [`phase1-report.md`](phase1-report.md)（阶段 1）；本文只记阶段 3 的下载路径、路径自动路由与适配器体系状态。

| 项 | 值 |
| --- | --- |
| 记录日期 | 2026-10-02 |
| 范围 | [13 §6](13-ROADMAP.md) 阶段 3：HLS / DASH / 分离音视频合并 / yt-dlp 页面解析 / 路径自动路由 / 站点适配器体系 |
| 结论 | **下载路径（P2/P3/P4）、路径自动路由与站点适配器体系（声明驱动，抖音 L1）完成并实测通过**（`T-ADP-01`~`T-ADP-09` 全部通过，见 §2.3）；字幕与缓存管理未开始（见 §4） |

---

## 1. 交付物

| # | 交付物 | 状态 | 落点 |
| --- | --- | --- | --- |
| 1 | **P2 · HLS / DASH 清单下载**（FFmpeg 单子进程、显式选流） | ✅ 完成 | [`internal/media/manifest.go`](../internal/media/manifest.go)（`executeManifest`）+ `manifest_test.go` |
| 2 | **P3 · 分离音视频合并**（双轨各自取字节 → `streamcopy` 合并） | ✅ 完成 | [`internal/media/merge.go`](../internal/media/merge.go)（`executeTracks`）+ `merge_test.go` |
| 3 | **P4 · 页面解析**（yt-dlp `-J` + Deno 运行时，凭据走受限配置文件） | ✅ 完成 | [`internal/media/page.go`](../internal/media/page.go)（`executePage`）+ `page_test.go`（19 个顶层用例） |
| 4 | **路径自动路由**（[05 §4.0](05-DOWNLOAD.md)：只在写字节前、只降一次） | ✅ 完成 | [`internal/media/route.go`](../internal/media/route.go)（`RouteMismatch`）+ [`internal/service/plans_scheduler.go`](../internal/service/plans_scheduler.go)（`executePlan` / `fallbackFor`） |
| 5 | **实际路径落库与可见**（B-316 / T-DL-13） | ✅ 完成 | `plans.resolved_kind`（[03 §2.1](03-DATA.md)）→ `PlanView.resolvedKind` → 前端 `routeNotice` |
| 6 | **进度两种口径不混算**（[05 §4.6.3](05-DOWNLOAD.md)） | ✅ 完成 | `DownloadProgress.TimeRatio` → `runningProgress(..., ratio *float64)` |
| 7 | **站点适配器体系**（[14](14-SITE-ADAPTERS.md)：加载优先级、构建生成、抖音移植） | ✅ 完成 | 桌面端 [`internal/adapter/`](../internal/adapter/)（加载/校验/匹配）+ [`internal/app/downloader.go`](../internal/app/downloader.go)（`loadAdapters`）→ 解析层的 `media.PageHints`；扩展端 [`extension/js/content-script.js`](../extension/js/content-script.js)（声明运行时）+ [`background.js`](../extension/js/background.js)（下发声明）；生成物 [`extension/site-adapters.json`](../extension/site-adapters.json) 由 [`build/gen-site-adapters.ps1`](../build/gen-site-adapters.ps1) 生成；抖音按 **L1（纯声明）** 落地 |
| 8 | 字幕（[05 §4.3](05-DOWNLOAD.md)） | ❌ 未开始 | 仅有错误码定义，无实现 |
| 9 | 缓存管理 / 保留期（[03 D4](03-DATA.md)） | ❌ 未开始 | — |

**新增错误码**（[05 §7](05-DOWNLOAD.md)）：`page_resolve_failed`（可重试）、`page_media_unavailable`（不可重试）、`headers_too_large`（不可重试）。

**新增常量**：`pageResolveOutputLimit = 8 MiB`（`-J` 的 info JSON 实测数百 KB，默认 256 KB 会截断）、`maxHeaderBudget = 6 KiB`（[05 §4.2](05-DOWNLOAD.md) 的请求头预算）。

---

## 2. 门禁（[11 §6](11-ACCEPTANCE.md) 的阶段 3 行）

`11 §6` 列出：**全部 `T-DL-*`、`T-FS-01`~`06`、全部 `T-ADP-*`**。

### 2.1 下载路径（`T-DL-*`）

| 门禁 | 状态 | 证据 |
| --- | --- | --- |
| `T-DL-01` | ✅ 通过 | `TestRunDirect_DeliveryFailureKeepsVerifiedTempFile`、`TestDeliverFile_RejectsLengthMismatch` |
| `T-DL-02` | ✅ 通过 | `TestRun_Tracks_MapsEveryInput`（合并走 `streamcopy`） |
| `T-DL-03` | ✅ 通过 | `TestScopedMediaHeaders`（跨主机一条凭据都不带，B-304）；`TestRun_Page_CredentialsUseConfigFile`（凭据只在受限文件里，命令行上无） |
| `T-DL-04` | ✅ 通过 | `TestRunPlan_StopsRetryingAfterBudgetExhausted` |
| `T-DL-05` | ✅ 通过 | `TestRecoverInterrupted_*`（单计划异常不终止循环，重启可恢复） |
| `T-DL-06` | ✅ 通过 | 健康探测 single-flight（阶段 2 落地，本阶段未改动） |
| `T-DL-07` | ✅ 通过 | `TestRunDirect_CancelDuringValidateDoesNotDeliver` |
| `T-DL-08` | ✅ 通过（自动部分） | 阶段 2 落地；人工项按 [11 §6](11-ACCEPTANCE.md) 归阶段 8 |
| `T-DL-09` | ✅ 通过 | `TestRun_MissingFFprobeRefusesToDeliver`、`TestRunDirect_NoStreamsFailsWithoutDelivering` |
| `T-DL-10` | ✅ 通过 | `internal/proxy` 的 `TestProxyFunc_LoopbackAlwaysDirect` |
| `T-DL-11` | ✅ 通过 | `TestRunPlan_StopsRetryingAfterBudgetExhausted`（`attempt_count` 持久化） |
| `T-DL-12` | ⏳ **未做** | 属**阶段 2.5**（P6 浏览器中转），非本阶段范围 |
| `T-DL-13` | ✅ 通过 | `TestFallbackFor_OnlyThreeRoutesCanChange`（11 子例）、`TestRunPlan_FallsBackToManifest`、`TestRunPlan_FallsBackOnlyOnce`、`TestPlanView_ExposesResolvedKind` |
| `T-DL-14` | ✅ 通过 | `TestSelectManifestVariant`、`TestRun_HLS_UnparsableReportsPage`、`TestRun_Page_QualityNoMatch`（无匹配档位报 `manifest_no_matching_stream`、**不退档**） |
| `T-DL-15` | ✅ 通过 | `TestRun_Tracks_MapsEveryInput`（两条 `-map` 显式指定）、`TestRun_Tracks_ReusesCompleteTrack`（重试复用已完整轨）、`TestRun_Tracks_RewritesUnverifiableTrack` |

### 2.2 文件系统（`T-FS-01`~`06`）

| 门禁 | 状态 | 证据 |
| --- | --- | --- |
| `T-FS-01` | ✅ 通过 | `TestRun_CancelCleansOnlyItsOwnPlanDir` |
| `T-FS-02` | ✅ 通过 | 同上（只清自己归属的临时目录） |
| `T-FS-03` | ✅ 通过 | `TestCleanupAfterFailure_KeepsNonEmptyPlanDir`、`TestCleanupAfterFailure_NeverRemovesTempRoot` |
| `T-FS-04` | ✅ 通过 | `TestCleanupAfterFailure_DiskFullRemovesStaging`（临时文件归属明确、位于程序目录） |
| `T-FS-05` | ✅ 通过 | 缓存清理未进入「已完成」目录（`deliver.go` 的交付路径与清理路径不同源） |
| `T-FS-06` | ✅ 通过 | `TestDeliverFile_NameCollisionGeneratesUniqueName`、`TestDeliverFile_UniqueNameExhausted`、`TestDeliverFile_RejectsNamesWithSeparators`、`TestDeliverFile_UniqueNameIsPredictable` |

### 2.3 站点适配器（`T-ADP-*`）

| 门禁 | 状态 | 证据 |
| --- | --- | --- |
| `T-ADP-01` | ✅ 通过 | **无越层声明**：抖音由 L2 改判 **L1**（依据 A-101），`codeReason` 与 `extension.js` 均已删除，`adapters/douyin/` 实测只有 `adapter.json` + `README.md` + `fixtures/`。**公共代码不含站点名**：`tests/js/test_adapter_douyin.js` 的 H 组与 `tests/js/test_adapter_runtime.js` 的 I 组剥掉注释后扫描 `content-script.js`、`background.js`、`eagle-bridge-candidate-logic.js`，不得出现生成物里的任何 adapter id |
| `T-ADP-02` | ✅ 通过 | `internal/adapter/adapter.go` 的 `allowedEntries` 白名单 + `checkEntries`；`TestLoad_RejectsExtraEntries` |
| `T-ADP-03` | ✅ 通过 | `build/gen-site-adapters.ps1`（生成 + `-Check` 两用）实测 `RESULT: OK`，`-Check` 幂等；生成物带 `"generated": true` 与 `"source": "adapters/"`；`tests/js/test_adapter_douyin.js` A 组逐字段比对声明与生成物，并断言生成物**不含** `resolve`/`errors` |
| `T-ADP-04` | ✅ 通过 | 声明结构里**不存在**安全边界字段：解码用 `decoder.DisallowUnknownFields()`，多写一个字段即加载失败（`TestLoad_RejectsUnknownField`）；`match.hosts` 之外不得含完整 URL/Cookie——`adapters/douyin/adapter.json` 与两个夹具人工核对通过，夹具内以 `note` 声明"不含签名参数/Cookie"（A-111） |
| `T-ADP-05` | ✅ 通过（条款未被触发） | 当前**没有任何 L2 适配器**（[14 §6](14-SITE-ADAPTERS.md) 开头已声明），A-108 要求的四个纯函数没有对象可查；L2 代码如何进入扩展包未定义 → [14 §12](14-SITE-ADAPTERS.md) 的 `SA5` |
| `T-ADP-06` | ✅ 通过（条款未被触发） | 抖音走 `resolve.engine = "yt-dlp"`，**没有** `builtin` 处理器，因此不需要 `internal/adapter/<name>.go`；加载期仍校验 `builtin` 必须带名字（`TestLoad_RejectsBuiltinWithoutName`） |
| `T-ADP-07` | ✅ 通过 | `adapters/README.md` 索引表（站点/ID/层/定制原因/`updatedFor`/文档）+ `adapters/douyin/README.md` 七小节齐全（匹配范围/抓取方法/字段映射/与通用路径的差异/已知问题/验证方法/变更记录）；无秘密内容 |
| `T-ADP-08` | ✅ 通过 | **夹具离线可跑**：`internal/adapter/adapter_test.go` 的 `TestResolveID_DouyinFixtures` / `TestMapError_DouyinFixtures`，以及 `tests/js/test_adapter_douyin.js`（纯 Node，不联网、不起浏览器）。**站点断言已归位且数量未减少**：桌面侧三条抖音断言（`?modal_id=` → 规范地址、**启动工具前**规范化、错误映射）由新建的 `internal/adapter/douyin_test.go` 承接；扩展侧断言由 `tests/js/test_adapter_douyin.js` 的 A~H 八组承接 |
| `T-ADP-09` | ✅ 通过 | A-114 `TestLoad_RejectsIDMismatch` · A-115 `TestLoad_RejectsHostConflict` · A-116 `TestLoad_RejectsBrokenJSONWithPath`（报错含文件路径）· A-117/A-118 由"声明结构里根本没有这两个字段"保证 · A-119 三条：`TestLoad_RejectsErrorRuleWithoutCodeOrMessage` / `TestLoad_RejectsCanonicalWithoutID` / `TestLoad_RejectsCanonicalOnUndeclaredHost`（含正向 `TestLoad_AcceptsCanonicalInsideDeclaredHosts` 防误伤） |

**小计：下载路径 14/15 条通过（`T-DL-12` 属阶段 2.5）· 文件系统 6/6 通过 · 适配器 9/9 通过**。

**适配器体系的两个消费端**（不属于 `T-ADP-*` 的判据，但属于交付物 7 的完整性）：

| 端 | 落点 | 说明 |
| --- | --- | --- |
| 桌面端 | `internal/app/app.go` 的【D8】步骤 → `internal/app/downloader.go` 的 `adapterHints` → `internal/media` 的 `PageHints` | 启动时加载 `程序安装目录/adapters/`；失败**不终止启动**，只把 A-116 的原文同时写进日志与 `StartupWarnings()`；解析前按 `resolve.canonicalizePageUrl` 规范化地址（[14 §10] 的 M12），解析失败时按 `errors` 把原始输出翻译成声明里的码与文案（原始输出只在内存里过一遍，B-722） |
| 扩展端 | `extension/js/content-script.js` 的声明运行时 + `extension/js/background.js` 的 `getSiteAdapters` | 声明由背景侧读包内 `site-adapters.json` 后经既有消息通道下发（**不加 `web_accessible_resources`**）；拿不到就退化为"没有适配器"的通用行为且只 `console.warn` 一次，候选发现**不因声明不可用而失效**；为避免同一候选被通用路径与声明路径各报一次，先等一次声明（有超时上限）再开跑 |

---

## 3. 真实站点实测（**含失败，如实记录**）

单元测试全部离线可跑（[12 §5](12-CONVENTIONS.md)），真实站点实测单独放在 [`internal/media/integration_test.go`](../internal/media/integration_test.go)（`//go:build integration`，默认 `go test ./...` 不编译）。

跑法：

```powershell
$env:HTTPS_PROXY='http://127.0.0.1:7890'
go test -tags integration ./internal/media/ -run TestIntegration -v -timeout 30m
```

### 3.1 成功：P4 整条链路真实可用（Wikimedia Commons）

| 项 | 值 |
| --- | --- |
| 页面 | `https://commons.wikimedia.org/wiki/File:Big_Buck_Bunny_medium.ogv` |
| 声明档位 | `240p`（命中 format 0，webm 426x240，自带音频） |
| 结果 | **PASS** |
| 交付 | `…\已完成\真实站点实测` |
| 字节数 | **24 852 110**（与本地文件大小一致） |
| 容器 / 时长 | `matroska,webm` / **596.5 s** |
| 耗时 | **6.11 s** |
| 阶段序列 | `downloading` → `merging` → `validating`，`INFO 文件已交付` |

这条证明：**页面解析 → 选流 → 取字节 → FFprobe 校验 → 原子交付**整条 P4 链路在本机真实跑通，且交付前的校验是实测过的（不是只跑了单测）。

### 3.2 失败：三个站点的真实拦截（**不是本程序的缺陷，但必须记下来**）

| 站点 | 命令 | 结果 |
| --- | --- | --- |
| **YouTube**（直连） | `yt-dlp --simulate --print "%(id)s\|%(title)s" https://www.youtube.com/watch?v=jNQXAC9IVRw` | `ERROR: [youtube] jNQXAC9IVRw: Sign in to confirm you're not a bot. Use --cookies-from-browser or --cookies for the authentication.` |
| **YouTube**（走代理 `127.0.0.1:7890`） | 同上 | **同样报 bot 风控**——说明判定发生在**出口 IP / PO token 层面**，不是"本机没代理" |
| **Vimeo** | `https://vimeo.com/76979871` | `ERROR: [vimeo] 76979871: Failed to fetch macos OAuth token: HTTP Error 401: Unauthorized` |
| **B 站** | `https://www.bilibili.com/video/BV1xx411c7mD` | `ERROR: [BiliBili] 1xx411c7mD: Unable to download JSON metadata: HTTP Error 412: Precondition Failed` |

`TestIntegrationPage_YouTube` **刻意不断言"能下"**，只断言"本程序不撒谎"：成功必须有文件，失败必须带码且码只能是 `page_resolve_failed` 或 `page_media_unavailable`。实测结果：`YouTube 这次没放行，如实报成 page_resolve_failed：无法解析这个页面，请稍后重试`（3.16 s）。

> **红线 `M10` 保持**：YouTube 的登录会话问题**不得声称已修复**。本次实测给出的结论是"**现状**"而非"已解决"——用户侧退路是浏览器下载模式（`11 §6` 的阶段 2.5），不是本阶段修好了 YouTube。

---

## 4. 未完成事项（**必须显式标注**）

| 项 | 状态 | 原因与下一步 |
| --- | --- | --- |
| 站点适配器体系（`T-ADP-01`~`09`） | ✅ 已完成 | [14 §11](14-SITE-ADAPTERS.md) 的八步流程走完（抖音：声明 → 通用路径核对 → 补 `identity`/`capture`/`title` → 无 `extension.js` → README 七小节 → 夹具与离线测试 → 索引表 → 生成物）；[14 §4.1](14-SITE-ADAPTERS.md) 的 `build/gen-site-adapters.ps1`（`SA2`）已落地并实测 `-Check` 幂等。**仍未做的两件事**：① 抖音按**真实浏览器页面**人工核对（[11 §5](11-ACCEPTANCE.md) 的 M12）——因此 `adapters/douyin/adapter.json` 的 `updatedFor` 保持 `null`；② `SA5`（若将来出现 L2 适配器，其代码如何进入扩展包）尚未定义 |
| 字幕（[05 §4.3](05-DOWNLOAD.md)） | ❌ 未开始 | 阶段 3 范围含字幕；`internal/media` 目前只有 `probe.go` 的类型字段提到 `subtitle` |
| 缓存管理 / 保留期 | ❌ 未开始 | [03 D4](03-DATA.md) 的默认值仍是待确认项（[13 §7.4](13-ROADMAP.md)） |
| `T-DL-12`（P6 浏览器中转） | ⏳ 属阶段 2.5 | 非本阶段范围 |
| `14 SA1`（首批适配器清单） | ⏳ 未定 | [13 §7.4](13-ROADMAP.md) 的待确认项，需与适配器体系一起定 |
| `golangci-lint` | ❌ 未安装 | 门禁第 1 项（[12 §6.2](12-CONVENTIONS.md)）仍为 `INCOMPLETE`（`build/check-go.ps1` 以退出码 2 如实表示） |
| `13 §7.4` 其余待确认项 | ⏳ 部分关闭 | `05 N6`（遗漏错误码）已由本次新增三码推进；`05 N9` 维持"不引入 FFmpeg 重连参数"；`05 N10`（P2 时间进度口径）已在 [05 §4.6.3](05-DOWNLOAD.md) 定稿并实测；其余（`03 D4/D5`、`05 N1`~`N5`、`N7/N8`、`14 SA1/SA3`）**仍开着** |

---

## 5. 本阶段顺带查实的事实（供后续复用）

1. **`RouteMismatch` 是控制信号，不是失败** —— 返回它时**保证尚未写入任何字节**（[05 §4.0](05-DOWNLOAD.md)），因此调用方可以在同一次执行内换路。它的 `Address` 字段**不进 `Error()`、不进日志**：地址可能带一次性签名参数（[03 §6](03-DATA.md)、B-722）。
2. **`attempt_count` 只在"转回重试"时写**（[`internal/service/plans_scheduler.go`](../internal/service/plans_scheduler.go) 的 `handleFailure`）——成功完成不写。判"改道不消耗重试预算"要用**两次都失败**的场景：`TestRunPlan_FallsBackOnlyOnce` 断言 `attempt_count == 1`（两次引擎调用、一次尝试）。
3. **`runOnce` 不能用来判断"执行结束"** —— 它等的是"计划不再是 `queued`"，而计划一被取走状态立刻变 `running`，因此它会在执行**中途**返回。改路场景下会读到只有 1 次调用。判定要用"引擎被调用了第 N 次"或"计划到达终态"这类确定信号。
4. **`scriptedRunner.ffmpegCalls()` 的判据是"不是 FFprobe"** —— 引入 yt-dlp 后，它会把 yt-dlp 调用算成 FFmpeg，产生"合并调用了两次 FFmpeg"这类**假失败**。测 yt-dlp 的替身必须覆写 `ffmpegCalls()`，同时排除 `isProbeCall` 与 `isYtDlpCall`。
5. **`-J` 的输出远超默认工具输出上限** —— `DefaultOutputLimit = 256 KiB` 会截断 info JSON，而截断的 JSON 必然解析失败。P4 单独用 8 MiB。
6. **凭据不能走命令行** —— [`internal/media/page.go`](../internal/media/page.go) 把请求头写成 `yt-dlp.conf` 再经 `--config-locations` 传入（`--add-headers` 是 yt-dlp 读请求头的唯一入口），用完即删；命令行上不出现任何凭据。
7. **Deno 不在 `PATH` 里** —— 它随程序分发在 `media-tools/`，必须显式 `--js-runtimes deno:<绝对路径>`，否则 yt-dlp 自己找不到。
8. **刻意不加 `--ignore-config`** —— 用户机器上的 yt-dlp 配置（尤其 `--proxy`）可能正是访问某些站点所必需的。（本机实测 `%APPDATA%\yt-dlp\config` 不存在，因此当前不引入任何用户配置。）
9. **PowerShell 文本改写会毁掉 Go 源码** —— `(Get-Content -Raw) -replace ... | Set-Content -NoNewline` 的编码转换会破坏中文（后续 grep 报 `line is not valid UTF-8`）。**教训：源码编辑一律用编辑工具，批量改写必须显式 `[System.IO.File]::ReadAllText/WriteAllText` + UTF-8 无 BOM，并且先备份。** 本次因 `git checkout --` 恢复而丢失了若干**未提交**的测试改动，已重写。
10. **`probe.ProbeInfo` 的 `Type` 字段** 已预留 `subtitle`（[05 §4.3](05-DOWNLOAD.md) 落地时不用改结构）。
11. **测试日志里可核对的顶层用例数**：`go test ./internal/... -v -count=1` 实测 **331 个顶层 `--- PASS`**、0 `FAIL`。

---

## 6. 验证命令（可复现）

```powershell
# 环境（见 CONTEXT §3.4）
$env:Path = "C:\Users\MSI\go-sdk\go\bin;" + $env:Path
$env:GOPROXY = 'https://goproxy.cn,direct'

# Go 侧（12 §1.1：不得用裸 ./...，node_modules 里有第三方 Go 源码）
go build ./ ./internal/...
go vet   ./ ./internal/...
gofmt -l ./internal

# 全部单元测试（离线，不依赖网络）
go test ./internal/... ./ -count=1

# 真实站点实测（需要能出网的代理；YouTube 用例只断言"不撒谎"）
$env:HTTPS_PROXY = 'http://127.0.0.1:7890'
go test -tags integration ./internal/media/ -run TestIntegration -v -timeout 30m

# 契约覆盖
pwsh -File tools/Check-ContractCoverage.ps1

# 站点适配器：声明 → 生成物的单向分发（12 §6.2 第 6 项门禁）
# 注意：本机只有 Windows PowerShell 5.1（无 pwsh 7），含中文的 .ps1 必须带 UTF-8 BOM
powershell -File build/gen-site-adapters.ps1 -Check

# 扩展侧（纯 Node，离线；不联网、不起浏览器）
node tests/js/test_adapter_runtime.js
node tests/js/test_adapter_douyin.js
node --check extension/js/content-script.js
node --check extension/js/background.js

# 适配器加载与消费端
go test ./internal/adapter/ ./internal/app/ ./internal/media/ ./internal/service/ -count=1
```

**`golangci-lint` 仍不可用**：本机未安装，[12 §6.2](12-CONVENTIONS.md) 门禁第 1 项维持 `INCOMPLETE`。

---

## 7. 结论

阶段 3 的四项内容——**下载路径（P2 / P3 / P4）、路径自动路由、实际路径可见、站点适配器体系（声明驱动，抖音 L1）**——**已完成**：`T-DL-01`~`11`、`13`~`15` 与 `T-FS-01`~`06` 通过并有真实站点实测支撑，`T-ADP-01`~`09` **9/9 通过**（见 §2.3），`T-DL-12` 属阶段 2.5。

**不能据此说阶段 3 全部完成**，仍缺三件：① 字幕（[05 §4.3](05-DOWNLOAD.md)）；② 缓存管理 / 保留期（[03 D4](03-DATA.md)）；③ 适配器的**真实浏览器端到端**与抖音 M12 人工核对——离线测试覆盖了声明解析、匹配、身份、标题、选主与运行时计划（`planAdapterDiscovery`），而 DOM 采集层、`chrome.runtime` 往返与启动时序**只能在真机浏览器里验**。

**YouTube 的现状照旧**：yt-dlp 直连与走代理都被要求"确认你不是机器人"（[CONTEXT §4](../CONTEXT.md) 的 M10 红线），本程序把这种情况如实报成 `page_resolve_failed`，**没有也不会声称已修复**。

交接提示：进度与下一步见 [`CONTEXT.md`](../CONTEXT.md) §2。
