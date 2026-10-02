# 04 · 接口规格

本文定义系统内部**五类接口**的契约。

---

## 1. 全景

| # | 接口 | 双方 | 传输 | 认证 |
| --- | --- | --- | --- | --- |
| IF-1 | 扩展 API | 浏览器扩展 → 桌面端 | 回环 HTTP | **仅 Origin 白名单** |
| IF-2 | 前端绑定 | WebView 前端 → Go | Wails 方法调用 | 同进程 |
| IF-3 | 事件推送 | Go → WebView 前端 | Wails Events | 同进程 |
| IF-4 | 捕获进程协议 | 主进程 ↔ `liudi-capture` | **管道 JSON-RPC** | 父子关系 |
| IF-5 | IDM hook | IDM → `liudi-hook` | inbox 文件 + 命名事件 | 本地目录 |

### 1.1 通则

| # | 规则 |
| --- | --- |
| G1 | 每个跨边界调用必须有**超时** |
| G2 | 错误必须返回稳定机器码 + 可安全展示的消息 |
| G3 | 秘密不得出现在日志、诊断或持久化中 |
| G4 | 载荷必须有大小上限 |
| G5 | 绑定层与 HTTP handler 只做参数校验与转发，不含业务逻辑 |

### 1.2 唯一业务入口：Service 层

**Wails 绑定与 HTTP handler 必须调用同一个 Service，不得各写一套业务逻辑。**

```text
                ┌─ Wails Binding
                │
UI / Extension ─┤
                │
                └─ HTTP Handler
                       │
                       ▼
                  App Service        ← 唯一业务入口
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       Store        Media        Eagle
```

| 规则 | 说明 |
| --- | --- |
| 职责 | Service 持有全部业务规则；Binding 与 Handler 只做参数解析、调用、结果序列化 |
| 禁止 | 在 Handler 或 Binding 里写业务判断（状态校验、重试决策、降级逻辑） |
| 好处 | 改逻辑只改一处；扩展与 UI 的行为必然一致 |

**不引入** Clean Architecture 的分层（domain / repository / usecase / adapter / port / gateway）。一个 Service 层足够。

---

## 2. IF-1 扩展 API

### 2.1 传输

| 项 | 规定 |
| --- | --- |
| 绑定 | **仅 `127.0.0.1`** |
| 端口 | 默认 `47652`；被占用时用备用端口并写入扩展可发现位置 |
| 协议 | HTTP/1.1，回环，无 TLS |
| 请求体上限 | **256 KB**；唯一例外是浏览器下载模式的中转上传端点（§2.5），其上限见该节 |
| 超时 | 每请求读写超时必须有界 |

**跨源**：中转上传使用 `PUT` / `DELETE` 与自定义请求头，浏览器会先发 `OPTIONS` 预检。预检请求只回 CORS 头、**不进入业务处理**；其余请求在 Origin 白名单通过后回显该 Origin。

### 2.2 认证：Origin 白名单

```text
读请求 Origin 头
  → 等于 settings.extension_origin  → 放行
  → 其他值或不带 Origin             → 403
```

- 白名单值默认来自**编译常量**（见 [01 §2.1](01-ARCHITECTURE.md)）；`settings.extension_origin` 仅作为开发调试的**覆盖值**。**安装器不写入任何 Origin。**
- **无配对码、无令牌、无恢复流程**（B-105、B-212）。
- 除 `GET /health` 外全部端点都需要 Origin 校验。

### 2.3 端点

**GET**

| 端点 | 用途 | schema 落点 |
| --- | --- | --- |
| `/health` | 健康与能力（含 `eagleAvailable`），免 Origin 校验 | **§2.3.2（阶段 2）** |
| `/api/plans` | 计划列表 | **§2.3.2（阶段 2）** |
| `/api/plan` | 单个计划详情 | **§2.3.2（阶段 2）** |
| `/api/jobs` | 导入任务列表 | 阶段 4 回填 |
| `/api/sites` | 站点规则列表 | 阶段 3 回填 |
| `/api/preview` | 任务预览图 | 阶段 6 回填（受 §7 的 `I7` 约束） |
| `/api/mode` | 读取浏览器下载模式（§2.5） | 阶段 2.5 回填（当前概要见 §2.5） |

**POST**

| 端点 | 用途 | schema 落点 |
| --- | --- | --- |
| `/api/plan` | 创建下载计划 | **§2.3.2（阶段 2）** |
| `/api/plan/retry` | 重试 | **§2.3.2（阶段 2）** |
| `/api/plan/stop` | 停止 | **§2.3.2（阶段 2）** |
| `/api/plan/remove` | 删除记录 | **§2.3.2（阶段 2）** |
| `/api/plan/open` | 打开文件或所在目录 | **§2.3.2（阶段 2）** |
| `/api/plan/import` | 补导 Eagle | 阶段 4 回填 |
| `/api/sites` | 设置站点规则 | 阶段 3 回填 |
| `/api/source` | 上报来源事件 | **§2.3.2（阶段 2）** |
| `/api/mode` | 设置浏览器下载模式（§2.5） | 阶段 2.5 回填（当前概要见 §2.5） |

**PUT / DELETE**

| 方法 | 端点 | 用途 | schema 落点 |
| --- | --- | --- | --- |
| PUT / POST / DELETE | `/api/upload` | 中转上传的开始与结束（§2.5） | 阶段 2.5 回填（当前概要见 §2.5） |

> **本表是端点清单的唯一出处**：新增端点必须先改本表，并同步 [07 AD-3](07-EXTENSION.md)——不得只改实现。完整 schema 按上表的「schema 落点」在对应阶段回填，阶段 2 的部分见 §2.3.2。

**两条边界**：

| 项 | 规定 |
| --- | --- |
| 站点适配器 | **不通过本 API 分发**。扩展使用包内构建副本（[14 §4](14-SITE-ADAPTERS.md)），因此桌面端未安装时扩展仍能发现候选 |
| 轮询频率 | 见 [07 P-108](07-EXTENSION.md)——本文不重复其取值 |

### 2.3.1 通用约定（全部端点）

| 项 | 规定 |
| --- | --- |
| 请求编码 | 有请求体时必须 `Content-Type: application/json; charset=utf-8` |
| 请求体上限 | 见 [03 §4.5](03-DATA.md)；**唯一例外**是 §2.5 的中转分片端点（取值见 [03 §4.6](03-DATA.md)） |
| 成功 | `{ "ok": true, "data": <该端点的返回类型> }`——**外壳就是 §3.1 的 `Result<T>`**（同构，不另立一套），端点与绑定方法共用 |
| 失败 | §2.4 的形状：稳定机器码 + 可直接展示的消息 |
| 时间 | JSON 数字，语义与单位同 [03 §1](03-DATA.md) 的 `P3`；唯一的毫秒字段是 §2.3.2 里 `/api/source` 的 `capturedAt` |
| 缓存 | 响应必须带 `Cache-Control: no-store`——状态类数据不得被缓存 |
| 幂等 | 逐端点规定见 §2.3.2；**不引入**通用幂等键（§3.2 的既有结论） |

