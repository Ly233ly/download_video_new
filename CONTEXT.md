# 项目上下文 · 从零重写「留底下载器」

> **这份文档是给新的对话窗口的起点，也是唯一的交接文档。它不是规范。**
> 规范是 `docs/01` ~ `docs/16` 与 `docs/adr/`——**规格内容只在那里定义**（设计原则 3）。
> 本文只回答四件事：现在在哪、下一步做什么、什么绝对不能碰、东西都在哪（**外加 §3.4 的本机环境基线**——换机器时的开工前必读）。
>
> **维护规则**：每轮对话结束时更新本文——**已解决的事项从"待办"移除，未解决的继续留在待办**；已解决但属于重要里程碑的，压成一行记到 §2 的完成记录里。
>
> 最后更新：2026-10-02

---

## 1. 一句话

把一个 Windows 本机媒体下载与归档工具，从 **Python/Tkinter + C#** 全量重写为 **Go + Wails**。

| 项 | 值 |
| --- | --- |
| **开发目录** | `E:\DSH\download_refactor`（唯一） |
| **远程仓库** | <https://github.com/Ly233ly/download_video_new>（`origin`，分支 `main`） |
| **旧项目** | `E:\Users\MSI\Desktop\codex_download`（**只读，永不修改**） |
| **开发方式** | 全 AI 开发，**文档是权威**，实现服从文档 |
| **当前状态** | **阶段 2（第一条完整链路）已交付，门禁 5/11 通过**——逐项证据见 [`docs/phase2-report.md`](docs/phase2-report.md)。**Go 侧与前端完成**：`go build`/`go vet` 干净、8 个包测试全绿；前端 `check` exit 0、**79 个测试通过**。已过：`T-BRAND-03` · `T-STB-01` · `T-FS-01` · `T-DL-01` · `T-DL-10`。**扩展侧**：源码已迁入并重写（`tests/js/` 8 个测试资产也已落地，其中**新增的 `test_list_rendering.js` 覆盖 `P-101`~`P-109` 并通过**）；`test_bilibili.js` / `test_youtube.js` / `test_wechat_channels_bridge.js` 为 **SKIP 且理由明确**（站点脚本属阶段 3、视频号属阶段 5，断言原样保留）；`test_auth_race.js` 与 `test_popup_logic.js` **待修复**。故 `T-EXT-10` 与 `T-EXT-15~19` 尚**未正式判定**。**阶段 1 门禁遗留不变**：`PF-A4` ⚠️ p50 通过、**p95 待无负载复测** · `PF-A3`/`T-STB-05` ⏳ 试点通过、**正式 10 分钟未测**（**已按用户要求暂缓**） |

---

## 2. 现在在哪

| 项 | 状态 |
| --- | --- |
| 规范 | **17 份 + 4 个 ADR**；已做四轮审阅（详细度 / 冗余 / 矛盾）并修完发现的问题 |
| 契约编号 | `B-xxx` 120 条 · `P-1xx` 10 条 · `A-1xx` 17 条，**全部有验收覆盖** |
| 设计稿 | [`design/mockup.html`](design/mockup.html) 已定稿（浅色主调 + 白卡片 + 蓝），含扩展弹窗的候选/任务/设置三个标签页 |
| 自检 | 文档断链 0、跨文档章节引用全有效、设计稿自检脚本 `exit=0` |
| 环境 | **Go 1.27.1 已装**（`C:\Users\MSI\go-sdk\go`，ZIP 免管理员）· **Wails CLI v2.16.0 已装**（`C:\Users\MSI\go\bin`）· WebView2 Runtime 已装 · **`wails build` 实测通过**（见 §3.4，**不需要 C 编译器**） |
| 代码 | **26 个 Go 文件 + 18 个 ts/tsx**（阶段 1 的 `D1`~`D7`），已入库并推送到 `origin/main`（`37a3a37`）。Go 门禁：[`build/check-go.ps1`](build/check-go.ps1) 的 gofmt 与 go vet 通过；`golangci-lint` 本机**未安装**，该步为 `INCOMPLETE`（退出码 2，**不视为通过**） |

### 已完成的关键事项（不需要重做）

