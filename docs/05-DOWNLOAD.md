# 05 · 下载引擎

本文定义计划状态机、下载路径、校验规则、错误码与恢复语义。

---

## 1. 职责与边界

| 项 | 规定 |
| --- | --- |
| 负责 | 计划持久化、路径选择、下载、合并、校验、交付、失败处理 |
| 不负责 | 候选发现（扩展/捕获）、Eagle 协议细节（见 08）、系统代理设置（见 06） |
| 并发 | 受全局信号量约束；用户可见任务优先于后台工作 |
| 取消 | 每个计划执行体持有 `context.Context` |
| 写入 | 直接使用 SQLite 事务；高频进度写入做节流，节流参数见 [03 §2.4](03-DATA.md) 的 `progress_throttle_ms` |

**核心原则**：未通过校验的输出**永不**交付；无法证明归属的文件**永不**删除。

---

## 2. 计划状态机

状态取值见 [03 §3.1](03-DATA.md)。

```text
              ┌──────────┐
              │  queued  │◄──── 重试到期 / 手动重试
              └────┬─────┘
                   ▼
             ┌───────────┐      phase：
             │  running  │      downloading → merging → validating
             └─────┬─────┘
                   │
        ┌──────────┼──────────┐
        ▼          ▼          ▼
   completed    failed    canceled
    （终态）    （终态）    （终态）
```

下载完成后**不再有导入相关状态**。需要导入时由 `jobs` 承接（见 [08](08-EAGLE-IDM.md)）。

### 2.1 转换规则

| 从 | 到 | 条件 |
| --- | --- | --- |
| `queued` | `running` | 到达 `next_attempt_at`，或首次调度 |
| `running` | `queued` | 可重试失败：`attempt_count` +1，写入 `next_attempt_at` |
| `running` | `completed` | 校验通过并交付文件 |
| `running` | `failed` | 不可重试错误，或重试耗尽 |
| 任意非终态 | `canceled` | 用户停止 |

**`phase` 只在 `running` 内推进，不改变 `status`**。序列**依下载路径而定**——把它写死成一条链会与实现冲突：

| 路径 | 序列 | 为什么是这个顺序 |
| --- | --- | --- |
| P1 直链（阶段 2） | `downloading` → `validating` → `merging` | 单轨没有 `streamcopy` 这一步。而"校验是交付的**唯一门槛**"（§1），所以校验必须排在交付之前；这里的 `merging` 表示"把**已校验**的字节交付进「已完成」" |
| P3 分离音视频（阶段 3） | `downloading` → `merging` → `validating` | 先 `streamcopy` 合并双轨，再校验合并后的成品 |

### 2.2 硬性约束

1. 只有 `completed` 允许 `progress = 100`。
2. 用户在校验阶段停止后，**完成状态不得覆盖取消状态**（B-308）。
3. `completed` / `failed` / `canceled` 是终态：不再自动调度；`failed` 只能手动重试。
4. 每次状态写入同时更新 `updated_at`。
5. `status = running` 时 `phase` 必须有值。
6. **下载与导入互不阻塞**：`plan` 完成后即为终态，导入失败**不回退** `plan` 状态。

---
## 3. 创建计划

### 3.1 入参

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| 媒体地址或页面地址 | 是 | 按 `media_kind` 解释 |
| `media_kind` | 是 | `direct` / `hls` / `dash` / `page` / `wechat` / `browser`（P6，见 §4.4） |
| 输出名 | 是 | 经 [03 §4.4](03-DATA.md) 清洗 |
| 输出容器 | 是 | 取值域见 [03 §3.3](03-DATA.md) |
| `merge_mode` | 是 | `single` / `av` / `subtitles` |
| 轨道选择 | 是 | 至少一个可用媒体流 |
| `import_to_eagle` | 是 | 默认 0。**不改变 plan 状态**——勾选时在 plan 完成后创建一条 `jobs` 记录 |
| `delete_after_import` | 是 | 默认 0；`import_to_eagle = 0` 时必须为 0 |
| 画质档位 | 否 | 视频号专用 |
| 会话上下文 | 否 | 视频号专用；**仅内存** |

### 3.2 创建期校验

以下情况必须**拒绝且不落库**：

