/*
 * 留底浏览器扩展 · 与桌面端的通信。
 *
 * 本文件是原项目代码在 docs/07-EXTENSION.md §7 的 `AD-1` ~ `AD-9` 范围内的
 * 适配版本：
 *   AD-1 删除配对码与令牌逻辑 → 只按 Origin 直连（桌面端按编译常量白名单放行）
 *   AD-2 找不到桌面端 → 暴露"未找到"结论供弹窗显示启动引导
 *   AD-3 端点路径 → 按 docs/04-INTERFACES.md §2.3 的清单
 *   AD-4 能力字段 → 健康接口的 `eagleAvailable` 等
 *   AD-5 端口发现 → 默认端口 47652 + 备用端口探测（见下）
 *   AD-6 清单版本号 → 健康接口 `version` 与 `chrome.runtime.getManifest()`
 *   AD-7 任务列表 → 仅任务状态、不低于 2 秒、弹窗关闭即停的轻量轮询（UI 侧）
 *   AD-8 浏览器下载模式 → `/api/mode` 与 `/api/upload`（中转上传）
 *
 * **不得**把 Cookie、Authorization 或签名 URL 写入任何 storage 区域（07 §3）。
 * 请求头与媒体地址只驻留内存：`eagleBridgeMediaUrls` 是内存态，Worker 被回收
 * 即丢失——这正是 04 §2.5 承认的边界（桌面端以会话空闲超时收尾）。
 */

const EAGLE_BRIDGE_DEFAULT_PORT = 47652;
const EAGLE_BRIDGE_STATE_KEY = "downloadTransferStation";
const EAGLE_BRIDGE_RETRY_ALARM = "downloadTransferStationRetry";
const EAGLE_BRIDGE_VERSION_ALARM = "eagleBridgeVersionCheck";
const EAGLE_BRIDGE_MAX_PENDING_EVENTS = 200;
const EAGLE_BRIDGE_SITE_CACHE_TTL = 30 * 1000;
const EAGLE_BRIDGE_API_TIMEOUT_MS = 8000;
const EAGLE_BRIDGE_HEALTH_TIMEOUT_MS = 2500;
// `GET /api/plans` 的 `limit` 默认 50，且**不静默截断**（04 §2.3.2）。
// 弹窗是连续滚动列表（B-310），这里显式取一页的较大上限。
const EAGLE_BRIDGE_PLANS_LIMIT = 200;

const METHODS_WITH_JSON_BODY = new Set(["POST", "PUT", "PATCH", "DELETE"]);

// docs/03-DATA.md §4.6：这组常量只有一处权威定义，扩展端预检引用其取值。
const EAGLE_BRIDGE_UPLOAD_CHUNK_BYTES = 1024 * 1024;
const EAGLE_BRIDGE_UPLOAD_TRACK_LIMIT_BYTES = 2 * 1024 * 1024 * 1024;
const EAGLE_BRIDGE_UPLOAD_STREAM_FALLBACK_BYTES = 64 * 1024 * 1024;
const EAGLE_BRIDGE_UPLOAD_TRACKS = ["main", "video", "audio"];

let eagleBridgeFlushPromise = null;
let eagleBridgeStateWriter = null;
const eagleBridgeSiteCache = new Map();
// 内存态：`planId:track` → 媒体地址。绝不写入任何 storage 区域（B-223）。
const eagleBridgeMediaUrls = new Map();
const eagleBridgeUploadLoops = new Map();

function eagleBridgeDefaultState() {
    return {
        port: EAGLE_BRIDGE_DEFAULT_PORT,
        connection: "checking",
        service: "",
        version: "",
        apiProtocol: null,
        eagleAvailable: null,
        mediaToolsReady: null,
        pendingEvents: [],
        uploads: {},
        lastPlanId: "",
        lastPlanStatus: ""
    };
}

async function eagleBridgeGetState() {
    const stored = await chrome.storage.session.get(EAGLE_BRIDGE_STATE_KEY).catch(() => ({}));
    const current = { ...eagleBridgeDefaultState(), ...(stored?.[EAGLE_BRIDGE_STATE_KEY] || {}) };
    // AD-1 之后旧版遗留字段已无意义，读到即丢弃。
    delete current.token;
    delete current.downloads;
    return current;
}

async function eagleBridgeUpdateState(changes) {
    if (!eagleBridgeStateWriter) {
        eagleBridgeStateWriter = EagleBridgeAuthLogic.createStateUpdateQueue(
            eagleBridgeGetState,
            next => chrome.storage.session.set({ [EAGLE_BRIDGE_STATE_KEY]: next })
        );
    }
    return eagleBridgeStateWriter(changes);
}