**HTTP 状态码与业务结果正交**：业务失败**不**改状态码，客户端一律读 JSON 的 `ok`。

| 状态码 | 何时 |
| --- | --- |
| `200` | 服务端已处理完请求（`ok` 为 `true` **或** `false`） |
| `400` | 参数或请求体形态错误：非合法 JSON、缺必填字段、字段类型不符、查询参数越界或取值不在被引用文档的取值域内 |
| `403` | Origin 不在白名单（`/health` 除外，见 §2.2） |
| `404` | 路径不在 §2.3 的清单内（清单内但尚未实现的端点见 `501`） |
| `405` | 方法与该端点的规定不符 |
| `413` | 请求体超过 [03 §4.5](03-DATA.md) 的上限 |
| `500` | 未预期内部错误——**仅**在无法构造业务错误码时使用，且必须记录日志（[12 §4](12-CONVENTIONS.md)） |
| `501` | 路径已在 §2.3 的清单内、但属后续阶段且当前未实现（见下表 `not_implemented`） |

**本机 API 层自有的错误码**（权威定义就在本表）。它们描述的是**请求本身**的问题，**不进入** `plans.error_code` / `jobs.error_code`——那两列的取值域在 [03 §3.4](03-DATA.md) 与 [05 §7](05-DOWNLOAD.md) / [08 §9](08-EAGLE-IDM.md)：

| 码 | 何时 | 状态码 |
| --- | --- | --- |
| `invalid_request` | 参数或请求体形态错误（上表 `400` 的所有情形） | `400` |
| `forbidden_origin` | Origin 不在白名单 | `403` |
| `not_found` | 路径不在 §2.3 的清单内 | `404` |
| `method_not_allowed` | 方法与该端点的规定不符 | `405` |
| `request_too_large` | 请求体超上限 | `413` |
| `internal_error` | 未预期内部错误 | `500` |
| `not_implemented` | 清单内、但属后续阶段未实现 | `501` |

| 规则 | 说明 |
| --- | --- |
| 业务错误的码归 Service | `plan_not_found`、`invalid_url` 这类**业务**码由 Service 返回（§1.2 的 G5），取值域见 [05 §7](05-DOWNLOAD.md) / [08 §9](08-EAGLE-IDM.md)；本文**不复制**它们的取值 |
| 业务失败的**码**与**状态码** | 码一律来自 Service；状态码由本层按下表映射。映射只表达"失败的**类别**"，**不改变判断依据**——调用方仍然只按 `error.code` 分支（与 §3.1 的 `Result<T>` 走同一条路径），状态码是给日志、代理与排错看的 |
| 消息 | `message` 必须可直接展示，**不含路径、堆栈或秘密**（§2.4、[12 §3.3](12-CONVENTIONS.md)） |
| 同码同义 | 同一含义不得有两个码；借用 [05 §7](05-DOWNLOAD.md) 中语义不同的码是禁止项（[03 §3.4](03-DATA.md)） |
| 新增流程 | 新增本层错误码必须先改本节 |

**业务码 → 状态码映射**（`plans` / `jobs` 的码先按 [05 §7](05-DOWNLOAD.md) 与 [08 §9](08-EAGLE-IDM.md) 的语义归类，再落状态）：

| 码的类别 | 状态码 | 依据 |
| --- | --- | --- |
| `plan_not_found`、`plan_file_missing` | `404` | 目标不存在 |
| `plan_not_retryable`、`plan_state_changed`、`plan_not_importable` | `409` | 与当前状态冲突 |
| `plan_file_not_owned` | `403` | 归属校验不通过（[05 §11](05-DOWNLOAD.md)） |
| `open_folder_unavailable` | `503` | 环境暂时不可用 |
| 创建期校验类（[05 §3.2](05-DOWNLOAD.md) 的输入类码与 [05 §7.2](05-DOWNLOAD.md) 的不可重试输入类） | `400` | 输入不合法 |
| **未登记的码** | `500` | **刻意如此**：不得把实现缺口伪装成"用户输入错了"；它同时是一条"有码没归类"的实现待补信号 |

### 2.3.2 阶段 2 端点（完整 schema）

其余端点按 §2.3 上表的「schema 落点」在对应阶段回填。本节用到的类型名（`PlanView`、`Paged<T>`、`CreatePlanRequest`…）**只在 §3.3 定义一次**，HTTP 端点与绑定方法共用同一份。

#### `GET /health`

免 Origin 校验（§2.2）。**请求**：无参数。

**响应 `data`**：`Health`——下表就是它的**全部**字段（即 [07 AD-4](07-EXTENSION.md) 所说的能力字段全集）。

| 字段 | 类型 | 判据 |
| --- | --- | --- |
| `service` | string | 固定 `"liudi-desktop"`；扩展据此确认对端是本软件，而不是占用同一端口的其他进程（[07 AD-2](07-EXTENSION.md)） |
| `version` | string | 产品版本；取值来源唯一（[12 §8.1](12-CONVENTIONS.md)）。**只用于显示与一致性核对**（B-104） |
| `apiProtocol` | int | 本机 API 协议版本，**当前为 `1`**。不兼容变更（增删端点、改字段语义、改取值域）**必须** `+1` |
| `eagleAvailable` | bool | Eagle 可用性（B-503）；由 [08 §3](08-EAGLE-IDM.md) 的探测与内存熔断得出 |
| `mediaToolsReady` | bool | [05 §5.1](05-DOWNLOAD.md) 的外部工具是否**全部**解析成功 |

| 规则 | 说明 |
| --- | --- |
| 始终成功 | **必须**返回 `200` + `ok: true`：某个能力为 `false` 只表示该项当前不可用，**不得**让整个响应失败（B-502、B-503） |
| 不含路径 | 响应里**不得**出现路径、URL、Cookie 或任何环境细节（B-722、[12 §4.3](12-CONVENTIONS.md)）；工具缺失只报 `false`，不报是哪个路径缺 |
| single-flight | 探测必须复用同一轮结果（B-307），**不得**让每个请求各自等一次外部连接 |
| 扩展侧判据 | `ok === true && data.service === "liudi-desktop"` → 视为已连接；`data.apiProtocol` 与扩展内的期望值不等 → **提示**版本不匹配并禁用依赖新协议的动作，**不得静默** |
| 不含 P6 | 浏览器下载模式的可用性与取值一律走 `/api/mode`（§2.5）——**不**在健康响应里放同名字段（否则"能力"与"设置值"两义） |