| 校验 | 错误码 |
| --- | --- |
| 媒体地址格式非法 | `invalid_url` |
| 目标为主机本机或内网地址 | `blocked_local_target` |
| `blob:` 地址 | `blob_not_downloadable` |
| 固定字节分片 | `fixed_range_fragment` |
| 合并方式不支持 | `invalid_merge_mode` |
| 输出容器不支持 | `invalid_container` |
| 缺少音视频内容 | `missing_media` |
| 视频号档位非法 | `wechat_spec_invalid` |
| 视频号解密键非法 | `wechat_key_invalid` |
| 视频号原画不可验证 | `wechat_original_unverifiable` |

---

## 4. 下载路径

| # | 路径 | 适用 | 实现要点 |
| --- | --- | --- | --- |
| P1 | 受控 HTTP 直链 | 单个 mp4 / 音频 | Range 支持、断点、字节校验 |
| P2 | FFmpeg 清单 | HLS / DASH | FFmpeg 读清单；流索引选择见 §5.3 |
| P3 | 分离音视频合并 | DASH 双轨 | 分别获取后 `streamcopy` 合并（B-302） |
| P4 | 页面解析 | yt-dlp + Deno | 见 §4.2 |
| P5 | 视频号 | 前缀解密 + 封装 | 见 [06](06-WECHAT.md) |
| **P6** | **浏览器中转** | 浏览器下载模式（B-220） | 见 §4.4；字节来自扩展上传，**桌面端不出站** |

**P6 共享什么、不共享什么**：

| 环节 | 是否与 P1–P4 共用 |
| --- | --- |
| 合并（`streamcopy`） | **共用** |
| FFprobe 校验（§6） | **共用** |
| 去重（B-506） | **共用** |
| 交付与落盘（§11） | **共用** |
| **自动重试（§8）** | **不共用**——见 §4.4.3，P6 失败一律转 `failed`，由扩展重发 |
| **取消（§10）** | **不共用**——取消要经会话关闭 + `plan_state_changed` 下达到浏览器 |

这是选择方案 A 而不是「浏览器下载到用户下载目录再交路径」的主要理由——**后者连合并都做不到**（软件不能动用户目录的文件，B-401）。

### 4.1 直链（P1）

| 要求 | 规定 |
| --- | --- |
| 进度 | 基于 `downloaded_bytes / total_bytes`；无 `Content-Length` 时用阶段语义，**不伪造百分比** |
| 断点 | 支持 `Range`；服务器不支持时从头下载 |
| 状态码 | 200 全量、206 分段、416 范围无效时回退全量 |
| 磁盘满 | 报 `disk_full`，清理本次临时产物，状态转 `failed`（不自动重试，释放空间后可手动重试） |
| 重定向 | **只允许同源跟随**——scheme + host + **port** 三者都相同；跨源一律**中止并报错**。**不得**依赖 `net/http` 的默认检查：它只比主机名、**忽略端口**，于是 `127.0.0.1:8080 → 127.0.0.1:9090` 会把 `Authorization` / `Cookie` 原样带走（B-304 的落点）。选"报错"而不是"剥离凭据后继续跟随"：后者会让「地址其实指向别处」被静默降级掩盖，而那种情况更可能是链接被改写，值得可见地失败（README 设计原则：不静默降级） |

### 4.2 页面解析（P4）

| 要求 | 规定 |
| --- | --- |
| 工具 | 固定版本 yt-dlp + Deno |
| 凭据 | 页面 Cookie / Authorization **只用于解析** |
| 禁止 | 不得把页面凭据转发给**不同主机**的媒体 CDN（B-304） |
| 请求头预算 | 传给 FFmpeg 的 HTTP 头总量不得超过 **6 KB**（沿用旧项目实测值），超限报 `headers_too_large` |
| 传递方式 | 经受限临时文件或 stdin，**禁止**放命令行 |
| 失败码 | `page_resolve_failed`、`page_media_unavailable` |

### 4.3 字幕

| 要求 | 规定 |
| --- | --- |
| 大小上限 | 单字幕 **50 MB**，超限报 `subtitle_too_large` |
| 扩展名 | 见 [03 §3.3](03-DATA.md) |
| 失败码 | `subtitle_download_failed` |

### 4.4 浏览器中转（P6）

**触发**：计划的创建来自浏览器下载模式（`webpage` 入口）。**媒体地址由扩展持有，不交给桌面端出站请求。**

**4.4.1 扩展端职责**