| 事项 | 结果 |
| --- | --- |
| 规范定稿与审阅 | 四轮审阅（详细度 / 冗余 / 矛盾）修掉 5 个真矛盾：M 编号错位、「标题栏」两义、读取方表述、R5/R7/R8 编号错、自检脚本描述不实；删 5 处结构性 + 4 处单行重复（**把约束救成了硬性禁止项**）；补 10 项已确认数值（超时/端口/重试/节流/滚动参数/hook 时限等）+ IP 拒绝集合、`error_code` 取值域、P6 取消下行等缺口 |
| 上游考古与边界 | 新建 [15 号文档](docs/15-UPSTREAM.md)：还原 cat-catch、两个视频号项目、3 项外部资料、8 个前端库；按源码核对 yt-dlp **已支持抖音**（`DouyinIE`）等 934 个站点、**不支持视频号**，据此划清适配器分工；只读研究 `ltaoo/wx_channels_download`，**原画教训**固化成 [06 §6.6](docs/06-WECHAT.md) 三条硬要求 |
| git 建仓 | 此前**完全没有版本控制**（规范无历史可回滚）；现已建本地仓 |
| 验收阈值补完 | `PF-A1~A7`、`ST-2/6/7`、`T-UI-01/06` 全部改成可机械执行的判据（三分类：硬数值 / 相对判据 / 流程判据）；§2 **十一张回归表全部补齐"类型"列**（117 项：A 99 · A+M 17 · M 1）。`ST-2` 直接引用 [01 §5.3](docs/01-ARCHITECTURE.md) 的代理恢复 3 s、`ST-6` 引用 [03 §5](docs/03-DATA.md) 的 `busy_timeout` 5000 ms——**不新定数值，只把既有定义接上**。提交 `9aa2fdb` |
| 阶段编号统一 | `11 §6` 补 0.5/2.5 两行、末行改"8（发布验证）"；`T-EXT-26` 从阶段 2 移到 2.5（[13](docs/13-ROADMAP.md) 侧原本两处互相矛盾）；阶段 3 统一 `T-ADP-*`；[13 §2](docs/13-ROADMAP.md) 补阶段 8 定义；[13 §10](docs/13-ROADMAP.md) 的 `D-3` 不再用"有明确改善" |
| 工具链就位 | Go 1.27.1 + Wails CLI v2.16.0 装好；**实建了一个一次性项目验证可构建**（`wails init -t vanilla` 1.7 s、`wails build` 9.3 s 产出 11.9 MB exe），证明**无需 C 编译器**；`wails doctor` 报 SUCCESS（见 §3.4） |
| 许可证路线已定 | **继续复用 cat-catch 扩展**（组合发行按 GPL-3.0 并提供对应源码）；依据：用户明确**本软件不出售**，Commons Clause 类附加条款不构成约束。**若将来改为公开分发，GPL-3.0 的提供源码义务仍然成立** |
| 阶段 0.5 已部分完成 | 建立 [`docs/baseline.md`](docs/baseline.md)：实测官方 1.6.3 冻结版的冷启动（p50 1136.3 ms）与空闲常驻（CPU 0.06 % / 64 MB / 444 句柄）；`B-3`~`B-8` 六项如实标"未测"。测量口径写在该文档 §4（脚本已按要求删除） |
| 前端框架定案 | 新增 [16 号文档](docs/16-FRONTEND.md)：**React 19 + React Compiler + `@tanstack/react-virtual`**；把 `P-101`~`P-110` 翻译成桌面端 React 约束（`P-101`/`P-102` 是扩展侧契约，不会自动成立）；原 [13 V7](docs/13-ROADMAP.md) 的 Svelte/React 对比实测**取消**。提交 `f55513c` |
| 验收矩阵自洽修复（2026-10-02） | `T-UI-08` 补"类型"列——修复前 117 项里仅 116 项带类型（`98 + 17 + 1 ≠ 117`，与本表"117 项"自相矛盾）；[13 §5](docs/13-ROADMAP.md) 阶段 2 门禁补 `T-BRAND-03`，与 [11 §6](docs/11-ACCEPTANCE.md) 的权威清单对齐。修复后复跑 `tools/Check-ContractCoverage.ps1`：`RESULT: OK`（B 120 · P 10 · A 17） |
| 阶段 1 待确认项全部关闭（2026-10-02） | [13 §7.1](docs/13-ROADMAP.md) 的 16 项逐条落定：**扩展固定身份**（ID `cfefnmhhollflbhgbdmphgnpeaeipfil`、`manifest.key`、私钥存档 `build/secrets/`——权威见 [07 §1.1](docs/07-EXTENSION.md)）· 默认输出目录 · 端口发现 · 清单权限（并修正 `minimum_chrome_version` 93 → 114）· **Firefox 裁决**（第一版只支持 Chromium，结构预留）· 安装目录与注册表键 · Go 工具链（gofmt + vet + golangci-lint）· 前端工具链（tsc + ESLint + Prettier + Vitest）· zustand / 不引入路由库 / 不引入 headless 库 · `Y1`/`Y2` 对照基准与规模。`tools/Check-ContractCoverage.ps1` 扩充为**三项检查**（契约覆盖 + 类型列完整 + 反向引用有效），`RESULT: OK`。`10 V5`（签名与杀软）方案与判据已定、结论待阶段 1 实测 |
| 阶段 1 骨架起步（2026-10-02） | **`D1` + `D3` 已跑通**：Wails 应用可启动（主窗口句柄非 0、工作集 30 MB）· **单实例 `T-STB-02` 通过**（第二个实例 1.1 s 内以退出码 0 退出并唤醒首个实例，首个仍存活）· 托盘用 `fyne.io/systray`（**无 CGO**，已实测）· SQLite 从零建库（5 张表 + WAL + `busy_timeout` + 每连接外键，**5 个结构测试全过**）。过程中踩掉三个坑：① **Wails 主包必须在仓库根**——实测放 `cmd/` 下报 `no Go files`，且 `//go:embed` 不允许 `..`（已改 [01 §3](docs/01-ARCHITECTURE.md) 与 [12 §1.1](docs/12-CONVENTIONS.md)）；② **`wails build` 的 bindings 阶段会真的执行 `main()`**——会占用单实例互斥体，导致"程序开着就无法重新构建"，已用 `-tags bindings` 分支修掉（`bindings_mode_*.go`）；③ **User 级 `GOPROXY` 指向不可达的官方源**（见 §3.4） |
| 阶段 1 · D5 + D6（2026-10-02） | **D5 结构化日志**：`log/slog` + `lumberjack`（10 MB × 5 轮转）、固定字段 `component`/`event`、Debug 默认关闭、**敏感键名兜底脱敏**（cookie/authorization/decode_key/token/secret…）、`logging.Slow` 记录 >50 ms 操作——**6 个测试**。**D6 代理恢复**：启动第 2 步（**排在数据库之前**）、凭据文件 `proxy-restore.json`（original 与 applied 双份）、四种结果 `no_file`/`restored`/`skipped`/`failed`、3 s 预算、**失败保留凭据**（P2）、**无凭据绝不动代理**（P4）、退出时同样执行（P5）、**容忍 UTF-8 BOM**（实测 PowerShell 的 `Set-Content -Encoding UTF8` 会写 BOM，不容忍会把"文件可读"误判成"文件损坏"而永久保留）——**7 个测试** + 1 个显式开启的注册表读写回环验证。端到端实测：`no_file`（代理未动）与 `restored`（凭据被删）均符合预期；回环验证证明**读写逐值忠实** |
| 阶段 1 · D7 + D4（2026-10-02） | **D7 媒体工具**：复用旧项目 `media-tools/`（ffmpeg + ffprobe **8.1.2** / yt-dlp **2026.06.09** / Deno **2.8.1**；exe 受 `*.exe` 规则排除、不入库）；`internal/media` 解析四个工具并读 `*-VERSION.json`（容忍 BOM），缺失**报出来但不阻塞启动**（不静默降级）；`LIUDI_TOOLS_DIR` 供开发时指定，**刻意不向上级目录搜索**——那会在缺失时悄悄用上别处的副本。端到端：不设变量时 `WARN tools_incomplete` 并进入启动警告，设变量时 `INFO tools_ready`——**5 个测试**。**D4 Service 层骨架**：`internal/service` 是 Wails 绑定与 HTTP handler 的**唯一**业务入口（[04 §1.2]），依赖注入 + 装配校验；落地 `store` 的 settings 读写（`value` 一律 JSON、非法值拒绝）与 [03 §2.4] 的两条回退规则（键不存在 / 解码失败都回退默认值且**不中断**）——**6 + 6 个测试** |
| 阶段 1 · D2 基础 UI（2026-10-02） | 前端落到规范栈：**React 19 + Vite 8 + Tailwind v4 + zustand + `@tanstack/react-virtual` + lucide**，并接入 **React Compiler**（构建产物含 `useMemoCache` 与 `_c(` 调用——[16 §5.2](docs/16-FRONTEND.md) 的手段 2 实测通过）。实现 [09 §2](docs/09-UI.md) 的信息架构：侧边栏（固定五项 + 诊断前分隔）· **主题切换在侧边栏底部**（[09 §2.2](docs/09-UI.md)）· 应用顶栏（Eagle 状态**如实显示"未检测"**，不假装可用，B-809）· 下载任务空列表页。设计 token 按 [09 §4.4/§4.6](docs/09-UI.md) 逐条落进 `theme/tokens.css`，**CSS 变量与 Tailwind 主题共用同一生成物**（[16 §4.7](docs/16-FRONTEND.md)）。主题走 **B-803 的首帧注入**：Go 侧中间件把 `settings.theme` 写进 index.html，同步内联脚本在 React 挂载前设好 `data-theme`；持久化失败**回滚界面**，不让界面与权威值不一致（B-804）。`bindings/` 是唯一调用 Wails 的地方（[16 §3.1](docs/16-FRONTEND.md)），返回 `Result` 而非静默吞错。**删除模板的第三方字体**（[09 §4.5](docs/09-UI.md) 禁止打包字体）。测试：Go 42 + 前端 8；`npm run check`（tsc + eslint 含 `react-hooks/recommended-latest` + prettier）exit 0。**过程中被测试抓到一个真 bug**：`dataset['data-theme']` 实际写的是 `data-data-theme`（dataset 的键不含 `data-` 前缀），已改用 `setAttribute` |
| 界面按设计稿修正 + 规范补缺口（2026-10-02） | 用户指出界面未按 `design/mockup.html` 做——**根因是我的判断链漏了一层**：README 用户需求第 4 条的落点写的就是 `09 · design/mockup.html`，而我读了 09 §2/§4、并把 `design/_check.mjs` 打印的 25 个 token 当成了设计稿的全部，**漏掉它同时是「布局层」的权威**。已按设计稿改 8 处：品牌从侧栏移到**应用顶栏**（logo · 品牌 · 版本号 · 状态 pill）· 侧栏只留导航（`gap` + `sep` 分组、`padding 14px 10px`）· 页面骨架加 `.sub` 副标题 · 状态 pill 文案 · 选中竖条 `left:-10px` 且右侧圆角 · 补齐 `border-strong`/`accent-ink`/`shadow`/`r-pill` 与深色阴影 · 版本号经 Vite `define` 从 package.json 注入（单一来源）。**规范同步补上缺口**：[09 §4.2](docs/09-UI.md) 现在明确「**两层权威**」——token 层在 09 §4，**布局层在 `design/mockup.html`**，并要求"实现任何页面前必须对照设计稿的对应 section"。刻意保留一处差异：主题切换仍在**侧边栏底部**（[09 §2.2](docs/09-UI.md) 的规定），设计稿把它放在演示控制条里（那是演示专用，不属于应用界面） |
| 阶段 1 门禁实测记录（2026-10-02） | 新建 [`docs/phase1-report.md`](docs/phase1-report.md)（**新实现**侧数据，与旧版 [`baseline.md`](docs/baseline.md) 分开存放）。结论：**3 项通过、1 项待复测、2 项口径不足**——`T-STB-02` ✅ · `PF-A4` p50 576.2 ms（旧版 1136.3，约快 2 倍）✅ / p95 因**测量期间外部负载（用户玩游戏）**污染而未判 · `PF-A3` 试点：私有工作集 86.6–88.5 MB、自有句柄 409、CPU 0.047% ✅（**正式 10 分钟未测**）。未测项逐条显式标注 |
| 阶段 1 门禁探针（2026-10-02） | 在正式工程外建 Wails 探针（`D:\tmp\liudi-probe`，**未入库**）实测：`PF-A4` 冷启动 **p50 456.5 ms / p95 553.3 ms**（阈值 1136.3 / 1363.6，余量 2.5 倍）· `V5` 管道 JSON-RPC **1000 次往返零错误**、子进程崩溃 **1 ms** 感知、父进程死亡 8–10 ms 子进程自退 · `V1` 体积 11.4 MB / 构建 7.6 s。**并暴露一处判据量纲错误**：工作集求和 330.1 MB vs 私有工作集求和 78.9 MB、句柄 3231（其中 2878 由 WebView2 引擎产生）→ 已修订 [11 §3](docs/11-ACCEPTANCE.md) 的"内存口径""句柄口径"与 `PF-A3`/`ST-7` 行，**阈值未放宽**；教训（"有基线≠可比"）记入 [baseline.md](docs/baseline.md) §3。`PF-A3` 正式 10 分钟测量与 `V4` 杀软验证待在骨架内执行 |