#### `GET /api/plans`

**请求（查询参数）**

| 参数 | 必填 | 规则 |
| --- | --- | --- |
| `status` | 否 | 过滤；`status=a,b` 与重复出现 `status=a&status=b` 两种写法**等价**（去重，保持首次出现顺序）；取值域见 [03 §3.1](03-DATA.md)，取值非法 → `400`；缺省 = 不按状态过滤 |
| `offset` | 否 | 默认 `0`；非整数或负数 → `400` |
| `limit` | 否 | 默认 `50`；非正整数或超过 [03 §4.5](03-DATA.md) 的单次返回上限 → `400`——**不静默截断**：少给条数会让调用方以为已经拿全 |

参数校验属 §2.3.1 的「参数校验」，**不是**业务判断（§1.2 的 G5 明确把它留给 Handler/Binding）。

**响应 `data`**：`Paged<PlanView>`（§3.3）。排序**必须**是 `updated_at DESC`——命中 [03 §5](03-DATA.md) 的 `Q2` 所指索引。

**错误**：只有形态错误（§2.3.1）。**列表为空是成功**（`items: []`），不是错误。

#### `GET /api/plan`

**请求**：`id`（查询参数，必填）。**响应 `data`**：`PlanView`（§3.3）。

**错误**：`plan_not_found`（[05 §7.3](05-DOWNLOAD.md)）——记录已被删除时返回同一个码。

#### `POST /api/plan`（创建）

**请求体**：`CreatePlanRequest`（§3.3）。**响应 `data`**：创建后的 `PlanView`（`status = queued`，[05 §2](05-DOWNLOAD.md)）。

| 规则 | 说明 |
| --- | --- |
| 校验 | 入参与创建期校验**必须**走 [05 §3](05-DOWNLOAD.md)，本接口不另定一套 |
| 地址只在内存 | 请求体里的媒体地址由 Service 持在**内存**，**不落库**（[03 §2.1](03-DATA.md)）；落库的 `source_url` 是**页面地址**（按 [03 §4.1](03-DATA.md) 归一化） |
| 阶段 2 的收窄 | 只接受 `mediaKind = direct` 且 `streams` 恰为一个 `main` 轨道；其余取值**拒绝且不落库**（[05 §3.2](05-DOWNLOAD.md)） |
| 导入意图 | `importToEagle = false` 时 `deleteAfterImport` **必须**落库为 0；Eagle 不可用时两者都落库为 0（B-504、[08 §3.1](08-EAGLE-IDM.md)）——请求里的 `true` 不得被采纳 |
| 不去重 | 重复提交由前端避免（§3.2 幂等性）；本接口**不**引入幂等键 |

**错误**：`invalid_url` · `blocked_local_target` · `blob_not_downloadable` · `fixed_range_fragment` · `invalid_merge_mode` · `invalid_container` · `missing_media`（[05 §3.2](05-DOWNLOAD.md)），以及 [05 §7](05-DOWNLOAD.md) 中与该次失败对应的码。

#### `POST /api/plan/stop`

**请求体**：`{ "id": string }`（必填）。**响应 `data`**：当前 `PlanView`。

| 规则 | 说明 |
| --- | --- |
| 非终态 | 转 `canceled`（[05 §10](05-DOWNLOAD.md)），且**不得**被后续完成回调覆盖（B-308） |
| 终态 | **幂等空操作**：原样返回当前 `PlanView`，不报错（扩展与界面都可能重复点击） |

**错误**：`plan_not_found`（[05 §7.3](05-DOWNLOAD.md)）。

#### `POST /api/plan/retry`

**请求体**：`{ "id": string }`（必填）。**响应 `data`**：`PlanView`（已转 `queued`）。

| 规则 | 说明 |
| --- | --- |
| 可重试状态 | 只有 `failed` 可手动重试（[05 §2.2](05-DOWNLOAD.md) 第 3 条）；其他状态 → `plan_not_retryable` |
| 上下文必须在内存 | 执行上下文只存在于内存（[03 §2.1](03-DATA.md)）：不再持有 → **拒绝**并返回 `context_expired`（[05 §7.2](05-DOWNLOAD.md)），提示「请重新创建任务」（[05 §9](05-DOWNLOAD.md)）——**不得**入队空转 |
| 计数不重置 | 手动重试**不重置** `attemptCount`（B-315 的持久化计数不得被界面操作抹掉）；`nextAttemptAt` 置为当前时间后，按 [05 §8](05-DOWNLOAD.md) 判定后续 |

**错误**：`plan_not_found` · `plan_not_retryable` · `context_expired`（[05 §7](05-DOWNLOAD.md)）。

#### `POST /api/plan/remove`（删除记录）

**请求体**：`{ "id": string }`（必填）。

**响应 `data`**：`{ "id": string, "removed": true, "filePreserved": bool }`

| 规则 | 说明 |
| --- | --- |
| 只删记录 | **不删除任何文件**：`已完成` 的交付文件与 `预览` 的预览一律保留（B-401、B-403）。`filePreserved` 如实反映交付文件此刻是否仍在磁盘上（`finalPath` 为空 → `false`） |
| 运行中 | 未终结的计划按 [05 §10](05-DOWNLOAD.md) 先取消、再删记录，同一次调用内完成 |
| 关联导入 | 同事务删除引用该计划的 `jobs` 行——`jobs.plan_id` 是外键（[03 §2](03-DATA.md)），删 `plans` 行前必须先处理它，**不得**留下违反外键的引用。`fingerprints` 行**保留**：那是内容级事实，不随记录删除而消失，[08 §4.1](08-EAGLE-IDM.md) 的去重判定照旧成立 |
| 重复删除 | **不静默成功**：删完再删返回 `plan_not_found` |
| 文件删除 | **不在本端点范围内**（批量清理语义见 [03 §8](03-DATA.md) 的 `D5`，阶段 3） |

**错误**：`plan_not_found`（[05 §7.3](05-DOWNLOAD.md)）。

#### `POST /api/plan/open`

**请求体**：`{ "id": string, "target": "file" | "folder" }`——两项都必填，**不设默认值**："打开文件"与"打开所在目录"必须由调用方明确。

**响应 `data`**：`{ "opened": true, "target": "file" | "folder" }`

| 规则 | 说明 |
| --- | --- |
| 前置条件 | 只有 `finalPath` 非空（即 [03 §3.1](03-DATA.md) 的 `completed`）才能打开；否则 `plan_file_missing` |
| 归属校验 | 打开前**必须**校验路径位于 [01 §8](01-ARCHITECTURE.md) 的 `已完成` 目录内，否则 `plan_file_not_owned`——**不得**打开任意路径 |
| 文件被外部删除 | 归属校验通过但文件已不在 → `plan_file_missing` |
| 无状态变更 | 本端点**不**修改任何计划状态 |

