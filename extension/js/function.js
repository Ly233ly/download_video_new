function isLockUrl(url) {
    for (const item of G.blockUrl || []) {
        if (!item.state) continue;
        item.url.lastIndex = 0;
        if (item.url.test(url)) return true;
    }
    return false;
}

/**
 * 07 §3 存储区域表：候选快照放 `chrome.storage.session`。
 *
 * 候选地址与请求头可能含 Cookie、Authorization 或临时签名，因此**只能**
 * 停留在内存与 session 区域，绝不进入 local/sync（07 §3 末条硬约束）。
 * 没有 session 区域时宁可不持久化，也不降级写入 local。
 */
function saveMediaData(data, callback = undefined) {
    if (!chrome.storage.session) {
        callback?.();
        return;
    }
    const snapshot = globalThis.EagleBridgeCandidateLogic?.boundedMediaSnapshot
        ? globalThis.EagleBridgeCandidateLogic.boundedMediaSnapshot(data)
        : data;
    chrome.storage.session.set({ MediaData: snapshot }, callback);
}

function loadMediaData(callback) {
    if (!chrome.storage.session) {
        callback({ MediaData: {} });
        return;
    }
    chrome.storage.session.get({ MediaData: {} }, callback);
}

function isSafeRegularExpression(pattern) {
    const value = String(pattern || "");
    if (!value || value.length > 256) return false;
    if (/\\[1-9]/.test(value)) return false;
    if (/\([^)]*[+*][^)]*\)[+*{]/.test(value)) return false;
    if (/\([^)]*\{\d+,?\d*\}[^)]*\)[+*{]/.test(value)) return false;
    if (/(?:\.\*|\.\+){2,}/.test(value)) return false;
    if ((value.match(/\|/g) || []).length > 32) return false;
    return true;
}
