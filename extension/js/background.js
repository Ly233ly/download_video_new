/**
 * MV3 Service Worker 加载入口。
 *
 * 第一版只支持 Chromium 114+（07 §10 `E3`）：不再有 Firefox 的
 * `background.scripts` 加载路径，也不再有 `js/polyfill.js` 垫片。
 * 旧版 `bootstrap.js`（一次性配对凭据）随 07 §7 `AD-1` 删除。
 */
if (typeof G === 'undefined') {
    importScripts("/js/function.js", "/js/init.js");
}
if (typeof EagleBridgeCandidateLogic === 'undefined') {
    importScripts("/js/eagle-bridge-candidate-logic.js");
}
if (typeof EagleBridgeUILogic === 'undefined') {
    importScripts("/js/eagle-bridge-ui-logic.js");
}
if (typeof EagleBridgeAuthLogic === 'undefined') {
    importScripts("/js/eagle-bridge-auth-logic.js");
}
if (typeof eagleBridgeCandidate === 'undefined') {
    importScripts("/js/eagle-bridge.js");
}

// Service Worker 5分钟后会强制终止扩展
// https://bugs.chromium.org/p/chromium/issues/detail?id=1271154
// https://stackoverflow.com/questions/66618136/persistent-service-worker-in-chrome-extension/70003493#70003493
chrome.webNavigation.onBeforeNavigate.addListener(function (details) {
    if (details?.frameId === 0 && Number.isInteger(details.tabId)) {
        // 顶层文档边界开始：丢弃第一方请求上下文。
        resolverRequestContextByTab.delete(details.tabId);
        clearCapturedTabForNavigation(details.tabId, "loading");
    }
});
chrome.webNavigation.onHistoryStateUpdated.addListener(function (details) {
    if (details?.frameId === 0) handleTabLocationChange(details.tabId, details.url, "history");
});
if (chrome.webNavigation.onReferenceFragmentUpdated) {
    chrome.webNavigation.onReferenceFragmentUpdated.addListener(function (details) {
        if (details?.frameId === 0) handleTabLocationChange(details.tabId, details.url, "fragment");
    });
}
chrome.runtime.onConnect.addListener(function (Port) {
    if (chrome.runtime.lastError || Port.name !== "HeartBeat") return;
    Port.postMessage("HeartBeat");
    Port.onMessage.addListener(function (message, Port) { return; });
    const interval = setInterval(function () {
        clearInterval(interval);
        Port.disconnect();
    }, 250000);
    Port.onDisconnect.addListener(function () {
        interval && clearInterval(interval);
        if (chrome.runtime.lastError) { return; }
    });
});
setInterval(chrome.runtime.getPlatformInfo, 25 * 1000);

// 全局变量
let debounce = undefined;
let debounceCount = 0;
let debounceTime = 0;
const reFilename = /filename="?([^"]+)"?/;
const mediaFramePreviewCache = new Map();
// 第一方请求上下文：仅供桌面端的 `page` 解析器复用当前页面的会话，值只存在于
// Service Worker 内存、按标签导航或关闭清空，**不落盘**（07 §3）。
// 注：按站点区分"哪一类请求才算第一方会话请求"属于站点专用逻辑，已按 07 §2
// 移出扩展包（阶段 3 落到 `adapters/<site>/extension.js`）。
const resolverRequestContextByTab = new Map();
// Invalidates asynchronous discovery work that was already in flight when a
// user clears a tab. Without this generation guard, a late visual/manifest
// callback can append the just-cleared item back into the list.
const captureGenerationByTab = new Map();
// Top-level page identity is independent from the document lifecycle because
// single-page applications can replace their content without reloading.
const lastPageUrlByTab = new Map();
const pendingPageLocationByTab = new Map();
const pageLocationInitializationWaitByTab = new Map();
// A cold Manifest V3 worker can receive navigation events before its option
// and media snapshots have finished loading. Preserve the document boundary
// until the snapshot is authoritative, then clear before accepting new media.
const pendingDocumentCleanupByTab = new Map();
let lastActiveWebTabId = 0;
const MAX_MANIFEST_CATALOG_BYTES = 2_000_000;