| 项 | 规定 |
| --- | --- |
| 取字节 | 用**页面的凭据模式**自行 `fetch`（携带该站点的 Cookie），不再拦截内容脚本的请求 |
| 范围请求 | 被中断后先用 `Range` 尝试续取；服务端不支持时整轨重取 |
| 上传 | 按 [04 §2.5](04-INTERFACES.md) 的顺序分片；`offset` 以桌面端返回值为准 |
| 进度 | 以**已上传字节 / 声明总字节**估算，只用于扩展弹窗内的过程显示 |
| 结束 | 完成后发 `DELETE ... complete`；用户取消发 `DELETE ... aborted` |
| 禁止 | 扩展对中转文件**不得**做哈希、去重或格式校验——这些只在工具链中做 |

**4.4.2 桌面端职责**

| 项 | 规定 |
| --- | --- |
| 落盘 | 分片顺序追加到 `temp/<plan_id>/`（§11），**不整文件驻留内存** |
| 合并 | 与 P3 共用同一实现：轨道到齐后 `streamcopy` 合并（B-302） |
| 校验 | 与 P1–P4 共用 FFprobe 校验（§6），**不得**因为字节来自本机而放宽 |
| 去重 | 与 P1–P4 共用同一条去重判定（B-506）；命中时保留文件|
| 出站 | **不发起**该媒体的出站请求（这正是本模式的目的） |

**4.4.3 失败与取消**

| 情况 | 处置 |
| --- | --- |
| 扩展报告 `aborted` | 清理该计划临时文件，plan 转 `failed`，错误码 `upload_aborted` |
| 空闲超时（无分片到达） | 同上，错误码 `upload_abandoned` |
| `complete` 时字节数不足或超额 | 保留临时文件，错误码 `upload_incomplete`，允许重发缺失分片 |
| 用户点停止 | 桌面端先置 `canceled` 并关闭上传会话，再忽略后续分片（B-308 优先于完成回调） |
| 超过大小上限 | 立即拒绝 **且不写入任何字节**，错误码 `browser_mode_size_exceeded`，扩展在弹窗内提示改用软件下载 |
| **磁盘满** | 与 P1 一致：停止接收、清理本次临时产物、plan 转 `failed`（`disk_full`），并返回该错误码让扩展**停止上传循环**（否则它会持续 POST 且每次都被拒）。提示「释放空间后在扩展中重试」 |
| 浏览器与软件同时可用但模式为开启 | 仍走 P6；用户要出站下载就关掉开关 |

**P6 的失败不自动重试**——与 P1–P5 的关键差异：

| 项 | 说明 |
| --- | --- |
| 为什么 | P1–P5 的重试是"桌面端重新出站取字节"，而 P6 的字节在浏览器侧，**桌面端没有取字节的途径**。把它送回 `queued` 只会空转或丢掉已收字节 |
| 因此 | §4.4.3 的所有失败码（含合并/校验阶段的 `output_*`、`disk_full`）**都不进入自动重试**，一律转 `failed`，由扩展侧重新发起上传 |
| 与 §8 的关系 | §8 的自动重试只适用于 P1–P5。P6 共用的是**合并与校验的实现**，不是重试语义 |

**取消如何下达到浏览器**（P1–P5 可直接取消 context 或杀子进程，P6 不行——取字节的循环在浏览器进程里）：

| 步骤 | 规定 |
| --- | --- |
| 1 | 桌面端置 `canceled`，关闭该上传会话 |
| 2 | 该 `planId` 之后的 `PUT` / `POST` **一律返回 `plan_state_changed`** |
| 3 | 扩展收到该错误码即**终止浏览器侧 fetch 与上传循环**，不再重发（弹窗内提示「任务已停止」） |

**4.4.4 进度**

浏览器模式下 `downloaded_bytes` 的口径是**桌面端已接收的字节数**，进度事件与节流规则不变（[04 §4.2](04-INTERFACES.md)）。扩展自己算的进度**不得**写入数据库。

### 4.5 大小上限与代价

中转上传的字节要经回环两趟（浏览器读一次、桌面端写一次），因此必须有上限，**超限立即拒绝而不是先传一半**。