/**
 * `AD-5` · 端口发现（B-214）。
 *
 * 权威定义在 docs/01-ARCHITECTURE.md §2.1.1 的 `S4`：主端口 `47652`；被占用时
 * 回退临时端口，并把实际端口写进 `%LOCALAPPDATA%\LiudiDownloader\api-port.json`
 * （内容 `{"port":47652,"pid":1234}`），扩展按 `pid` 校验进程仍在再使用端口。
 *
 * **实现边界（必须如实记录）**：MV3 扩展**没有读取任意本地文件的能力**——
 * `chrome.runtime.getURL()` 只能取包内资源，`file://` 需要用户在扩展详情页
 * 单独开启"允许访问文件网址"，且宿主尚未给该路径放行。因此本阶段实现为：
 *   1. 声明式候选端口清单：`47652` 优先，其后是紧随其后的备用端口；
 *   2. 逐个 `GET /health` 探测，取第一个 `service === "idm-eagle"` 的端口；
 *   3. 全部失败 → `connection = "offline"`，弹窗显示启动引导（AD-2）。
 *
 * `api-port.json` 的读取被收敛到 `eagleBridgeApiPortHint()` 一个函数：一旦
 * 桌面端改用一个免认证的发现端点（或用户为扩展放行该路径），只改这一处。
 * 这是 B-214 的**部分实现**，已在交付说明中标注。
 */
function eagleBridgePortCandidates() {
    const candidates = [EAGLE_BRIDGE_DEFAULT_PORT];
    for (let offset = 1; offset <= 8; offset += 1) candidates.push(EAGLE_BRIDGE_DEFAULT_PORT + offset);
    return candidates;
}

/**
 * `api-port.json` 的读取钩子。当前恒返回 0（无可用读法），保留唯一入口以便
 * 后续替换，不让"端口发现"散落在多处。
 */
async function eagleBridgeApiPortHint() {
    return 0;
}

async function eagleBridgeProbePort(port) {
    const base = EagleBridgeAuthLogic.apiBase(port);
    if (!base) return { reachable: false, identified: false, reason: "invalid_port" };
    try {
        const payload = await EagleBridgeAuthLogic.fetchJsonWithTimeout(
            fetch,
            `${base}/health`,
            { cache: "no-store" },
            EAGLE_BRIDGE_HEALTH_TIMEOUT_MS
        );
        const response = payload.response;
        const result = payload.result || {};
        return {
            reachable: Boolean(response?.ok),
            identified: EagleBridgeAuthLogic.isDesktopHealth(result),
            reason: response?.ok ? "" : `http_${Number(response?.status) || 0}`,
            capabilities: EagleBridgeAuthLogic.capabilitySnapshot(result)
        };
    } catch (error) {
        return { reachable: false, identified: false, reason: String(error?.name || "unreachable") };
    }
}

async function eagleBridgeDiscoverDesktop(options = {}) {
    const ports = Array.isArray(options.ports) && options.ports.length
        ? [...options.ports]
        : eagleBridgePortCandidates();
    const hint = await eagleBridgeApiPortHint();
    if (hint && !ports.includes(hint)) ports.unshift(hint);
    for (const port of ports) {
        const probe = await eagleBridgeProbePort(port);
        if (probe.identified) {
            return {
                port,
                connection: EagleBridgeAuthLogic.CONNECTION_STATES.CONNECTED,
                capabilities: probe.capabilities,
                reason: ""
            };
        }
    }
    const current = await eagleBridgeGetState();
    return {
        port: current.port || EAGLE_BRIDGE_DEFAULT_PORT,
        connection: EagleBridgeAuthLogic.CONNECTION_STATES.OFFLINE,
        capabilities: null,
        reason: "not_found"
    };
}

const eagleBridgeConnectionGate = EagleBridgeAuthLogic.createLatestRequestGate();

/**
 * 探测桌面端并落盘结论。并发探测由 `createLatestRequestGate` 保证只有最新一次
 * 生效——这是"连接可达性竞态"的防护点（取代旧版的令牌竞态）。
 */
async function eagleBridgeRefreshConnection(options = {}) {
    const ticket = eagleBridgeConnectionGate.begin();
    const discovery = await eagleBridgeDiscoverDesktop(options);
    if (!eagleBridgeConnectionGate.isCurrent(ticket)) {
        // 已有更新的探测在途：丢弃本次结论，避免旧结果覆盖新状态。
        const latest = await eagleBridgeGetState();
        return { connection: latest.connection, port: latest.port, stale: true };
    }
    const capabilities = discovery.capabilities || {};
    await eagleBridgeUpdateState({
        port: discovery.port,
        connection: discovery.connection,
        service: capabilities.service || "",
        version: capabilities.version || "",
        apiProtocol: capabilities.apiProtocol ?? null,
        eagleAvailable: capabilities.eagleAvailable ?? null,
        mediaToolsReady: capabilities.mediaToolsReady ?? null
    });
    eagleBridgeScheduleRetry();
    return {
        connection: discovery.connection,
        port: discovery.port,
        version: capabilities.version || "",
        apiProtocol: capabilities.apiProtocol ?? null,
        eagleAvailable: capabilities.eagleAvailable ?? null,
        mediaToolsReady: capabilities.mediaToolsReady ?? null,
        stale: false
    };
}

async function eagleBridgeDesktopAvailable() {
    const state = await eagleBridgeGetState();
    if (state.connection === EagleBridgeAuthLogic.CONNECTION_STATES.CONNECTED) return true;
    const refreshed = await eagleBridgeRefreshConnection().catch(() => null);
    return refreshed?.connection === EagleBridgeAuthLogic.CONNECTION_STATES.CONNECTED;
}