| 阶段 1 代码入库 + 收尾清理（2026-10-02） | 阶段 1 全部代码此前**只在工作区、未入库**（本表的「代码」行还写着"零行"）。已按两个逻辑提交推到远端：`103363f` 规范同步 · `37a3a37` 骨架实现（71 文件 +10680 行）。**三处缺口一并清掉**：① 本表「代码」行改为实测值；② [12 §10 `Z1`](docs/12-CONVENTIONS.md) 声明的落点 [`build/check-go.ps1`](build/check-go.ps1) 已落地——gofmt 范围用 `git ls-files '*.go'`、`go vet` 用同源反推的包目录、`golangci-lint` 未装时显式 SKIP 并以退出码 2 表示「门禁未完整执行」（**不谎报通过**）；③ 裸 `go vet ./...` 会下探 `frontend/node_modules` 里的第三方 Go 源码（实测 `flatted/golang/pkg/flatted`），而**嵌套 `go.mod` 隔离会打断 `//go:embed`**（实测 `cannot embed directory frontend/dist: in different module`）→ 改为显式给出包范围，教训记入 [12 §1.1](docs/12-CONVENTIONS.md) |

| 阶段 2 规范回填完成（2026-10-02） | [13 §7.2](docs/13-ROADMAP.md) 的 **9 项待确认项全部关闭**——这是"先出文档再开发"（用户需求第 14 条）要求的那道门。`04` 新增 §2.3.1 通用约定（成功外壳即 §3.1 的 `Result<T>`、状态码表、本机 API 层自有错误码 7 个）、§2.3.2 阶段 2 九个端点的逐条 schema、§3.3 视图与请求类型（`PlanView` 24 字段 + `canRetry`/`canStop`/`canOpen`/`canImport` 的**机械判据**）；`03` 新增 §2.1.1 —— `stream_plan` 被定义为 `CreatePlanRequest.streams[]` 的**可持久化投影**，只留 `track`/`kind`/`quality`/`container`，**媒体地址与字节数一律丢弃**（B-722）。媒体地址只在内存、重启转 `failed`+`context_expired` 的理由已写明：它**不与 [05 §9](docs/05-DOWNLOAD.md) 的"直链可重建→继续调度"冲突**——那行要求**能证明**可重建，一次性签名直链拿不到这个证明，故按"无法判断→保守 failed"处理。另：`07 E7` 定为固定 **3000 ms**、`14 SA2` 定为独立脚本 `build/gen-site-adapters.ps1`（**阶段 2 不需要**，随阶段 3 适配器体系落地）、`12 Z4` 确认 10 MB×5 与实现一致（**无需改代码**，只把"默认"确认为最终值）。校验：`Check-ContractCoverage.ps1` → `RESULT: OK` |

