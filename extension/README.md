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
| `js/content-script.js` | 页面内发现与上报（站点无关） |
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

## 测试

回归门禁在 [`../tests/js/`](../tests/js/)：

```powershell
# 全部 8 个（4 个真实门禁 + 3 个跳过式门禁 + 1 个等待资产的门禁）
Get-ChildItem tests/js/*.js | ForEach-Object { node $_.FullName; "exit=$LASTEXITCODE  $($_.Name)" }

# 只跑可直接判定的 5 个
node tests/js/test_candidate_presentation.js
node tests/js/test_popup_logic.js
node tests/js/test_auth_race.js
node tests/js/test_list_rendering.js
node tests/js/test_youtube_session.js
```

自检（每次改动后都要跑）：

```powershell
Get-ChildItem -Recurse -File extension -Include *.js | ForEach-Object { node --check $_.FullName }
node -e "JSON.parse(require('fs').readFileSync('extension/manifest.json','utf8'))"
```

## 尚未完成（阶段 2 的诚实边界）

| 项 | 状态 |
| --- | --- |
| 站点专用注入脚本（B 站 / YouTube / 搜索） | **不提供**。`07` §2 已取消 `catch-script/`，归 `adapters/<site>/extension.js`（阶段 3）。属阶段性功能回退 |
| 抖音 / Instagram / Vimeo 的站点专用识别 | 同上；相关分支已从扩展源码删除，`test_bilibili.js` / `test_youtube.js` 保留全部断言但在目标缺失时 SKIP |
| 视频号 bridge | 属阶段 5；`test_wechat_channels_bridge.js` 同样 SKIP |
| 增强发现入口 | 弹窗设置页的「诊断」分组保留一个**禁用**的占位行，说明站点专用注入脚本不在本版本内提供。`Message: "script"` 恒返回 `"error no exists"`，与旧版的行为契约一致 |
| `api-port.json` 端口发现（`AD-5` / B-214） | **部分实现**。MV3 扩展无权读取任意本地文件；当前按默认端口 + 紧随其后的备用端口逐个 `/health` 探测。收敛在 `eagle-bridge.js` 的 `eagleBridgeApiPortHint()` 一处 |
| `04 §7 I1`/`I2`/`I4`/`I5` | **已由协作者回填**（`04 §2.3.1` / `§2.3.2` / `§3.3`）：`service` 固定 `"liudi-desktop"`、`apiProtocol` 当前为 `1`、`/api/plans` 返回 `Paged<PlanView>`。扩展已按该 schema 对齐；`taskView()` 仍保留旧 snake_case 别名作过渡兜底 |
| 浏览器下载模式的完整恢复 | 属阶段 2.5。客户端协议（`PUT`/`POST`/`DELETE`）与上限预检已实现；Worker 回收后地址丢失的任务无法续传（B-223 的固有边界），由桌面端按会话空闲超时收尾 |
| `AD-5` 端口发现只做到"最佳努力" | `api-port.json` 的读取钩子 `eagleBridgeApiPortHint()` **当前恒返回 0**（MV3 扩展无权读取任意本地文件）。这是 B-214 的**部分实现**：主端口 47652 可用，备用端口只在紧随其后的序号范围内探测 |
