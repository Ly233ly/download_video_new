/**
 * 留底浏览器扩展 · 运行时配置与全局状态。
 *
 * 本文件取代旧版的 `js/init.js` 与 `js/polyfill.js`：
 *   - 第一版只支持 Chromium 114+（`sidePanel` 与 `side_panel` 的下限），
 *     因此不再做 Firefox 能力探测，也不再提供侧边栏 API 的降级垫片；
 *   - 捕获选项的本地副本从 `chrome.storage.sync` 迁到 `chrome.storage.local`
 *     （07 §3：跨 Worker 生命周期且需持久化的状态放 `local`）；
 *   - 删除增强发现脚本清单（`catch-script/search.js` 已取消，站点专用脚本的
 *     归处是 `adapters/<site>/extension.js`，见 07 §2 与阶段 3）。
 *
 * 本文件不承载任何契约逻辑：只提供配置、缓存容器与选项编译。
 */

var G = {
    initSyncComplete: false,
    initLocalComplete: true,
    initMediaComplete: false,
    blockUrlSet: new Set(),
};

// 候选快照：仅内存 + chrome.storage.session，绝不进入 local/sync。
var cacheData = { init: true };

G.tabId = -1;

G.OptionLists = {
    Ext: [
        "flv", "hlv", "f4v", "mp4", "mp3", "wma", "wav", "m4a", "webm",
        "ogg", "ogv", "mov", "mkv", "m4s", "m3u8", "m3u", "mpeg", "avi",
        "wmv", "asf", "movie", "divx", "mpeg4", "vid", "aac", "mpd", "weba", "opus"
    ].map(ext => ({ ext, size: 0, operator: ">=", unit: "KB", state: true })),
    Type: [
        "audio/*", "video/*", "application/ogg", "application/vnd.apple.mpegurl",
        "application/x-mpegurl", "application/mpegurl", "application/octet-stream-m3u8",
        "application/dash+xml", "application/m4s"
    ].map(type => ({ type, size: 0, operator: ">=", unit: "KB", state: true })),
    // 正则规则默认全部关闭（`state: false`）。旧版在这里预置了若干站点 CDN
    // 形态的规则（B 站直播分片、Instagram/Facebook 字节分片）。按 07 §2，
    // 站点专用规则不随扩展包分发——它们归 `adapters/<site>/extension.js`，
    // 阶段 3 落地。这里保留**空数组**，规则完全由用户在选项里自行添加。
    Regex: [],
    autoClearMode: 1,
    checkDuplicates: true,
    enable: true,
    badgeNumber: true,
    blockUrl: [],
    blockUrlWhite: false,
    maxLength: 9999,
    sidePanel: false,
    deepSearch: false,
};

// 第一版只支持 Chromium 系；不再按 UA 探测 Firefox。
G.version = Number.parseInt(
    String(navigator.userAgent).match(/(?:Chrome|Chromium|Edg)\/(\d+)/)?.[1] || "114",
    10
);

G.CAPTURE_OPTIONS_KEY = "captureOptions";

function compileRangeMap(items, keyName) {
    return new Map(items.map(source => {
        const item = { ...source, operator: source.operator ?? ">=" };
        if (item.operator === "~") {
            const [minimum, maximum] = String(item.size || "").split("-");
            item.min = minimum ? Number.parseInt(minimum, 10) : 0;
            item.max = maximum ? Number.parseInt(maximum, 10) : 0;
        }
        return [item[keyName], item];
    }));
}

function compileRegexRules(items) {
    return items.map(source => {
        const item = { ...source };
        let regex;
        if (!isSafeRegularExpression(item.regex)) item.state = false;
        else {
            try { regex = new RegExp(item.regex, item.type); }
            catch (_error) { item.state = false; }
        }
        return { regex, ext: item.ext, blackList: item.blackList, state: item.state };
    });
}

function compileBlockedUrls(items) {
    return items.map(item => ({ url: wildcardToRegex(item.url), state: item.state }));
}

function applyOptions(items) {
    items.Ext = compileRangeMap(items.Ext, "ext");
    items.Type = compileRangeMap(items.Type, "type");
    items.Regex = compileRegexRules(items.Regex);
    items.blockUrl = compileBlockedUrls(items.blockUrl);
    G = { ...items, ...G };
    chrome.sidePanel?.setPanelBehavior?.({ openPanelOnActionClick: Boolean(items.sidePanel) });
    chrome.action.setIcon({ path: "/icons/icon-128.png" });
    G.initSyncComplete = true;
}

function InitOptions() {
    loadMediaData(function (items) {
        cacheData = items.MediaData?.init ? {} : (items.MediaData || {});
        G.initMediaComplete = true;
    });
    chrome.storage.local.get(G.CAPTURE_OPTIONS_KEY, function (stored) {
        const items = chrome.runtime.lastError
            ? { ...G.OptionLists }
            : { ...G.OptionLists, ...(stored?.[G.CAPTURE_OPTIONS_KEY] || {}) };
        applyOptions(items);
        chrome.tabs.query({}, function (tabs) {
            for (const tab of tabs) {
                if (!tab.url) continue;
                if (isLockUrl(tab.url)) G.blockUrlSet.add(tab.id);
            }
        });
    });
}

InitOptions();

chrome.storage.onChanged.addListener(function (changes) {
    if (changes.MediaData) {
        if (changes.MediaData.newValue?.init) cacheData = {};
        return;
    }
    const options = changes[G.CAPTURE_OPTIONS_KEY];
    if (!options) return;
    const value = options.newValue || {};
    if (value.Ext) G.Ext = compileRangeMap(value.Ext, "ext");
    if (value.Type) G.Type = compileRangeMap(value.Type, "type");
    if (value.Regex) G.Regex = compileRegexRules(value.Regex);
    if (value.blockUrl) G.blockUrl = compileBlockedUrls(value.blockUrl);
    if (value.sidePanel !== undefined) {
        chrome.sidePanel?.setPanelBehavior?.({ openPanelOnActionClick: Boolean(value.sidePanel) });
    }
    for (const [key, item] of Object.entries(value)) {
        if (["Ext", "Type", "Regex", "blockUrl", "sidePanel"].includes(key)) continue;
        G[key] = item;
    }
});

function wildcardToRegex(urlPattern) {
    const regexPattern = String(urlPattern || "")
        .replace(/[.+^${}()|[\]\\]/g, "\\$&")
        .replace(/\*/g, ".*")
        .replace(/\?/g, ".");
    return new RegExp(`^${regexPattern}$`, "i");
}