| 阶段 2 实现落地（2026-10-02） | D1~D6 全部有落点：`internal/api`（回环 API + Origin 白名单 + `S4` 端口发现）· `internal/media`（P1 直链：Range 续传、200/206/416、FFprobe 校验、原子交付——**原 `internal/download` 已按 [01 §3](docs/01-ARCHITECTURE.md) 合并进来**）· `internal/service`（计划业务 + 调度 + [04 §4.2](docs/04-INTERFACES.md) 的 200 ms 完整视图推送）· `internal/store/plans.go`（**机械保证**而非约定：`total_bytes` 三态用 `*int64`、`completed` 与 `final_path` 同事务**并有触发器注入的回归测试**、调度查询强制 `INDEXED BY idx_plans_due`）· `internal/ui`（绑定方法 + 事件出口）· `frontend/src`（下载页实时列表）。**门禁 5/11 通过**，未过的 6 项全在扩展侧（测试资产未迁入）。逐项证据与 5 条顺带查实的事实（`systray` 无通知 API · Windows `EADDRINUSE` 数值与 Go 常量不符 · `net/http` 重定向忽略端口 · `project.nsi` 非独立版本源 · 测试曾污染用户目录）见 [`docs/phase2-report.md`](docs/phase2-report.md) |

---

## 3. 下一步做什么

