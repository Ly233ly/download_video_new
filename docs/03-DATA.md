# 03 · 数据规格

本文定义持久化结构、取值域与输入校验。

**核心决定**：全新库，结构版本 `2`，**不做任何历史数据迁移**——旧实现（v6）的数据不再读取（[ADR-004](adr/ADR-004-no-migration.md)）。

**结构版本与迁移**（`P6` 的落点）：结构版本记在 SQLite 内置的 `PRAGMA user_version`（不占业务表）。**本程序自身的结构变更只允许通过一次性迁移步骤完成**：每个版本号对应一组有序 DDL，例如 v2 为 `plans` 增加 `resolved_kind`（§2.1）。打开库时的处置：

| `user_version` | 处置 |
| --- | --- |
| `0`（新库） | 按**当前** DDL 建表，写入当前版本 |
| `0 < v < 当前` | 按序执行 `v → 当前` 的每一步迁移，再写入当前版本 |
| `= 当前` | 不做任何事 |
| `> 当前` | **拒绝打开**并提示程序版本过旧（[01 §5.1](01-ARCHITECTURE.md)） |

迁移步骤**只做结构变更**（加列、加表、加索引），**不得**用启动时的大范围 `UPDATE` 改写业务语义（`P6`）。**这与 ADR-004 不冲突**：ADR-004 否决的是「沿用旧实现的 v1→v6 迁移链」，本程序自身的一次性结构迁移是 `P6` 的要求。代价是**回滚到更早的程序版本会被拒绝打开**（因为库版本更高）——这是刻意选择的「宁可拒绝，也不读错结构」。

---

## 1. 原则

| # | 原则 |
| --- | --- |
| P1 | 唯一持久化引擎为 SQLite，驱动 `modernc.org/sqlite`（纯 Go，无 CGO） |
| P2 | 直接使用 SQLite 事务 + WAL + `busy_timeout`。**第一版不引入单写者队列**——写入量很小（建计划、改进度、改状态、存设置），SQLite 自身足够；将来实测出写竞争再加（**唯一出处**；[01 §4](01-ARCHITECTURE.md) 只引用不重复） |
| P3 | 时间统一为 **REAL 类型的 Unix 秒**（含小数） |
| P4 | 布尔统一为 `INTEGER` + `CHECK (x IN (0,1))` |
| P5 | 状态与枚举为 `TEXT` + `CHECK` 约束，取值域见 §3 |
| P6 | 结构变更只允许通过一次性迁移，禁止用启动时大范围 `UPDATE` 代替（规则见文首「结构版本与迁移」） |
| P7 | 秘密永不落盘，见 §6 |
| P8 | 所有列表查询必须有索引支撑且有 `LIMIT`（**唯一出处**；[01 §7](01-ARCHITECTURE.md) 只引用不重复） |

---

## 2. 表结构

共 **5 张表**。

**表间关系**：`jobs.plan_id → plans.id`（可空、唯一）。

| 关系 | 含义 |
| --- | --- |
| `plan_id` 非空 | 该导入任务来自某个下载计划（Eagle 补导） |
| `plan_id` 为空 | 来自 IDM hook 的独立导入 |

**方向是刻意的**：`plans` 只管下载，**完全不知道 `jobs` 的存在**；`jobs` 单向引用 `plans`。这使删除计划、删除任务、IDM 导入与补导的逻辑都更自然。

### 2.1 `plans` — 下载计划