**错误**：`plan_not_found` · `plan_file_missing` · `plan_file_not_owned` · `open_folder_unavailable`（[05 §7.3](05-DOWNLOAD.md)）。

#### `POST /api/source`（上报来源事件）

**请求体**

| 字段 | 类型 | 必填 | 规则 |
| --- | --- | --- | --- |
| `eventType` | string | 是 | 必须是下表**全集**之一；未知取值**拒绝**（不得按默认值处理） |
| `pageUrl` | string | 是 | 页面地址；按 [03 §4.1](03-DATA.md) 归一化后使用；长度上限见 [03 §4.5](03-DATA.md) |
| `pageTitle` | string | 否 | 标题；上限见 [03 §4.5](03-DATA.md)，超出截断 |
| `mediaUrl` | string | 否 | 媒体地址；**只用于与文件名做匹配提示**——不落库（[03 §6](03-DATA.md)、B-722）、不进日志（B-303）、不得出现在响应里 |
| `tabId` | int | 否 | 来源标签页标识；只用于同一标签内的重复上报判定 |
| `capturedAt` | number | 否 | 事件发生时间，**毫秒**（浏览器 `Date.now()` 口径）。缺失或明显失真（未来时间 / 过旧）时以服务端接收时间代替，**不得**因此拒绝请求 |

**`eventType` 全集**

| 取值 | 含义 | 抑制 |
| --- | --- | --- |
| `media` | 页面请求或播放媒体时的自动上报 | 否 |
| `page_download_click` | 用户点击了页面上的下载 / 媒体入口（由内容脚本判定） | 否 |
| `manual` | 用户在扩展里**手动指定**当前页面为来源 | 否 |
| `ignore` | 用户明确选择**本次不导入** | **是** |
| `site_disabled` | 该站点处于**关闭自动导入**状态 | **是** |

**响应 `data`**：`{ "accepted": true, "suppressed": bool }`（`suppressed` = 该事件属上表的抑制类）。

| 规则 | 说明 |
| --- | --- |
| 只在内存 | 事件**不写任何表**（[03 §2](03-DATA.md) 的 5 张表里没有来源事件表）；进程重启即丢失，该轮导入按"无来源"处理——[08 §8.4](08-EAGLE-IDM.md) 的宽限期过后**无来源也导入**，只是不写 `website`（B-507） |
| 消费方 | IDM 导入流程按 [08 §8.4](08-EAGLE-IDM.md) 的来源宽限期取最近的事件；`mediaUrl` 仅用于与文件名比对 |
| 抑制语义 | `suppressed = true` 的事件是该轮导入**不导入**的唯一来源（B-604）：匹配到的任务按 [03 §3.2](03-DATA.md) 的 `skipped` 处理，原因码 `by_user`。**不得**由"缺少来源事件"反推站点关闭 |
| 幂等 | 同一 `pageUrl` + `mediaUrl` + `eventType` 在 **3 秒**内的重复上报按一条处理（沿用旧实现口径），响应仍是成功 |
| 不改状态 | 本端点不创建计划、不改 `plans` / `jobs` 状态——抑制效果在导入流程消费该事件时生效（[08 §8.4](08-EAGLE-IDM.md)） |
| 不写站点表 | `site_rules.enabled` 只由 [03 §2.5](03-DATA.md) 的写入口（阶段 3 的 `/api/sites` 与扩展内操作）改变；本端点**不写** `site_rules` |

**旧取值的处置**：旧实现用过的 `video_request` 与 `download_intent` **不再保留**——前者与 `media` 同义，后者只是"字段缺失时的兜底值"（本设计里 `eventType` 必填）。同一个含义不得有两个取值（同 [03 §3.4](03-DATA.md) 的唯一性规则）。

### 2.5 浏览器下载模式与中转上传

**模式定义**：浏览器下载模式开启时，扩展用**浏览器自身的会话**取媒体字节，再流式 POST 给桌面端；关闭时按 [05 §4](05-DOWNLOAD.md) 的下载路径由桌面端出站获取。

| 项 | 规定 |
| --- | --- |
| 默认值 | **关闭**（`browser_download_mode = 0`） |
| 开关位置 | **只在扩展弹窗的「设置」标签页内**，桌面端设置页**不设**此开关（[09 §4.8](09-UI.md)、[07 §6.3](07-EXTENSION.md)） |
| 权威位置 | 桌面端数据库 `settings.browser_download_mode`，经 `/api/mode` 读写。扩展**不**用本地副本做权威；弹窗打开时读一次 |
| 设计原因 | 部分站点（如 YouTube）对同一直链的握手校验严格，桌面端重复请求拿不到；浏览器凭自身会话可直接取得 |
| 适用边界 | **不适用于视频号**——该路径产出的是加密字节，必须由捕获链路的会话密钥解密（[06](06-WECHAT.md)）。此模式下视频号入口保持原逻辑并提示 |
| 模式切换时机 | 模式在**创建计划时**决定并写入计划记录，运行中改开关**不影响**已创建的计划 |

**端点**

| 方法 | 端点 | 入参 | 返回 |
| --- | --- | --- | --- |
| GET | `/api/mode` | — | `{ browserDownloadMode: bool }` |
| POST | `/api/mode` | `{ browserDownloadMode: bool }` | 同 GET |
| PUT | `/api/upload` | `{ planId, track, declaredBytes, contentType, sourceUrl }` | `{ file, received }` |
| POST | `/api/upload?planId=&track=&offset=` | 请求体为**裸字节分片** | `{ received }` |
| DELETE | `/api/upload` | `{ planId, track, state: complete \| aborted }` | `{ ok, path? }` |

| 规则 | 说明 |
| --- | --- |
| 会话键 | `planId + track`；`track` 取 `main` / `video` / `audio` |
| 幂等与续传 | `PUT` 可重复调用：若临时文件已存在，返回**已接收字节数**与同一 `file`，扩展从该偏移继续 |
| 分片语义 | 桌面端按 `offset` **顺序追加**；`offset` 不等于当前已接收字节数时返回 `upload_offset_mismatch`（含当前值），扩展从返回的偏移重发 |
| 校验 | `DELETE ... complete` 时比对 `declaredBytes` 与实际字节数，不符返回 `upload_incomplete` 并保留临时文件 |
| 轨迹汇聚 | 同一计划的多个 `track` 落在同一 `temp/<plan_id>/` 下；全部到齐后才进入 `merging` |
| 终止 | `aborted` 或会话空闲超时 → 清理该计划未完成的临时文件，plan 转 `failed`（`upload_aborted` / `upload_abandoned`） |
| 轮询解耦 | 中转上传**不得**依赖扩展弹窗存活；弹窗关闭不中断上传循环 |