### 3.1 待办（按优先顺序）

| 优先 | 事项 | 为什么是这个位置 |
| --- | --- | --- |
| **1** | **扩展收尾：迁入测试资产 → 过 `T-EXT-10`/`T-EXT-15~19`** | 阶段 2 只剩这 6 项门禁。要做：把旧项目那 7 个 JS 测试迁入 `extension/tests/` 并适配（`test_auth_race.js` 按 [07 §7](docs/07-EXTENSION.md) 改写为"连接可达性竞态"）· **新增 `P-101/P-102` 自动化检查**（状态更新后断言列表容器未被整体重建——[07 §8](docs/07-EXTENSION.md) 明文说这是"防止卡顿回归的唯一可执行手段"）· 人工项 `M1`（浏览器扩展管理页加载）。⚠️ **不做长测试**——确需长测试必须**分段唤醒**逐段检查，或**先跑几秒短测再上长测** |
| **2** | 阶段 1 门禁遗留（**已按用户要求暂缓**） | `PF-A4` 冷启动 **n ≥ 20**（n=10 时 p95 就等于 max，一个样本即可决定判定）· `PF-A3`/`T-STB-05` 的 10 分钟稳定段（内存按**私有工作集**、句柄分自有/WebView2 两类）。结果补进 [`docs/phase1-report.md`](docs/phase1-report.md)；跑之前先征得同意。`V4` 杀软验证留到阶段 7 有安装器再验 |
| **3** | 阶段 0.5 剩余项 | 见下方待办 A：`B-3`~`B-8` 六项未测 + 空闲采样时长不足。**不阻塞阶段 1**（它测的是旧版，只要旧项目不变就不过期） |
| **4** | 阶段 2.5 浏览器下载模式 | 解决用户最初提的 YouTube 链接问题（`04 I6`、`07 E8` 等协议项仍未解决） |
| **5** | 装 `golangci-lint` 并实测 [`.golangci.yml`](.golangci.yml) | [12 §10 `Z1`](docs/12-CONVENTIONS.md) 已定工具链、[`build/check-go.ps1`](build/check-go.ps1) 已落地，但本机没装 → 门禁第 1 项目前是 `INCOMPLETE`。一条命令：`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`；装完要核对配置语法（脚本按 **v2** 写：`version: "2"` + `default: none`） |

阶段划分、每阶段门禁、待确认项总清单都在 [`docs/13-ROADMAP.md`](docs/13-ROADMAP.md)。

### 3.2 待办 A：阶段 0.5 基线（**已部分完成**，缺口在 `docs/baseline.md` §3）

[`docs/baseline.md`](docs/baseline.md) **已建立**，被测对象是**官方 1.6.3 冻结发行版**（后端 SHA-256 与官方验证页逐字节一致）。