```sql
CREATE TABLE IF NOT EXISTS plans (
    id                  TEXT PRIMARY KEY,
    source_url          TEXT NOT NULL DEFAULT '',
    source_title        TEXT NOT NULL DEFAULT '',
    media_kind          TEXT NOT NULL
                        CHECK (media_kind IN ('direct','hls','dash','page','wechat','browser')),
    resolved_kind       TEXT
                        CHECK (resolved_kind IS NULL OR resolved_kind IN
                               ('direct','hls','dash','page','wechat','browser')),
    output_name         TEXT NOT NULL,
    output_container    TEXT NOT NULL
                        CHECK (output_container IN ('mp4','mkv','webm','m4a','mp3','ts')),
    merge_mode          TEXT NOT NULL
                        CHECK (merge_mode IN ('single','av','subtitles')),
    quality_label       TEXT NOT NULL DEFAULT '',
    stream_plan         TEXT NOT NULL DEFAULT '[]',
    import_to_eagle     INTEGER NOT NULL DEFAULT 0 CHECK (import_to_eagle IN (0,1)),
    delete_after_import INTEGER NOT NULL DEFAULT 0 CHECK (delete_after_import IN (0,1)),
    status              TEXT NOT NULL CHECK (status IN (
                            'queued','running','completed','failed','canceled')),
    phase               TEXT NOT NULL DEFAULT ''
                        CHECK (phase IN ('','downloading','merging','validating')),
    progress            REAL NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    downloaded_bytes    INTEGER NOT NULL DEFAULT 0,
    total_bytes         INTEGER,
    phase_detail        TEXT NOT NULL DEFAULT '',
    final_path          TEXT,
    preview_path        TEXT,
    attempt_count       INTEGER NOT NULL DEFAULT 0,
    next_attempt_at     REAL,
    error_code          TEXT,
    error_message       TEXT,
    created_at          REAL NOT NULL,
    updated_at          REAL NOT NULL,
    completed_at        REAL
);

CREATE INDEX IF NOT EXISTS idx_plans_status   ON plans(status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_plans_updated  ON plans(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_plans_created  ON plans(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_plans_due      ON plans(next_attempt_at)
    WHERE status = 'queued';
```

**说明**：

- `source_url` 为归一化后的页面地址（规则见 §4.1），**不含追踪参数**。
- `stream_plan` 为 JSON 数组，描述所选轨道与质量档位，**不得包含签名 URL 或解密键**；精确结构见 §2.1.1。
- `media_kind` 是**调用方的路径提示**，`resolved_kind` 是**实际执行的路径**（规则见 [05 §4.0](05-DOWNLOAD.md)）。`resolved_kind` 为 `NULL` 表示尚未确定（计划还没开始执行），它在执行开始时与 `status = 'running'` **同事务**写入。两者不同即说明发生了降级——界面据此显示实际路径（B-316）。
- **本表没有媒体地址列，也不得新增**：媒体 URL 与签名参数禁止进入数据库（B-722、§6）。创建计划时请求体里的地址只由 Service 持在**内存**，仅用于本次执行；落库的只有归一化后的**页面**地址（`source_url`，规则见 §4.1）与标题。
- **重启后的处置**（[05 §9](05-DOWNLOAD.md)）：阶段 2 的地址来自扩展发现的直链，**可能是带一次性签名参数的 CDN 链接**，无法保证重启后仍然有效。因此启动时**未完成的计划**——`running`，以及 `queued` 与等待重试的计划——一律按「上下文不可恢复」处理：置为 `failed`，错误码 `context_expired`，提示「请重新创建任务」。这就是 [05 §9](05-DOWNLOAD.md) 的「无法判断 → 保守置为 `failed`，**不得**让任务空转重试」在阶段 2 的落点。**它与该节第一行（"直链可重建 → 继续调度"）不冲突**：那一行要求上下文**能被证明**可重建，而阶段 2 的直链在重启后既没有地址、也无从判断签名是否过期，拿不到这个证明。阶段 3 起，某条路径若**能自行证明**可重建（可重新解析的页面、清单地址），才按该节第一行继续调度——**默认仍是 `context_expired`**。
- `attempt_count` / `next_attempt_at` 支撑 B-315（重试计数持久化）。
- `final_path` / `preview_path` 是程序自身产物路径，允许持久化。
- **`total_bytes` 为 `NULL` 表示"长度未知"**（服务端未给 `Content-Length`），**不得用 `0` 表示未知**——`0` 是"长度为零"，两者语义不同。
- **`status = 'completed'` 时 `final_path` 必须非空**（界面据此显示路径与入口，B-309）。写入与校验必须同事务完成，不允许出现"已完成但没有路径"的记录。
- `phase_detail` 只允许存放**可直接展示的短文案**（如「正在合并音视频」），**不得包含路径、URL 或秘密**。
- `error_code` 的取值域见 §3.4；`status = 'failed'` 时该字段必须非空。

### 2.1.1 `stream_plan` 的 JSON 结构

`stream_plan` 是 `TEXT` 列，内容为 UTF-8 JSON **数组**，描述**所选轨道与质量档位**——它是计划创建入参（[05 §3.1](05-DOWNLOAD.md) 的「轨道选择」）在库内的投影。

**阶段 2 的最小可用结构**（单轨直链）：

