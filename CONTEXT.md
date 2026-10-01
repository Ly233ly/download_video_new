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
| **当前状态** | **规范与设计稿已定稿，仍是零代码** |

---

## 2. 现在在哪

| 项 | 状态 |
| --- | --- |
| 规范 | **16 份 + 4 个 ADR**；已做四轮审阅（详细度 / 冗余 / 矛盾）并修完发现的问题 |
| 契约编号 | `B-xxx` 120 条 · `P-1xx` 10 条 · `A-1xx` 17 条，**全部有验收覆盖** |
| 设计稿 | [`design/mockup.html`](design/mockup.html) 已定稿（浅色主调 + 白卡片 + 蓝），含扩展弹窗的候选/任务/设置三个标签页 |
| 自检 | 文档断链 0、跨文档章节引用全有效、设计稿自检脚本 `exit=0` |
| 环境 | **Go 1.27.1 已装**（`C:\Users\MSI\go-sdk\go`，ZIP 免管理员）· **Wails CLI v2.16.0 已装**（`C:\Users\MSI\go\bin`）· WebView2 Runtime 已装 · **`wails build` 实测通过**（见 §3.4，**不需要 C 编译器**） |
| 代码 | **零行** |

### 已完成的关键事项（不需要重做）

| 事项 | 结果 |
| --- | --- |
| 规范定稿与审阅 | 四轮审阅（详细度 / 冗余 / 矛盾）修掉 5 个真矛盾：M 编号错位、「标题栏」两义、读取方表述、R5/R7/R8 编号错、自检脚本描述不实；删 5 处结构性 + 4 处单行重复（**把约束救成了硬性禁止项**）；补 10 项已确认数值（超时/端口/重试/节流/滚动参数/hook 时限等）+ IP 拒绝集合、`error_code` 取值域、P6 取消下行等缺口 |
| 上游考古与边界 | 新建 [15 号文档](docs/15-UPSTREAM.md)：还原 cat-catch、两个视频号项目、3 项外部资料、8 个前端库；按源码核对 yt-dlp **已支持抖音**（`DouyinIE`）等 934 个站点、**不支持视频号**，据此划清适配器分工；只读研究 `ltaoo/wx_channels_download`，**原画教训**固化成 [06 §6.6](docs/06-WECHAT.md) 三条硬要求 |
| git 建仓 | 此前**完全没有版本控制**（规范无历史可回滚）；现已建本地仓 |
| 验收阈值补完 | `PF-A1~A7`、`ST-2/6/7`、`T-UI-01/06` 全部改成可机械执行的判据（三分类：硬数值 / 相对判据 / 流程判据）；§2 **十一张回归表全部补齐"类型"列**（117 项：A 98 · A+M 17 · M 1）。`ST-2` 直接引用 [01 §5.3](docs/01-ARCHITECTURE.md) 的代理恢复 3 s、`ST-6` 引用 [03 §5](docs/03-DATA.md) 的 `busy_timeout` 5000 ms——**不新定数值，只把既有定义接上**。提交 `9aa2fdb` |
| 阶段编号统一 | `11 §6` 补 0.5/2.5 两行、末行改"8（发布验证）"；`T-EXT-26` 从阶段 2 移到 2.5（[13](docs/13-ROADMAP.md) 侧原本两处互相矛盾）；阶段 3 统一 `T-ADP-*`；[13 §2](docs/13-ROADMAP.md) 补阶段 8 定义；[13 §10](docs/13-ROADMAP.md) 的 `D-3` 不再用"有明确改善" |
| 工具链就位 | Go 1.27.1 + Wails CLI v2.16.0 装好；**实建了一个一次性项目验证可构建**（`wails init -t vanilla` 1.7 s、`wails build` 9.3 s 产出 11.9 MB exe），证明**无需 C 编译器**；`wails doctor` 报 SUCCESS（见 §3.4） |
| 许可证路线已定 | **继续复用 cat-catch 扩展**（组合发行按 GPL-3.0 并提供对应源码）；依据：用户明确**本软件不出售**，Commons Clause 类附加条款不构成约束。**若将来改为公开分发，GPL-3.0 的提供源码义务仍然成立** |
| 阶段 0.5 已部分完成 | 建立 [`docs/baseline.md`](docs/baseline.md)：实测官方 1.6.3 冻结版的冷启动（p50 1136.3 ms）与空闲常驻（CPU 0.06 % / 64 MB / 444 句柄）；`B-3`~`B-8` 六项如实标"未测"。测量脚手架在 `measurement/`（可复现，含隔离解包与单实例互斥体处理） |

---

## 3. 下一步做什么

### 3.1 待办（按优先顺序）

| 优先 | 事项 | 为什么是这个位置 |
| --- | --- | --- |
| **1** | **量旧版性能基线**（阶段 0.5） | 见下方待办 A。注意它测的是**旧版**，只要旧项目不变就不会过期，因此不抢在阶段 1 前面 |
| **2** | 阶段 1 骨架 | 窗口 + 建库 + 代理恢复 + 工具解析；前端按 [16](docs/16-FRONTEND.md) 落地，其 §7 的 `F1`~`F4`（测试框架 / 状态管理 / 路由 / 组件原语）属阶段 1 必须解决项 |
| **3** | 阶段 2 第一条链路 | 浏览器点一下 → 文件真的落到磁盘 |
| **4** | 阶段 2.5 浏览器下载模式 | 解决用户最初提的 YouTube 链接问题 |

阶段划分、每阶段门禁、待确认项总清单都在 [`docs/13-ROADMAP.md`](docs/13-ROADMAP.md)。

### 3.2 待办 A：阶段 0.5 基线（**已部分完成**，缺口在 `docs/baseline.md` §3）

[`docs/baseline.md`](docs/baseline.md) **已建立**，被测对象是**官方 1.6.3 冻结发行版**（后端 SHA-256 与官方验证页逐字节一致），测量脚本在 `measurement/`。

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
| `GOPROXY` 已是 `https://goproxy.cn,direct` | Go 模块下载不必走代理；本次装 Wails CLI 用时 25 s |
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
| 已测得的**性能数值** | `docs/baseline.md`（阶段 0.5 交付物，尚未创建） |
| 阈值公式与判据 | [`docs/11-ACCEPTANCE.md`](docs/11-ACCEPTANCE.md) |
| 运行时长/端口/上限等**常量** | 按其归属文档（如 [01 §5.3](docs/01-ARCHITECTURE.md) 超时、[03](docs/03-DATA.md) 配置键），其余地方只引用 |
| 决策理由 | [`docs/adr/`](docs/adr/) |
