# `extension/` · 留底浏览器扩展

本目录是浏览器扩展的**源码即产物**（无打包器，C9）。规范权威在 [`../docs/07-EXTENSION.md`](../docs/07-EXTENSION.md)，
弹窗呈现规范在 [`../docs/09-UI.md`](../docs/09-UI.md) §4.8，端点契约在 [`../docs/04-INTERFACES.md`](../docs/04-INTERFACES.md) §2。

## 加载方式

Chrome / Edge 114+（`sidePanel` 与 `side_panel` 的下限）：

```
chrome://extensions → 打开「开发者模式」→「加载已解压的扩展程序」→ 选本目录
```

`manifest.json` 的固定 `key` 使扩展 ID 恒为 `cfefnmhhollflbhgbdmphgnpeaeipfil`，
桌面端以编译常量内置该 Origin 作为白名单——这是「装完即用、无需配对」的技术前提（`E1`）。
**换 `key` 就换 ID**：`docs/07` §1.1、本目录的 `manifest.json`、Go 侧白名单常量是同一事实的三处落点。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `manifest.json` | 清单（第一版唯一的清单） |
| `popup.html` | 入口只含空壳 `#eagleBridgeRoot`；界面全部由 `js/eagle-bridge-ui.js` 用 DOM API 构建 |
| `content.js` | 页面内下载动作的点击来源记录 |
| `js/background.js` | Service Worker：导航边界、代次、发现、保活 |
| `js/content-script.js` | 页面内发现与上报（站点无关：只按下发到的声明数据决定行为） |
| `site-adapters.json` | **构建生成**的站点适配器声明（事实源在 [`../adapters/`](../adapters/)，禁止手工编辑）。由 `js/background.js` 读入下发、`js/content-script.js` 执行，见 [`../docs/14-SITE-ADAPTERS.md`](../docs/14-SITE-ADAPTERS.md) §5 |
| `js/init.js` | 运行时配置与捕获选项（取代旧 `init.js` + `polyfill.js`） |
| `js/function.js` | 存储区域与正则工具 |
| `js/virtual-list.js` | **列表渲染核心**：节点复用、虚拟滚动、图片并发闸门（`P-101`~`P-107`） |
| `js/eagle-bridge-candidate-logic.js` | 候选组归组与选择 |
| `js/eagle-bridge-ui-logic.js` | 弹窗展示逻辑（纯函数，可在 Node 下测试） |
| `js/eagle-bridge-ui.js` | 弹窗界面（标签页 / 列表 / 设置 / 底部操作栏） |
| `js/eagle-bridge-auth-logic.js` | 连接可达性（取代旧的配对令牌状态机） |
| `js/eagle-bridge.js` | 与桌面端通信（含 `AD-8` 的浏览器下载模式中转上传） |

旧版的 `bootstrap.js`（一次性配对凭据）与 `js/polyfill.js`（侧边栏 API 垫片）
按 `AD-1` 与 `E3` 删除；`catch-script/` 与站点专用 `*-content.js` 按 07 §2 不迁入。

## 列表渲染的三条硬纪律

`docs/07` §6.2 的 `P-101`~`P-110` 是本目录最容易回归的部分。落地位置与判据：

1. **禁止整体重建**（`P-102`）：`js/eagle-bridge-ui.js` 与 `js/virtual-list.js` 里
   **没有任何 `innerHTML` 赋值**。列表容器及其祖先只能通过 `createElement` /
   `appendChild` / 文本与属性更新来改变。
2. **按稳定 ID 复用节点**（`P-101`/`P-103`）：`virtual-list.js` 维护
   `候选组 ID → 行节点` 的索引；只有 ID 序列真正变化才增删行。
3. **固定尺寸**（`P-105`）：行高 `56px`、可见 `12` 行、上方缓冲 `3` 行、下方缓冲 `5` 行
   是 `virtual-list.js` 顶部的**常量**，不得改成测量式（`E6` 已因此关闭）。

行内只放 **2 个节点**（行本身 + 一个文本节点），缩略图由行自身的
`background-image` 承担——这是 `PF-A7`（200 条候选下 DOM 节点总数 ≤ **60**）
与 `P-105`（20 行窗口）共同推出的上界。`P-107` 的「用图片 URL、只对可视区内的行
赋值、并发上限 4」相应落在 `virtual-list.js` 的 `patchImage()` / `createImageLoader()`。

## 站点适配器：声明从哪来、谁执行、怎么降级

站点差异**只以数据形式**存在（`07` §2：扩展源码里禁止出现站点名判断）。链路是：

1. **声明从哪来**：事实源是 [`../adapters/`](../adapters/) 下的 `adapter.json`，由 `build/gen-site-adapters.ps1`
   生成 `extension/site-adapters.json`（生成物，禁止手工编辑，`A-105`）。本目录只消费生成物。
2. **谁读**：`js/background.js` 启动时 `fetch(chrome.runtime.getURL("site-adapters.json"))`
   读一次并缓存（Service Worker 里读包内文件**不需要** `web_accessible_resources`，
   `manifest.json` 也就不该为它加这一项）。