```json
[
  { "track": "main", "kind": "video", "quality": "1080p", "container": "mp4" }
]
```

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `track` | string | 是 | 轨道标识；取值域与 [04 §2.5](04-INTERFACES.md) 的上传会话键是**同一个词**（`main` / `video` / `audio`），不在此重复定义 |
| `kind` | string | 是 | 媒体类型：`video` / `audio`。**`main` 轨按主媒体类型取值**——音视频同文件也取 `video`，纯音频才取 `audio`。字幕**不进**本字段——字幕按 [05 §5.3](05-DOWNLOAD.md) 单独下载 |
| `quality` | string | 否 | 可读画质标签（如 `1080p`）；没有档位概念时用空字符串。**必须是可读文本**，不得塞入 URL 或键 |
| `container` | string | 否 | 该轨的容器 / 扩展名，取值域见 §3.3；单轨时应与 `output_container` 一致 |

| 规则 | 说明 |
| --- | --- |
| 禁止内容 | **不得**包含媒体 URL、签名参数、Cookie / `Authorization`、视频号 `decode_key` 或任何解密键（B-722、B-223、§6）——写入前必须逐项自检 |
| 元素个数 | 按路径取值（阶段 3 起）：`direct` — 一条 `main`；`hls` / `dash` — 一条 `main`，或 `[]`（等清单解析），或 **`video` + `audio` 各一条**（P3，[05 §4.6](05-DOWNLOAD.md)）；`page` / `wechat` — `[]`（轨道要等解析才知道）。**不得**出现两条以上视频轨或两条以上音频轨——多档位只通过 `quality` 标签表达，不通过多元素 |
| 不单定上限 | 它的规模随请求体上限受约束（§4.5 的本地 API 请求体上限），**不另设常量** |
| 写入时机 | 只在创建计划时由 [05 §3](05-DOWNLOAD.md) 的入参写入，**不得**在下载过程中改写——它记录的是**用户的选择** |
| 读方约束 | 读方**不得**试图从它重建媒体地址：地址根本不在库里（见 §2.1 的「本表没有媒体地址列」） |

**阶段 3 补齐**：HLS / DASH / 分离音视频 / 页面解析所需的其余字段（流索引、编码、语言、时长等）在阶段 3 加入。补齐必须**向后兼容**：新增字段一律可缺省，读方必须容忍未知字段与缺省字段——阶段 2 写出的 `stream_plan` 在阶段 3 仍必须可读。

### 2.2 `jobs` — 导入任务

```sql
CREATE TABLE IF NOT EXISTS jobs (
    id              TEXT PRIMARY KEY,
    plan_id         TEXT UNIQUE,
    file_path       TEXT NOT NULL,
    file_name       TEXT NOT NULL,
    extension       TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN (
                        'queued','waiting','running','imported','failed','skipped')),
    source_url      TEXT,
    source_title    TEXT,
    fingerprint     TEXT,
    eagle_item_id   TEXT,
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    next_attempt_at REAL,
    error_code      TEXT,
    error_message   TEXT,
    created_at      REAL NOT NULL,
    updated_at      REAL NOT NULL,
    completed_at    REAL,
    FOREIGN KEY(plan_id) REFERENCES plans(id)
);

CREATE INDEX IF NOT EXISTS idx_jobs_due     ON jobs(status, next_attempt_at, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_file    ON jobs(file_path, status);
```

### 2.3 `fingerprints` — 内容去重

```sql
CREATE TABLE IF NOT EXISTS fingerprints (
    fingerprint TEXT PRIMARY KEY,
    job_id      TEXT NOT NULL,
    file_size   INTEGER NOT NULL,
    created_at  REAL NOT NULL
);
```

### 2.4 `settings` — 配置（唯一权威源）

```sql
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at REAL NOT NULL
);
```

- `value` 一律 JSON 编码；解码失败必须回退到调用方默认值，不得中断启动。
- **这是配置的唯一权威源**，不另设配置文件。

已知键：

| 键 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `theme` | string | `"light"` | `light` / `dark` |
| `proxy_mode` | string | `"auto"` | `auto` / `direct` / `manual` |
| `proxy_url` | string | `""` | 手动代理地址 |
| `cache_retention_days` | int | `30` | `0` 表示关闭自动清理 |
| `download_concurrency` | int | `3` | 同时下载数（出站下载） |
| `retry_max` | int | `5` | 下载重试上限，范围 `0–20`；`0` 表示不自动重试 |
| `progress_throttle_ms` | int | `500` | 进度写库节流间隔，范围 `100–5000` ms |
| `upload_session_limit` | int | `3` | 同时进行的中转上传会话数（**不占用** `download_concurrency`） |
| `output_dir` | string | `""` | 空表示默认目录 |
| `browser_download_mode` | int | `0` | **浏览器下载模式**（B-220）；`0` 用软件下载 · `1` 用浏览器网页下载。**写入方只有扩展弹窗；读取方还包括桌面端**——创建计划时必须读它来决定这条计划走 P6 还是出站下载（B-221）。权威值在桌面端，扩展不用本地副本做权威 |
| `extension_origin` | string | `""` | 允许的扩展 Origin **覆盖值**；为空时使用内置编译常量（见 [01 §2.1](01-ARCHITECTURE.md)） |
| `last_update_check` | float | `0` | 上次成功检查时间 |