function eagleBridgeBaseUrl(state) {
    return EagleBridgeAuthLogic.apiBase(state?.port) || EagleBridgeAuthLogic.apiBase(EAGLE_BRIDGE_DEFAULT_PORT);
}

/**
 * 统一请求入口。
 *
 * 04 §2.2：除 `GET /health` 外全部端点都需要 Origin 校验；扩展**不发送任何
 * 令牌**——浏览器自动带上的 `Origin: chrome-extension://<id>` 就是凭据。
 */
async function eagleBridgeApi(path, options = {}) {
    const state = await eagleBridgeGetState();
    const base = eagleBridgeBaseUrl(state);
    const method = String(options.method || "GET").toUpperCase();
    const headers = { ...(options.headers || {}) };
    if (METHODS_WITH_JSON_BODY.has(method) && options.body !== undefined && !headers["Content-Type"]) {
        headers["Content-Type"] = "application/json";
    }
    const payload = await EagleBridgeAuthLogic.fetchJsonWithTimeout(
        fetch,
        `${base}${path}`,
        { ...options, headers },
        EAGLE_BRIDGE_API_TIMEOUT_MS
    );
    const response = payload.response;
    const result = payload.result;
    if (!response?.ok || result?.ok === false) {
        const error = EagleBridgeAuthLogic.normalizeError(result, response);
        const failure = new Error(error.message);
        failure.code = error.code;
        failure.status = Number(response?.status) || 0;
        failure.result = result;
        throw failure;
    }
    if (payload.jsonError || !result || typeof result !== "object") {
        throw new Error("留底桌面端返回格式错误");
    }
    return result.data;
}

function eagleBridgeRead(path, payload = {}) {
    return eagleBridgeApi(path, {
        method: "POST",
        body: JSON.stringify(payload)
    });
}

/** GET + 查询参数（04 §2.3.2：`GET /api/plan` 的入参是查询参数 `id`）。 */
function eagleBridgeGet(path, query = {}) {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(query)) {
        if (value === undefined || value === null || value === "") continue;
        search.set(key, String(value));
    }
    const suffix = search.toString();
    return eagleBridgeApi(suffix ? `${path}?${suffix}` : path, { method: "GET" });
}

function eagleBridgeSanitizeEvent(event) {
    // `eventType` 必须落在 04 §2.3.2 的全集内；未知取值**不得**按默认值处理
    // （桌面端会 400 拒绝）。这里主动收敛，避免把未知类型发出去。
    const requestedType = String(event.eventType || "media");
    const eventType = SOURCE_EVENT_TYPES.has(requestedType) ? requestedType : "media";
    const clean = {
        pageUrl: String(event.pageUrl || ""),
        pageTitle: String(event.pageTitle || "").slice(0, 500),
        mediaUrl: String(event.mediaUrl || ""),
        eventType,
        tabId: Number.isInteger(event.tabId) ? event.tabId : undefined,
        // 毫秒（`Date.now()` 口径）——04 §2.3.2 唯一的毫秒字段。
        capturedAt: Number(event.capturedAt || Date.now())
    };
    if (event.deferSiteCheck) clean.deferSiteCheck = true;
    return clean;
}

const SOURCE_EVENT_TYPES = new Set(["media", "page_download_click", "manual", "ignore", "site_disabled"]);

async function eagleBridgeCurrentTab() {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!tab?.url?.startsWith("http")) throw new Error("当前页面不是普通网页");
    return tab;
}

async function eagleBridgeEnsureDiscovery(tabId) {
    const normalizedTabId = Number(tabId);
    if (!Number.isInteger(normalizedTabId) || normalizedTabId < 0) {
        return { ready: false, injected: false, reason: "invalid_tab" };
    }
    const tab = await chrome.tabs.get(normalizedTabId);
    return EagleBridgeCandidateLogic.ensureContentDiscovery(chrome, tab);
}

async function eagleBridgeExplicitSource(eventType) {
    const tab = await eagleBridgeCurrentTab();
    return eagleBridgeQueueSourceEvent({
        pageUrl: tab.url,
        pageTitle: tab.title || "",
        mediaUrl: "",
        eventType,
        tabId: tab.id,
        capturedAt: Date.now()
    });
}

async function eagleBridgeSourceClick(event, senderTab) {
    const clean = eagleBridgeSanitizeEvent({
        ...(event || {}),
        pageUrl: event?.pageUrl || senderTab?.url || "",
        pageTitle: event?.pageTitle || senderTab?.title || "",
        tabId: senderTab?.id,
        capturedAt: event?.capturedAt || Date.now()
    });
    let domain = "";
    try {
        domain = new URL(clean.pageUrl).hostname;
    } catch (_error) {
        throw new Error("页面来源地址无效");
    }
    try {
        const site = await eagleBridgeSiteStatus(domain);
        if (!site?.enabled) clean.eventType = "site_disabled";
    } catch (_error) {
        clean.deferSiteCheck = true;
    }
    return eagleBridgeQueueSourceEvent(clean);
}