| 项 | 规定 |
| --- | --- |
| 分片上限 | 单次请求体、总大小上限的**取值只在 [03 §4.6](03-DATA.md) 定义**，本文不重复 |
| 超时 | 每次分片请求读写超时有界；会话本身不设总时长上限 |
| 内存 | 桌面端**不得**把单个分片以上的内容驻留内存 |
| 降级 | 扩展 `ReadableStream` 请求体不可用时，上限降到 **64 MB**，超限提示改用软件下载 |

> `sourceUrl` 仅用于来源展示与站点规则判定，**不落盘为可点击链接以外的形式**，且不得进入日志（B-303）。

### 2.4 错误响应

```json
{ "ok": false, "error": { "code": "plan_not_found", "message": "任务不存在" } }
```

`message` 必须可直接展示，**不含路径、堆栈或秘密**。

**HTTP 状态码与业务结果的关系见 §2.3.1**：由 Service 返回的业务码（`plan_not_found` 这类）其状态码按 §2.3.1 的**映射表**决定；请求本身的问题（参数、Origin、路径、方法、体积、未实现）用该节的**状态码表**。两者都只是"类别标签"——客户端一律按 `error.code` 分支。

---

## 3. IF-2 前端绑定（Wails 方法）

### 3.1 约定

```ts
type Result<T> =
  | { ok: true; data: T }
  | { ok: false; error: { code: string; message: string } }
```

| # | 规则 |
| --- | --- |
| B1 | 方法不得阻塞渲染超过 16 ms；耗时工作异步执行并经事件回推 |
| B2 | 不得返回秘密字段 |
| B3 | 列表方法必须有分页或硬上限 |
| B4 | 前端不得通过绑定层直接访问文件系统、进程或注册表 |

### 3.2 方法清单

**应用**

| 方法 | 返回 |
| --- | --- |
| `AppStatus` | `AppStatus` |
| `AppQuit` | — |
| `AppOpenFolder` | — |
| `AppOpenLogFolder` | — |

**计划**

| 方法 | 入参 | 返回 |
| --- | --- | --- |
| `PlansList` | `{status[], offset, limit}` | `Paged<PlanView>` |
| `PlanGet` | `id` | `PlanView` |
| `PlanCreate` | `CreatePlanRequest` | `PlanView` |
| `PlanRetry` | `id` | `PlanView` |
| `PlanStop` | `id` | `PlanView` |
| `PlanRemove` | `id` | — |
| `PlanClearTerminal` | — | `{removed:int}` |
| `PlanOpenOutput` | `id` | — |
| `PlanImport` | `id` | `PlanView` |
| `PlanCreateFromCandidate` | `{candidateId, qualityLabel, importToEagle, deleteAfterImport}` | `PlanView` |

**`PlanCreateFromCandidate` 是视频号的提交入口**：候选列表不返回媒体 URL（避免泄露签名与解密信息），用户选中后只提交 `candidateId`，由后端从**当前内存会话**取出地址、轨道与解密上下文。页内按钮与桌面候选页走**同一条**创建逻辑。

**任务**

| 方法 | 入参 | 返回 |
| --- | --- | --- |
| `JobsList` | `{status[], offset, limit}` | `Paged<JobView>` |
| `JobRetry` | `id` | `JobView` |
| `JobOpenFile` | `id` | — |
| `JobOpenSource` | `id` | — |
| `JobRemove` | `id` | — |
| `JobAttachSource` | `id, url` | `JobView` |

**媒体与站点**

| 方法 | 返回 |
| --- | --- |
| `MediaHealth` | `MediaHealth`（single-flight） |
| `PreviewGet` | `PreviewResult` |
| `SitesList` | `SiteRuleView[]` |
| `SiteSet` | `domain, enabled, includeSubdomains` → `SiteRuleView` |

**设置与缓存**

| 方法 | 返回 |
| --- | --- |
| `SettingsGet` | `SettingsView` |
| `SettingsUpdate` | `SettingsPatch` → `SettingsView` |
| `CacheStatus` | `CacheStatus` |
| `CacheClear` | `manual` / `auto` → `CacheClearResult` |

**视频号捕获**

| 方法 | 返回 |
| --- | --- |
| `CaptureState` | `CaptureState` |
| `CaptureStart` | `CaptureState` |
| `CaptureStop` | `CaptureState` |
| `CaptureCandidates` | `[]CaptureCandidateView`（**不含 URL 与解密信息**） |
| `CaptureCertStatus` | `CertStatus` |
| `CaptureCertInstall` | `CertStatus` |

**Eagle 与更新**

| 方法 | 返回 |
| --- | --- |
| `EagleStatus` | `EagleStatus` |
| `UpdateCheck` | `UpdateInfo` |
| `UpdateApply` | — |

**诊断**

| 方法 | 返回 |
| --- | --- |
| `DiagnosticsExport` | 导出路径（**经秘密过滤**） |

**幂等性**：只对**真正需要**的操作做幂等——`PlanImport` 复用同一 `jobs` 记录（B-505）、inbox 按 `file_path` 去重、`Retry` 按当前状态判断。**不引入通用幂等键**（作用域、保留期、响应缓存）；`PlanCreate` 的重复提交由前端禁止重复点击避免。

### 3.3 视图与请求类型

本节是绑定方法与 HTTP 端点**共用**的数据类型：下表即 §3.1 的 `Result<T>` 里的 `T`，也是 §2.3.2 各端点 `data` 的类型。**同一个类型不在两处各定义一份**——两端共用同一个 Service（§1.2），类型自然也只有一份。

#### 3.3.1 `Paged<T>`

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `items` | `T[]` | 当前窗口的记录 |
| `offset` | int | 与请求一致 |
| `limit` | int | 与请求一致（**不截断**：越界在 §2.3.2 的参数校验就被 `400` 拒掉） |

**不含总数**：界面是连续滚动列表、没有分页控件（B-310），算总数只会引入无界的 `COUNT`（[03 §5](03-DATA.md) 的 `Q4`）。判"还有更多"的方式：`items.length === limit`。

#### 3.3.2 `PlanView`

**除 `retryMax` 外，全部字段来自 `plans` 行（[03 §2.1](03-DATA.md)）**；派生布尔只做状态推导，**不得**引入 `plans` 没有的事实（B-313）。