### 2.5 `site_rules` — 站点开关

```sql
CREATE TABLE IF NOT EXISTS site_rules (
    domain             TEXT PRIMARY KEY,
    enabled            INTEGER NOT NULL CHECK (enabled IN (0,1)),
    include_subdomains INTEGER NOT NULL DEFAULT 1 CHECK (include_subdomains IN (0,1)),
    updated_at         REAL NOT NULL
);
```

---

## 3. 取值域

### 3.1 `plans.status`（只有 5 个）

**下载与导入彻底分离**：`plans` 只负责下载，导入由 `jobs` 负责。因此这里**没有** `ready_to_import` / `imported` / `completed_local` 这些混入导入语义的状态。

| 值 | 含义 | 终态 |
| --- | --- | --- |
| `queued` | 已创建，等待执行或等待重试 | 否 |
| `running` | 执行中（细阶段见 `phase`） | 否 |
| `completed` | 下载、合并、校验完成，文件已交付 | **是** |
| `failed` | 重试耗尽或不可重试 | **是** |
| `canceled` | 用户取消 | **是** |

**重试不是独立状态**，用 `attempt_count` + `next_attempt_at` 表达：

| 场景 | 表达 |
| --- | --- |
| 刚创建 | `queued`, `attempt_count = 0` |
| 等待重试 | `queued`, `attempt_count = 2`, `next_attempt_at` 为未来时间 |
| 执行中 | `running`, `phase` = `downloading` / `merging` / `validating` |

界面据此显示「等待重试 · 第 2/5 次」这类文案。

**这修正了一个真实矛盾**：旧设计把 `completed_local` 定义为终态，却规定它补导后转为 `ready_to_import`——终态变成了非终态。拆开后 `Plan` 完成后永远是 `completed`，导入状态记在 `jobs`。

### 3.2 `jobs.status`（只有 6 个）

`jobs` 只负责 Eagle / IDM 导入，不掺下载状态。

| 值 | 含义 | 终态 |
| --- | --- | --- |
| `queued` | 等待导入（含等待重试） | 否 |
| `waiting` | 等待 Eagle 启动 | 否 |
| `running` | 正在调用 Eagle API | 否 |
| `imported` | 已导入 | **是** |
| `failed` | 重试耗尽或不可重试 | **是** |
| `skipped` | 有意跳过 | **是** |

`skipped` 的具体原因用 `error_code` 区分：`duplicate`（内容已存在）、`non_video`（非视频）、`by_user`（用户忽略）。

### 3.3 其他枚举

| 项 | 取值 |
| --- | --- |
| `output_container` | `mp4` `mkv` `webm` `m4a` `mp3` `ts` |
| `merge_mode` | `single`（输入自足，**不做**跨轨合并）· `av`（输入是分离的音视频轨，**必须**合并为单文件）· `subtitles`（在主媒体之外还要下载字幕；是否跨轨合并由输入形态决定）——与路径的关系见 [05 §4.6](05-DOWNLOAD.md) |
| `media_kind` | `direct` `hls` `dash` `page` `wechat` `browser`（**路径提示**，不是命令） |
| `resolved_kind` | 同 `media_kind`（**实际执行的路径**，见 [05 §4.0](05-DOWNLOAD.md)）；`NULL` = 尚未确定 |
| 视频扩展名 | `avi m2ts m4v mkv mov mp4 mpeg mpg ts webm wmv` |
| 字幕扩展名 | `vtt srt ass ssa ttml` |
| 清单扩展名 | `m3u8` `m3u` `mpd` |

### 3.4 `error_code` 取值域

`plans.error_code` 与 `jobs.error_code` 共用一套**稳定机器码**。本表只登记**归属与出处**——每条错误码的**完整取值列表**在各自主文档定义，此处不复制：