async function eagleBridgeQueueSourceEvent(event) {
    const clean = eagleBridgeSanitizeEvent(event);
    await eagleBridgeUpdateState(current => ({
        pendingEvents: [...(Array.isArray(current.pendingEvents) ? current.pendingEvents : []), clean]
            .slice(-EAGLE_BRIDGE_MAX_PENDING_EVENTS)
    }));
    eagleBridgeScheduleRetry();
    return eagleBridgeFlushEvents();
}

async function eagleBridgeFlushEvents() {
    if (eagleBridgeFlushPromise) return eagleBridgeFlushPromise;
    eagleBridgeFlushPromise = (async () => {
        let state = await eagleBridgeGetState();
        if (state.connection !== EagleBridgeAuthLogic.CONNECTION_STATES.CONNECTED
            && !(await eagleBridgeDesktopAvailable())) {
            return false;
        }
        state = await eagleBridgeGetState();
        const pending = Array.isArray(state.pendingEvents) ? [...state.pendingEvents] : [];
        let consumed = 0;
        for (const queuedEvent of pending) {
            const event = { ...queuedEvent };
            try {
                if (event.deferSiteCheck) {
                    const domain = new URL(event.pageUrl).hostname;
                    const site = await eagleBridgeSiteStatus(domain);
                    delete event.deferSiteCheck;
                    if (!site?.enabled) event.eventType = "site_disabled";
                }
                await eagleBridgeApi("/api/source", {
                    method: "POST",
                    body: JSON.stringify(event)
                });
                consumed += 1;
            } catch (error) {
                if (/未开启自动导入/.test(String(error?.message || ""))) {
                    consumed += 1;
                    continue;
                }
                break;
            }
        }
        if (consumed) {
            await eagleBridgeUpdateState(current => ({
                pendingEvents: (Array.isArray(current.pendingEvents) ? current.pendingEvents : []).slice(consumed)
            }));
        }
        return consumed === pending.length;
    })().finally(() => {
        eagleBridgeFlushPromise = null;
    });
    return eagleBridgeFlushPromise;
}

function eagleBridgeScheduleRetry() {
    chrome.alarms.create(EAGLE_BRIDGE_RETRY_ALARM, { delayInMinutes: 0.5 });
    setTimeout(() => eagleBridgeFlushEvents(), 1000);
    setTimeout(() => eagleBridgeFlushEvents(), 5000);
}

function eagleBridgeIsNewerVersion(candidate, current) {
    const left = String(candidate || "").split(".").map(value => Number.parseInt(value, 10) || 0);
    const right = String(current || "").split(".").map(value => Number.parseInt(value, 10) || 0);
    for (let index = 0; index < Math.max(left.length, right.length); index += 1) {
        if ((left[index] || 0) !== (right[index] || 0)) return (left[index] || 0) > (right[index] || 0);
    }
    return false;
}

/**
 * `AD-6` · 清单版本号随产品版本同步（B-104）。
 * 桌面端版本高于扩展清单版本时说明扩展已被新版取代，重载以取回与桌面端同一
 * 版本的扩展代码。
 */
async function eagleBridgeCheckDesktopVersion() {
    try {
        const state = await eagleBridgeGetState();
        const health = await EagleBridgeAuthLogic.fetchJsonWithTimeout(
            fetch,
            `${eagleBridgeBaseUrl(state)}/health`,
            { cache: "no-store" },
            EAGLE_BRIDGE_HEALTH_TIMEOUT_MS
        );
        const version = health.result?.version;
        if (health.response?.ok && eagleBridgeIsNewerVersion(version, chrome.runtime.getManifest().version)) {
            chrome.runtime.reload();
        }
    } catch (_error) {
        // 桌面端离线时保持当前扩展，下一次唤醒再检查。
    }
}

function eagleBridgeScheduleVersionCheck() {
    chrome.alarms.create(EAGLE_BRIDGE_VERSION_ALARM, { periodInMinutes: 30 });
}

async function eagleBridgeSiteStatus(domain) {
    const normalized = String(domain || "").toLowerCase();
    const cached = eagleBridgeSiteCache.get(normalized);
    if (cached && Date.now() - cached.time < EAGLE_BRIDGE_SITE_CACHE_TTL) return cached.value;
    const value = await eagleBridgeRead("/api/sites", { domain: normalized });
    eagleBridgeSiteCache.set(normalized, { time: Date.now(), value });
    return value;
}

async function eagleBridgeCandidate(info) {
    if (!info?.webUrl || !String(info.webUrl).startsWith("http")) return;
    let domain = "";
    try {
        domain = new URL(info.webUrl).hostname;
    } catch (_error) {
        return;
    }
    try {
        const state = await eagleBridgeSiteStatus(domain);
        if (!state?.enabled) return;
        await eagleBridgeQueueSourceEvent({
            pageUrl: info.webUrl,
            pageTitle: info.title || "",
            mediaUrl: info.url || "",
            eventType: "media",
            tabId: info.tabId,
            capturedAt: info.getTime || Date.now()
        });
    } catch (_error) {
        return;
    }
}

