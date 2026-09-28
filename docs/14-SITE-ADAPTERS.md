# 14 · 站点适配器

本文定义「特定抓取方法」的组织方式、命名规范、目录位置与文档要求。

**这是旧实现中最混乱的部分**：同一站点的定制逻辑散落在 3 个文件里，没有命名规范，没有文档，加入新站点只能靠改公共代码。

**已证实的完整先例**：抖音。§10 把它逐字段还原为规范中的第一个适配器。

---

## 1. 为什么需要适配器

### 1.1 先划清与 yt-dlp 的边界

**适配器不负责"解析出媒体地址"——那是 [yt-dlp](15-UPSTREAM.md) 的职责**（934 个站点提取器）。适配器只负责 yt-dlp **做不到**的那部分。

| 环节 | 谁负责 |
| --- | --- |
| 在**页面上认出**这是哪个视频（候选发现） | **适配器** |
| 判断当前页面地址是否属于该站点 | **适配器**（且规则不得比 yt-dlp 更宽） |
| 从页面地址**解析出媒体流** | **yt-dlp** |
| 身份绑定、画质档位、错误翻译 | **适配器** |

> **由此得出**：不要为 yt-dlp 已支持的站点重写解析逻辑。判断标准是——**这一步能不能只靠一个 URL 完成？** 能，就交给 yt-dlp；不能（要在 DOM 里判断、要读实时播放状态），才是适配器的活。

### 1.2 四类必须定制的原因

通用发现（嗅探媒体请求 + 读页面元数据）能覆盖大多数站点。少数站点必须定制，原因分四类：

| 原因 | 抖音实例（实测行号） |
| --- | --- |
| 页面身份不唯一 | 需要 `data-aweme-id` → `data-video-id` → `className` → `location.href` 四类信号才拼得出视频 ID（`content-script.js:184-190`） |
| 需要放宽通用约束 | 通用路径要求 blob 源；抖音允许直连流（`content-script.js:389-390`） |
| 多播放器需选主 | 页面同时存在多个 `<video>`，只有当前播放器是目标（`content-script.js:372`） |
| 错误需翻译 | yt-dlp 的 `Unsupported URL` / `Fresh cookies` 对用户无意义（`media.py:1936-1946`） |

**注意这四条全都不是"解析"**——它们都是"在页面上判断"。这正印证了 §1.1 的边界。

**旧实现的代价**（实测）：

| 位置 | 抖音相关命中 |
| --- | --- |
| `chrome-extension/js/content-script.js` | 44 |
| `chrome-extension/js/eagle-bridge-candidate-logic.js` | 15 |
| `src/idm_eagle_bridge/media.py` | 7 |
| 跨端共享的语义 | 视频 ID 与规范化地址在两端**各写一遍** |
| 文档 | **0** |

有测试覆盖（`test_candidate_presentation.js` 25 处、`test_popup_logic.js` 36 处、`test_extension.py` 8 处、`test_media.py` 21 处），但这些测试**没有清单**说明它们保护的是哪个站点的哪条规则。

---

## 2. 三层模型

| 层 | 名称 | 形态 | 何时用 |
| --- | --- | --- | --- |
| L0 | 通用发现 | 无适配器 | **默认**。绝大多数站点 |
| L1 | 声明式适配器 | 只写 `adapter.json` | 能用声明表达的一切：匹配、身份、规范化、标题、错误映射、工具参数 |
| L2 | 代码适配器 | `adapter.json` + `extension.js`（扩展侧） | 必须遍历 DOM 做决策的场景，如"从多个播放器里选主" |

**契约 A-101**：能用 L1 表达的，**禁止**写 L2。L2 仅在该站点的判定逻辑无法用声明表达时才允许，且必须在 `adapter.json` 的 `codeReason` 字段写明理由。

**契约 A-102**：L2 的代码**只允许**放在该站点的适配器目录内。禁止在 `content-script.js`、`media.py` 等公共文件里写站点名判断。

---

## 3. 目录与命名

**唯一源目录**：仓库根的 `adapters/`。

```
adapters/
  README.md                    适配器索引（必须，见 §7）
  generic/
    adapter.json               通用兜底，无匹配时生效
  douyin/
    adapter.json               声明（唯一事实源）
    README.md                  站点文档（必须）
    extension.js               扩展侧代码（仅 L2，可选）
    fixtures/                  离线测试夹具
      feed-item.html
      signals-modal-id.json
```