| 归属 | 定义处 | 说明 |
| --- | --- | --- |
| `plans` 下载类 | [05 §7](05-DOWNLOAD.md) | 可重试 / 不可重试两张表 |
| `jobs` 导入类 | [08 §9](08-EAGLE-IDM.md) | Eagle 与 IDM 的错误码表 |
| 计划与文件类 | [05 §7.3](05-DOWNLOAD.md) | `plan_not_found`、`plan_file_missing` 等 |

| 规则 | 说明 |
| --- | --- |
| 非空条件 | `plans.status = 'failed'` 与 `jobs.status = 'failed'` 时 `error_code` **必须非空** |
| `skipped` | `jobs.status = 'skipped'` 时 `error_code` 表示跳过原因（`duplicate` / `non_video` / `by_user`），见 [03 §3.2](03-DATA.md) |
| 唯一性 | 同一个码在两张表里含义必须相同；**不得**为同一含义新增第二个码 |
| 新增流程 | 新增错误码时必须同时登记到对应主文档的表中（常驻规则，见 [13 §7](13-ROADMAP.md) `05 N6`） |

---

## 4. 输入校验

只做**输入合法性校验**，目标是防止程序自身出错，不构成权限或身份体系。

### 4.1 页面 URL

必须实现：

1. scheme 必须为 `http` 或 `https`，否则拒绝。
2. 必须有 hostname。
3. 禁止包含 username 或 password。
4. 主机名转小写、去除末尾 `.`；IDNA 编码为 ASCII。
5. 默认端口（http:80 / https:443）必须省略。
6. 丢弃全部 `utm_*` 参数，以及 `fbclid` `gclid` `dclid` `msclkid` `mc_cid` `mc_eid` `igshid` `yclid` `_ga` `_gl`。
7. **丢弃 fragment**。
8. path 为空时取 `/`。

### 4.2 域名归一化

1. 去空白、转小写、去末尾 `.`。
2. 含 `://` 或 `/` 时取 hostname。
3. 形如 `host:port` 且不以 `[` 开头时取 `:` 前部分。
4. 空值拒绝。
5. 合法 IP 字面量原样返回。
6. 否则按 IDNA 编码；失败拒绝。
7. 任一 label 为空或长度 > 63 拒绝。
8. 任一 label 以 `-` 开头或结尾拒绝。

### 4.3 下载目标安全检查

提交下载时必须拒绝：

| 条件 | 理由 |
| --- | --- |
| 主机为 `localhost` / `localhost.localdomain` | 防误将本机服务当作媒体源 |
| 主机不是全局单播地址 | 防误将本机服务当作媒体源 |

**"不是全局单播"必须按具体集合判定，不能直接照抄 Go 的 `IsGlobalUnicast()`**——它对 `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16` 返回 `true`，照字面实现会**漏掉整个内网**。

必须拒绝的集合：

| 类别 | 范围 |
| --- | --- |
| 回环 | `127.0.0.0/8`、`::1` |
| 私有 | `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`fc00::/7` |
| 链路本地 | `169.254.0.0/16`、`fe80::/10` |
| 运营商级 NAT | `100.64.0.0/10` |
| 未指定 / 广播 / 组播 | `0.0.0.0`、`::`、`255.255.255.255`、`224.0.0.0/4`、`ff00::/8` |
| IPv4-mapped IPv6 | `::ffff:0:0/96`（**必须先还原为 IPv4 再按上表判定**，否则 `::ffff:192.168.1.1` 会绕过） |

| 情况 | 处置 |
| --- | --- |
| 主机名字面量命中上表 | 拒绝，`blocked_local_target` |
| DNS 解析失败或超时 | **拒绝**（不重试、不放行），`invalid_url` |
| 解析出多个地址 | **任一**命中上表即拒绝 |

**只检查提交的那一条 URL 的主机**。这是防"程序被页面误导去访问本机服务"的 bug，不是完整的 SSRF 防护体系。

### 4.4 输出文件名

`output_name` 必须：

1. 去除 `\ / : * ? " < > |` 与控制字符。
2. 去除首尾空白与点号。
3. 拒绝 Windows 保留名（`CON` `PRN` `AUX` `NUL` `COM1-9` `LPT1-9`，不分大小写）。
4. 长度上限 **150** 字符（为路径与后缀预留空间）。
5. 清洗后为空则回退到内容 ID 或时间戳。

### 4.5 长度与数量上限