| 项 | 规定 |
| --- | --- |
| 常量出处 | 分片、单轨道、单计划三个上限的**取值只在 [03 §4.6](03-DATA.md) 定义**，本文不重复 |
| 预检 | 扩展端用声明值预检，桌面端**复核**；两处判定引用同一组常量 |
| 拒绝语义 | 超限**不写入任何字节**，错误码 `browser_mode_size_exceeded` |
| 为什么单轨道也有上限 | 该文件必须先完整存在于浏览器侧，浏览器自身有内存与 blob 上限 |

> **代价必须如实告知用户**：浏览器模式下的媒体要经扩展中转，大文件比直连慢、且占用浏览器内存。这是为绕过严格链接握手付出的代价，不是缺陷。告知方式由弹窗设置页的 ⓘ 承载（[09 §4.8](09-UI.md)）。

---

## 5. 外部工具与 FFmpeg

### 5.1 工具解析

按固定顺序返回**绝对路径**：

1. 环境变量显式指定
2. 程序目录下 `media-tools/`
3. 冻结运行时内相对路径
4. 系统 `PATH`
5. 均失败 → 明确错误，**不得**回退到网络下载

### 5.2 调用规范

| 规则 | 说明 |
| --- | --- |
| 参数 | 必须用参数数组，禁止拼接 shell 字符串 |
| 超时 | 每次调用有超时与 `context` 取消。**已定值**（阶段 2 落地）：单次调用上限 **10 分钟**、强杀后的回收等待 **10 s**；另：重定向跟随上限 **16 跳** |
| 输出 | stdout/stderr 捕获且有上限，超限截断 |
| 退出码 | 非零映射为稳定内部错误码，不把原始 stderr 直接给用户 |
| 终止 | 用户停止时先优雅终止再强杀，并等待回收 |
| 并发 | 受全局信号量约束 |

### 5.3 清单流选择

| 步骤 | 要求 |
| --- | --- |
| 探测 | 用 FFprobe 探测清单内各流 |
| 选择 | 按用户所选画质/音轨匹配流索引 |
| 回退 | 无匹配时报错，**不得**静默选默认流 |
| 字幕 | 单独下载，不并入主媒体（除非用户选择内嵌） |

---

## 6. 输出校验

校验是交付的**唯一门槛**。

| 校验 | 失败码 |
| --- | --- |
| FFprobe 无可用流 | `output_no_streams` |
| 缺少所选视频流 | `output_no_video` |
| 缺少所选音频流 | `output_no_audio` |
| 时长与预期不符 | `output_duration_mismatch` |
| 视频号分辨率不符（原画） | `wechat_original_resolution_mismatch` |
| 视频号分辨率不符（明确档） | `wechat_quality_resolution_mismatch` |
| 视频号字节数与声明不符 | `wechat_size_mismatch` |
| 视频号下载不完整 | `wechat_incomplete` |

### 6.1 时长容差

时长校验必须带容差，阈值必须在**阶段 3** 用真实样本测定（见 §12 `N1`），**不得凭猜测写死**。

### 6.2 分辨率校验

- 原画档：`max(w,h) > 1280` 且 `min(w,h) > 720`（B-713）。
- 明确档：必须与所选档一致，横竖屏可互换。
- 不一致 → 删除结果并报错，**不得降级交付**（B-716、B-717）。

---

## 7. 错误码

### 7.1 可重试（自动退避）

`download_failed` · `page_resolve_failed` · `subtitle_download_failed` · `wechat_download_failed` · `eagle_import_error`

### 7.2 不可重试（直接 `failed`）

`invalid_url` · `blocked_local_target` · `blob_not_downloadable` · `fixed_range_fragment` · `invalid_merge_mode` · `invalid_container` · `missing_media` · `headers_too_large` · `subtitle_too_large` · `wechat_spec_invalid` · `wechat_key_invalid` · `wechat_original_unverifiable` · `output_no_streams` · `output_no_video` · `output_no_audio` · `output_duration_mismatch` · `wechat_original_resolution_mismatch` · `wechat_quality_resolution_mismatch` · `wechat_size_mismatch` · `wechat_incomplete` · `disk_full` · `upload_incomplete` · `upload_aborted` · `upload_abandoned` · `browser_mode_size_exceeded` · `upload_offset_mismatch` · `context_expired`

### 7.3 计划与文件

`plan_not_found` · `plan_not_retryable` · `plan_state_changed` · `plan_file_missing` · `plan_file_not_owned` · `plan_not_importable` · `output_name_exhausted` · `open_folder_unavailable`

### 7.4 要求