function eagleBridgeSafeExtension(value, fallback = "bin") {
    const cleaned = String(value || "").toLowerCase().replace(/[^a-z0-9]/g, "");
    return cleaned.slice(0, 10) || fallback;
}

function eagleBridgePublicStream(data, index) {
    const type = String(data.type || "").toLowerCase();
    const role = ["video", "audio", "subtitle"].includes(data.role)
        ? data.role
        : type.startsWith("audio/") ? "audio" : type.startsWith("video/") ? "video" : `media${index + 1}`;
    const fallbackExtension = role === "audio" ? "m4a" : role === "subtitle" ? "vtt" : "mp4";
    return {
        clientIndex: index,
        url: String(data.url || ""),
        role,
        name: String(data.downFileName || data.name || `${role}.${eagleBridgeSafeExtension(data.ext)}`),
        extension: eagleBridgeSafeExtension(data.ext, fallbackExtension),
        mimeType: type,
        size: Number.isFinite(Number(data._size ?? data.size)) ? Number(data._size ?? data.size) : null,
        width: Number.isFinite(Number(data.videoWidth)) ? Number(data.videoWidth) : null,
        height: Number.isFinite(Number(data.videoHeight)) ? Number(data.videoHeight) : null,
        duration: Number.isFinite(Number(data.duration)) ? Number(data.duration) : null,
        codec: String(data.codec || ""),
        language: String(data.language || ""),
        label: String(data.label || ""),
        resolver: ["youtube", "page"].includes(data.resolver) ? data.resolver : "",
        preferredQuality: /^\d{2,5}p$/i.test(String(data.preferredQuality || "")) ? String(data.preferredQuality).toLowerCase() : "",
        drm: Boolean(data.drm || data.pssh || data.keySystem)
    };
}

function eagleBridgePrivateHeaders(data) {
    const source = { ...(data.requestHeaders || {}) };
    if (data.cookie) source.cookie = data.cookie;
    const allowed = {};
    for (const [key, value] of Object.entries(source)) {
        const lower = key.toLowerCase();
        if (["referer", "origin", "authorization", "cookie", "user-agent"].includes(lower) && typeof value === "string") {
            allowed[lower] = value;
        }
    }
    return allowed;
}

function eagleBridgePlanHeaders(item) {
    // 只为本次提交取出运行时请求头（Cookie / Authorization 只走请求，不落盘）。
    //
    // 旧版在这里按站点命中"最新第一方会话请求"覆盖头信息（用于应对登录后
    // Cookie 轮换）。该分支需要按站点识别会话请求形态，属站点专用逻辑，
    // 已按 07 §2 移出扩展包（阶段 3 落到 `adapters/<site>/extension.js`）。
    return eagleBridgePrivateHeaders(item);
}

async function eagleBridgeCreatePlan(items, options = {}) {
    if (!Array.isArray(items) || !items.length) throw new Error("请先选择要下载的媒体");
    if (items.some(item => item.drm || item.pssh || item.keySystem)) {
        throw new Error("检测到 DRM 保护，本程序不会下载或尝试绕过");
    }
    const first = items[0];
    const payload = {
        pageUrl: String(first.resolver === "page" ? first.url : (first.webUrl || first.initiator || "")),
        pageTitle: String(first._title || first.title || ""),
        thumbnailUrl: String(first.thumbnailUrl || ""),
        outputName: String(options.outputName || first.downFileName || first.name || first.title || "media"),
        outputContainer: String(options.outputContainer || (items.length > 1 ? "mkv" : eagleBridgeSafeExtension(first.ext, "mp4"))),
        mergeMode: items.length > 1 ? "local_streamcopy" : "direct",
        route: "desktop",
        importToEagle: options.importToEagle !== false,
        deleteAfterImport: options.deleteAfterImport === true,
        tabId: Number.isInteger(first.tabId) ? first.tabId : null,
        browserDownloadMode: options.browserDownloadMode === true,
        streams: items.map(eagleBridgePublicStream),
        runtimeHeaders: items.map(item => {
            const headers = eagleBridgePlanHeaders(item);
            return {
                referer: headers.referer,
                origin: headers.origin,
                "user-agent": headers["user-agent"],
                authorization: headers.authorization,
                cookie: headers.cookie
            };
        })
    };
    const plan = await eagleBridgeApi("/api/plan", {
        method: "POST",
        body: JSON.stringify(payload)
    });
    await eagleBridgeUpdateState({ lastPlanId: plan.id, lastPlanStatus: plan.status });
    if (options.browserDownloadMode === true) {
        // 浏览器下载模式的地址只驻留内存（B-223）：Worker 被回收即丢失，
        // 已声明的中转会话由桌面端按会话空闲超时收尾（04 §2.5）。
        items.forEach((item, index) => {
            const track = eagleBridgeTrackOf(item, index, items.length);
            eagleBridgeMediaUrls.set(eagleBridgeUploadKey(plan.id, track), String(item.url || ""));
        });
    }
    return plan;
}

function eagleBridgeTrackOf(item, index, total) {
    const role = String(item?.role || "").toLowerCase();
    if (EAGLE_BRIDGE_UPLOAD_TRACKS.includes(role)) return role;
    if (total === 1) return "main";
    return index === 0 ? "video" : "audio";
}