| 已测 | 结果 | 代入 |
| --- | --- | --- |
| `B-1` 冷启动（完整用户路径） | p50 **1136.3 ms** / p95 1238.2 ms | [`PF-A4`](docs/11-ACCEPTANCE.md) |
| `B-2` 空闲常驻 | CPU 0.06 % · 工作集 64.16 MB · 句柄 444 | [`PF-A3`](docs/11-ACCEPTANCE.md) |
| `B-3`~`B-8`（滚动帧率 / 状态更新 / 捕获吞吐 / 并发响应 / 扩展候选 / 1h 增长） | **未测**，原因逐项写在 `docs/baseline.md` §3 | `PF-A1`/`PF-A2`/`PF-A5`/`PF-A6`/`PF-A7` 仍待代入 |

**两个已知口径缺口**（不得当成已完成）：空闲采样只采了 2 分钟（口径要求 ≥ 10 分钟）；`B-3`~`B-8` 需要人工操作或真实流量，属 [11 §5](docs/11-ACCEPTANCE.md) 的人工复验边界。

### 3.3 待办 C：视频号高清（M5）

**已知未解决，且可能是外部阻塞。** 现状与证据：

| 事实 | 来源 |
| --- | --- |
| yt-dlp **不支持视频号**（941 个提取器里没有 wechat/weixin/channels/finder/sph） | [15 §4.2](docs/15-UPSTREAM.md) |
| 该项目**没有上游解析器可依**，捕获/绑定/解密/画质策略全链路自研 | [06 §1](docs/06-WECHAT.md) |
| 真实抓包样本里 `spec[]` 六个档位（`xWT111`~`xWT128`）**全是 720 级或更低** | [06 §6.6](docs/06-WECHAT.md) |
| 平行项目 `ltaoo/wx_channels_download` 的"原画"是**合成**的，且其源码注释显示**当前被禁用**；精简 query 的尝试也被弃用 | [06 §6.6](docs/06-WECHAT.md) |
| 旧实现 2026-08-25 实测：选择"原视频"得到的**仍是 720p** | 旧项目 `WECHAT_CHANNELS.md` |

**结论**：另一个同类项目试过"靠元数据合成原画"这条路并放弃了。因此已固化三条硬要求（禁止声明式原画档位、必须实测验证、字节数与分辨率双重比对）。

**可选的下一步验证**：在你机器上实跑一次 `ltaoo/wx_channels_download`——如果它也下不出原画，M5 即可确认为外部阻塞；如果它能，说明它有我们没拿到的接口。

### 3.4 本机环境基线（换机器要复核）

> **这是所有者这台机器（`PC-20250918XZTS`）在 2026-10-02 的状态，不是所有机器共有的事实。** 换机器/重装时逐行复核；下表最后三行才是跨机器通用的注意点。

| 事实 | 影响 |
| --- | --- |
| 本机是 **Windows 11 23H2**，当前账户 `MSI` **在 Administrators 组内**；但会话默认是 **UAC 过滤令牌**（`Medium` + "Group used for deny only"） | 需要管理员时走 `Start-Process powershell.exe -Verb RunAs`（实测可用、**无弹窗**，因为 `ConsentPromptBehaviorAdmin=0`） |
| Go **1.27.1** 装在 `C:\Users\MSI\go-sdk\go`（官方 ZIP 解压，非 MSI）；`GOROOT` / `GOPATH`（`C:\Users\MSI\go`）/ 用户 `Path` 已持久化 | 新开的 shell 才生效；本次会话内需手动前置 `Path` |
| Wails CLI **v2.16.0** 装在 `C:\Users\MSI\go\bin\wails.exe` | 随 `GOPATH` 走，不用单独装 |
| **WebView2 Runtime 已装**（154.x，`EdgeWebView\Application`） | Wails 的运行时前置已满足，安装器不必承担部署 |
| **`GOPROXY` 实际是 `https://proxy.golang.org,direct`（User 级环境变量），该源在本机不可达**（实测 `dial tcp ...:443` 超时）——2026-10-02 复核发现 | `go env -w GOPROXY=https://goproxy.cn,direct` **会被 OS 环境变量压制**（报 `does not override conflicting OS environment variable`）。两条修法：① 构建脚本里显式 `$env:GOPROXY='https://goproxy.cn,direct'`；② 删掉 User 级 `GOPROXY`，让 `go env` 文件里的 `goproxy.cn` 生效。**换机器/重装后先验这一行** |
| **Go 官方 MSI 无提权装不上**：报 `Error 1925`（写 `C:\Program Files` 被拒后回滚） | **不要重试 MSI**。用官方 ZIP 免管理员安装，并核对官方 SHA-256（本次已验证） |
| **本机没有 `pwsh`（PowerShell 7）**，只有 Windows PowerShell 5.1 | 提权时**必须用 `powershell.exe`**，用 `pwsh` 会报"系统找不到指定的文件" |
| **没有 C 编译器**（`gcc` / `clang` / `cl` 全无）——但**实测不影响构建**：`wails build` 在无 gcc 的情况下成功产出 `probe.exe`（11.9 MB，9.3 s）。v2.16.0 在 Windows 走纯 Go 的 WebView2Loader，**不要求 CGO** | **不要为 Wails 装 MinGW**。只有将来引入需要 CGO 的依赖（如 `mattn/go-sqlite3`）时才需要——[03 §2](docs/03-DATA.md) 选的是纯 Go 的 `modernc.org/sqlite`，所以按当前技术栈**不需要**。`wails doctor` 也不会检查编译器，别把它当缺口 |
| GPU 列表里有 3 个虚拟显示适配器（MuMu / ToDesk / GameViewer）+ NVIDIA 3090 + Intel UHD | **做 PF-A1 / PF-A7 帧率实测量时必须确认渲染落在哪个适配器上**，否则帧率数据不可信 |
| **`git push` 连不上 GitHub**：直连超时；本机代理是 **`127.0.0.1:7890`**（FlClashCore，系统代理也指向它），但 **git 不读系统代理** | 已配好 `git config --global http.https://github.com.proxy http://127.0.0.1:7890`。若推送又失败，先确认 FlClash 在运行、节点可用（`Test-NetConnection 127.0.0.1 -Port 7890`） |