| 字段 | 类型 | 来源 | 说明与硬约束 |
| --- | --- | --- | --- |
| `id` | string | `id` | 计划 ID |
| `sourceTitle` | string | `source_title` | 标题；可为空字符串 |
| `sourceUrl` | string | `source_url` | 归一化后的页面地址（[03 §4.1](03-DATA.md)）；**只在展开后展示**（B-203），**不得**进入日志（B-303）与诊断导出（B-722） |
| `mediaKind` | string | `media_kind` | 取值域见 [03 §3.3](03-DATA.md) |
| `outputName` | string | `output_name` | 经 [03 §4.4](03-DATA.md) 清洗后的输出名 |
| `outputContainer` | string | `output_container` | 取值域见 [03 §3.3](03-DATA.md) |
| `qualityLabel` | string | `quality_label` | 可读档位标签；**必须如实**——未经验证不得标为高清（B-714、B-723）；可为空字符串 |
| `status` | string | `status` | 取值域与终态判定见 [03 §3.1](03-DATA.md) |
| `phase` | string | `phase` | 取值域见 [03 §2.1](03-DATA.md)；`status = running` 时**必须非空**（[05 §2.2](05-DOWNLOAD.md) 第 5 条） |
| `phaseDetail` | string | `phase_detail` | 可直接展示的短文案；**不得**含路径、URL 或秘密（[03 §2.1](03-DATA.md)） |
| `progress` | number | `progress` | 0–100。**只有 `completed` 允许 100**（[05 §2.2](05-DOWNLOAD.md) 第 1 条、B-312）；其余状态必须 < 100 |
| `downloadedBytes` | int | `downloaded_bytes` | P6 下的口径是**桌面端已接收字节数**（[05 §4.4.4](05-DOWNLOAD.md)） |
| `totalBytes` | int \| null | `total_bytes` | `null` = **长度未知**；**不得用 `0` 表示未知**（[03 §2.1](03-DATA.md)） |
| `attemptCount` | int | `attempt_count` | 已用尝试次数；落库且**重启不重置**（B-315） |
| `retryMax` | int | [03 §2.4](03-DATA.md) 的 `retry_max` | 全表同一个值；界面据此显示「第 2/5 次」这类文案 |
| `nextAttemptAt` | number \| null | `next_attempt_at` | 下次自动尝试时间；`null` = 不等待重试 |
| `finalPath` | string \| null | `final_path` | `status = completed` 时**必须非空**（[03 §2.1](03-DATA.md)、B-309）；其余状态为 `null` |
| `previewPath` | string \| null | `preview_path` | 预览文件路径；它怎么被加载见 §7 的 `I7`（阶段 6） |
| `errorCode` | string \| null | `error_code` | `status = failed` 时**必须非空**（[03 §3.4](03-DATA.md)） |
| `errorMessage` | string \| null | `error_message` | 可直接展示的中文消息（[05 §7.4](05-DOWNLOAD.md)）；**不得**含路径、URL 或秘密 |
| `createdAt` | number | `created_at` | 单位与语义见 [03 §1](03-DATA.md) 的 `P3` |
| `updatedAt` | number | `updated_at` | 同上；每次状态写入都会更新（[05 §2.2](05-DOWNLOAD.md) 第 4 条） |
| `completedAt` | number \| null | `completed_at` | 完成时间；未完成时为 `null` |
| `canRetry` | bool | 派生 | `status = failed`（[05 §2.2](05-DOWNLOAD.md) 第 3 条：终态只能手动重试） |
| `canStop` | bool | 派生 | `status` 为**非终态**（终态集合见 [03 §3.1](03-DATA.md)） |
| `canOpen` | bool | 派生 | `status = completed`（等价于 `finalPath` 非空，B-309）。文件被外部删除时它**仍为 `true`**——界面只投影数据库真实状态（B-313），打开失败由 `/api/plan/open` 返回 `plan_file_missing` |
| `canImport` | bool | 派生 | `status = completed`（[08 §4.2](08-EAGLE-IDM.md)：任何 `completed` 的计划都可补导）。**Eagle 是否可用不在本视图里**：按钮启用与否由 `/health` 的 `eagleAvailable` 决定（B-211、B-504） |

#### 3.3.3 `CreatePlanRequest`

字段与 [05 §3.1](05-DOWNLOAD.md) 的入参**一一对应**（新增字段必须先改 [05 §3](05-DOWNLOAD.md)）。

| 字段 | 类型 | 必填 | 说明与落点 |
| --- | --- | --- | --- |
| `mediaKind` | string | 是 | 取值域见 [03 §3.3](03-DATA.md) → `plans.media_kind`；**阶段 2 只接受 `direct`** |
| `pageUrl` | string | 否 | 页面地址；按 [03 §4.1](03-DATA.md) 归一化后 → `plans.source_url`。**扩展创建时必须提供**（否则界面上没有来源可显示，B-203）；缺失时落库为空字符串 |
| `pageTitle` | string | 否 | → `plans.source_title`；上限见 [03 §4.5](03-DATA.md) |
| `outputName` | string | 是 | 服务端**必须**按 [03 §4.4](03-DATA.md) 再清洗一次（不信任调用方）→ `plans.output_name` |
| `outputContainer` | string | 是 | 取值域见 [03 §3.3](03-DATA.md)；不支持 → `invalid_container`（[05 §3.2](05-DOWNLOAD.md)） |
| `mergeMode` | string | 是 | 取值域见 [03 §3.3](03-DATA.md)；不支持 → `invalid_merge_mode`（[05 §3.2](05-DOWNLOAD.md)）；**阶段 2 必须为 `single`** |
| `streams` | array | 是 | 轨道选择（[05 §3.1](05-DOWNLOAD.md) 的「轨道选择」）；元素见下表。**阶段 2 恰好一个 `main` 轨道** |
| `qualityLabel` | string | 否 | 可读画质档位（[05 §3.1](05-DOWNLOAD.md) 的「画质档位」）→ `plans.quality_label`；无档位时为空字符串 |
| `importToEagle` | bool | 否 | → `plans.import_to_eagle`；缺省 `false`（[05 §3.1](05-DOWNLOAD.md) 的默认 0） |
| `deleteAfterImport` | bool | 否 | → `plans.delete_after_import`；缺省 `false`。`importToEagle = false` 或 Eagle 不可用时**必须**落库为 0（[05 §3.1](05-DOWNLOAD.md)、B-504） |

**`streams` 元素**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `track` | string | 是 | 轨道标识；取值域与 §2.5 的上传会话键、[03 §2.1.1](03-DATA.md) 的 `stream_plan.track` 是**同一个词**，不在此重复定义 |
| `kind` | string | 是 | 媒体类型：`video` / `audio`。`main` 轨按其主媒体类型取值（音视频同文件取 `video`，纯音频取 `audio`）——与 [03 §2.1.1](03-DATA.md) 的 `stream_plan.kind` 同一个词 |
| `url` | string | 是 | 媒体地址；**只驻留内存**（B-722、[03 §2.1](03-DATA.md)）。合法性按 [05 §3.2](05-DOWNLOAD.md) 的创建期校验 |
| `quality` | string | 否 | 该轨的画质标签；无档位时为空字符串 |
| `container` | string | 否 | 该轨的容器 / 扩展名（取值域见 [03 §3.3](03-DATA.md)）；缺省时按 `outputContainer` |
| `bytes` | int | 否 | 声明字节数（供进度与 [03 §4.6](03-DATA.md) 的上限预检用）；**未知时省略**——对应 `totalBytes = null`，不得用 `0` 表示未知 |