async function readBoundedManifestText(response) {
    const declaredLength = Number(response.headers.get("content-length") || 0);
    if (declaredLength > MAX_MANIFEST_CATALOG_BYTES) return "";
    if (!response.body?.getReader) {
        const text = await response.text();
        return text.length <= MAX_MANIFEST_CATALOG_BYTES ? text : "";
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let text = "";
    let bytes = 0;
    while (true) {
        const chunk = await reader.read();
        if (chunk.done) break;
        bytes += chunk.value.byteLength;
        if (bytes > MAX_MANIFEST_CATALOG_BYTES) {
            await reader.cancel().catch(function () { return; });
            return "";
        }
        text += decoder.decode(chunk.value, { stream: true });
    }
    return text + decoder.decode();
}

async function enrichManifestQualities(info) {
    if (Array.isArray(info?.availableQualities) && info.availableQualities.length) return;
    const extension = String(info?.ext || "").toLowerCase();
    const type = String(info?.type || "").toLowerCase();
    const manifestKind = ["m3u8", "m3u", "mpd"].includes(extension)
        ? extension
        : type.includes("dash+xml") ? "mpd"
            : type.includes("mpegurl") ? "m3u8" : "";
    if (!manifestKind || !/^https?:\/\//i.test(String(info?.url || ""))) return;
    const controller = new AbortController();
    const timeout = setTimeout(function () { controller.abort(); }, 4000);
    try {
        const headers = {};
        for (const [key, value] of Object.entries(info.requestHeaders || {})) {
            if (String(key).toLowerCase() === "authorization" && typeof value === "string") {
                headers.Authorization = value;
            }
        }
        const response = await fetch(info.url, {
            cache: "no-store",
            credentials: "include",
            headers,
            signal: controller.signal
        });
        if (!response.ok) return;
        const text = await readBoundedManifestText(response);
        const qualities = EagleBridgeCandidateLogic.parseManifestQualities(text, manifestKind);
        if (!qualities.length) return;
        info.availableQualities = qualities;
        info.qualitySource = "manifest_catalog";
        info.label = `最高 ${qualities[0]}`;
    } catch (_error) {
        return;
    } finally {
        clearTimeout(timeout);
    }
}

function mediaFramePreviewKey(tabId, groupKey) {
    return `${Number(tabId) || 0}:${String(groupKey || "")}`;
}

function rememberMediaFrame(tabId, groupKey, value) {
    const frame = globalThis.EagleBridgeCandidateLogic.safeFrameDataUrl(value);
    if (!frame || !groupKey) return "";
    mediaFramePreviewCache.set(mediaFramePreviewKey(tabId, groupKey), {
        dataUrl: frame,
        updatedAt: Date.now()
    });
    if (mediaFramePreviewCache.size > 48) {
        const oldest = [...mediaFramePreviewCache.entries()]
            .sort((left, right) => left[1].updatedAt - right[1].updatedAt)
            .slice(0, mediaFramePreviewCache.size - 48);
        oldest.forEach(([key]) => mediaFramePreviewCache.delete(key));
    }
    return frame;
}

function mediaFramesForTab(tabId) {
    const prefix = `${Number(tabId) || 0}:`;
    const frames = {};
    for (const [key, value] of mediaFramePreviewCache.entries()) {
        if (!key.startsWith(prefix)) continue;
        frames[key.slice(prefix.length)] = value.dataUrl;
    }
    return frames;
}

function invalidateCapturedTabContext(tabId, resetDedupe = false) {
    const normalizedTabId = Number(tabId) || 0;
    captureGenerationByTab.set(
        normalizedTabId,
        (captureGenerationByTab.get(normalizedTabId) || 0) + 1
    );
    if (resetDedupe) G.urlMap.delete(normalizedTabId);
    resolverRequestContextByTab.delete(normalizedTabId);
    const prefix = `${normalizedTabId}:`;
    for (const key of mediaFramePreviewCache.keys()) {
        if (key.startsWith(prefix)) mediaFramePreviewCache.delete(key);
    }
}

function clearCapturedTab(tabId, resetDedupe = false) {
    const normalizedTabId = Number(tabId) || 0;
    invalidateCapturedTabContext(normalizedTabId, resetDedupe);
    delete cacheData[normalizedTabId];
}

function applyDocumentNavigationCleanup(tabId, sources) {
    const normalizedTabId = Number(tabId) || 0;
    if (normalizedTabId <= 0) return false;
    const shouldClear = [...sources].some(source => (
        globalThis.EagleBridgeCandidateLogic.shouldClearCapturedCandidates(
            source,
            G.autoClearMode
        )
    ));
    if (!shouldClear) return false;
    clearCapturedTab(normalizedTabId, true);
    saveMediaData(cacheData);
    SetIcon({ tabId: normalizedTabId });
    return true;
}

function clearCapturedTabForNavigation(tabId, source) {
    const normalizedTabId = Number(tabId) || 0;
    if (normalizedTabId <= 0) return false;
    const sources = pendingDocumentCleanupByTab.get(normalizedTabId) || new Set();
    sources.add(String(source || ""));
    if (!G.initSyncComplete || !G.initMediaComplete) {
        pendingDocumentCleanupByTab.set(normalizedTabId, sources);
        return false;
    }
    pendingDocumentCleanupByTab.delete(normalizedTabId);
    return applyDocumentNavigationCleanup(normalizedTabId, sources);
}

function flushPendingDocumentCleanup(tabId) {
    const normalizedTabId = Number(tabId) || 0;
    const sources = pendingDocumentCleanupByTab.get(normalizedTabId);
    if (!sources || !G.initSyncComplete || !G.initMediaComplete) return false;
    pendingDocumentCleanupByTab.delete(normalizedTabId);
    return applyDocumentNavigationCleanup(normalizedTabId, sources);
}

function sendRuntimeNotice(message) {
    try {
        chrome.runtime.sendMessage(message, function () { void chrome.runtime.lastError; });
    } catch (_error) { /* No extension view is currently open. */ }
}

function announceTabLocationChanged(tabId, url, source) {
    sendRuntimeNotice({ Message: "tabLocationChanged", tabId, url, source });
}

function applyTabLocationChange(tabId, url, source = "history") {
    const normalizedTabId = Number(tabId) || 0;
    const normalizedUrl = String(url || "");
    if (normalizedTabId <= 0 || isSpecialPage(normalizedUrl)) return false;
    if (lastPageUrlByTab.get(normalizedTabId) === normalizedUrl) return false;
    lastPageUrlByTab.set(normalizedTabId, normalizedUrl);

    if (globalThis.EagleBridgeCandidateLogic.shouldClearCapturedCandidates(
        source,
        G.autoClearMode
    )) {
        clearCapturedTab(normalizedTabId, true);
        saveMediaData(cacheData);
        SetIcon({ tabId: normalizedTabId });
    } else {
        // SPA route changes select another work inside the same document. Keep
        // earlier candidates while allowing the new route to rediscover URLs.
        invalidateCapturedTabContext(normalizedTabId, true);
    }

    try {
        chrome.tabs.sendMessage(
            normalizedTabId,
            { Message: "pageLocationChanged", url: normalizedUrl, source },
            function () { void chrome.runtime.lastError; }
        );
    } catch (_error) { /* The new document may not have a receiver yet. */ }
    announceTabLocationChanged(normalizedTabId, normalizedUrl, source);
    return true;
}

function handleTabLocationChange(tabId, url, source = "history") {
    const normalizedTabId = Number(tabId) || 0;
    const normalizedUrl = String(url || "");
    if (normalizedTabId <= 0 || isSpecialPage(normalizedUrl)) return false;
    if (G.initSyncComplete && G.initMediaComplete) {
        return applyTabLocationChange(normalizedTabId, normalizedUrl, source);
    }

    pendingPageLocationByTab.set(normalizedTabId, {
        tabId: normalizedTabId,
        url: normalizedUrl,
        source
    });
    if (!pageLocationInitializationWaitByTab.has(normalizedTabId)) {
        const wait = globalThis.EagleBridgeCandidateLogic.waitForSnapshot(
            () => Boolean(G.initSyncComplete && G.initMediaComplete),
            () => true,
            5000
        ).then(() => {
            pageLocationInitializationWaitByTab.delete(normalizedTabId);
            const pending = pendingPageLocationByTab.get(normalizedTabId);
            pendingPageLocationByTab.delete(normalizedTabId);
            if (pending) applyTabLocationChange(pending.tabId, pending.url, pending.source);
        });
        pageLocationInitializationWaitByTab.set(normalizedTabId, wait);
    }
    return true;
}

function isSupportedWebTab(tab) {
    return Number.isInteger(Number(tab?.id))
        && Number(tab.id) > 0
        && /^https?:\/\//i.test(String(tab?.url || ""));
}

function rememberActiveWebTab(tab, announce = true) {
    if (!isSupportedWebTab(tab)) return false;
    const changed = Number(tab.id) !== lastActiveWebTabId;
    lastActiveWebTabId = Number(tab.id);
    G.tabId = lastActiveWebTabId;
    if (announce && changed) {
        sendRuntimeNotice({
            Message: "activeWebTabChanged",
            tabId: lastActiveWebTabId,
            url: String(tab.url || "")
        });
    }
    return true;
}

async function getTabIfSupported(tabId) {
    const normalizedTabId = Number(tabId) || 0;
    if (normalizedTabId <= 0) return null;
    try {
        const tab = await chrome.tabs.get(normalizedTabId);
        return isSupportedWebTab(tab) ? tab : null;
    } catch (_error) {
        return null;
    }
}

async function resolveActiveWebTab(preferredTabId = 0) {
    for (const tabId of [lastActiveWebTabId, Number(preferredTabId) || 0]) {
        const tab = await getTabIfSupported(tabId);
        if (tab) return tab;
    }
    const activeTabs = await chrome.tabs.query({ active: true }).catch(() => []);
    const candidates = activeTabs
        .filter(isSupportedWebTab)
        .sort((left, right) => Number(right.lastAccessed || 0) - Number(left.lastAccessed || 0));
    if (candidates[0]) {
        rememberActiveWebTab(candidates[0], false);
        return candidates[0];
    }
    return null;
}

function getMediaVisualContext(tabId, frameId, mediaUrl) {
    return new Promise(resolve => {
        let settled = false;
        const finish = value => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            resolve(value && typeof value === "object" ? value : {});
        };
        const timer = setTimeout(() => finish({}), 450);
        try {
            chrome.tabs.sendMessage(
                tabId,
                { Message: "getMediaVisualContext", url: mediaUrl },
                { frameId: Number.isInteger(frameId) && frameId >= 0 ? frameId : 0 },
                response => {
                    if (chrome.runtime.lastError) { finish({}); return; }
                    finish(response);
                }
            );
        } catch (_error) {
            finish({});
        }
    });
}

function getEmbeddingFrameRect(tabId, frameUrl) {
    return new Promise(resolve => {
        const timer = setTimeout(() => resolve(null), 450);
        try {
            chrome.tabs.sendMessage(tabId, { Message: "getEmbeddingFrameRect", frameUrl }, { frameId: 0 }, response => {
                clearTimeout(timer);
                if (chrome.runtime.lastError) { resolve(null); return; }
                resolve(response && typeof response === "object" ? response : null);
            });
        } catch (_error) {
            clearTimeout(timer);
            resolve(null);
        }
    });
}

function captureVisibleTab(windowId) {
    return new Promise(resolve => {
        try {
            chrome.tabs.captureVisibleTab(windowId, { format: "jpeg", quality: 68 }, dataUrl => {
                if (chrome.runtime.lastError) { resolve(""); return; }
                resolve(typeof dataUrl === "string" ? dataUrl : "");
            });
        } catch (_error) {
            resolve("");
        }
    });
}

async function blobToDataUrl(blob) {
    const bytes = new Uint8Array(await blob.arrayBuffer());
    let binary = "";
    for (let offset = 0; offset < bytes.length; offset += 0x8000) {
        binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
    }
    return `data:${blob.type || "image/jpeg"};base64,${btoa(binary)}`;
}

async function captureVisibleVideoFrame(webInfo, frameId, visualContext) {
    if (!webInfo?.active || !Number.isInteger(webInfo.windowId)) return "";
    let rect = visualContext?.captureRect;
    if (Number.isInteger(frameId) && frameId > 0 && visualContext?.frameUrl) {
        rect = await getEmbeddingFrameRect(webInfo.id, visualContext.frameUrl) || rect;
    }
    const viewportWidth = Number(rect?.viewportWidth);
    const viewportHeight = Number(rect?.viewportHeight);
    if (![rect?.x, rect?.y, rect?.width, rect?.height, viewportWidth, viewportHeight].every(value => Number.isFinite(Number(value)))) return "";
    if (Number(rect.width) < 48 || Number(rect.height) < 27 || viewportWidth <= 0 || viewportHeight <= 0) return "";
    const screenshot = await captureVisibleTab(webInfo.windowId);
    if (!screenshot || typeof createImageBitmap !== "function" || typeof OffscreenCanvas !== "function") return "";
    try {
        const bitmap = await createImageBitmap(await (await fetch(screenshot)).blob());
        const scaleX = bitmap.width / viewportWidth;
        const scaleY = bitmap.height / viewportHeight;
        const sourceX = Math.max(0, Number(rect.x) * scaleX);
        const sourceY = Math.max(0, Number(rect.y) * scaleY);
        const sourceWidth = Math.min(bitmap.width - sourceX, Number(rect.width) * scaleX);
        const sourceHeight = Math.min(bitmap.height - sourceY, Number(rect.height) * scaleY);
        if (sourceWidth < 48 || sourceHeight < 27) { bitmap.close(); return ""; }
        const outputWidth = Math.min(360, Math.max(160, Math.round(sourceWidth)));
        const outputHeight = Math.max(90, Math.round(outputWidth * sourceHeight / sourceWidth));
        const canvas = new OffscreenCanvas(outputWidth, outputHeight);
        canvas.getContext("2d", { alpha: false }).drawImage(bitmap, sourceX, sourceY, sourceWidth, sourceHeight, 0, 0, outputWidth, outputHeight);
        bitmap.close();
        const result = await blobToDataUrl(await canvas.convertToBlob({ type: "image/jpeg", quality: 0.72 }));
        return globalThis.EagleBridgeCandidateLogic.safeFrameDataUrl(result);
    } catch (_error) {
        return "";
    }
}

function visibleMediaCount(items) {
    try {
        const groups = globalThis.EagleBridgeUILogic.groupCandidates(Array.isArray(items) ? items : []);
        return globalThis.EagleBridgeUILogic.partitionGroups(groups).visible.length;
    } catch (_error) {
        return Array.isArray(items) ? items.length : 0;
    }
}

function updateVisibleMediaCount(tabId) {
    SetIcon({ number: visibleMediaCount(cacheData[tabId]), tabId });
}

const scheduleVisibleMediaCount = globalThis.EagleBridgeCandidateLogic.createKeyedBoundedScheduler(
    updateVisibleMediaCount,
    250
);

G.urlMap = new Map();   // url查重map
G.requestHeaders = new Map();   // 临时储存请求头
G.blackList = new Set();    // 正则屏蔽资源列表

/**
 *  定时任务
 *  nowClear clear 清理冗余数据
 *  save 保存数据
 */
chrome.alarms.onAlarm.addListener(function (alarm) {
    if (alarm.name === "nowClear" || alarm.name === "clear") {
        clearRedundant();
        return;
    }
    if (alarm.name === "save") {
        saveMediaData(cacheData);
        return;
    }
});

// onBeforeRequest 浏览器发送请求之前使用正则匹配发送请求的URL
// chrome.webRequest.onBeforeRequest.addListener(
//     function (data) {
//         try { findMedia(data, true); } catch (e) { console.log(e); }
//     }, { urls: ["<all_urls>"] }, ["requestBody"]
// );
// 保存requestHeaders
chrome.webRequest.onSendHeaders.addListener(
    function (data) {
        if (G && G.initSyncComplete && !G.enable) { return; }
        if (data.requestHeaders) {
            G.requestHeaders.set(data.requestId, data.requestHeaders);
            data.allRequestHeaders = data.requestHeaders;
            try {
                const hostname = new URL(data.url).hostname.toLowerCase();
                if (Number.isInteger(data.tabId) && data.tabId > 0) {
                    const context = getRequestHeaders(data);
                    if (context) {
                        const byHost = resolverRequestContextByTab.get(data.tabId) || new Map();
                        byHost.set(hostname, { ...(byHost.get(hostname) || {}), ...context });
                        resolverRequestContextByTab.set(data.tabId, byHost);
                    }
                }
            } catch (_error) {
                // Invalid request URLs are handled by the normal media path.
            }
        }
        try { findMedia(data, true); } catch (e) { console.log(e); }
    }, { urls: ["<all_urls>"] }, ['requestHeaders',
        chrome.webRequest.OnBeforeSendHeadersOptions.EXTRA_HEADERS].filter(Boolean)
);
// onResponseStarted 浏览器接收到第一个字节触发，保证有更多信息判断资源类型
chrome.webRequest.onResponseStarted.addListener(
    function (data) {
        try {
            data.allRequestHeaders = G.requestHeaders.get(data.requestId);
            if (data.allRequestHeaders) {
                G.requestHeaders.delete(data.requestId);
            }
            findMedia(data);
        } catch (e) { console.log(e, data); }
    }, { urls: ["<all_urls>"] }, ["responseHeaders"]
);
// 删除失败的requestHeadersData
chrome.webRequest.onErrorOccurred.addListener(
    function (data) {
        G.requestHeaders.delete(data.requestId);
        G.blackList.delete(data.requestId);
    }, { urls: ["<all_urls>"] }
);

function findMedia(data, isRegex = false, filter = false, timer = false) {
    // Service Worker被强行杀死之后重新自我唤醒，等待全局变量初始化完成。
    if (!G || !G.initSyncComplete || !G.initLocalComplete || G.tabId == undefined || cacheData.init) {
        if (timer) { return; }
        setTimeout(() => {
            findMedia(data, isRegex, filter, true);
        }, 500);
        return;
    }
    // Any cold-start navigation cleanup must run before the first candidate
    // from the new document is inserted. It must never be deferred behind an
    // alarm callback that can delete that freshly inserted candidate later.
    flushPendingDocumentCleanup(data.tabId);

    // 检查 是否启用 是否在当前标签是否在屏蔽列表中
    const blockUrlFlag = data.tabId && data.tabId > 0 && G.blockUrlSet.has(data.tabId);
    if (!G.enable || (G.blockUrlWhite ? !blockUrlFlag : blockUrlFlag)) {
        return;
    }

    data.getTime = Date.now();

    if (!isRegex && G.blackList.has(data.requestId)) {
        G.blackList.delete(data.requestId);
        return;
    }
    // 屏蔽特殊页面发起的资源
    if (data.initiator != "null" &&
        data.initiator != undefined &&
        isSpecialPage(data.initiator)) { return; }
    // 屏蔽特殊页面的资源
    if (isSpecialPage(data.url)) { return; }
    const urlParsing = new URL(data.url);
    let [name, ext] = fileNameParse(urlParsing.pathname);

    //正则匹配
    if (isRegex && !filter) {
        for (let key in G.Regex) {
            if (!G.Regex[key].state) { continue; }
            G.Regex[key].regex.lastIndex = 0;
            let result = G.Regex[key].regex.exec(data.url);
            if (result == null) { continue; }
            if (G.Regex[key].blackList) {
                G.blackList.add(data.requestId);
                return;
            }
            data.extraExt = G.Regex[key].ext ? G.Regex[key].ext : undefined;
            if (result.length == 1) {
                findMedia(data, true, true);
                return;
            }
            result.shift();
            result = result.map(str => decodeURIComponent(str));
            if (!result[0].startsWith('https://') && !result[0].startsWith('http://')) {
                result[0] = urlParsing.protocol + "//" + data.url;
            }
            data.url = result.join("");
            findMedia(data, true, true);
            return;
        }
        return;
    }

    // 非正则匹配
    if (!isRegex) {
        // 获取头部信息
        data.header = getResponseHeadersValue(data);
        //检查后缀
        if (!filter && ext != undefined) {
            filter = CheckExtension(ext, data.header?.size);
            if (filter == "break") { return; }
        }
        //检查类型
        if (!filter && data.header?.type != undefined) {
            filter = CheckType(data.header.type, data.header?.size);
            if (filter == "break") { return; }
        }
        //查找附件
        if (!filter && data.header?.attachment != undefined) {
            const res = data.header.attachment.match(reFilename);
            if (res && res[1]) {
                [name, ext] = fileNameParse(decodeURIComponent(res[1]));
                filter = CheckExtension(ext, 0);
                if (filter == "break") { return; }
            }
        }
        //放过类型为media的资源
        if (data.type == "media") {
            filter = true;
        }
    }

    if (!filter) { return; }

    // 谜之原因 获取得资源 tabId可能为 -1 firefox中则正常
    // 检查是 -1 使用当前激活标签得tabID
    data.tabId = data.tabId == -1 ? G.tabId : data.tabId;

    cacheData[data.tabId] ??= [];
    cacheData[G.tabId] ??= [];
    const captureGeneration = captureGenerationByTab.get(data.tabId) || 0;

    // 缓存达到上限时只淘汰最旧项；清空整个标签会让刚捕获的
    // 媒体和用户仍在查看的旧媒体同时消失。
    if (cacheData[data.tabId].length > G.maxLength) {
        cacheData[data.tabId].splice(0, cacheData[data.tabId].length - G.maxLength);
        saveMediaData(cacheData);
    }

    // 查重 避免CPU占用 大于500 强制关闭查重
    // if (G.checkDuplicates && cacheData[data.tabId].length <= 500) {
    //     for (let item of cacheData[data.tabId]) {
    //         if (item.url.length == data.url.length &&
    //             item.cacheURL.pathname == urlParsing.pathname &&
    //             item.cacheURL.host == urlParsing.host &&
    //             item.cacheURL.search == urlParsing.search) { return; }
    //     }
    // }

    if (G.checkDuplicates && cacheData[data.tabId].length <= 500) {
        const tabFingerprints = G.urlMap.get(data.tabId) || new Set();
        if (tabFingerprints.has(data.url)) {
            return; // 找到重复，直接返回
        }
        tabFingerprints.add(data.url);
        G.urlMap.set(data.tabId, tabFingerprints);
        if (tabFingerprints.size >= 500) {
            tabFingerprints.clear();
        }
    }

    chrome.tabs.get(data.tabId, async function (webInfo) {
        if (chrome.runtime.lastError) { return; }
        if ((captureGenerationByTab.get(data.tabId) || 0) !== captureGeneration) return;
        const visualContext = await getMediaVisualContext(data.tabId, data.frameId, data.url);
        if ((captureGenerationByTab.get(data.tabId) || 0) !== captureGeneration) return;
        data.requestHeaders = getRequestHeaders(data);
        // 通用 `page` 解析器：复用同一标签、同一主机的第一方请求上下文。
        // 站点专用的会话请求筛选已按 07 §2 移出扩展包（阶段 3 的适配器）。
        if (data.mediaMeta?.resolver === "page") {
            let pageHostname = "";
            try { pageHostname = new URL(data.url).hostname.toLowerCase(); } catch (_error) { /* invalid URLs fail later */ }
            const byHost = resolverRequestContextByTab.get(data.tabId);
            const pageContext = byHost?.get(pageHostname)
                || byHost?.get(pageHostname.replace(/^www\./, ""))
                || byHost?.get(`www.${pageHostname}`)
                || {};
            data.requestHeaders = { ...pageContext, ...(data.requestHeaders || {}) };
        }
        // requestHeaders 中cookie 单独列出来
        if (data.requestHeaders?.cookie) {
            data.cookie = data.requestHeaders.cookie;
            data.requestHeaders.cookie = undefined;
        }
        const info = {
            name: name,
            url: data.url,
            size: data.header?.size,
            ext: ext,
            type: data.mime ?? data.header?.type,
            tabId: data.tabId,
            isRegex: isRegex,
            requestId: data.requestId ?? Date.now().toString(),
            initiator: data.initiator,
            requestHeaders: data.requestHeaders,
            cookie: data.cookie,
            contentIdentity: data.header?.contentIdentity,
            // cacheURL: { host: urlParsing.host, search: urlParsing.search, pathname: urlParsing.pathname },
            getTime: data.getTime
        };
        const visualGroupKey = typeof visualContext.groupKey === "string" && visualContext.groupKey
            ? `frame-${Number.isInteger(data.frameId) ? data.frameId : 0}:${visualContext.groupKey}`
            : "";
        const trustedVisualContext = visualContext.visualMatch === "exact";
        if (trustedVisualContext && typeof visualContext.thumbnailUrl === "string") {
            info.thumbnailUrl = visualContext.thumbnailUrl.slice(0, 4096);
        }
        if (trustedVisualContext && visualGroupKey) info.groupKey = visualGroupKey;
        if (trustedVisualContext && Number.isFinite(Number(visualContext.duration)) && Number(visualContext.duration) > 0) info.duration = Number(visualContext.duration);
        if (trustedVisualContext && Number.isFinite(Number(visualContext.width)) && Number(visualContext.width) > 0) info.playerWidth = Number(visualContext.width);
        if (trustedVisualContext && Number.isFinite(Number(visualContext.height)) && Number(visualContext.height) > 0) info.playerHeight = Number(visualContext.height);
        if (trustedVisualContext && typeof visualContext.title === "string" && visualContext.title.trim()) {
            info.title = visualContext.title.trim().slice(0, 220);
        }
        info.frameId = Number.isInteger(data.frameId) ? data.frameId : 0;
        if (data.mediaMeta && typeof data.mediaMeta === "object") {
            const meta = data.mediaMeta;
            info.role = ["video", "audio", "subtitle"].includes(meta.role) ? meta.role : undefined;
            info.streamId = String(meta.streamId ?? meta.id ?? "").replace(/[^a-z0-9_.:-]/gi, "").slice(0, 100) || undefined;
            info.title = typeof meta.title === "string" ? meta.title.slice(0, 220) : undefined;
            info.label = typeof meta.label === "string" ? meta.label.slice(0, 120) : undefined;
            info.codec = typeof meta.codec === "string" ? meta.codec.slice(0, 100) : undefined;
            info.videoWidth = Number.isFinite(Number(meta.width)) ? Number(meta.width) : undefined;
            info.videoHeight = Number.isFinite(Number(meta.height)) ? Number(meta.height) : undefined;
            info.frameRate = typeof meta.frameRate === "string" ? meta.frameRate.slice(0, 40) : undefined;
            info.bitrate = Number.isFinite(Number(meta.bitrate)) ? Number(meta.bitrate) : undefined;
            info.duration = Number.isFinite(Number(meta.duration)) ? Number(meta.duration) : undefined;
            if (typeof meta.thumbnailUrl === "string" && meta.thumbnailUrl) {
                info.thumbnailUrl = meta.thumbnailUrl.slice(0, 4096);
            }
            info.resolverFrameDataUrl = globalThis.EagleBridgeCandidateLogic.safeFrameDataUrl(meta.frameDataUrl);
            if (typeof meta.groupKey === "string" && meta.groupKey) {
                const metaGroupKey = meta.groupKey.slice(0, 200);
                info.groupKey = metaGroupKey.startsWith("frame-")
                    ? metaGroupKey
                    : `frame-${Number.isInteger(data.frameId) ? data.frameId : 0}:${metaGroupKey}`;
            }
            info.availableQualities = Array.isArray(meta.availableQualities)
                ? meta.availableQualities.filter(value => /^\d{2,5}p$/i.test(String(value || ""))).slice(0, 32)
                : undefined;
            info.qualitySource = typeof meta.qualitySource === "string" ? meta.qualitySource.slice(0, 40) : undefined;
            info.resolver = ["youtube", "page"].includes(meta.resolver) ? meta.resolver : undefined;
            info.reconstructedRange = Boolean(meta.reconstructedRange);
            info.separateAv = Boolean(meta.separateAv);
            info.drm = Boolean(meta.drm);
            if (Number.isFinite(Number(meta.estimatedSize)) && Number(meta.estimatedSize) > 0) {
                info.size = Number(meta.estimatedSize);
            }
        }
        // 不存在扩展使用类型
        if (info.ext === undefined && info.type !== undefined) {
            info.ext = info.type.split("/")[1];
        }
        // 正则匹配的备注扩展
        if (data.extraExt) {
            info.ext = data.extraExt;
        }
        // 不存在 initiator 和 referer 使用web url代替initiator
        if (info.initiator == undefined || info.initiator == "null") {
            info.initiator = info.requestHeaders?.referer ?? webInfo?.url;
        }
        // 装载页面信息
        info.title = info.title || webInfo?.title || "NULL";
        info.favIconUrl = webInfo?.favIconUrl;
        info.webUrl = webInfo?.url;
        // 无法归属到页面播放器的裸媒体请求：只作为技术资源分区处理（B-205）。
        // 旧版此处先按站点域名（`douyin.com`）判断是否打标；站点判定已按 07 §2
        // 移出扩展包，这里只保留"是否绑定到播放器"这一站点无关的条件。
        // 阶段 3 由适配器补充站点条件。
        info.unboundPageMedia = !data.mediaMeta
            && !trustedVisualContext
            && /^(?:video|audio)\//i.test(String(info.type || ""));
        await enrichManifestQualities(info);
        if ((captureGenerationByTab.get(data.tabId) || 0) !== captureGeneration) return;
        if (typeof eagleBridgeCandidate === "function") {
            eagleBridgeCandidate(info).catch(function () { return; });
        }
        // 屏蔽资源
        if (!isRegex && G.blackList.has(data.requestId)) {
            G.blackList.delete(data.requestId);
            return;
        }
        // 发送到 popup；媒体下载只能由新版界面提交给本机软件。
        const previewKey = mediaFramePreviewKey(info.tabId, info.groupKey);
        const cachedFrame = mediaFramePreviewCache.get(previewKey)?.dataUrl || "";
        const visualFrame = trustedVisualContext
            ? globalThis.EagleBridgeCandidateLogic.safeFrameDataUrl(visualContext.frameDataUrl)
            : "";
        const capturedFrame = visualFrame || info.resolverFrameDataUrl || cachedFrame
            || await captureVisibleVideoFrame(webInfo, info.frameId, visualContext);
        if ((captureGenerationByTab.get(data.tabId) || 0) !== captureGeneration) return;
        delete info.resolverFrameDataUrl;
        const frameDataUrl = rememberMediaFrame(info.tabId, info.groupKey, capturedFrame);
        chrome.runtime.sendMessage({
            Message: "popupAddData",
            data: frameDataUrl ? { ...info, frameDataUrl } : info
        }, function () {
            if (chrome.runtime.lastError) { return; }
        });

        // 储存数据
        cacheData[info.tabId] ??= [];
        cacheData[info.tabId].push(info);
        scheduleVisibleMediaCount(info.tabId);

        // 当前标签媒体数量大于100 开启防抖 等待5秒储存 或 积累10个资源储存一次。
        if (cacheData[info.tabId].length >= 100 && debounceCount <= 10) {
            debounceCount++;
            clearTimeout(debounce);
            debounce = setTimeout(function () { save(info.tabId); }, 5000);
            return;
        }
        // 时间间隔小于500毫秒 等待2秒储存
        if (Date.now() - debounceTime <= 500) {
            clearTimeout(debounce);
            debounceTime = Date.now();
            debounce = setTimeout(function () { save(info.tabId); }, 2000);
            return;
        }
        save(info.tabId);
    });
}
// cacheData数据 储存到 chrome.storage.local
function save(tabId) {
    clearTimeout(debounce);
    debounceTime = Date.now();
    debounceCount = 0;
    if (cacheData[tabId]) {
        // Session storage keeps a bounded rolling window. Continue updating it
        // after 99 captures so a restarted MV3 worker cannot resurrect a stale
        // early snapshot and hide the newest media.
        saveMediaData(cacheData, function () {
            chrome.runtime.lastError && console.log(chrome.runtime.lastError);
        });
        scheduleVisibleMediaCount(tabId);
    }
}

/**
 * 站点适配器声明（14 §5 / §5.3）。
 *
 * 内容脚本读不到扩展包内的文件（manifest.json 里没有、也不该为这个数据文件加上
 * `web_accessible_resources`），所以声明由这里读一次并缓存，再经既有消息通道按需下发。
 * 读不到 / 解析失败时退化为"没有适配器"的通用行为，只 console.warn 一次——
 * **不得**让候选发现整体失效。
 */
let siteAdaptersPromise = null;

function loadSiteAdapters() {
    if (!siteAdaptersPromise) {
        siteAdaptersPromise = fetch(chrome.runtime.getURL("site-adapters.json"), { cache: "no-store" })
            .then(response => {
                if (!response.ok) throw new Error("HTTP " + response.status);
                return response.text();
            })
            .then(text => {
                const parsed = JSON.parse(text);
                return Array.isArray(parsed && parsed.adapters) ? parsed.adapters : [];
            })
            .catch(error => {
                console.warn("[留底] 站点适配器声明不可用，退化为通用发现行为：", (error && error.message) || error);
                return [];
            });
    }
    return siteAdaptersPromise;
}

// 启动时预热一次：内容脚本在页面一开始就会问，提前读好可以少等一轮。
loadSiteAdapters();

/**
 * 监听 扩展 message 事件
 */
chrome.runtime.onMessage.addListener(function (Message, sender, sendResponse) {
    if (chrome.runtime.lastError) { return; }
    // Eagle bridge messages have their own async listener.  The legacy
    // listener must never answer them while its caches are still starting,
    // otherwise Chrome accepts the first "error" response and drops the
    // real health/plan response from eagle-bridge.js.
    if (Message?.eagleBridge) { return false; }
    // Restoring session media is asynchronous. Keep the response channel
    // open until the authoritative snapshot exists so the popup does not
    // paint an empty list on its first open.
    if (Message.Message == "getAllData") {
        globalThis.EagleBridgeCandidateLogic.waitForSnapshot(
            () => Boolean(G.initMediaComplete),
            () => cacheData
        ).then(snapshot => {
            const tabId = Message.tabId ?? G.tabId;
            flushPendingDocumentCleanup(tabId);
            scheduleVisibleMediaCount(tabId);
            sendResponse(cacheData && typeof cacheData === "object" ? cacheData : {});
        });
        return true;
    }
    if (Message.Message == "getMediaPreviews") {
        sendResponse(mediaFramesForTab(Message.tabId ?? G.tabId));
        return true;
    }
    if (Message.Message == "getActiveWebTab") {
        resolveActiveWebTab(Message.preferredTabId).then(sendResponse);
        return true;
    }
    if (Message.Message == "notifyPageLocationChanged" && sender?.tab?.id) {
        sendResponse(handleTabLocationChange(sender.tab.id, Message.url, "content"));
        return true;
    }
    // 站点适配器声明下发（14 §5.3）。放在下面那道初始化缓存门**之前**：声明与那些
    // 缓存无关，内容脚本在页面一开始就会问，不该被初始化时序挡住。
    if (Message.Message == "getSiteAdapters") {
        loadSiteAdapters().then(adapters => {
            sendResponse(adapters.length
                ? { ok: true, adapters }
                : { ok: false, reason: "unavailable", adapters: [] });
        });
        return true;
    }
    if (!G.initLocalComplete || !G.initSyncComplete) {
        sendResponse("error");
        return true;
    }
    // 以下检查是否有 tabId 不存在使用当前标签
    Message.tabId = Message.tabId ?? G.tabId;

    // 从缓存中保存数据到本地
    if (Message.Message == "pushData") {
        saveMediaData(cacheData);
        sendResponse("ok");
        return true;
    }
    /**
     * 设置扩展图标数字
     * 提供 type 删除标签为 tabId 的数字
     * 不提供type 删除所有标签的数字
     */
    if (Message.Message == "ClearIcon") {
        Message.type ? SetIcon({ tabId: Message.tabId }) : SetIcon();
        sendResponse("ok");
        return true;
    }
    // 启用/禁用扩展
    if (Message.Message == "enable") {
        G.enable = !G.enable;
        chrome.storage.sync.set({ enable: G.enable });
        chrome.action.setIcon({ path: "/icons/icon-128.png" });
        sendResponse(G.enable);
        return true;
    }
    /**
     * 提供requestId数组 获取指定的数据
     */
    if (Message.Message == "getData" && Message.requestId) {
        // 判断Message.requestId是否数组
        if (!Array.isArray(Message.requestId)) {
            Message.requestId = [Message.requestId];
        }
        const response = [];
        if (Message.requestId.length) {
            for (let item in cacheData) {
                for (let data of cacheData[item]) {
                    if (Message.requestId.includes(data.requestId)) {
                        response.push(data);
                    }
                }
            }
        }
        sendResponse(response.length ? response : "error");
        return true;
    }
    /**
     * 提供 tabId 获取该标签数据
     */
    if (Message.Message == "getData") {
        sendResponse(cacheData[Message.tabId]);
        return true;
    }
    /**
     * 获取捕获总开关。
     *
     * 旧版这里还会返回各"增强发现脚本"的按标签启用状态；`catch-script/` 已按
     * 07 §2 取消（站点专用脚本的归处是 `adapters/<site>/extension.js`，
     * 阶段 3 落地），因此第一版只返回捕获总开关。
     */
    if (Message.Message == "getButtonState") {
        sendResponse({ enable: G.enable });
        return true;
    }
    /**
     * 增强发现脚本注入。
     *
     * 07 §2：`catch-script/` **已取消**。站点专用脚本不随扩展包分发，因此本
     * 阶段该通道恒返回 `error no exists`——弹窗据此把入口显示为不可用，
     * 阶段 3 接入 `adapters/<site>/extension.js` 后恢复。
     */
    if (Message.Message == "script") {
        sendResponse("error no exists");
        return true;
    }
    // Heart Beat
    if (Message.Message == "HeartBeat") {
        chrome.tabs.query({ active: true, currentWindow: true }, function (tabs) {
            if (tabs[0] && tabs[0].id) {
                G.tabId = tabs[0].id;
            }
        });
        sendResponse("HeartBeat OK");
        return true;
    }
    // 清理数据
    if (Message.Message == "clearData") {
        // 当前标签
        if (Message.type) {
            clearCapturedTab(Message.tabId);
            saveMediaData(cacheData);
            clearRedundant();
            sendResponse("OK");
            return true;
        }
        // 其他标签
        for (let item in cacheData) {
            if (item == Message.tabId) { continue; }
            clearCapturedTab(item);
        }
        saveMediaData(cacheData);
        clearRedundant();
        sendResponse("OK");
        return true;
    }
    // 清理冗余数据
    if (Message.Message == "clearRedundant") {
        clearRedundant();
        sendResponse("OK");
        return true;
    }
    // 从 content-script 或 catch-script 传来的媒体url
    if (Message.Message == "addMedia") {
        if (Number.isInteger(sender?.tab?.id)) {
            findMedia({
                url: Message.url,
                tabId: sender.tab.id,
                frameId: Number.isInteger(sender.frameId) ? sender.frameId : 0,
                extraExt: Message.extraExt,
                mime: Message.mime,
                requestId: Message.requestId,
                initiator: Message.href,
                requestHeaders: Message.requestHeaders,
                mediaMeta: Message.mediaMeta
            }, true, true);
            sendResponse("ok");
            return true;
        }
        chrome.tabs.query({}, function (tabs) {
            for (let item of tabs) {
                if (item.url == Message.href) {
                    findMedia({ url: Message.url, tabId: item.id, frameId: 0, extraExt: Message.extraExt, mime: Message.mime, requestId: Message.requestId, requestHeaders: Message.requestHeaders, mediaMeta: Message.mediaMeta }, true, true);
                    return true;
                }
            }
            findMedia({ url: Message.url, tabId: -1, extraExt: Message.extraExt, mime: Message.mime, requestId: Message.requestId, initiator: Message.href, requestHeaders: Message.requestHeaders, mediaMeta: Message.mediaMeta }, true, true);
        });
        sendResponse("ok");
        return true;
    }
});

// 选定标签 更新G.tabId
// chrome.tabs.onHighlighted.addListener(function (activeInfo) {
//     if (activeInfo.windowId == -1 || !activeInfo.tabIds || !activeInfo.tabIds.length) { return; }
//     G.tabId = activeInfo.tabIds[0];
// });

/**
 * 监听 切换标签
 * 更新全局变量 G.tabId 为当前标签
 */
chrome.tabs.onActivated.addListener(function (activeInfo) {
    chrome.tabs.get(activeInfo.tabId, function (tab) {
        if (chrome.runtime.lastError || !rememberActiveWebTab(tab)) return;
        if (cacheData[G.tabId] !== undefined) {
            SetIcon({ number: cacheData[G.tabId].length, tabId: G.tabId });
            return;
        }
        SetIcon({ tabId: G.tabId });
    });
});

// 切换窗口，更新全局变量G.tabId
chrome.windows.onFocusChanged.addListener(function (activeInfo) {
    if (activeInfo == -1) { return; }
    chrome.tabs.query({ active: true, windowId: activeInfo }, function (tabs) {
        if (chrome.runtime.lastError) return;
        rememberActiveWebTab(tabs[0]);
    });
});

/**
 * 监听 标签页面更新
 * 检查 清理数据
 * 检查 是否在屏蔽列表中
 */
chrome.tabs.onUpdated.addListener(function (tabId, changeInfo, tab) {
    if (isSpecialPage(tab.url) || tabId <= 0 || !G.initSyncComplete) { return; }
    // console.log('onUpdated', tabId, changeInfo, tab);
    // 检查当前标签是否在屏蔽列表中
    if (changeInfo.url && tabId > 0) {
        if (G.blockUrl.length) {
            G.blockUrlSet.delete(tabId);
            if (isLockUrl(changeInfo.url)) {
                G.blockUrlSet.add(tabId);
            }
        }

    }
    chrome.sidePanel.setOptions({
        tabId,
        path: "popup.html?tabId=" + tabId
    });
});

/**
 * 监听 frame 正在载入
 * 检查 是否在屏蔽列表中 (frameId == 0 为主框架)
 * 检查 自动清理 (frameId == 0 为主框架)
 * 检查 注入脚本
 */
chrome.webNavigation.onCommitted.addListener(function (details) {
    if (isSpecialPage(details.url) || details.tabId <= 0) { return; }
    // console.log('onCommitted', details);

    if (details.frameId == 0
        && (!['auto_subframe', 'manual_subframe', 'form_submit'].includes(details.transitionType))) {
        clearCapturedTabForNavigation(details.tabId, "committed");
    }
    if (!G.initSyncComplete) { return; }

    // 刷新页面 检查是否在屏蔽列表中
    if (details.frameId == 0) {
        lastPageUrlByTab.set(details.tabId, String(details.url || ""));
        G.blockUrlSet.delete(details.tabId);
        if (isLockUrl(details.url)) {
            G.blockUrlSet.add(details.tabId);
        }
        announceTabLocationChanged(details.tabId, details.url, "committed");
    }

    // 说明：旧版在此按 `G.scriptList` 注入 `catch-script/<name>.js`（含站点
    // 专用脚本）。07 §2 已取消 `catch-script/`，站点专用注入脚本改由
    // `adapters/<site>/extension.js` 在阶段 3 提供；扩展包内不再有任何
    // 按站点注入的分支。本阶段**没有**替代注入路径，属阶段性功能回退。
});

/**
 * 监听 标签关闭 清理数据
 */
chrome.tabs.onRemoved.addListener(function (tabId) {
    clearCapturedTab(tabId, true);
    lastPageUrlByTab.delete(tabId);
    pendingPageLocationByTab.delete(tabId);
    pageLocationInitializationWaitByTab.delete(tabId);
    pendingDocumentCleanupByTab.delete(tabId);
    if (lastActiveWebTabId === Number(tabId)) lastActiveWebTabId = 0;
    // 清理缓存数据
    chrome.alarms.get("nowClear", function (alarm) {
        !alarm && chrome.alarms.create("nowClear", { when: Date.now() + 1000 });
    });
    if (G.initSyncComplete) {
        G.blockUrlSet.has(tabId) && G.blockUrlSet.delete(tabId);
    }
});

// 操作符检查
function operatorCheck(size, Obj) {
    const unitNumber = {
        "B": 1,
        "BYTE": 1,
        "KB": 1024,
        "MB": 1048576,
        "GB": 1073741824
    };
    const unit = (Obj.unit || "B");
    const targetSize = Obj.size * (unitNumber[unit] || 1);
    switch (Obj.operator) {
        case "=":
            return size == targetSize;
        case "<":
            return size < targetSize;
        case ">":
            return size > targetSize;
        case "<=":
            return size <= targetSize;
        case ">=":
            return size >= targetSize;
        case "!=":
            return size != targetSize;
        case "~":
            return (Obj.min ? size >= Obj.min * (unitNumber[unit] || 1) : true) && (Obj.max ? size <= Obj.max * (unitNumber[unit] || 1) : true);
        default:
            return size <= targetSize;
    }
}

/**
 * 检查扩展名和大小
 * @param {String} ext 
 * @param {Number} size 
 * @returns {Boolean|String}
 */
function CheckExtension(ext, size) {
    const Ext = G.Ext.get(ext);
    if (!Ext) { return false; }
    if (!Ext.state) { return "break"; }
    if (Ext.size != 0 && size != undefined && !operatorCheck(size, Ext)) {
        return "break";
    }
    return true;
}

/**
 * 检查类型和大小
 * @param {String} dataType 
 * @param {Number} dataSize 
 * @returns {Boolean|String}
 */
function CheckType(dataType, dataSize) {
    const typeInfo = G.Type.get(dataType.split("/")[0] + "/*") || G.Type.get(dataType);
    if (!typeInfo) { return false; }
    if (!typeInfo.state) { return "break"; }
    if (typeInfo.size != 0 && dataSize != undefined && !operatorCheck(dataSize, typeInfo)) {
        return "break";
    }
    return true;
}

/**
 * 获取文件名及扩展名
 * @param {String} pathname 
 * @returns {Array}
 */
function fileNameParse(pathname) {
    let fileName = decodeURI(pathname.split("/").pop());
    let ext = fileName.split(".");
    ext = ext.length == 1 ? undefined : ext.pop().toLowerCase();
    return [fileName, ext ? ext : undefined];
}

/**
 * 获取响应头信息
 * @param {Object} data 
 * @returns {Object}
 */
function getResponseHeadersValue(data) {
    const header = {};
    let digest = "";
    let contentMd5 = "";
    let etag = "";
    if (data.responseHeaders == undefined || data.responseHeaders.length == 0) { return header; }
    for (let item of data.responseHeaders) {
        item.name = item.name.toLowerCase();
        if (item.name == "content-length") {
            header.size ??= parseInt(item.value);
        } else if (item.name == "content-type") {
            header.type = item.value.split(";")[0].toLowerCase();
        } else if (item.name == "content-disposition") {
            header.attachment = item.value;
        } else if (item.name == "content-range") {
            let size = item.value.split('/')[1];
            if (size !== '*') {
                header.size = parseInt(size);
            }
        } else if (item.name == "digest") {
            digest = String(item.value || "");
        } else if (item.name == "content-md5") {
            contentMd5 = String(item.value || "");
        } else if (item.name == "etag" && !/^\s*W\//i.test(String(item.value || ""))) {
            etag = String(item.value || "");
        }
    }
    const identity = [["digest", digest], ["md5", contentMd5], ["etag", etag]]
        .find(([, value]) => value.length >= 8 && value.length <= 256 && !/[\r\n]/.test(value));
    if (identity) header.contentIdentity = `${identity[0]}:${identity[1]}`;
    return header;
}

/**
 * 获取请求头
 * @param {Object} data 
 * @returns {Object|Boolean}
 */
function getRequestHeaders(data) {
    const allowedNames = new Set(["referer", "origin", "authorization", "cookie", "user-agent"]);
    const result = {};
    for (const [name, value] of Object.entries(data.requestHeaders || {})) {
        const lower = String(name).toLowerCase();
        if (allowedNames.has(lower) && typeof value === "string" && !/[\r\n]/.test(value)) {
            result[lower] = value;
        }
    }
    for (const item of Array.isArray(data.allRequestHeaders) ? data.allRequestHeaders : []) {
        const lower = String(item?.name || "").toLowerCase();
        const value = item?.value;
        if (allowedNames.has(lower) && typeof value === "string" && !/[\r\n]/.test(value)) {
            result[lower] = value;
        }
    }
    return Object.keys(result).length ? result : false;
}
//设置扩展图标
function SetIcon(obj) {
    if (obj?.number == 0 || obj?.number == undefined) {
        chrome.action.setBadgeText({ text: "", tabId: obj?.tabId ?? G.tabId }, function () { if (chrome.runtime.lastError) { return; } });
        // chrome.action.setTitle({ title: "还没闻到味儿~", tabId: obj.tabId }, function () { if (chrome.runtime.lastError) { return; } });
    } else if (G.badgeNumber) {
        obj.number = obj.number > 999 ? "999+" : obj.number.toString();
        chrome.action.setBadgeText({ text: obj.number, tabId: obj.tabId }, function () { if (chrome.runtime.lastError) { return; } });
        // chrome.action.setTitle({ title: "抓到 " + obj.number + " 条鱼", tabId: obj.tabId }, function () { if (chrome.runtime.lastError) { return; } });
    }
}

// 判断特殊页面
function isSpecialPage(url) {
    if (!url || url == "null") { return true; }
    return !(url.startsWith("http://") || url.startsWith("https://") || url.startsWith("blob:"));
}

/**
 * 清理冗余数据
 */
function clearRedundant() {
    chrome.tabs.query({}, function (tabs) {
        const allTabId = new Set(tabs.map(tab => tab.id));

        if (!cacheData.init) {
            // 清理 缓存数据
            let cacheDataFlag = false;
            for (let key in cacheData) {
                if (!allTabId.has(Number(key))) {
                    cacheDataFlag = true;
                    delete cacheData[key];
                }
            }
            cacheDataFlag && saveMediaData(cacheData);
        }

        // 清理
        G.urlMap.forEach((_, key) => {
            !allTabId.has(key) && G.urlMap.delete(key);
        });
        captureGenerationByTab.forEach((_, key) => {
            !allTabId.has(key) && captureGenerationByTab.delete(key);
        });
        lastPageUrlByTab.forEach((_, key) => {
            !allTabId.has(key) && lastPageUrlByTab.delete(key);
        });
        pendingPageLocationByTab.forEach((_, key) => {
            !allTabId.has(key) && pendingPageLocationByTab.delete(key);
        });
        pageLocationInitializationWaitByTab.forEach((_, key) => {
            !allTabId.has(key) && pageLocationInitializationWaitByTab.delete(key);
        });
        pendingDocumentCleanupByTab.forEach((_, key) => {
            !allTabId.has(key) && pendingDocumentCleanupByTab.delete(key);
        });
        if (!allTabId.has(lastActiveWebTabId)) lastActiveWebTabId = 0;

        G.blockUrlSet = new Set([...G.blockUrlSet].filter(x => allTabId.has(x)));

        if (G.requestHeaders.size >= 10240) {
            G.requestHeaders.clear();
        }
    });
}

// 扩展升级：只重置过时的捕获缓存（`MediaData` 在 session 区）。
// 捕获选项 `captureOptions`（含站点开关的本地副本，放 local）保留。
chrome.runtime.onInstalled.addListener(function (details) {
    if (details.reason == "update") {
        if (chrome.storage.session) {
            chrome.storage.session.clear(InitOptions);
        } else {
            InitOptions();
        }
        chrome.alarms.create("nowClear", { when: Date.now() + 3000 });
    }
});

// 测试
// chrome.storage.local.get(function (data) { console.log("storageLocal", data.MediaData) });
// chrome.storage.session.get(function (data) { console.log("storageSession", data.MediaData) });
// chrome.tabs.query({}, function (tabs) { for (let item of tabs) { console.log("tabId", item.id); } });