---

## 4. 绝对不能碰的（红线）

### 用户提出的硬性要求

逐条列在 [`README.md`](README.md) 的「用户需求（不可违背）」表，共 14 条，每条都标了规范落点。**任何时候都不得违反。**

### 两条"不得声称已修复"

| 编号 | 问题 | 状态 |
| --- | --- | --- |
| **M5** | 视频号高清命中 | 旧版**未通过**；与重写无关，架构改进解决不了（见 §3 待办 C） |
| **M10** | YouTube 登录会话下载 | 旧版**未通过**；同上 |

**发行说明中绝对不得宣称这两项已修复。** 验收编号与豁免规则见 [`docs/11-ACCEPTANCE.md`](docs/11-ACCEPTANCE.md) §5。

「浏览器下载模式」是针对 M10 的**用户侧退路**，不是修复——桌面端出站下载在 YouTube 上仍可能失败，那时用户要手动开那个开关。

### 其它硬约束

1. **旧项目只读**：`E:\Users\MSI\Desktop\codex_download` 永不修改
2. **不得重新引入已删除的机制**：见 [`docs/01-ARCHITECTURE.md`](docs/01-ARCHITECTURE.md) §9 的 `N12`（配对码、令牌、租约、单写者队列、迁移链等）；除非能回答"它解决了用户实际遇到的什么问题"
3. **不加本地 API 令牌**：本机其他进程能访问那个端口属于"防攻击者"，不在威胁范围内（[ADR-003](docs/adr/ADR-003-simplified-security.md)）
4. **不要臆断性能数据**：需要说性能就去旧源码量，现有结论全部是可复现的计数
5. **加站点先读 [`docs/14`](docs/14-SITE-ADAPTERS.md) §11 的八步流程**，不要为了某个站点去改公共发现代码
6. **视觉方向已否决 macOS 方案**：目标基调是「浅色主调 + 白卡片 + 单一蓝色强调色 `#2f6df6`」（[`docs/09-UI.md`](docs/09-UI.md) §4.1）。**不得重新引入**
7. **上游来源三类边界**（[`docs/15`](docs/15-UPSTREAM.md)）：复用类要履行 GPL 义务；**行为研究类一行代码都不能进**；二进制类要钉版本 + SHA-256 + 许可证通知
8. **不得复制 `ltaoo/wx_channels_download` 的代码**：本项目只读其行为与原理，结论以"事实"形式写入 [06](docs/06-WECHAT.md)。**与"不出售"无关**——它属于行为研究类，代码零复制（[15 §3.1](docs/15-UPSTREAM.md)）
9. **语言策略**：同一语言能复用就复用，跨语言则重写；**后端必须保持单一语言（Go）**

---

## 5. 东西都在哪