### 3.1 命名规范

| 对象 | 规则 | 示例 |
| --- | --- | --- |
| 目录名（站点 ID） | 主域名去掉公共后缀；小写 ASCII；只允许 `[a-z0-9-]` | `douyin.com` → `douyin` |
| 冲突时 | 加后缀区分，在 `README.md` 说明 | `bilibili` / `bilibili-tw` |
| 声明文件 | 固定名 `adapter.json` | — |
| 站点文档 | 固定名 `README.md` | — |
| 扩展侧代码 | 固定名 `extension.js` | — |
| 测试夹具 | 放在 `fixtures/`，命名 `<场景>.<ext>` | `feed-item.html`、`signals-modal-id.json` |
| 临时/参考文件 | 一律放 `fixtures/` | — |

**契约 A-103**：适配器目录内**只允许**出现上表列出的文件名。多出来的文件（草稿、截图、抓包、笔记）必须移出仓库或放进 `fixtures/`。

### 3.2 大小写与分隔

- 目录、文件名**全小写**。
- 多词用连字符，不用下划线：`wechat-channels`，不是 `wechat_channels`。

**为什么不与 [12 §2](12-CONVENTIONS.md) 冲突**：12 的"小写下划线"针对的是 **Go 源文件**。适配器目录名是**站点标识**，会被写进 `match.hosts` 与构建产物，连字符更贴近域名习惯。两者是针对不同对象的规则。

---

## 4. 单一源头与构建期分发

**问题**：适配器需要被**两端**使用，但两端取不到同一份文件。

| 端 | 能力 |
| --- | --- |
| 桌面端（Go） | 能读程序安装目录的 `adapters/` |
| 扩展（MV3） | **只能读自己包内的文件**。扩展在桌面端未安装时也必须能发现候选，因此**不能**依赖桌面端接口 |

**契约 A-104**：`adapters/` 是唯一事实源。构建时把扩展所需的字段抽取为 `extension/site-adapters.json`。

| 字段组 | 扩展需要 | 桌面端需要 |
| --- | --- | --- |
| `match` | ✓ | ✓ |
| `identity` | ✓ | ✓ |
| `capture` | ✓ | — |
| `title` | ✓ | — |
| `resolve` | — | ✓ |
| `errors` | — | ✓ |

**契约 A-105**：`extension/site-adapters.json` 是**生成物**，禁止手工编辑。文件头必须带 `"generated": true` 与 `"source": "adapters/"`，生成物规则见 [12 §6.3](12-CONVENTIONS.md)。

**这样做的好处**：加站点只改一个目录；扩展侧不再有与桌面端重复的 ID 解析逻辑（旧实现正是两端各写一遍）。

---

## 5. `adapter.json` 字段