**服务端把 `streams` 投影为 `plans.stream_plan`**（[03 §2.1.1](03-DATA.md)）：只保留 `track` / `kind` / `quality` / `container`，**地址与字节数一律丢弃**。

#### 3.3.4 `JobView`

字段来自 `jobs` 行（[03 §2.2](03-DATA.md)）；派生布尔只做状态推导。

| 字段 | 类型 | 来源 | 说明 |
| --- | --- | --- | --- |
| `id` | string | `id` | 任务 ID |
| `planId` | string \| null | `plan_id` | `null` = 来自 IDM hook 的独立导入（[03 §2](03-DATA.md)） |
| `filePath` | string | `file_path` | 可落库、可在本地日志出现，但**不得**进入诊断导出与用户可见错误消息（[03 §6](03-DATA.md)） |
| `fileName` | string | `file_name` | 文件名 |
| `extension` | string | `extension` | 扩展名；视频扩展名集合见 [03 §3.3](03-DATA.md) |
| `status` | string | `status` | 取值域与终态判定见 [03 §3.2](03-DATA.md) |
| `sourceUrl` | string \| null | `source_url` | 无可靠来源时为 `null`——**不得猜测网址**（B-507）；展示受 B-203 约束、不得进日志（B-303） |
| `sourceTitle` | string \| null | `source_title` | 同上 |
| `fingerprint` | string \| null | `fingerprint` | 内容指纹；界面以等宽字体展示（[09 §4.5](09-UI.md)） |
| `eagleItemId` | string \| null | `eagle_item_id` | Eagle 条目 ID；未导入时为 `null` |
| `attemptCount` | int | `attempt_count` | 已用尝试次数 |
| `retryMax` | int | [08 §8.5](08-EAGLE-IDM.md) | 任务重试上限——**与 `PlanView.retryMax` 不同源**：那个取自 [03 §2.4](03-DATA.md) 的 `retry_max` |
| `nextAttemptAt` | number \| null | `next_attempt_at` | 下次尝试时间；`null` = 不等待 |
| `errorCode` | string \| null | `error_code` | `status = failed` 时**必须非空**（[03 §3.4](03-DATA.md)）；`status = skipped` 时表示跳过原因（[03 §3.2](03-DATA.md)）；取值域见 [08 §9](08-EAGLE-IDM.md) |
| `errorMessage` | string \| null | `error_message` | 可直接展示的中文消息；**不得**含路径、URL 或秘密 |
| `createdAt` | number | `created_at` | 单位与语义见 [03 §1](03-DATA.md) 的 `P3` |
| `updatedAt` | number | `updated_at` | 同上 |
| `completedAt` | number \| null | `completed_at` | 完成时间；未完成时为 `null` |
| `canRetry` | bool | 派生 | `status = failed`（[08 §8.5](08-EAGLE-IDM.md)：达上限转 `failed`） |
| `canOpenFile` | bool | 派生 | `filePath` 非空 |
| `canAttachSource` | bool | 派生 | `sourceUrl` 为空 **且** `status` 为非终态——只有这两种情况才需要附加来源（[08 §8.4](08-EAGLE-IDM.md)）。**Eagle 是否可用不在本视图里**：由 `/health` 的 `eagleAvailable` 决定（B-504） |

#### 3.3.5 `SettingsView` / `SettingsPatch`

**`SettingsView` 覆盖 `settings` 表的全部已知键**（[03 §2.4](03-DATA.md) 的 12 个）。类型、默认值与取值范围**只在 [03 §2.4](03-DATA.md) 定义**，本表只做「键 → 视图字段 → 界面呈现」的映射。

| 视图字段 | 类型 | `settings` 键（[03 §2.4](03-DATA.md)） | 桌面端界面呈现 |
| --- | --- | --- | --- |
| `theme` | string | `theme` | 外观组（[09 §3.4](09-UI.md)）；首帧注入见 B-803 |
| `proxyMode` | string | `proxy_mode` | 网络代理组 |
| `proxyUrl` | string | `proxy_url` | 网络代理组 |
| `cacheRetentionDays` | int | `cache_retention_days` | 文件管理组 |
| `downloadConcurrency` | int | `download_concurrency` | **不在桌面设置页呈现**（[09 §3.4](09-UI.md) 未列该项）；实测取值见 [13 §7.4](13-ROADMAP.md) 的 `05 N4` |
| `retryMax` | int | `retry_max` | **不在桌面设置页呈现**；它同时是 §3.3.2 的 `PlanView.retryMax` 的来源 |
| `progressThrottleMs` | int | `progress_throttle_ms` | **不在桌面设置页呈现**；实测取值见 [13 §7.4](13-ROADMAP.md) 的 `05 N3` |
| `uploadSessionLimit` | int | `upload_session_limit` | **不在桌面设置页呈现**（P6 相关，阶段 2.5） |
| `outputDir` | string | `output_dir` | **不在桌面设置页呈现**；空表示默认目录（[01 §8](01-ARCHITECTURE.md)） |
| `browserDownloadMode` | bool | `browser_download_mode` | **只在扩展弹窗的「设置」标签页呈现**（B-220）：桌面端设置页**不得**出现该开关（[09 §3.4](09-UI.md)、T-UI-08） |
| `extensionOrigin` | string | `extension_origin` | **不呈现**：它只是开发调试用的白名单覆盖值（§2.2） |
| `lastUpdateCheck` | number | `last_update_check` | 更新组（可显示上次检查时间） |

| 规则 | 说明 |
| --- | --- |
| 布尔转换 | 库里以 `INTEGER` 0/1 存的键（[03 §2.4](03-DATA.md)）在本视图与 §2.5 的 `/api/mode` 里一律是 JSON `bool`——转换只做一次 |
| `SettingsPatch` | `SettingsView` 字段的**子集**：只含要修改的键，未出现的键保持不变；未知键**拒绝**；取值校验按 [03 §2.4](03-DATA.md) 的范围 |
| 保存失败 | 必须返回 §3.1 的错误结果；界面留在当前页并保留用户输入（[09 §3.4](09-UI.md)） |
| 不含秘密 | 本视图不含任何秘密（B2、[03 §6](03-DATA.md)） |

---

## 4. IF-3 事件推送

前端**不轮询**——状态由事件驱动。

### 4.1 极简模型

**Go 是唯一权威状态，事件只负责通知「东西变了」。**

```text
状态变化
  → 发事件 planChanged(id)
  → 前端重新 PlanGet(id)
```