/**
 * `AD-8` · 浏览器下载模式与中转上传（07 §6.3 / 04 §2.5）。
 *
 * 上传循环绑定**该次下载**而非弹窗（B-222）：循环跑在 Service Worker 里，
 * 弹窗关闭不影响；Worker 被回收后按 `storage.session` 里的进度与偏移恢复
 * （B-223：只存进度与偏移，不存媒体地址与字节）。
 */
function eagleBridgeUploadKey(planId, track) {
    return `${String(planId)}:${String(track)}`;
}

async function eagleBridgeBeginUpload(payload = {}) {
    const planId = String(payload.planId || "");
    const track = String(payload.track || "main");
    if (!planId || !EAGLE_BRIDGE_UPLOAD_TRACKS.includes(track)) {
        throw new Error("中转上传参数无效");
    }
    const declaredBytes = Math.max(0, Math.floor(Number(payload.declaredBytes) || 0));
    // B-224：超限必须在发出任何字节之前拒绝。
    if (declaredBytes > EAGLE_BRIDGE_UPLOAD_TRACK_LIMIT_BYTES) {
        const error = new Error("文件超过浏览器中转上限（单轨 2.0 GiB），请改用软件下载");
        error.code = "browser_mode_size_exceeded";
        throw error;
    }
    const started = await eagleBridgeApi("/api/upload", {
        method: "PUT",
        body: JSON.stringify({
            planId,
            track,
            declaredBytes,
            contentType: String(payload.contentType || "application/octet-stream"),
            sourceUrl: String(payload.sourceUrl || "")
        })
    });
    const key = eagleBridgeUploadKey(planId, track);
    if (payload.sourceUrl) eagleBridgeMediaUrls.set(key, String(payload.sourceUrl));
    return {
        planId,
        track,
        file: String(started?.file || ""),
        // B-225：必须使用 PUT 返回的 received 作为首个分片偏移。
        received: Math.max(0, Math.floor(Number(started?.received) || 0))
    };
}

async function eagleBridgePushChunk(planId, track, offset, bytes) {
    const query = new URLSearchParams({ planId, track, offset: String(offset) });
    const state = await eagleBridgeGetState();
    const response = await EagleBridgeAuthLogic.fetchWithTimeout(
        fetch,
        `${eagleBridgeBaseUrl(state)}/api/upload?${query.toString()}`,
        {
            method: "POST",
            headers: { "Content-Type": "application/octet-stream" },
            body: bytes
        },
        EAGLE_BRIDGE_API_TIMEOUT_MS * 6
    );
    let result = null;
    try { result = await response.json(); } catch (_error) { result = null; }
    if (!response.ok || result?.ok === false) {
        const error = EagleBridgeAuthLogic.normalizeError(result, response, "中转上传失败");
        const failure = new Error(error.message);
        failure.code = error.code;
        failure.received = Number(result?.error?.received ?? result?.received);
        throw failure;
    }
    return Math.max(0, Math.floor(Number(result?.data?.received ?? result?.received) || 0));
}

async function eagleBridgeFinishUpload(planId, track, uploadState) {
    return eagleBridgeApi("/api/upload", {
        method: "DELETE",
        body: JSON.stringify({ planId, track, state: uploadState })
    });
}

/** 只写进度与偏移，绝不写入媒体地址或字节（B-223）。 */
async function eagleBridgeNoteUploadProgress(planId, track, received, declaredBytes, done = false, error = "") {
    await eagleBridgeUpdateState(current => {
        const uploads = { ...(current.uploads || {}) };
        uploads[eagleBridgeUploadKey(planId, track)] = {
            planId: String(planId),
            track: String(track),
            received: Math.max(0, Math.floor(Number(received) || 0)),
            declaredBytes: Math.max(0, Math.floor(Number(declaredBytes) || 0)),
            done: Boolean(done),
            error: String(error || "").slice(0, 200),
            updatedAt: Date.now()
        };
        return { uploads };
    }).catch(() => undefined);
}

/**
 * 一次中转上传的完整循环。与列表渲染完全解耦（P-110）：进度只按节流写入
 * `storage.session`，不进入渲染帧预算。
 */