| 项 | 上限 |
| --- | --- |
| 页面 URL | 2048 字符 |
| 标题 | 500 字符 |
| 错误消息 | 1000 字符（超出截断） |
| 本地 API 请求体 | 256 KB |
| 单次返回的计划数 | 200 |
| 单次返回的任务数 | 200 |
| FFmpeg 请求头总量 | 6 KB（沿用旧项目实测值） |
| 字幕文件 | 50 MB |

### 4.6 浏览器中转的上限（P6，见 [05 §4.4](05-DOWNLOAD.md)）

**这组常量只有一处定义**，扩展端预检与桌面端复核都必须引用它们，不得各写一份（[05 §4.5](05-DOWNLOAD.md) 与 [04 §2.5](04-INTERFACES.md) 只做引用，不重复定义）。
| 项 | 上限 | 说明 |
| --- | --- | --- |
| 单次分片请求体 | **1 MB** | 唯一不受「本地 API 请求体 256 KB」约束的请求体 |
| 单轨道合计 | **2.0 GiB** | 按 `image/video` 或 `audio` 单轨计 |
| 单计划合计 | **8.0 GiB** | DASH 双轨等多轨合计 |

超限必须在**写入任何字节之前**拒绝，错误码 `browser_mode_size_exceeded`。

---

## 5. 查询约束

| # | 要求 |
| --- | --- |
| Q1 | 全部列表查询必须命中索引且有 `LIMIT` |
| Q2 | 计划列表默认按 `updated_at DESC` 排序，必须命中 `idx_plans_updated`。**注意**：`idx_plans_status` 无法服务纯排序查询（实测查询计划会退化为 `SCAN` + 临时 B 树） |
| Q3 | 调度查询为 `WHERE status = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= ?) ORDER BY next_attempt_at`，必须命中 `idx_plans_due` |
| Q4 | 禁止无界全表排序 |
| Q5 | 补导占有使用 `BEGIN IMMEDIATE` |
| Q6 | 长事务禁止跨网络或子进程调用持锁 |

---

## 6. 秘密边界

以下内容**禁止**写入任何表、索引或迁移日志：

| 禁止落盘 |
| --- |
| Cookie 与 `Set-Cookie` |
| `Authorization` 请求头 |
| 完整签名 URL 及其 query（`sign` `token` `encfilekey` `hy` `idx` `basedata`） |
| 视频号 `decode_key` 与解密键 |
| TLS 会话密钥 |

**允许落盘**：程序自己的产物路径（`final_path`、`preview_path`、`jobs.file_path`）、归一化后的页面地址、可读质量标签。

**有条件允许**：为把凭据传给外部工具而创建的临时文件。必须满足：位于程序自己的临时目录、权限收紧、用后立即删除、不放入命令行。这是功能需要，不是绝对禁令的例外。

> 说明：简化前曾把"用户绝对路径"也列入禁止范围，这与必须持久化文件路径相冲突。现在明确区分：**路径可以落盘，也可以出现在本地日志中便于排错，但不得进入诊断导出与用户可见错误消息**。

---

## 7. 数据库连接设置

| 参数 | 值 | 理由 |
| --- | --- | --- |
| `journal_mode` | WAL | 读不阻塞写 |
| `busy_timeout` | 5000 ms | 避免瞬时锁冲突直接失败 |
| `foreign_keys` | **每连接显式开启** | SQLite 不默认启用外键约束 |
| 最大读连接 | 4 | 够用且不浪费 |

**外键约束必须在每个连接上执行 `PRAGMA foreign_keys = ON`**，不能只依赖建表声明。

---

## 8. 待确认

| # | 项 | 何时确认 |
| --- | --- | --- |
| ~~D1~~ | ~~扩展的固定 Origin 值~~ **已定**：值、`manifest.key` 与生成方式见 [07 §1.1](07-EXTENSION.md)（不在本文复制） | 已解决 |
| ~~D2~~ | ~~`stream_plan` 的精确 JSON 结构~~ **已定（阶段 2 提前，完整字段阶段 3 补齐）**：阶段 2 的最小可用结构、字段表与禁止内容见 **§2.1.1** | 已解决 |
| ~~D3~~ | ~~默认输出目录的最终路径~~ **已定**：`%USERPROFILE%\Downloads\留底下载器\`，含 `临时\<plan_id>\`、`预览\`、`已完成\` 三个子目录——权威定义见 [01 §8](01-ARCHITECTURE.md) | 已解决 |
| D4 | 缓存保留期默认值（当前设 30 天） | 阶段 3 |
| D5 | 是否需要"清空历史记录"的批量删除语义 | 阶段 3 |
