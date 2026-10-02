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

| 端点 | 用途 |
| --- | --- |
| `/health` | 健康与能力（含 `eagleAvailable`），免 Origin 校验 |
| `/api/plans` | 计划列表 |
| `/api/plan` | 单个计划详情 |
| `/api/jobs` | 导入任务列表 |
| `/api/sites` | 站点规则列表 |
| `/api/preview` | 任务预览图 |
| `/api/mode` | 读取浏览器下载模式（§2.5） |

**POST**

| 端点 | 用途 |
| --- | --- |
| `/api/plan` | 创建下载计划 |
| `/api/plan/retry` | 重试 |
| `/api/plan/stop` | 停止 |
| `/api/plan/remove` | 删除记录 |
| `/api/plan/open` | 打开文件或所在目录 |
| `/api/plan/import` | 补导 Eagle |
| `/api/sites` | 设置站点规则 |
| `/api/source` | 上报来源事件 |
| `/api/mode` | 设置浏览器下载模式（§2.5） |

**PUT / DELETE**

| 端点 | 用途 |
| --- | --- |
| `/api/upload` | 中转上传的开始与结束（§2.5） |

> 完整请求/响应 schema 在阶段 2 回填（见 §7）。

**两条边界**：

| 项 | 规定 |
| --- | --- |
| 站点适配器 | **不通过本 API 分发**。扩展使用包内构建副本（[14 §4](14-SITE-ADAPTERS.md)），因此桌面端未安装时扩展仍能发现候选 |
| 轮询频率 | 见 [07 P-108](07-EXTENSION.md)——本文不重复其取值 |

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
| I1 | 每端点的请求/响应 schema | 阶段 2 |
| I2 | `PlanView` / `JobView` / `SettingsView` 字段 | 阶段 2 |
| ~~I3~~ | ~~端口发现与备用端口的具体机制~~ **已定**：主端口 `47652`；被占用时回退临时端口；实际端口写入 `%LOCALAPPDATA%\LiudiDownloader\api-port.json`（内容含 `port` 与 `pid`，扩展按 `pid` 校验进程仍在再用）——权威定义见 [01 §2.1.1](01-ARCHITECTURE.md) 的 `S4`，本文不复制 | 已解决（阶段 1 提前） |
| I4 | `/api/source` 的事件类型全集 | 阶段 2 |
| I5 | 健康接口的能力字段全集 | 阶段 2 |
| I7 | 预览资源的加载方式（路径还是流） | 阶段 6 |