async function eagleBridgeRunUpload(task = {}) {
    const planId = String(task.planId || "");
    const track = String(task.track || "main");
    const key = eagleBridgeUploadKey(planId, track);
    if (eagleBridgeUploadLoops.has(key)) return { ok: true, reused: true };
    const sourceUrl = String(task.sourceUrl || eagleBridgeMediaUrls.get(key) || "");
    if (!sourceUrl) {
        // Worker 被回收后地址已丢失（B-223 的必然结果）：如实报告，桌面端按
        // 会话空闲超时收尾，弹窗提示用户重新发起。
        return { ok: false, code: "upload_source_lost", message: "中转地址已随 Service Worker 回收丢失，请重新发起下载" };
    }
    // 大文件中转期间主动保活（07 §6.3「Worker 回收风险」）；`alarms` 的最小
    // 周期不足以覆盖，这里用廉价的心跳调用维持 Worker 存活。
    const keepAlive = setInterval(() => { chrome.runtime.getPlatformInfo(() => undefined); }, 20000);
    const loop = (async () => {
        const started = await eagleBridgeBeginUpload({ ...task, planId, track, sourceUrl });
        let offset = started.received;
        const declared = Math.max(0, Math.floor(Number(task.declaredBytes) || 0));
        const response = await fetch(sourceUrl, { credentials: "include" });
        if (!response.ok) throw new Error(`媒体地址返回 ${response.status}`);
        const reader = response.body?.getReader ? response.body.getReader() : null;
        if (!reader) {
            // 04 §2.5 降级：`ReadableStream` 请求体不可用时上限降到 64 MB。
            const buffer = await response.arrayBuffer();
            if (buffer.byteLength > EAGLE_BRIDGE_UPLOAD_STREAM_FALLBACK_BYTES) {
                const error = new Error("浏览器不支持流式中转，文件超过 64 MB，请改用软件下载");
                error.code = "upload_stream_unavailable";
                throw error;
            }
            offset = await eagleBridgePushChunk(planId, track, offset, buffer);
        } else {
            let pending = new Uint8Array(0);
            for (;;) {
                const chunk = await reader.read();
                if (chunk.done) break;
                const merged = new Uint8Array(pending.length + chunk.value.length);
                merged.set(pending, 0);
                merged.set(chunk.value, pending.length);
                let cursor = 0;
                while (merged.length - cursor >= EAGLE_BRIDGE_UPLOAD_CHUNK_BYTES) {
                    const slice = merged.subarray(cursor, cursor + EAGLE_BRIDGE_UPLOAD_CHUNK_BYTES);
                    offset = await eagleBridgePushChunk(planId, track, offset, slice);
                    cursor += EAGLE_BRIDGE_UPLOAD_CHUNK_BYTES;
                    await eagleBridgeNoteUploadProgress(planId, track, offset, declared);
                }
                pending = merged.subarray(cursor);
            }
            if (pending.length) offset = await eagleBridgePushChunk(planId, track, offset, pending);
        }
        await eagleBridgeFinishUpload(planId, track, "complete");
        await eagleBridgeNoteUploadProgress(planId, track, offset, declared, true);
        return { ok: true, received: offset };
    })().catch(async error => {
        await eagleBridgeNoteUploadProgress(planId, track, offset, task.declaredBytes, false, String(error?.message || error));
        throw error;
    }).finally(() => {
        clearInterval(keepAlive);
        eagleBridgeUploadLoops.delete(key);
        eagleBridgeMediaUrls.delete(key);
    });
    eagleBridgeUploadLoops.set(key, loop);
    return loop;
}

/**
 * Worker 冷启动后恢复未完成的中转上传（B-222）。
 * 只恢复"地址仍在内存"的任务——其余任务无法凭空重建地址（B-223 的边界），
 * 由桌面端按会话空闲超时收尾。
 */
async function eagleBridgeResumeUploads() {
    const state = await eagleBridgeGetState();
    for (const entry of Object.values(state.uploads || {})) {
        if (entry?.done) continue;
        const key = eagleBridgeUploadKey(entry.planId, entry.track);
        if (!eagleBridgeMediaUrls.has(key)) continue;
        eagleBridgeRunUpload({
            planId: entry.planId,
            track: entry.track,
            declaredBytes: entry.declaredBytes,
            sourceUrl: eagleBridgeMediaUrls.get(key)
        }).catch(() => undefined);
    }
}

async function eagleBridgeReadMode() {
    const data = await eagleBridgeApi("/api/mode", { method: "GET" });
    return Boolean(data?.browserDownloadMode);
}

async function eagleBridgeWriteMode(enabled) {
    const data = await eagleBridgeApi("/api/mode", {
        method: "POST",
        body: JSON.stringify({ browserDownloadMode: Boolean(enabled) })
    });
    return Boolean(data?.browserDownloadMode);
}

chrome.alarms.onAlarm.addListener(alarm => {
    if (alarm.name === EAGLE_BRIDGE_RETRY_ALARM) eagleBridgeFlushEvents();
    if (alarm.name === EAGLE_BRIDGE_VERSION_ALARM) eagleBridgeCheckDesktopVersion();
});

chrome.runtime.onStartup.addListener(() => {
    eagleBridgeRefreshConnection().catch(() => undefined);
    eagleBridgeScheduleVersionCheck();
    eagleBridgeCheckDesktopVersion();
});