每个错误码必须提供**可安全展示的中文消息**，不得包含路径、URL 或秘密。

---

## 8. 重试与失败

| 项 | 规定 |
| --- | --- |
| 上限 | 见 [03 §2.4](03-DATA.md) 的 `retry_max`（默认 **5** 次） |
| 退避 | `min(30 × 2^(attempt-1), 1800)` 秒 |
| 持久化 | `attempt_count` 与 `next_attempt_at` 落库，**重启不重置**（B-315） |
| 耗尽 | 状态转 `failed`，不再自动调度 |
| 不可重试 | 直接 `failed`，`attempt_count` 不再增加 |
| 用户可见 | 失败必须显示档位与 HTTP/网络摘要（不含秘密） |

---

## 9. 中断恢复

启动时必须：

1. 找出所有 `running` 状态的计划。
2. 按上下文可恢复性决定：
   - 可重试 → `queued`，`next_attempt_at = now`
   - 上下文不可恢复（如视频号会话失效）→ `failed`，错误码 `context_expired`
3. 清理程序创建的临时产物（**仅**有明确归属的）。
4. **不得**删除 `completed` 计划的文件。

**另外必须判断等待中的计划**：`queued` 与等待重试的计划可能依赖**只存在于内存**的上下文（页面会话 Cookie、视频号解密信息、一次性签名地址）。重启后逐项判断：

| 情况 | 处置 |
| --- | --- |
| 上下文可重建（直链、可重新解析的页面） | 继续调度 |
| 上下文不可恢复（视频号会话、一次性签名 URL） | 置为 `failed`，错误码 `context_expired`，提示「请重新创建任务」 |
| 无法判断 | 保守置为 `failed` 并提示，**不得**让任务空转重试 |
| **浏览器中转未完成的轨道**（存在 `temp/<plan_id>/` 且未走完 `complete`） | 置为 `failed`，错误码 `context_expired`，**保留**已接收字节并对齐扩展记录，提示「请在扩展中重试」；扩展重试时经 `PUT` 取回偏移后**从已接收处继续**（[04 §2.5](04-INTERFACES.md)） |

---
## 10. 取消与停止

| 场景 | 要求 |
| --- | --- |
| 用户点击停止 | 取消 `context`，优雅终止子进程，再强杀 |
| 停止后状态 | `canceled`，**不得**被后续完成回调覆盖（B-308） |
| 产物清理 | 只清理程序创建且有归属的临时文件 |
| 已完成计划 | 停止不影响文件 |

---

## 11. 文件与目录

```text
%USERPROFILE%\Downloads\留底下载器\
├── 临时\<plan_id>\             程序临时分片（有任务归属）
│   ├── main.bin                P6 单轨中转
│   ├── video.bin / audio.bin   P6 双轨中转
│   └── ...                     P1–P5 的中间产物
├── 预览\<plan_id>.jpg          任务预览
└── 已完成\<输出名>             最终交付副本
```

| 规则 | 说明 |
| --- | --- |
| 归属 | `临时` 与 `预览` 由程序管理；`已完成` 不被缓存清理触碰（B-405） |
| 同名 | 必须原子创建；不允许"先判断不存在再覆盖写"。冲突时生成唯一名，过多则报 `output_name_exhausted` |
| 空目录 | 计划结束后清理自己创建的空目录 |
| 原文件 | 用户与 IDM 文件**永不**移动、删除或修改（B-401） |
| **中转文件** | P6 的字节由扩展上传、由**程序自己**写入 `临时`，因此满足 B-404 的归属要求；用户下载目录**不受影响** |

---

## 12. 待确认

| # | 项 | 何时确认 |
| --- | --- | --- |
| N1 | 时长校验容差的实测阈值 | 阶段 3 |
| N2 | 预览生成的尺寸与格式 | 阶段 3 |
| N3 | 进度写库的节流参数 | 阶段 3 |
| N4 | 下载并发默认值（当前设 3） | 阶段 3 |
| N5 | 字幕内嵌的 FFmpeg 参数 | 阶段 3 |
| N6 | 是否存在遗漏的错误码 | 阶段 3 |
| N7 | P6 分片大小与空闲超时的实测取值（当前 1 MB / 10 分钟） | 阶段 3 |
| N8 | P6 大小上限（当前单轨 2.0 GiB、单计划 8.0 GiB）的实测校核 | 阶段 3 |
