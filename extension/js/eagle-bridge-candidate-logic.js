(function (root, factory) {
    const api = factory();
    if (typeof module === "object" && module.exports) module.exports = api;
    root.EagleBridgeCandidateLogic = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
    "use strict";

    const SITE_ICON = /(?:^|[\/_\-.])(favicon|apple-touch-icon|site-icon|site-logo)(?:[\/_\-.]|$)/i;
    const FRAME_DATA_URL = /^data:image\/(?:jpeg|png|webp);base64,[a-z0-9+/=]+$/i;
    const MAX_FRAME_DATA_URL_LENGTH = 300000;

    function safeThumbnailUrl(value) {
        try {
            const url = new URL(String(value || ""));
            return ["http:", "https:"].includes(url.protocol) && url.href.length <= 4096 ? url.href : "";
        } catch (_error) {
            return "";
        }
    }

    function safeFrameDataUrl(value) {
        const frame = String(value || "");
        return frame.length >= 32
            && frame.length <= MAX_FRAME_DATA_URL_LENGTH
            && FRAME_DATA_URL.test(frame)
            ? frame : "";
    }

    function canonicalMediaSource(value) {
        try {
            const url = new URL(String(value || ""), "https://invalid.local/");
            if (!["http:", "https:", "blob:"].includes(url.protocol)) return "";
            return url.protocol === "blob:"
                ? `blob:${url.pathname.split("/").slice(0, 2).join("/")}`
                : `${url.origin}${url.pathname}`;
        } catch (_error) {
            return "";
        }
    }

    function fnv1a(value) {
        let hash = 0x811c9dc5;
        for (const char of String(value || "")) {
            hash ^= char.charCodeAt(0);
            hash = Math.imul(hash, 0x01000193) >>> 0;
        }
        return hash.toString(36);
    }

    function stableVisualKey(source, ordinal = 0, duration = 0) {
        const canonical = canonicalMediaSource(source);
        const durationSecond = Number.isFinite(Number(duration)) ? Math.round(Number(duration)) : 0;
        return `player-${fnv1a(`${canonical}|${Number(ordinal) || 0}|${durationSecond}`)}`;
    }

    function resolveVisualMatch(players, mediaUrl) {
        const target = canonicalMediaSource(mediaUrl);
        if (!target) return { kind: "none", selected: null, matches: [] };
        const matches = (Array.isArray(players) ? players : []).filter(player =>
            (Array.isArray(player?.sources) ? player.sources : [])
                .some(source => canonicalMediaSource(source) === target)
        );
        return {
            kind: matches.length ? "exact" : "none",
            selected: matches[0] || null,
            matches
        };
    }

    function qualityHeight(value) {
        const match = String(value || "").match(/(?:^|\D)(\d{2,5})\s*p(?:\D|$)/i);
        return match ? Number(match[1]) || 0 : 0;
    }

    function sortedQualities(values) {
        const unique = new Map();
        for (const value of Array.isArray(values) ? values : []) {
            const height = qualityHeight(value);
            if (height >= 100 && height <= 10000) unique.set(height, `${height}p`);
        }
        return [...unique.entries()].sort((left, right) => right[0] - left[0]).map(([, label]) => label);
    }

    function reconstructByteRangeUrl(value) {
        let url;
        try { url = new URL(String(value || "")); } catch (_error) { return null; }
        const parameters = new Map([...url.searchParams].map(([name, parameter]) => [name.toLowerCase(), { name, parameter }]));
        const startEntry = parameters.get("bytestart");
        const endEntry = parameters.get("byteend");
        if (!startEntry || !endEntry || !/^\d+$/.test(startEntry.parameter) || !/^\d+$/.test(endEntry.parameter)) return null;
        const start = Number(startEntry.parameter);
        const end = Number(endEntry.parameter);
        if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start < 0 || end < start) return null;
        url.searchParams.delete(startEntry.name);
        url.searchParams.delete(endEntry.name);
        return { url: url.href, start, end, span: end - start + 1 };
    }

    function decodeBase64Text(value) {
        const source = String(value || "").replace(/-/g, "+").replace(/_/g, "/");
        if (!source || source.length > 8192) return "";
        try { return atob(source + "=".repeat((4 - source.length % 4) % 4)); } catch (_error) { return ""; }
    }

    /**
     * 站点专用的 CDN 元数据解析（按站点 CDN 域名分流并拼装站点专有分组键）
     * 与站点播放器选择**已移除**（07 §2：扩展源码里禁止出现站点名判断）。
     * 它们归 `adapters/<site>/extension.js`，阶段 3 落地。
     *
     * 保留下来的 `decodeBase64Text` 与 `reconstructByteRangeUrl` 是站点无关的
     * 通用工具：后者识别 `bytestart` / `byteend` 这对传输偏移参数，供固定字节
     * 分片判定使用。
     */

    /**
     * 内容页 URL 推导（站点无关）。
     *
     * 旧版先判断抖音域名并把 `modal_id` 规范成 `/video/<id>`；域名分支已移除。
     * 留下的通用规则：同源、http(s)、路径命中媒体/讨论/故事形态，或路径配一个
     * 已知的内容 ID 查询参数。
     */
    function chooseContentPageUrl(currentValue, linkValues) {
        let current;
        try { current = new URL(String(currentValue || "")); } catch (_error) { return ""; }
        if (!["http:", "https:"].includes(current.protocol)) return "";
        const safeContentQuery = url => {
            const rules = [
                ["v", /^\/watch\/?$/i],
                ["video_id", /^\/(?:watch|video|videos)\/?$/i],
                ["story_fbid", /^\/(?:watch|video|videos|[^/]+)\/?$/i]
            ];
            for (const [name, pathRule] of rules) {
                const value = url.searchParams.get(name) || "";
                if (pathRule.test(url.pathname) && /^[a-z0-9_-]{5,80}$/i.test(value)) return [name, value];
            }
            return null;
        };
        const canonical = value => {
            try {
                const url = new URL(String(value || ""), current.href);
                if (url.origin !== current.origin || !["http:", "https:"].includes(url.protocol)) return null;
                const contentQuery = safeContentQuery(url);
                url.search = "";
                url.hash = "";
                url.pathname = url.pathname.replace(/\/+$/, "") || "/";
                if (contentQuery) url.searchParams.set(contentQuery[0], contentQuery[1]);
                return url;
            } catch (_error) { return null; }
        };
        const mediaPath = /\/(?:p|reel|reels|video|videos|clip|clips|status|post|posts|pin)\/[a-z0-9_-]+$/i;
        const discussionPath = /\/comments\/[a-z0-9_-]+(?:\/[a-z0-9_-]+)?$/i;
        const storyPath = /\/stories\/[a-z0-9_.-]+\/[a-z0-9_-]+$/i;
        const compoundVideoPath = /\/(?:video|clip)[-_][a-z0-9_-]+$/i;
        const isContentUrl = url => mediaPath.test(url.pathname)
            || discussionPath.test(url.pathname)
            || storyPath.test(url.pathname)
            || compoundVideoPath.test(url.pathname)
            || Boolean(safeContentQuery(url));
        const currentCanonical = canonical(current.href);
        if (currentCanonical && isContentUrl(currentCanonical)) return currentCanonical.href;
        const candidates = [...new Set((Array.isArray(linkValues) ? linkValues : [])
            .map(canonical)
            .filter(url => url && isContentUrl(url))
            .map(url => url.href))];
        candidates.sort((left, right) => new URL(left).pathname.length - new URL(right).pathname.length || left.localeCompare(right));
        return candidates[0] || "";
    }

    function chooseNearbyContentPageUrl(currentValue, linkGroups) {
        const currentPage = chooseContentPageUrl(currentValue, []);
        for (const links of Array.isArray(linkGroups) ? linkGroups : []) {
            for (const value of Array.isArray(links) ? links : []) {
                const linkedPage = chooseContentPageUrl(value, []);
                if (linkedPage && linkedPage !== currentPage) return linkedPage;
            }
        }
        return currentPage;
    }

    function chooseStructuredVideoPageUrl(currentValue, signals = {}) {
        let current;
        try { current = new URL(String(currentValue || "")); } catch (_error) { return ""; }
        if (!["http:", "https:"].includes(current.protocol)) return "";
        const ogType = String(signals.ogType || "").trim().toLowerCase();
        const twitterCard = String(signals.twitterCard || "").trim().toLowerCase();
        const validMediaUrl = value => {
            try { return ["http:", "https:"].includes(new URL(String(value || ""), current.href).protocol); }
            catch (_error) { return false; }
        };
        const declaresVideo = /^video(?:[.:]|$)/i.test(ogType)
            || twitterCard === "player"
            || validMediaUrl(signals.playerUrl)
            || validMediaUrl(signals.videoUrl);
        if (!declaresVideo) return "";

        let page;
        try { page = new URL(String(signals.canonicalUrl || current.href), current.href); }
        catch (_error) { page = new URL(current.href); }
        if (page.origin !== current.origin || !["http:", "https:"].includes(page.protocol)) {
            page = new URL(current.href);
        }
        page.hash = "";
        page.search = "";
        page.pathname = page.pathname.replace(/\/+$/, "") || "/";
        if (page.pathname === "/") return "";
        return page.href;
    }

    function selectContentTitle(signals = {}) {
        const fallback = String(signals.fallback || "").replace(/\s+/g, " ").trim().slice(0, 220);
        const noisyExact = /^(?:显示更多|展开|收起|播放|暂停|重播|静音|取消静音|全屏|退出全屏|more|show more|play|pause|replay|mute|unmute|fullscreen)$/i;
        const metric = /^(?:\d{1,2}:\d{2}(?:\s*\/\s*\d{1,2}:\d{2})?|\d+(?:[.,]\d+)?(?:万|亿|[kmb])?|[·•…]+)$/i;
        const relativeTime = /^\d+\s*(?:秒|分钟|小时|天|周|个月|月|年|seconds?|minutes?|hours?|days?|weeks?|months?|years?)(?:前)?$/i;
        const normalize = value => String(value || "")
            .replace(/\s+/g, " ")
            .replace(/\s*(?:显示更多|展开|show more)\s*$/i, "")
            .trim()
            .slice(0, 220);
        const meaningful = value => {
            if (!value || value.length < 2 || noisyExact.test(value) || metric.test(value) || relativeTime.test(value)) return false;
            if (/^@[a-z0-9_.-]{2,80}$/i.test(value)) return false;
            return /[\p{L}\p{N}]/u.test(value);
        };
        const collections = [
            [signals.captions, 4_000],
            [signals.headings, 3_000],
            [signals.lines, 2_000],
            [signals.imageAlts, 1_000]
        ];
        let selected = "";
        let selectedScore = -1;
        for (const [values, base] of collections) {
            for (const raw of Array.isArray(values) ? values : []) {
                const value = normalize(raw);
                if (!meaningful(value)) continue;
                const words = (value.match(/[\p{L}\p{N}]+/gu) || []).length;
                const score = base + Math.min(value.length, 160) * 8 + Math.min(words, 24) * 15;
                if (score > selectedScore) {
                    selected = value;
                    selectedScore = score;
                }
            }
        }
        return selected || fallback || "网页视频";
    }

    function createBoundedScheduler(callback, delayMs = 250, timers = globalThis) {
        let timerId = null;
        const delay = Math.max(0, Number(delayMs) || 0);
        return function schedule() {
            if (timerId !== null) return false;
            timerId = timers.setTimeout(() => {
                timerId = null;
                callback();
            }, delay);
            return true;
        };
    }

    function createKeyedBoundedScheduler(callback, delayMs = 250, timers = globalThis) {
        const timerIds = new Map();
        const delay = Math.max(0, Number(delayMs) || 0);
        return function schedule(key) {
            if (timerIds.has(key)) return false;
            const timerId = timers.setTimeout(() => {
                timerIds.delete(key);
                callback(key);
            }, delay);
            timerIds.set(key, timerId);
            return true;
        };
    }

    function createLocationChangeTracker(initialUrl, callback) {
        let currentUrl = String(initialUrl || "");
        return function updateLocation(nextUrl, context) {
            const normalizedUrl = String(nextUrl || "");
            if (!normalizedUrl || normalizedUrl === currentUrl) return false;
            const previousUrl = currentUrl;
            currentUrl = normalizedUrl;
            callback?.(normalizedUrl, previousUrl, context);
            return true;
        };
    }

    function shouldClearCapturedCandidates(source, autoClearMode) {
        const normalizedSource = String(source || "").toLowerCase();
        const mode = Number(autoClearMode) || 0;
        return (normalizedSource === "committed" && mode === 1)
            || (normalizedSource === "loading" && mode === 2);
    }

    async function ensureContentDiscovery(chromeApi, tab) {
        const tabId = Number(tab?.id);
        const tabUrl = String(tab?.url || "");
        if (!Number.isInteger(tabId) || tabId < 0 || !/^https?:\/\//i.test(tabUrl)) {
            return { ready: false, injected: false, reason: "unsupported_tab" };
        }
        const scan = async () => {
            try {
                const response = await chromeApi.tabs.sendMessage(tabId, { Message: "discoverPageResolvers" });
                return Boolean(response?.ok);
            } catch (_error) {
                return false;
            }
        };
        if (await scan()) return { ready: true, injected: false };
        if (!chromeApi?.scripting?.executeScript) {
            return { ready: false, injected: false, reason: "scripting_unavailable" };
        }
        try {
            await chromeApi.scripting.executeScript({
                target: { tabId, allFrames: false },
                files: ["js/eagle-bridge-candidate-logic.js", "js/content-script.js"]
            });
        } catch (_error) {
            return { ready: false, injected: false, reason: "injection_failed" };
        }
        return { ready: await scan(), injected: true };
    }

    function parseManifestQualities(source, kind = "") {
        const text = String(source || "");
        if (!text || text.length > 2_000_000) return [];
        const manifestKind = String(kind || "").toLowerCase();
        const heights = [];
        if (manifestKind.includes("mpd") || /<MPD(?:\s|>)/i.test(text)) {
            for (const match of text.matchAll(/\bheight\s*=\s*["'](\d{2,5})["']/gi)) {
                heights.push(`${match[1]}p`);
            }
        } else {
            for (const match of text.matchAll(/\bRESOLUTION\s*=\s*(\d{2,5})\s*[x×]\s*(\d{2,5})/gi)) {
                heights.push(`${match[2]}p`);
            }
        }
        return sortedQualities(heights);
    }

    /**
     * 站点播放器配置解析（Vimeo `window.playerConfig`）**已移除**（07 §2）。
     * 该逻辑按站点脚本标记与站点 CDN 结构解析播放器清单，归
     * `adapters/vimeo/extension.js`，阶段 3 落地。
     */

    function selectThumbnail(sources = {}) {
        for (const kind of ["exact", "playing", "visible", "nearby", "metadata"]) {
            const values = Array.isArray(sources[kind]) ? sources[kind] : [];
            for (const value of values) {
                const url = safeThumbnailUrl(value);
                if (!url) continue;
                if (kind === "metadata" && SITE_ICON.test(new URL(url).pathname)) continue;
                return url;
            }
        }
        return "";
    }

    function waitForSnapshot(isReady, readSnapshot, timeoutMs = 2000, pollMs = 20) {
        return new Promise(resolve => {
            const started = Date.now();
            const check = () => {
                if (isReady() || Date.now() - started >= timeoutMs) {
                    resolve(readSnapshot());
                    return;
                }
                setTimeout(check, pollMs);
            };
            check();
        });
    }

    function boundedMediaSnapshot(data, perTabLimit = 400, totalLimit = 1000) {
        const perTab = Math.max(1, Math.floor(Number(perTabLimit)) || 400);
        const total = Math.max(1, Math.floor(Number(totalLimit)) || 1000);
        const rows = [];
        let sequence = 0;
        for (const [tabId, items] of Object.entries(data && typeof data === "object" ? data : {})) {
            if (!Array.isArray(items)) continue;
            for (const item of items.slice(-perTab)) {
                if (!item || typeof item !== "object") continue;
                rows.push({
                    tabId,
                    item,
                    capturedAt: Number(item.getTime || item.capturedAt || item.timestamp || 0),
                    sequence: sequence++
                });
            }
        }
        const selected = new Set([...rows]
            .sort((left, right) => left.capturedAt - right.capturedAt || left.sequence - right.sequence)
            .slice(-total));
        const snapshot = {};
        for (const row of rows) {
            if (!selected.has(row)) continue;
            (snapshot[row.tabId] ??= []).push(row.item);
        }
        return snapshot;
    }

    // ——— 站点适配器（[14] 的 L1 声明式引擎）———
    // 本段是**通用代码**：不含任何站点名。它把 adapters/<id>/adapter.json 里声明的
    // 匹配范围、ID 来源、页面地址规范化、标题模板、主播放器选择与源约束，
    // 变成扩展侧可以执行的行为（契约 A-101、A-102、T-ADP-01）。

    const ADAPTER_TITLE_LIMIT = 220;
    const ADAPTER_ID_FALLBACK_SELECTION = "all";
    const ADAPTER_PRIMARY_CURRENT = "current-player";
    const ADAPTER_PRIMARY_FIRST = "first";
    const ADAPTER_PRIMARY_ALL = "all";

    function normalizeAdapterHost(value) {
        let host = String(value || "").trim().toLowerCase();
        if (host.startsWith("[")) {
            const bracket = host.indexOf("]");
            return bracket > 0 ? host.slice(1, bracket) : "";
        }
        const colon = host.lastIndexOf(":");
        if (colon > 0 && /^\d+$/.test(host.slice(colon + 1))) host = host.slice(0, colon);
        return host.replace(/\.+$/, "");
    }

    function matchAdapterHostPattern(pattern, host) {
        const rule = String(pattern || "").trim().toLowerCase();
        if (!rule || !host) return { exact: false, ok: false };
        if (rule.startsWith("*.")) {
            const suffix = rule.slice(2);
            // `*.example.com` 只覆盖子域，不含裸域（[14 §5.3] 的匹配语义）
            return { exact: false, ok: Boolean(suffix) && host.endsWith(`.${suffix}`) };
        }
        return { exact: true, ok: rule === host };
    }

    function matchAdapter(adapters, host) {
        const target = normalizeAdapterHost(host);
        if (!target) return null;
        let chosen = null;
        let chosenExact = false;
        let chosenPriority = -Infinity;
        let fallback = null;
        for (const adapter of Array.isArray(adapters) ? adapters : []) {
            if (!adapter || typeof adapter !== "object") continue;
            if (adapter.id === "generic") {
                fallback = fallback || adapter;
                continue;
            }
            const match = adapter.match && typeof adapter.match === "object" ? adapter.match : {};
            const priority = Number(match.priority) || 0;
            let matched = false;
            let exact = false;
            for (const pattern of Array.isArray(match.hosts) ? match.hosts : []) {
                const result = matchAdapterHostPattern(pattern, target);
                if (!result.ok) continue;
                matched = true;
                exact = exact || result.exact;
            }
            if (!matched) continue;
            // 优先级降序；同优先级下精确域名优先于通配；同级同等精确度保留先遇到的
            if (priority > chosenPriority || (priority === chosenPriority && exact && !chosenExact)) {
                chosen = adapter;
                chosenExact = exact;
                chosenPriority = priority;
            }
        }
        return chosen || fallback;
    }

    function expandAdapterId(expression, groups) {
        const source = Array.isArray(groups) ? groups : [];
        let failed = false;
        const rendered = String(expression === undefined || expression === null ? "" : expression)
            .replace(/\$([0-9])/g, (_all, digit) => {
                const value = source[Number(digit)];
                if (value === undefined || value === null) {
                    failed = true;
                    return "";
                }
                return String(value);
            });
        if (failed) return "";
        return rendered;
    }

    function adapterRulesOf(adapter) {
        const identity = adapter && adapter.identity;
        return identity && Array.isArray(identity.urlRules) ? identity.urlRules : [];
    }

    function adapterIdFromUrl(rules, value) {
        let parsed = null;
        try {
            parsed = new URL(String(value || ""));
        } catch (_error) {
            return "";
        }
        for (const rule of rules) {
            if (!rule || typeof rule !== "object") continue;
            if (rule.path) {
                let matched = null;
                try {
                    matched = parsed.pathname.match(new RegExp(rule.path));
                } catch (_error) {
                    matched = null;
                }
                if (matched) return expandAdapterId(rule.id, matched);
                continue;
            }
            if (!rule.query) continue;
            const raw = parsed.searchParams.get(String(rule.query));
            if (raw === null) continue;
            if (rule.pattern) {
                let allowed = false;
                try {
                    allowed = new RegExp(rule.pattern).test(raw);
                } catch (_error) {
                    allowed = false;
                }
                if (!allowed) continue;
            }
            return expandAdapterId(rule.id, [raw]);
        }
        return "";
    }

    function adapterIdFromText(rules, value) {
        const text = String(value === undefined || value === null ? "" : value);
        if (!text) return "";
        for (const rule of rules) {
            if (!rule || typeof rule !== "object" || !rule.pattern) continue;
            let matched = null;
            try {
                matched = text.match(new RegExp(rule.pattern));
            } catch (_error) {
                matched = null;
            }
            if (matched) return expandAdapterId(rule.id, matched);
        }
        return "";
    }

    function videoIdFromAdapterSignals(adapter, input = {}) {
        const rules = adapterRulesOf(adapter);
        if (!rules.length) return "";
        const pageUrl = String(input.pageUrl || "");
        // 信号优先于地址栏：抖音这类信息流的地址会随滚动变化，正在播的那个才是目标
        // （[14 §10] 的「既不能靠当前地址、也不能靠第一个 video」）。
        for (const signal of Array.isArray(input.signals) ? input.signals : []) {
            const text = String(signal === undefined || signal === null ? "" : signal).trim();
            if (!text) continue;
            const fromUrl = /^https?:/i.test(text) ? adapterIdFromUrl(rules, text) : "";
            if (fromUrl) return fromUrl;
            // `pattern` 形态的规则（含"整串就是 ID"与"从类名等文本里抽 ID"两种用法）
            const fromText = adapterIdFromText(rules, text);
            if (fromText) return fromText;
        }
        if (!pageUrl) return "";
        const fromPage = adapterIdFromUrl(rules, pageUrl);
        if (fromPage) return fromPage;
        return adapterIdFromText(rules, pageUrl);
    }

    function adapterRequiresId(adapter) {
        return Boolean(adapter && adapter.identity && adapter.identity.requireId === true);
    }

    function canonicalPageUrlFromAdapter(adapter, videoId, fallbackUrl) {
        const canonical = String((adapter && adapter.identity && adapter.identity.canonical) || "");
        const id = String(videoId || "").trim();
        if (canonical && id) return canonical.replace(/\{id\}/g, id);
        return String(fallbackUrl || "");
    }

    function cleanAdapterText(value) {
        return String(value === undefined || value === null ? "" : value)
            .replace(/\s+/g, " ")
            .replace(/展开\s*$/u, "")
            .trim();
    }

    function cleanAdapterTitle(value) {
        return String(value === undefined || value === null ? "" : value)
            .replace(/\s+/g, " ")
            .replace(/^[\s\-·|]+/u, "")
            .replace(/[\s\-·|]+$/u, "");
    }

    function adapterCandidateTitle(adapter, input = {}) {
        const title = (adapter && adapter.title) || {};
        const values = {
            nickname: cleanAdapterText(input.nickname),
            description: cleanAdapterText(input.description),
            id: String(input.videoId || "").trim()
        };
        const template = String(title.template || "");
        if (template) {
            const rendered = cleanAdapterTitle(template.replace(/\{(nickname|description|id)\}/g,
                (_all, key) => values[key] || ""));
            if (rendered) return rendered.slice(0, ADAPTER_TITLE_LIMIT);
        }
        const fallback = cleanAdapterTitle(String(title.fallback || "").replace(/\{id\}/g, values.id));
        return fallback.slice(0, ADAPTER_TITLE_LIMIT);
    }

    function adapterPrimarySelection(adapter) {
        const capture = (adapter && adapter.capture) || {};
        const value = String(capture.primarySelection || "");
        return [ADAPTER_PRIMARY_CURRENT, ADAPTER_PRIMARY_FIRST, ADAPTER_PRIMARY_ALL].includes(value)
            ? value : ADAPTER_ID_FALLBACK_SELECTION;
    }

    function adapterVisibleArea(video) {
        const rect = (video && video.rect) || {};
        const width = Number(rect.width) || 0;
        const height = Number(rect.height) || 0;
        if (!(width > 0) || !(height > 0)) return 0;
        const viewport = (video && video.viewport) || {};
        const viewportWidth = Number(viewport.width) || 0;
        const viewportHeight = Number(viewport.height) || 0;
        if (!(viewportWidth > 0) || !(viewportHeight > 0)) return width * height;
        const left = Math.max(0, Number(rect.left) || 0);
        const top = Math.max(0, Number(rect.top) || 0);
        const right = Math.min(viewportWidth, left + width);
        const bottom = Math.min(viewportHeight, top + height);
        return Math.max(0, right - left) * Math.max(0, bottom - top);
    }

    function primaryVideoScore(video) {
        if (!video || typeof video !== "object") return -1;
        const readyState = Number(video.readyState) || 0;
        const duration = Number(video.duration) || 0;
        const area = adapterVisibleArea(video);
        if (readyState < 2 || !(duration > 0) || !(area > 0)) return -1;
        const width = Number((video.rect || {}).width) || 0;
        const height = Number((video.rect || {}).height) || 0;
        const playing = video.paused === false && video.ended !== true;
        const progressed = Number(video.currentTime) > 0.05;
        return (playing ? 4e12 : 0)
            + (progressed ? 2e12 : 0)
            + area * 1000
            + Math.min(width * height, 1e9)
            + Math.min(duration, 86400);
    }

    function adapterVideoIndex(video, position) {
        const declared = Number(video && video.index);
        return Number.isInteger(declared) && declared >= 0 ? declared : position;
    }

    function selectPrimaryVideoIndex(adapter, videos) {
        const items = Array.isArray(videos) ? videos : [];
        const selection = adapterPrimarySelection(adapter);
        if (selection === ADAPTER_PRIMARY_ALL) return -1;
        let bestPosition = -1;
        let bestScore = -1;
        for (let position = 0; position < items.length; position += 1) {
            const score = primaryVideoScore(items[position]);
            if (score < 0) continue;
            if (selection === ADAPTER_PRIMARY_FIRST) return adapterVideoIndex(items[position], position);
            if (score > bestScore) {
                bestScore = score;
                bestPosition = position;
            }
        }
        return bestPosition < 0 ? -1 : adapterVideoIndex(items[bestPosition], bestPosition);
    }

    function adapterAllowsCapture(adapter, input = {}) {
        const capture = (adapter && adapter.capture) || {};
        const hasBlob = input.hasBlobSource === true;
        const hasDirect = input.hasDirectSource === true;
        if (capture.requireBlobSource === true) return hasBlob;
        if (hasBlob) return true;
        return capture.allowDirectStream === true && hasDirect;
    }

    return {
        safeThumbnailUrl,
        safeFrameDataUrl,
        stableVisualKey,
        resolveVisualMatch,
        reconstructByteRangeUrl,
        chooseContentPageUrl,
        chooseNearbyContentPageUrl,
        chooseStructuredVideoPageUrl,
        selectContentTitle,
        createBoundedScheduler,
        createKeyedBoundedScheduler,
        createLocationChangeTracker,
        shouldClearCapturedCandidates,
        ensureContentDiscovery,
        parseManifestQualities,
        selectThumbnail,
        waitForSnapshot,
        boundedMediaSnapshot,
        normalizeAdapterHost,
        matchAdapter,
        videoIdFromAdapterSignals,
        adapterRequiresId,
        canonicalPageUrlFromAdapter,
        adapterCandidateTitle,
        adapterPrimarySelection,
        primaryVideoScore,
        selectPrimaryVideoIndex,
        adapterAllowsCapture
    };
});