```jsonc
{
  "id": "douyin",                          // 必须与目录名一致
  "name": "抖音",                          // 界面展示名
  "version": 1,                            // 本适配器版本，改动即递增
  "updatedFor": null,                      // 最后按真实页面核对的时间（YYYY-MM）；从未核对填 null
  "match": {
    "hosts": ["douyin.com", "*.douyin.com"],
    "priority": 100                        // 越大越优先；generic 为 0
  },
  "identity": {
    "urlRules": [
      { "path": "^/video/(\\d{10,30})/?$", "id": "$1" },
      { "query": "modal_id", "pattern": "^\\d{10,30}$", "id": "$0" }
    ],
    "domSignals": ["[data-aweme-id]", "[data-video-id]"],
    "canonical": "https://www.douyin.com/video/{id}",
    "requireId": true                      // 取不到 ID 时不上报候选
  },
  "capture": {
    "containers": ["[data-e2e=\"feed-item\"]", "[data-e2e=\"feed-active-video\"]"],
    "primarySelection": "current-player",   // current-player | first | all
    "allowDirectStream": true,              // 通用为 false
    "requireBlobSource": false              // 通用为 true
  },
  "title": {
    "template": "{nickname} - {description}",
    "fallback": "抖音视频 {id}"
  },
  "resolve": {
    "engine": "yt-dlp",
    "requiresFreshCookies": true,
    "canonicalizePageUrl": true
  },
  "errors": [
    { "match": "Unsupported URL",  "code": "douyin_page_unsupported",
      "message": "抖音内容页不是受支持的视频详情地址，请刷新当前视频后重试" },
    { "match": "Fresh cookies",    "code": "douyin_session_expired",
      "message": "抖音需要当前浏览器的新鲜会话，请刷新抖音页面后重试" }
  ],
  "codeReason": null                       // L2 时必填
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `id` | ✓ | 必须等于目录名，加载时校验 |
| `version` | ✓ | 整数，改动适配器即递增 |
| `updatedFor` | ✓ | 最后按真实页面核对的时间（`YYYY-MM`）。**从未按真实页面核对过填 `null`**，界面显示「尚未核对」 |
| `match.hosts` | ✓ | 支持 `*.` 前缀通配。**不写协议、不写路径** |
| `match.priority` | ✓ | 越大越优先 |
| `identity` | — | 无则为纯通用发现 |
| `identity.requireId` | — | `true` 时取不到 ID 丢弃候选，避免产生无法归属的条目 |
| `capture.primarySelection` | — | 见 §5.1 |
| `capture.allowDirectStream` / `requireBlobSource` | — | **放宽或收紧通用约束的唯一入口** |
| `resolve.engine` | — | 目前只允许 `yt-dlp` 与 `builtin` |
| `resolve.builtin` | — | `engine` 为 `builtin` 时必填，指向 §6.2 的内置处理器名 |
| `errors` | — | 按顺序匹配，**首个命中生效** |
| `codeReason` | — | L2 必填 |

### 5.1 `primarySelection`

| 值 | 语义 |
| --- | --- |
| `current-player` | 在多个 `<video>` 中选当前播放的那个（抖音） |
| `first` | 取文档中第一个 |
| `all` | 全部上报为独立候选（默认） |

### 5.2 约束

**契约 A-106**：`adapter.json` **不得**声明任何与安全边界有关的字段。不存在"该站点跳过校验""该站点允许绝对路径"这类配置。适配器只影响**发现与解析**，不影响输入校验、路径归属与删除保护。

**契约 A-107**：`match.hosts` 之外的任何字段都**不得**包含完整 URL、Cookie、令牌或签名参数。适配器随程序分发，属于公开内容。

**契约 A-117**：适配器**不得**声明或切换**浏览器下载模式**（[04 §2.5](04-INTERFACES.md)）。该开关只由用户在扩展弹窗内决定，**禁止**"检测到某站点就自动改用浏览器下载"——用户明确否决了按站点自动切换：同一站点在不同会话下的失败原因不同，自动切换会让"为什么这次变慢了"无法解释。适配器最多可以在**诊断信息**里记录某站点曾出现握手类失败，供用户自行决定是否切换。

### 5.3 加载与优先级

启动时扫描 `adapters/` 下的**一级子目录**，读取各自的 `adapter.json`。

**匹配顺序**（自上而下，先命中者胜）：

| 顺序 | 规则 |
| --- | --- |
| 1 | `match.priority` 降序 |
| 2 | 同优先级下，精确域名优先于通配域名 |
| 3 | 都不匹配时使用 `generic` |

**契约 A-114**：`adapter.json` 的 `id` **必须**等于其目录名。不一致时启动失败并指出两个值。

**契约 A-115**：同一域名被两个适配器以**相同 `priority`** 声明时，启动失败并列出冲突域名与两个目录名。**禁止**静默取其一——那会让"某站点行为忽然变了"变成无法定位的问题。

**契约 A-116**：任一 `adapter.json` 解析失败时，**必须**报错并指出文件路径，**禁止**静默跳过。静默跳过的表现是"这个站点突然抓不到了"，用户与开发者都无从排查。

> 这三条针对的都是**程序自己的错误**，符合第 2 条设计原则：不防攻击者，但要让自己出错时立刻看得见。

---

## 6. 代码适配器（L2）

### 6.1 扩展侧 `extension.js`

必须导出固定签名的函数，**只允许**使用传入参数，不读全局页面状态：

| 函数 | 签名 | 返回 |
| --- | --- | --- |
| `videoIdFromSignals` | `(signals: string[]) => string` | 视频 ID，取不到返回 `""` |
| `selectPrimaryIndex` | `(videos: Array<{index, rect, isPlaying}>) => number` | 主播放器下标，`-1` 表示无 |
| `candidateTitle` | `({nickname, description, videoId}) => string` | 标题 |
| `normalizePageUrl` | `(url: string, videoId: string) => string` | 规范化页面地址 |

**契约 A-108**：这四个函数**必须纯函数**——不访问 `document`、不发请求、不读 storage。信号由调用方收集后传入。这样它们可以被 `tests/js/` 直接单测，不需要浏览器环境。

### 6.2 桌面侧

**不引入 Go 插件机制**（Windows 上 `plugin` 包限制多且需要 CGO，与 [ADR-002](adr/ADR-002-go-wails.md) 的纯 Go 约束冲突）。

桌面端只有两种形态：

| 形态 | 适用 |
| --- | --- |
| `resolve.engine = "yt-dlp"` + `errors` 映射 | 绝大多数定制，含抖音 |
| `resolve.engine = "builtin"` + `builtin: "<name>"` | 确需 Go 代码时（如视频号），代码放 `internal/adapter/<name>.go` |

**契约 A-109**：`internal/adapter/` 下的每个内置处理器，都必须在 `adapters/<site>/README.md` 与 `adapter.json` 中登记。**不允许**存在没有对应适配器目录的内置处理器。

---

## 7. 文档要求

### 7.1 `adapters/README.md`（索引）

必须包含一张表，每行一个适配器：

| 列 | 内容 |
| --- | --- |
| 站点 | `adapter.json` 的 `name` |
| ID | 目录名 |
| 层 | L1 / L2 |
| 定制原因 | 一句话 |
| 最后核对 | `updatedFor` |
| 文档 | 指向 `adapters/<id>/README.md` |

### 7.2 `adapters/<id>/README.md`（站点文档）

**契约 A-110**：每个适配器目录**必须**有 `README.md`，且必须包含以下小节：

| 小节 | 内容 |
| --- | --- |
| 匹配范围 | 域名、是否含子域、不覆盖什么 |
| 抓取方法 | 媒体地址是怎么拿到的，写清链路 |
| 字段映射 | 页面上哪个元素/接口 → 候选的哪个字段 |
| 与通用路径的差异 | 放宽了什么、收紧了什么、为什么 |
| 已知问题 | 当前未解决的行为 |
| 验证方法 | 手工步骤 + 对应 fixtures |
| 变更记录 | 日期、改了什么、依据 |

**契约 A-111**：站点文档里**禁止**写入真实 Cookie、签名 URL、账号信息或抓包原文。需要举例时用占位符。

---

## 8. 测试要求

**契约 A-112**：每个适配器**必须**有 `fixtures/`，且测试**离线可跑**——不访问真实站点。

| 层 | 测什么 | 新仓库落点（Go / JS） |
| --- | --- | --- |
| L1 | 加载后匹配、URL 规则命中、错误映射 | `internal/adapter/adapter_test.go` |
| L2 扩展侧 | §6.1 的四个纯函数 | `extension/tests/test_adapter_<id>.js` |
| L2 桌面侧 | 内置处理器的解析结果 | `internal/adapter/<id>_test.go` |

> 技术栈是 Go + 原生 JS（[12 §5.1](12-CONVENTIONS.md) 的测试层次），**不引入 Python**。上表第一、三行的测试必须能在 `go test ./...` 下跑完。

**契约 A-113**：抖音的现有测试必须**按适配器归位**，不得散落在通用测试文件里。下表左列是**旧实现的 Python/JS 测试**（位于只读的旧项目），右列是归位目标：

| 旧实现的测试 | 归位到 |
| --- | --- |
| `test_candidate_presentation.js` 中的抖音部分 | `extension/tests/test_adapter_douyin.js` |
| `test_popup_logic.js` 中的抖音部分 | `extension/tests/test_adapter_douyin.js` |
| `test_media.py` 中的抖音部分 | `internal/adapter/douyin_test.go` |
| `test_extension.py` 中的抖音部分 | `internal/adapter/douyin_test.go` |

归位时**不得降低覆盖**：原断言数量必须全部保留（[07 §8](07-EXTENSION.md)）。

---

## 9. 与 `site_rules` 的区别

两者名字接近，语义完全不同，**不得混用**：

| | `adapters/<id>/adapter.json` | `site_rules` 表（[03 §2.5](03-DATA.md)） |
| --- | --- | --- |
| 归属 | 随程序分发的**内置**内容 | **用户**在设置里的开关 |
| 内容 | 怎么抓 | 抓不抓 |
| 存储 | 磁盘文件 | SQLite |
| 可变 | 由版本更新 | 用户随时改 |
| 作用域 | 发现与解析 | 是否在该站点工作 |

**优先级**：`site_rules` 判定为禁用时，**先于**适配器匹配生效——压根不进入发现流程。

---

## 10. 首个适配器：抖音

本节只记录**旧实现出处**——即每个规范字段是从旧项目的哪一行还原出来的，供移植时回溯。**字段的实际取值不在这里**：唯一事实源是 [`adapters/douyin/adapter.json`](../adapters/douyin/adapter.json)（契约 A-104）。

> 早先这里有一列"取值"，与 `adapter.json` 构成两处定义，并且**已经漂移**（此处写 `feed-item`，json 写 `[data-e2e="feed-item"]`）。按 A-104 删列：声明以 json 为准，本文只管"从哪来"。

| 规范字段 | 旧实现出处 |
| --- | --- |
| `match.hosts` | `content-script.js:311`、`media.py:242` |
| `identity.urlRules` | `media.py:243-248` |
| `identity.domSignals` | `content-script.js:185-189` |
| `identity.canonical` | `media.py:250` |
| `identity.requireId` | `content-script.js:402`（无 ID 直接 `continue`） |
| `capture.containers` | `content-script.js:182-183` |
| `capture.primarySelection` | `content-script.js:372` |
| `capture.allowDirectStream` / `requireBlobSource` | `content-script.js:389-390` |
| `title.template` / `title.fallback` | `content-script.js:321` / `:322` |
| `resolve.engine` 与新鲜 Cookie 要求 | `media.py:1934-1959` |
| `errors[0]` | `media.py:1937-1941` |
| `errors[1]` | `media.py:1942-1946` |
| `updatedFor` | —（`null`：**从未按新架构核对**） |

**L2 理由**：`selectPrimaryIndex` 需要读取实时播放状态（`videoIndex === douyinPrimaryIndex`，`content-script.js:372`），无法用声明表达。故此适配器 `codeReason` 非空。

**迁移状态**：**待移植**。旧项目的抖音实现从未按新架构核对，`M` 系列手工验收中也没有抖音专项。移植时必须执行 [11 §5](11-ACCEPTANCE.md) 的 **M12**（已登记）。

---

## 11. 新增一个适配器的流程

1. 在 `adapters/` 下建 `<site-id>/` 目录。
2. 写 `adapter.json`，先只填 `match` + `identity`，跑通用路径。
3. 通用路径确实不足时，再补 `capture` / `title` / `resolve` / `errors`。
4. 仍不足且无法声明时才加 `extension.js`，并填 `codeReason`。
5. 写 `README.md`（§7.2 的七个小节）。
6. 把真实页面存成 `fixtures/`，写离线测试。
7. 在 `adapters/README.md` 索引表加一行。
8. 跑构建，确认 `extension/site-adapters.json` 已更新且未被手工修改。

**禁止**：为了让某个站点通过，去改公共发现代码的分支。那些分支属于所有站点。

---

## 12. 待确认

| # | 项 | 何时确认 |
| --- | --- | --- |
| SA1 | 首批适配器清单（除抖音、视频号外还移植哪些） | 阶段 3 |
| SA2 | `site-adapters.json` 由哪个构建步骤生成 | 阶段 2 |
| ~~SA3~~ | ~~适配器 `version` 与产品版本是否需要绑定~~ **已定：不绑定**。适配器必须能独立于产品版本更新——站点改版不会等我们的发版节奏（见 §8 `R10`）。`version` 只表示该适配器自身声明的修订号 | 已解决 |
| SA4 | 诊断页的适配器状态：`updatedFor` 为 `null` 显示「尚未核对」，距今过久显示「可能过期」 | 阶段 7 |