chrome.runtime.onInstalled.addListener(() => {
    eagleBridgeRefreshConnection().catch(() => undefined);
    eagleBridgeScheduleVersionCheck();
    eagleBridgeCheckDesktopVersion();
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
    if (!message?.eagleBridge) return undefined;
    (async () => {
        switch (message.eagleBridge) {
            case "connectionState": {
                const state = await eagleBridgeGetState();
                return {
                    connection: state.connection,
                    port: state.port,
                    version: state.version,
                    apiProtocol: state.apiProtocol ?? null,
                    eagleAvailable: state.eagleAvailable,
                    mediaToolsReady: state.mediaToolsReady ?? null,
                    pendingEvents: (state.pendingEvents || []).length,
                    lastPlanId: state.lastPlanId
                };
            }
            case "connect": {
                const refreshed = await eagleBridgeRefreshConnection();
                const state = await eagleBridgeGetState();
                let browserDownloadMode = null;
                if (refreshed.connection === EagleBridgeAuthLogic.CONNECTION_STATES.CONNECTED) {
                    // `/api/mode` 的权威在桌面端；弹窗打开时读一次即可（04 §2.5）。
                    browserDownloadMode = await eagleBridgeReadMode().catch(() => null);
                }
                return { ...refreshed, browserDownloadMode, pendingEvents: (state.pendingEvents || []).length };
            }
            case "health": {
                const state = await eagleBridgeGetState();
                const health = await EagleBridgeAuthLogic.fetchJsonWithTimeout(
                    fetch,
                    `${eagleBridgeBaseUrl(state)}/health`,
                    { cache: "no-store" },
                    EAGLE_BRIDGE_HEALTH_TIMEOUT_MS
                );
                if (!health.response?.ok || !EagleBridgeAuthLogic.isDesktopHealth(health.result)) {
                    const error = new Error("留底桌面端未响应健康检查");
                    error.code = "desktop_unreachable";
                    throw error;
                }
                return EagleBridgeAuthLogic.capabilitySnapshot(health.result);
            }
            case "siteStatus":
                return eagleBridgeSiteStatus(String(message.domain || ""));
            case "setSite": {
                const data = await eagleBridgeApi("/api/sites", {
                    method: "POST",
                    body: JSON.stringify({ domain: message.domain, enabled: Boolean(message.enabled), includeSubdomains: true })
                });
                eagleBridgeSiteCache.delete(String(message.domain || "").toLowerCase());
                return data;
            }
            case "currentTab":
                return eagleBridgeCurrentTab();
            case "ensureDiscovery":
                return eagleBridgeEnsureDiscovery(message.tabId);
            case "sourceClick":
                return eagleBridgeSourceClick(message.event || {}, sender.tab);
            case "manualSource":
                return eagleBridgeExplicitSource("manual");
            case "ignoreNext":
                return eagleBridgeExplicitSource("ignore");
            case "source":
                return eagleBridgeQueueSourceEvent(message.event || {});
            case "createPlan":
                return eagleBridgeCreatePlan(message.items || [], message.options || {});
            case "plan":
                return eagleBridgeGet("/api/plan", { id: String(message.planId || "") });
            case "plans":
                // `GET /api/plans` 返回 `Paged<PlanView>`；`limit` 默认 50 且
                // **不静默截断**（04 §2.3.2）。弹窗是连续滚动列表（B-310），
                // 这里按上限取一页并按 `items.length === limit` 判"还有更多"。
                return eagleBridgeGet("/api/plans", { offset: 0, limit: EAGLE_BRIDGE_PLANS_LIMIT });
            case "jobs":
                return eagleBridgeGet("/api/jobs", { offset: 0, limit: EAGLE_BRIDGE_PLANS_LIMIT });
            case "planPreview":
                return eagleBridgeGet("/api/preview", { id: String(message.planId || "") });
            case "openPlanOutput":
                return eagleBridgeApi("/api/plan/open", {
                    method: "POST",
                    // `target` 两项都必填、不设默认值（04 §2.3.2）；扩展的
                    // "打开所在文件夹"语义固定为 `folder`。
                    body: JSON.stringify({ id: String(message.planId || ""), target: "folder" })
                });
            case "importPlan":
                return eagleBridgeApi("/api/plan/import", {
                    method: "POST",
                    body: JSON.stringify({ id: String(message.planId || "") })
                });
            case "stopPlan":
                return eagleBridgeApi("/api/plan/stop", {
                    method: "POST",
                    body: JSON.stringify({ id: String(message.planId || "") })
                });
            case "retryPlan":
                return eagleBridgeApi("/api/plan/retry", {
                    method: "POST",
                    body: JSON.stringify({ id: String(message.planId || "") })
                });
            case "removePlan":
                return eagleBridgeApi("/api/plan/remove", {
                    method: "POST",
                    body: JSON.stringify({ id: String(message.planId || "") })
                });
            case "readMode":
                return eagleBridgeReadMode();
            case "writeMode":
                return eagleBridgeWriteMode(message.enabled);
            case "beginUpload":
                return eagleBridgeBeginUpload(message.payload || {});
            case "runUpload":
                return eagleBridgeRunUpload(message.task || {});
            default:
                throw new Error("未知的留底浏览器扩展操作");
        }
    })().then(
        data => sendResponse({ ok: true, data }),
        error => {
            const failure = {
                ok: false,
                error: String(error?.message || error),
                code: String(error?.code || "")
            };
            if (Number.isFinite(Number(error?.received))) failure.received = Number(error.received);
            sendResponse(failure);
        }
    );
    return true;
});

eagleBridgeRefreshConnection().catch(() => undefined);
eagleBridgeScheduleVersionCheck();
eagleBridgeCheckDesktopVersion();
eagleBridgeResumeUploads().catch(() => undefined);