| 事件 | 载荷 | 前端动作 |
| --- | --- | --- |
| `planChanged` | `{id}` | 重新 `PlanGet(id)` |
| `plansChanged` | `{}` | 重新 `PlansList()`（批量变化时用） |
| `jobChanged` | `{id}` | 重新拉取任务列表 |
| `captureState` | `CaptureState` | 直接覆盖 |
| `captureCandidate` | `CaptureCandidateView` | 追加到候选列表 |
| `eagleStatus` | `EagleStatus` | 直接覆盖 |
| `settingsChanged` | `{}` | 重新 `SettingsGet()` |
| `appNotice` | `{level, code, message}` | 弹出提示 |
| `updateState` | `UpdateInfo` | 直接覆盖 |

### 4.2 进度例外

下载进度是高频变化，**不**走「通知 + 重查」（会产生大量往返）。

| 规则 | 说明 |
| --- | --- |
| 推送内容 | 每 **200 ms** 推送一次**完整** `PlanView` |
| 前端处理 | 直接覆盖本地该条记录 |
| 为什么不需要版本号 | 推送由同一 goroutine 串行发出，后到的必然更新 |
| 终态 | **立即**推送一次，不受 200 ms 节流限制 |

### 4.3 乱序防护

「通知 + 重查」唯一的风险是**同一个 id 的两次 `PlanGet` 并发返回乱序**。

防护放在前端本地，不涉及后端：

```text
为每个 id 维护本地请求序号
发起 PlanGet 时 seq++
返回时若 seq < 最新序号 → 丢弃
```

这比后端维护实体版本号简单得多，完全够用。

### 4.4 启动与重连

前端启动、窗口重新聚焦或断线重连时，直接调用 `PlansList()` 对齐。

**明确不做**：实体版本号、`plans:sync` 事件、后端版本比较、终态强制二次回查、事件重放与可靠性协议。

> **理由**：事件不是数据库。它只负责告诉 UI「东西变了」——权威状态永远在 Go 侧。

---
## 5. IF-4 捕获进程协议

### 5.1 启动

| 项 | 规定 |
| --- | --- |
| 启动者 | 主进程，仅在用户明确开始捕获时 |
| 参数 | `--capture-port`、`--cert-dir`、`--parent-pid` |
| 通信 | **stdin/stdout 行分隔 JSON**；stderr 仅日志 |
| 就绪 | 子进程输出 `{"event":"ready"}` 后父进程才开始改代理 |
| 失败 | 超时未就绪 → 父进程终止子进程并中止，**不改代理** |

### 5.2 方法（父 → 子）

| 方法 | 说明 |
| --- | --- |
| `start` | 装载证书、监听端口，就绪后输出 `ready`。**不改写系统代理**——代理由父进程在收到 `ready` 后设置 |
| `stop` | 停止捕获并恢复代理 |
| `state` | 查询状态 |
| `shutdown` | 优雅退出 |

### 5.3 事件（子 → 父）

| 事件 | 说明 |
| --- | --- |
| `ready` | 就绪 |
| `state` | 状态变化 |
| `candidate` | 候选上报（**允许携带内存中的媒体信息**，父进程不得落盘） |
| `feed` | 当前绑定上报 |
| `diagnostic` | 计数与错误（**无秘密**） |
| `fatal` | 不可恢复错误 |

### 5.4 生命周期

```text
父进程退出      → 子进程 stdin EOF → 自行退出并恢复代理
子进程崩溃      → 父进程 read 失败 → 父进程恢复代理
子进程挂死      → 父进程超时后强杀 → 父进程恢复代理
```

**代理设置与恢复的唯一协调者是主进程。**

| 时机 | 负责方 | 动作 |
| --- | --- | --- |
| 正常启动 | 主进程 | 收到子进程 `ready` 后，保存快照并写入系统代理 |
| 正常停止 | 主进程 | 收到 UI/托盘停止指令后，通知子进程退出，由主进程恢复代理并删除恢复文件 |
| 子进程崩溃 | 主进程 | read 失败即触发恢复 |
| **父进程消失** | **子进程（兜底）** | 检测 stdin EOF 后自行恢复代理，再退出 |

子进程兜底时读取与父进程**同一份** `proxy-restore.json`，沿用同样的值比对规则。恢复文件的删除由**最后一个持有者**执行：正常情况下是主进程；兜底情况下是子进程。

---

## 6. IF-5 IDM hook

### 6.1 调用

```text
IdmEagleHook.exe <文件路径...>
```

| 项 | 规定 |
| --- | --- |
| 动作 | 每个路径写一个 `inbox/<时间戳>-<随机>.json`，发命名事件 `Local\LiudiWake`，退出 |
| 禁止 | 不碰数据库、不发网络请求、不做哈希、不等待文件稳定 |
| 返回 | 必须**立即返回**（进程总耗时 **≤ 100 ms**），不得因主程序未运行而失败 |
| 幂等 | 重复调用不产生重复任务（由主进程按 `file_path` 去重） |

### 6.2 inbox 文件格式

```json
{ "path": "D:\\Downloads\\video.mp4", "received_at": 1758921234.5 }
```

主进程启动时与运行时都消费该目录，处理完删除文件。

---

## 7. 待确认

| # | 项 | 何时确认 |
| --- | --- | --- |
| ~~I1~~ | ~~每端点的请求/响应 schema~~ **已定**：阶段 2 端点的完整 schema 见 **§2.3.2**；其余端点按 §2.3 的「schema 落点」列在对应阶段回填；通用约定（响应外壳、时间、状态码）见 **§2.3.1** | 已解决（阶段 2 开工前） |
| ~~I2~~ | ~~`PlanView` / `JobView` / `SettingsView` 字段~~ **已定**：三者与 `Paged<T>`、`CreatePlanRequest` 见 **§3.3** | 已解决（阶段 2 开工前） |
| ~~I3~~ | ~~端口发现与备用端口的具体机制~~ **已定**：主端口 `47652`；被占用时回退临时端口；实际端口写入 `%LOCALAPPDATA%\LiudiDownloader\api-port.json`（内容含 `port` 与 `pid`，扩展按 `pid` 校验进程仍在再用）——权威定义见 [01 §2.1.1](01-ARCHITECTURE.md) 的 `S4`，本文不复制 | 已解决（阶段 1 提前） |
| ~~I4~~ | ~~`/api/source` 的事件类型全集~~ **已定**：取值、含义与抑制语义见 **§2.3.2** 的 `/api/source` | 已解决（阶段 2 开工前） |
| ~~I5~~ | ~~健康接口的能力字段全集~~ **已定**：`service` / `version` / `apiProtocol` / `eagleAvailable` / `mediaToolsReady` 的判据见 **§2.3.2** 的 `GET /health` | 已解决（阶段 2 开工前） |
| I7 | 预览资源的加载方式（路径还是流） | 阶段 6 |
