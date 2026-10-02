/*
 * 运行时请求头的提交纪律（站点无关）。
 *
 * 旧版 `test_youtube_session.js` 测的是**站点专用的会话复用**：按站点识别
 * "第一方会话请求"并用它覆盖提交时的 Cookie。按 07 §2（扩展源码里禁止出现
 * 站点名判断），那套分支已移出扩展包，阶段 3 落到
 * `adapters/<site>/extension.js`。
 *
 * 这里保留旧测试真正防住的三条**站点无关**不变量，覆盖强度不降低：
 *   1. 运行时请求头按**每个候选自己**的请求上下文提交，不串号；
 *   2. Cookie / Authorization **只出现在 `runtimeHeaders`**，绝不混进
 *      `streams`（04 §2.3 的载荷形状；07 §3 的秘密不得落盘）；
 *   3. 提交**不得改写候选对象本身**（提交是只读的，候选可能被后续任务复用）。
 */

"use strict";

const assert = require("assert");
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const root = path.resolve(__dirname, "..", "..");
const bridgeSource = fs.readFileSync(path.join(root, "extension", "js", "eagle-bridge.js"), "utf8");
const backgroundSource = fs.readFileSync(path.join(root, "extension", "js", "background.js"), "utf8");

let sendHeadersListener = null;
let submitted = null;

const context = {
    URL,
    URLSearchParams,
    console,
    G: { requestHeaders: new Map(), initSyncComplete: true, enable: true },
    resolverRequestContextByTab: new Map(),
    findMedia() {},
    chrome: {
        webRequest: {
            onSendHeaders: { addListener(fn) { sendHeadersListener = fn; } },
            OnBeforeSendHeadersOptions: { EXTRA_HEADERS: "extraHeaders" }
        }
    },
    eagleBridgeApi: async (_url, options) => {
        submitted = JSON.parse(options.body);
        return { id: "test", status: "queued" };
    },
    async eagleBridgeUpdateState() {}
};
// 切片之外的依赖在沙箱里补桩（与生产实现同名同签名）。
context.eagleBridgeSafeExtension = value => String(value || "bin").replace(/[^a-z0-9]/g, "") || "bin";
context.eagleBridgeTrackOf = () => "main";
vm.createContext(context);
vm.runInContext(
    backgroundSource.slice(
        backgroundSource.indexOf("function getRequestHeaders("),
        backgroundSource.indexOf("//设置扩展图标")
    ),
    context
);
vm.runInContext(
    backgroundSource.slice(
        backgroundSource.indexOf("chrome.webRequest.onSendHeaders.addListener("),
        backgroundSource.indexOf("// onResponseStarted")
    ),
    context
);
vm.runInContext(
    bridgeSource.slice(
        bridgeSource.indexOf("function eagleBridgePublicStream("),
        bridgeSource.indexOf("chrome.alarms.onAlarm.addListener")
    ),
    context
);

const observe = (url, headers, tabId = 7) => sendHeadersListener({
    url,
    type: "xmlhttprequest",
    tabId,
    requestId: String(Math.random()),
    requestHeaders: Object.entries(headers).map(([name, value]) => ({ name, value }))
});

(async () => {
    assert.strictEqual(typeof context.eagleBridgeCreatePlan, "function",
        "eagle-bridge.js lost its plan-creation entry point");

    const first = {
        url: "https://cdn.example/video.mp4?token=one",
        webUrl: "https://example.com/watch",
        title: "第一条",
        ext: "mp4",
        type: "video/mp4",
        tabId: 7,
        cookie: "SID=first",
        requestHeaders: { referer: "https://example.com/watch", authorization: "Bearer first" }
    };
    const second = {
        url: "https://cdn.example/audio.m4a?token=two",
        webUrl: "https://example.com/watch",
        title: "第一条",
        ext: "m4a",
        type: "audio/mp4",
        tabId: 7,
        cookie: "SID=second",
        requestHeaders: { referer: "https://example.com/watch" }
    };

    await context.eagleBridgeCreatePlan([first]);
    // 每个候选提交自己的请求头，不串号。
    assert.strictEqual(submitted.runtimeHeaders[0].cookie, "SID=first");
    assert.strictEqual(submitted.runtimeHeaders[0].authorization, "Bearer first");
    assert.strictEqual(submitted.runtimeHeaders[0].referer, "https://example.com/watch");

    // 秘密只能出现在 runtimeHeaders，绝不混进 streams（04 §2.3 / 07 §3）。
    const serializedStreams = JSON.stringify(submitted.streams);
    assert.strictEqual(serializedStreams.includes("SID="), false,
        "cookies must never leak into the stream list");
    assert.strictEqual(serializedStreams.includes("Bearer"), false,
        "authorization must never leak into the stream list");

    // 提交是只读的：不得改写候选对象（后续任务会复用它）。
    assert.strictEqual(first.cookie, "SID=first");
    assert.strictEqual(first.requestHeaders.authorization, "Bearer first");

    await context.eagleBridgeCreatePlan([second]);
    assert.strictEqual(submitted.runtimeHeaders[0].cookie, "SID=second",
        "each candidate must submit its own request context");
    assert.strictEqual(submitted.runtimeHeaders[0].authorization, undefined,
        "a candidate without authorization must not inherit another candidate's header");

    // 没有请求上下文的候选：提交空头而不是伪造。
    await context.eagleBridgeCreatePlan([{ url: "https://cdn.example/plain.mp4", ext: "mp4", tabId: 7 }]);
    assert.strictEqual(submitted.runtimeHeaders[0].cookie, undefined);
    assert.strictEqual(submitted.runtimeHeaders[0].referer, undefined);

    // 扩展侧不再按站点识别"会话请求"：background.js 里不得出现站点名判断。
    assert.ok(!/youtubeRequestContextByTab/.test(backgroundSource),
        "07 §2: the per-site session-context map must be gone from background.js");
    assert.ok(!/hostname\s*===\s*"www\.youtube\.com"/.test(backgroundSource),
        "07 §2: background.js must not branch on a site hostname");

    console.log("Runtime request-header submission OK");
})().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