3. **谁执行**：内容脚本读不到包内文件，于是向背景侧发 `{ Message: "getSiteAdapters" }`，
   背景侧回 `{ ok: true, adapters: [...] }`；`js/content-script.js` 用返回的声明调
   `matchAdapter` 选中当前页面的适配器，再按其 `capture` / `identity` / `title`
   决定候选容器、身份 ID、内容页地址、标题与"报哪些、报几个"。通用引擎
   （`js/eagle-bridge-candidate-logic.js` 末尾的 `adapter*` 纯函数）是这些声明的唯一执行者。
4. **怎么降级**：文件缺失、JSON 解析失败、消息通道失败**任一**发生，都退化为"没有适配器"
   （`matchAdapter` 返回 `null`）的通用行为——只处理页面自己供给的 `blob:` 播放源、
   全部播放器各自上报、地址与标题沿用页面自身——并只 `console.warn` 一次。
   内容脚本最多等声明 `400ms` 就开跑，**候选发现不因声明不可用而失效**（`14` §5.3）。
   声明里的选择器若非法（`querySelectorAll` 抛错），只跳过那一条，其余选择器照常收集。

## 测试

回归门禁在 [`../tests/js/`](../tests/js/)：

```powershell
# 全部（7 个真实门禁 + 3 个等待资产、目标缺失时自动 SKIP 的门禁）
Get-ChildItem tests/js/*.js | ForEach-Object { node $_.FullName; "exit=$LASTEXITCODE  $($_.Name)" }

# 只跑可直接判定的 7 个
node tests/js/test_candidate_presentation.js
node tests/js/test_popup_logic.js
node tests/js/test_auth_race.js
node tests/js/test_list_rendering.js
node tests/js/test_youtube_session.js
node tests/js/test_adapter_douyin.js
node tests/js/test_adapter_runtime.js
```

`test_adapter_douyin.js` 守"事实源与生成物一致 + 公共代码里没有站点名"，
`test_adapter_runtime.js` 守"声明 → 行为"里与浏览器无关的一半（选容器、取身份 ID、
定主播放器、放开直连流、拼标题、降级）。DOM 采集与消息通道不做离线判定——
本项目不引入 jsdom，那部分靠上面的 `node --check` 与手工回归。

自检（每次改动后都要跑）：

```powershell
Get-ChildItem -Recurse -File extension -Include *.js | ForEach-Object { node --check $_.FullName }
node -e "JSON.parse(require('fs').readFileSync('extension/manifest.json','utf8'))"
```

## 尚未完成（阶段 2 的诚实边界）

| 项 | 状态 |
| --- | --- |
| 站点专用注入脚本（B 站 / YouTube / 搜索） | **不提供**。`07` §2 已取消 `catch-script/`，归 `adapters/<site>/extension.js`（阶段 3）。属阶段性功能回退 |
| 抖音 / Instagram / Vimeo 的站点专用识别 | **站点专用代码不再存在**：识别改由声明数据驱动（`site-adapters.json` → `content-script.js`，见上节），扩展源码里没有站点名。声明目前只落了抖音与通用兜底两份；缺声明的站点（Instagram / Vimeo 等）按通用行为跑。`test_bilibili.js` / `test_youtube.js` 保留全部断言但在目标缺失时 SKIP |
| 视频号 bridge | 属阶段 5；`test_wechat_channels_bridge.js` 同样 SKIP |
| 增强发现入口 | 弹窗设置页的「诊断」分组保留一个**禁用**的占位行，说明站点专用注入脚本不在本版本内提供。`Message: "script"` 恒返回 `"error no exists"`，与旧版的行为契约一致 |
| `api-port.json` 端口发现（`AD-5` / B-214） | **部分实现**。MV3 扩展无权读取任意本地文件；当前按默认端口 + 紧随其后的备用端口逐个 `/health` 探测。收敛在 `eagle-bridge.js` 的 `eagleBridgeApiPortHint()` 一处 |
| `04 §7 I1`/`I2`/`I4`/`I5` | **已由协作者回填**（`04 §2.3.1` / `§2.3.2` / `§3.3`）：`service` 固定 `"liudi-desktop"`、`apiProtocol` 当前为 `1`、`/api/plans` 返回 `Paged<PlanView>`。扩展已按该 schema 对齐；`taskView()` 仍保留旧 snake_case 别名作过渡兜底 |
| 浏览器下载模式的完整恢复 | 属阶段 2.5。客户端协议（`PUT`/`POST`/`DELETE`）与上限预检已实现；Worker 回收后地址丢失的任务无法续传（B-223 的固有边界），由桌面端按会话空闲超时收尾 |
| `AD-5` 端口发现只做到"最佳努力" | `api-port.json` 的读取钩子 `eagleBridgeApiPortHint()` **当前恒返回 0**（MV3 扩展无权读取任意本地文件）。这是 B-214 的**部分实现**：主端口 47652 可用，备用端口只在紧随其后的序号范围内探测 |