| 想找什么 | 去哪 |
| --- | --- |
| 用户要求、设计原则、技术栈、环境前置 | [`README.md`](README.md) |
| 进程模型、通信、目录、启动关闭时序、禁止事项 | [`docs/01-ARCHITECTURE.md`](docs/01-ARCHITECTURE.md) |
| 行为契约 `B-xxx`（120 条） | [`docs/02-BEHAVIOR.md`](docs/02-BEHAVIOR.md) |
| 数据库 schema、取值域、输入校验、配置键 | [`docs/03-DATA.md`](docs/03-DATA.md) |
| 扩展 API、Wails 绑定、事件、进程协议 | [`docs/04-INTERFACES.md`](docs/04-INTERFACES.md) |
| 下载状态机、6 条下载路径（含 P6 浏览器中转） | [`docs/05-DOWNLOAD.md`](docs/05-DOWNLOAD.md) |
| 视频号捕获（最难、已知未解决） | [`docs/06-WECHAT.md`](docs/06-WECHAT.md) |
| 扩展复用策略、列表渲染契约 `P-1xx` | [`docs/07-EXTENSION.md`](docs/07-EXTENSION.md) |
| Eagle 导入、IDM hook | [`docs/08-EAGLE-IDM.md`](docs/08-EAGLE-IDM.md) |
| 设计系统、页面规格、交互与性能要求 | [`docs/09-UI.md`](docs/09-UI.md) |
| 安装、升级、回滚、卸载、自动更新 | [`docs/10-DISTRIBUTION.md`](docs/10-DISTRIBUTION.md) |
| 回归矩阵 `T-*`、性能 `PF-A*`、稳定性 `ST-*`、人工 `M*` | [`docs/11-ACCEPTANCE.md`](docs/11-ACCEPTANCE.md) |
| 目录结构、命名、错误、日志、测试、生成物 | [`docs/12-CONVENTIONS.md`](docs/12-CONVENTIONS.md) |
| **分阶段计划、门禁、待确认总清单、风险** | [`docs/13-ROADMAP.md`](docs/13-ROADMAP.md) |
| 站点适配器规范 `A-1xx` | [`docs/14-SITE-ADAPTERS.md`](docs/14-SITE-ADAPTERS.md) |
| **上游来源、许可证义务、第三方库、参考项目** | [`docs/15-UPSTREAM.md`](docs/15-UPSTREAM.md) |
| 前端框架规范（React 19 / React Compiler / 虚拟列表 / 渲染约束） | [`docs/16-FRONTEND.md`](docs/16-FRONTEND.md) |
| 旧版性能基线数值（阶段 0.5 实测 + 未测项） | [`docs/baseline.md`](docs/baseline.md) |
| 为什么这么决定 | [`docs/adr/`](docs/adr/) |
| 界面设计稿（双击浏览器打开） | [`design/mockup.html`](design/mockup.html) |
| 设计稿自检 | `node design/_check.mjs` |
| 站点适配器声明 | [`adapters/`](adapters/README.md) |

### 外部参考项目速查（详见 [15](docs/15-UPSTREAM.md)）

| 项目 | 类型 | 要点 |
| --- | --- | --- |
| `xifangczy/cat-catch` | **复用**（GPL-3.0） | 媒体发现来源；固定 2.7.1 / 提交 `7a77612b` |
| `yt-dlp` | **随包二进制** | **934 个站点提取器 = 项目依赖的全网覆盖**；固定 `2026.06.09` |
| `FFmpeg` / `Deno` | **随包二进制** | 固定版本 + SHA-256 |
| `ltaoo/wx_channels_download` | **行为研究** | 视频号平行实现；原画做法已固化教训 |
| `Evil0ctal/WeChat-Channels-Video-File-Decryption` | **行为研究** | 解密方向参考 |
| `Hanson/WechatSphDecrypt` | **行为研究** | 同上 |

---

## 6. 几条给新窗口的方法论提醒

1. **文档是权威。** 实现与文档冲突时**先改文档**；行为或架构边界变更必须同步文档（[`docs/12`](docs/12-CONVENTIONS.md)）
2. **一处事实，一处定义。** 同一个值只允许有一个权威位置，其余地方引用它。四轮审阅发现的最大问题就是这条被违反（常量抄了三份、M 编号两套、视觉方向两处相反）——**新增内容前先搜一遍是否已有定义**
3. **改动后跑自检**：文档断链与章节引用、`node design/_check.mjs`、契约编号覆盖。这三项目前都是手工验证的，**值得先脚本化**（规范里的 `12 Z3`，已从阶段 2 提前到阶段 1）
4. **不要为了"看起来更详细"而复制既有内容**：重复的副本必然漂移（`14 §10` 与 `adapter.json` 已经漂过一次，已修）
5. **验证优先于假设**：本项目已多次证明"看起来对的做法"是错的——`IsGlobalUnicast()` 会漏内网、合成原画档位被同类项目弃用、M 编号错位会放过红线项。**动手前先核对源码或实测**
6. **交接文档要克制**：只记状态、待办、红线、导航。规格一律指向 `docs/`——这是这份文档不重复规范内容的原因

### 四类记录位置（把"一处事实，一处定义"落到操作层面）

| 记录类型 | 权威位置 |
| --- | --- |
| 已测得的**性能数值** | [`docs/baseline.md`](docs/baseline.md)（阶段 0.5，已建立；未测项在 §3） |
| 阈值公式与判据 | [`docs/11-ACCEPTANCE.md`](docs/11-ACCEPTANCE.md) |
| 运行时长/端口/上限等**常量** | 按其归属文档（如 [01 §5.3](docs/01-ARCHITECTURE.md) 超时、[03](docs/03-DATA.md) 配置键），其余地方只引用 |
| 决策理由 | [`docs/adr/`](docs/adr/) |
