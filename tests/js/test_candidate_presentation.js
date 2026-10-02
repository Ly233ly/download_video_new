/*
 * 候选呈现与启动快照的跨站点回归。
 *
 * 迁移说明（对照 07 §2 / §8）：
 *   站点专用断言（Instagram CDN 元数据、抖音播放器选择与 feed 标题、
 *   Vimeo 播放器配置解析）在原实现里已按「扩展源码里禁止出现站点名判断」
 *   移出扩展包，归 `adapters/<site>/extension.js`（阶段 3）。
 *   本文件把这些断言改写为**站点无关的等价断言**，覆盖强度不降低：
 *     - `parseInstagramCdnMetadata` 的分组/时长/码率估算
 *         → `reconstructByteRangeUrl` 的偏移剥离 + 固定字节分片判定
 *           （`ui.groupCandidates` 的 `segmentOnly` 与 `validateSelection` 拒绝）
 *     - `douyinVideoIdFromSignals` / `douyinCandidateTitle`
 *         → `selectContentTitle` 的多条目标题区分（同一函数、同一职责）
 *     - `selectPrimaryPageVideo`
 *         → `resolveVisualMatch` / `stableVisualKey` 的播放器归属判定
 *     - `parseVimeoPlayerConfig`
 *         → `parseManifestQualities` 的清单画质目录（HLS 与 DASH 各一条）
 */

const path = require("path");

const root = path.resolve(__dirname, "..", "..");
const presentation = require(path.join(root, "extension", "js", "eagle-bridge-candidate-logic.js"));
const ui = require(path.join(root, "extension", "js", "eagle-bridge-ui-logic.js"));

const douyinFrame = presentation.selectThumbnail({
    playing: ["https://p3-sign.douyinpic.com/egg-video-frame.webp"],
    metadata: ["https://lf1-cdn-tos.bytegoofy.com/obj/icon/favicon.ico"]
});
if (douyinFrame !== "https://p3-sign.douyinpic.com/egg-video-frame.webp") {
    throw new Error("Playing-video artwork must win over site metadata");
}

const embeddedPlayer = presentation.selectThumbnail({
    exact: ["javascript:alert(1)"],
    visible: ["https://i.vimeocdn.com/video/project-frame.jpg"],
    metadata: ["https://a5.behance.net/favicon.ico"]
});
if (embeddedPlayer !== "https://i.vimeocdn.com/video/project-frame.jpg") {
    throw new Error("Embedded-player artwork must be accepted without a site adapter");
}

const frame = "data:image/jpeg;base64," + "A".repeat(128);
if (presentation.safeFrameDataUrl(frame) !== frame) {
    throw new Error("A bounded JPEG captured from the playing video must be accepted as an in-memory preview");
}
if (presentation.safeFrameDataUrl("data:text/html;base64,PHNjcmlwdD4=") !== "") {
    throw new Error("Only image data URLs may be used as in-memory video frames");
}
if (presentation.stableVisualKey("https://player.vimeo.com/video/123?token=one", 2, 91.2)
    !== presentation.stableVisualKey("https://player.vimeo.com/video/123?token=two", 2, 91.4)) {
    throw new Error("Signed-query changes must not create a new player identity");
}

const rangeFragmentUrl = "https://media.example/o1/v/t2/f2/m367/clip.mp4?token=signed&bytestart=886&byteend=173864";
const reconstructedFragment = presentation.reconstructByteRangeUrl(rangeFragmentUrl);
if (!reconstructedFragment || reconstructedFragment.start !== 886 || reconstructedFragment.end !== 173864) {
    throw new Error("Paired bytestart/byteend values must be recognized as one fixed playback fragment");
}
if (/bytestart|byteend/i.test(reconstructedFragment.url) || !reconstructedFragment.url.includes("token=signed")) {
    throw new Error("Range reconstruction must remove only transport offsets and preserve signed media parameters");
}
// 旧版在这里调用站点专用的 `parseInstagramCdnMetadata()`（按 CDN 域名分流并
// 组装站点分组键）。改为站点无关的等价断言：一段带固定字节偏移的媒体请求
// 必须被判成**传输分片**，不得作为完整视频进入下载。
{
    const fragmentGroup = ui.groupCandidates([{
        tabId: 9,
        requestId: "signed-byte-fragment",
        url: rangeFragmentUrl,
        webUrl: "https://example.com/",
        title: "Snippet",
        ext: "mp4",
        type: "video/mp4",
        duration: 0,
        _size: 172_979
    }])[0];
    if (!fragmentGroup || fragmentGroup.segmentOnly !== true) {
        throw new Error("A signed media request with an explicit byte range must stay a transport fragment");
    }
    const fragmentVerdict = ui.validateSelection(fragmentGroup, ui.createDefaultSelection(fragmentGroup), {
        desktopAvailable: true
    });
    if (fragmentVerdict.code !== "segment_only") {
        throw new Error("Raw byte fragments must never reach the desktop downloader");
    }
}

const instagramPermalink = presentation.chooseContentPageUrl("https://example.com/feed", [
    "https://example.com/audio/27606406132373961/",
    "https://example.com/p/7523401234567890123/liked_by/",
    "https://example.com/p/7523401234567890123/",
    "https://example.com/reels/7523401234567890123/"
]);
if (instagramPermalink !== "https://example.com/p/7523401234567890123") {
    throw new Error("A feed video must resolve to its stable content permalink, not the home feed or audio/liked-by pages");
}
if (presentation.chooseContentPageUrl("https://example.com/", []) !== "") {
    throw new Error("A generic feed without a stable content permalink must not be guessed");
}
const nearbyGridVideo = presentation.chooseNearbyContentPageUrl("https://example.com/videos/current", [
    [],
    ["https://example.com/videos/related?tracking=1"],
    ["https://example.com/videos/far-away"]
]);
if (nearbyGridVideo !== "https://example.com/videos/related") {
    throw new Error("The closest distinct content link must identify a related video card");
}
if (presentation.chooseNearbyContentPageUrl("https://example.com/videos/current", [[], []]) !== "https://example.com/videos/current") {
    throw new Error("A primary player without a nearer content link must keep the current work page");
}
if (presentation.chooseContentPageUrl("https://example.com/user/status/123456789?tracking=1", []) !== "https://example.com/user/status/123456789") {
    throw new Error("A single-content page may safely use its own canonical URL for desktop resolution");
}

// 站点无关的内容页判据：桌面端 `page` 解析器只接受带明确数字内容 ID 的路径，
// 首页 / 信息流 / 列表页与纯字母数字 ID 一律不放行（后者是第一版的已知代价）。
{
    const accepted = ui.groupCandidates([{
        tabId: 9,
        requestId: "numeric-content-page",
        url: "https://example.com/video/7523401234567890123",
        webUrl: "https://example.com/",
        title: "内容页",
        groupKey: "page:content",
        resolver: "page",
        role: "video",
        duration: 41.5
    }]);
    if (accepted.length !== 1) {
        throw new Error("A numeric content permalink must remain eligible for desktop page resolution");
    }
    for (const rejected of [
        "https://example.com/",
        "https://example.com/feed",
        "https://example.com/p/Da9rBuVjAGK"
    ]) {
        const groups = ui.groupCandidates([{
            tabId: 9,
            requestId: `rejected-${rejected}`,
            url: rejected,
            webUrl: "https://example.com/",
            title: "不是内容页",
            groupKey: `page:${rejected}`,
            resolver: "page",
            role: "video",
            duration: 12
        }]);
        if (groups.length !== 0) {
            throw new Error(`A non-content or non-numeric page must not become a resolver candidate: ${rejected}`);
        }
    }
}
const genericFeedTitle = presentation.selectContentTitle({
    headings: [],
    captions: [],
    lines: [
        "alan酱",
        "@alanshen144905",
        "·",
        "15小时",
        "我单方面宣布，Joy-Con 才是 Voice Coding 的最佳神器，没有之一。",
        "0:41 / 0:48",
        "18",
        "50",
        "351",
        "5.6万"
    ],
    imageAlts: ["alan酱的头像"],
    fallback: "主页 / X"
});
if (genericFeedTitle !== "我单方面宣布，Joy-Con 才是 Voice Coding 的最佳神器，没有之一。") {
    throw new Error(`A generic feed item must use its own meaningful caption instead of the tab title; got ${genericFeedTitle}`);
}
if (presentation.selectContentTitle({ lines: ["18", "0:41 / 0:48"], fallback: "主页 / X" }) !== "主页 / X") {
    throw new Error("Pure counters and player timestamps must not replace the page-title fallback");
}

// 站点无关：同一信息流里的两条内容必须各自得到自己的标题，不得塌回标签页标题。
{
    const first = presentation.selectContentTitle({
        lines: ["@热话动漫", "这一集的瑞克，终于不像个疯子，像个外公 #动漫解说 展开"],
        fallback: "信息流"
    });
    const second = presentation.selectContentTitle({
        lines: ["@木板解说", "第7集：深度拆解瑞克和莫蒂 S6E4《夜晚家庭》 展开"],
        fallback: "信息流"
    });
    if (!first.includes("这一集的瑞克") || !second.includes("第7集")) {
        throw new Error("Each feed item must use its own caption instead of the tab title");
    }
    if (first === second || new Set([first, second]).size !== 2) {
        throw new Error("Different feed items must never collapse to one tab title");
    }
}

const scheduledCallbacks = [];
let boundedScanCount = 0;
const boundedSchedule = presentation.createBoundedScheduler(
    () => { boundedScanCount += 1; },
    250,
    { setTimeout: callback => { scheduledCallbacks.push(callback); return scheduledCallbacks.length; } }
);
for (let index = 0; index < 100; index += 1) boundedSchedule();
if (scheduledCallbacks.length !== 1) {
    throw new Error("Continuous feed mutations must not postpone media discovery forever");
}
scheduledCallbacks.shift()();
if (boundedScanCount !== 1) throw new Error("The bounded discovery scheduler must run the queued scan");
boundedSchedule();
if (scheduledCallbacks.length !== 1) throw new Error("A new scan must be schedulable after the previous scan completes");

const keyedCallbacks = [];
const keyedRuns = [];
const scheduleByTab = presentation.createKeyedBoundedScheduler(
    tabId => { keyedRuns.push(tabId); },
    250,
    { setTimeout: callback => { keyedCallbacks.push(callback); return keyedCallbacks.length; } }
);
for (let index = 0; index < 400; index += 1) scheduleByTab(9);
scheduleByTab(10);
if (keyedCallbacks.length !== 2) {
    throw new Error("A media burst must schedule at most one expensive badge regroup per tab");
}
keyedCallbacks.splice(0).forEach(callback => callback());
if (keyedRuns.join(",") !== "9,10") {
    throw new Error(`Each tab must eventually refresh its badge once; got ${keyedRuns.join(",")}`);
}
scheduleByTab(9);
if (keyedCallbacks.length !== 1) {
    throw new Error("A tab badge must be schedulable again after its previous refresh completes");
}

const observedLocations = [];
const updateLocation = presentation.createLocationChangeTracker(
    "https://example.com/watch/one",
    (nextUrl, previousUrl, context) => observedLocations.push({ nextUrl, previousUrl, context })
);
if (updateLocation("https://example.com/watch/one", { source: "same" })) {
    throw new Error("A repeated location must not restart discovery");
}
if (!updateLocation("https://example.com/watch/two?autoplay=1", { source: "history" })
    || !updateLocation("https://example.com/#/watch/three", { source: "fragment" })) {
    throw new Error("Path, query and hash navigation must all restart generic discovery");
}
if (observedLocations.length !== 2
    || observedLocations[0].previousUrl !== "https://example.com/watch/one"
    || observedLocations[1].context?.source !== "fragment") {
    throw new Error("Location tracking must preserve page order and event context");
}

if (typeof presentation.shouldClearCapturedCandidates !== "function") {
    throw new Error("Navigation cleanup policy must distinguish SPA video switches from full document loads");
}

{
    const snapshot = presentation.boundedMediaSnapshot({
        1: Array.from({ length: 5 }, (_, index) => ({ requestId: `tab-1-${index}`, getTime: index + 1 })),
        2: Array.from({ length: 5 }, (_, index) => ({ requestId: `tab-2-${index}`, getTime: index + 11 })),
        3: Array.from({ length: 5 }, (_, index) => ({ requestId: `tab-3-${index}`, getTime: index + 21 }))
    }, 3, 5);
    const saved = Object.values(snapshot).flat();
    if (saved.length !== 5 || saved.at(-1)?.requestId !== "tab-3-4") {
        throw new Error("Session persistence must keep a bounded rolling window of the newest captured media");
    }
    if (Object.values(snapshot).some(items => items.length > 3)) {
        throw new Error("No tab may exceed the session snapshot per-tab budget");
    }
}
for (const source of ["history", "fragment", "content"]) {
    for (const mode of [1, 2]) {
        if (presentation.shouldClearCapturedCandidates(source, mode)) {
            throw new Error(`SPA ${source} navigation must retain earlier media candidates in auto-clear mode ${mode}`);
        }
    }
}
if (!presentation.shouldClearCapturedCandidates("committed", 1)
    || !presentation.shouldClearCapturedCandidates("loading", 2)
    || presentation.shouldClearCapturedCandidates("committed", 2)
    || presentation.shouldClearCapturedCandidates("loading", 1)) {
    throw new Error("Full document navigation must keep the configured mode-1/mode-2 cleanup behavior");
}

const injectionCalls = [];
let discoveryMessageCount = 0;
const recoveredDiscoveryPromise = presentation.ensureContentDiscovery({
    tabs: {
        sendMessage: async (_tabId, message) => {
            discoveryMessageCount += 1;
            if (discoveryMessageCount === 1) throw new Error("Receiving end does not exist");
            return { ok: message.Message === "discoverPageResolvers" };
        }
    },
    scripting: {
        executeScript: async details => { injectionCalls.push(details); }
    }
}, { id: 42, url: "https://example.com/feed" }).then(recoveredDiscovery => {
    if (!recoveredDiscovery?.ready || !recoveredDiscovery?.injected || injectionCalls.length !== 1) {
        throw new Error("Opening the popup must recover discovery on a page left open across an extension reload");
    }
    if (injectionCalls[0]?.target?.tabId !== 42
        || injectionCalls[0]?.files?.join(",") !== "js/eagle-bridge-candidate-logic.js,js/content-script.js") {
        throw new Error("Recovery must inject only the active discovery scripts into the requested web tab");
    }
});
const alreadyReadyInjectionCalls = [];
const alreadyReadyDiscoveryPromise = presentation.ensureContentDiscovery({
    tabs: { sendMessage: async () => ({ ok: true }) },
    scripting: { executeScript: async details => { alreadyReadyInjectionCalls.push(details); } }
}, { id: 43, url: "https://example.com/feed/two" }).then(result => {
    if (!result?.ready || result?.injected || alreadyReadyInjectionCalls.length) {
        throw new Error("A responsive content script must be scanned without duplicate injection");
    }
});
const restrictedDiscoveryPromise = presentation.ensureContentDiscovery({
    tabs: { sendMessage: async () => { throw new Error("must not be called"); } },
    scripting: { executeScript: async () => { throw new Error("must not be called"); } }
}, { id: 44, url: "chrome://extensions" }).then(result => {
    if (result?.ready || result?.injected || result?.reason !== "unsupported_tab") {
        throw new Error("Restricted browser pages must never receive discovery injection");
    }
});
const genericPermalinkMatrix = [
    ["https://example.com/@creator/video/7523401234567890123?lang=en", "https://example.com/@creator/video/7523401234567890123"],
    ["https://example.com/r/videos/comments/123456789/a_title/?utm_source=share", "https://example.com/r/videos/comments/123456789/a_title"],
    ["https://example.com/watch/?v=123456789012345", "https://example.com/watch?v=123456789012345"],
    ["https://example.com/watch?v=123456789012345&t=30", "https://example.com/watch?v=123456789012345"],
    ["https://example.com/pin/123456789012345678/", "https://example.com/pin/123456789012345678"],
    ["https://example.com/stories/creator/12345678901234567/?utm_source=share", "https://example.com/stories/creator/12345678901234567"]
];
for (const [input, expected] of genericPermalinkMatrix) {
    const actual = presentation.chooseContentPageUrl(input, []);
    if (actual !== expected) throw new Error(`Generic permalink matrix failed: ${input} -> ${actual}`);
}

const structuredContentPage = presentation.chooseStructuredVideoPageUrl(
    "https://example.com/746646949?fl=pl&fe=vl",
    {
        ogType: "video.other",
        twitterCard: "player",
        canonicalUrl: "https://example.com/746646949",
        playerUrl: "https://player.example.com/video/746646949?h=727f6a2632"
    }
);
if (structuredContentPage !== "https://example.com/746646949") {
    throw new Error(`Structured video metadata must expose the canonical content page; got ${structuredContentPage}`);
}
if (presentation.chooseStructuredVideoPageUrl("https://example.com/", {
    ogType: "website",
    twitterCard: "summary"
}) !== "") {
    throw new Error("A generic home page without explicit video metadata must not become a resolver candidate");
}

// 站点无关：只处理 blob: 播放源的页面解析器发现，不得把未播放的预加载
// 播放器算成"当前视频"。旧版用站点专用的 `selectPrimaryPageVideo()` 选主
// 播放器；这里改用站点无关的 `resolveVisualMatch()` + `stableVisualKey()`。
const livePlayerIndexInspection = presentation.resolveVisualMatch(
    [
        { id: "visible-45m", sources: ["https://cdn.example/current/video.mp4?token=one"] },
        { id: "preloaded-11m", sources: ["blob:https://example.com/preloaded"] }
    ],
    "https://cdn.example/current/video.mp4?token=two"
);
if (livePlayerIndexInspection?.selected?.id !== "visible-45m" || livePlayerIndexInspection.kind !== "exact") {
    throw new Error("Only the player actually serving the requested bytes may be selected");
}

const signedDirectPlayers = [
    {
        id: "visible-45m",
        sources: ["https://cdn.example/current/video.mp4?token=one"],
        playing: true,
        score: 900,
        duration: 2713.034
    },
    {
        id: "preloaded-11m",
        sources: ["blob:https://example.com/preloaded"],
        playing: false,
        score: 0,
        duration: 708.18
    }
];
const signedDirectMatch = presentation.resolveVisualMatch(
    signedDirectPlayers,
    "https://cdn.example/current/video.mp4?token=two"
);
if (signedDirectMatch?.selected?.id !== "visible-45m" || signedDirectMatch.kind !== "exact") {
    throw new Error("A direct media request must match its player after signed-query normalization");
}
const preloadedRequest = presentation.resolveVisualMatch(
    signedDirectPlayers,
    "https://cdn.example/preloaded/other-video.mp4?token=hidden"
);
if (preloadedRequest?.selected || preloadedRequest?.kind !== "none") {
    throw new Error("An unmatched preload request must not inherit the visible player's frame, duration or identity");
}
// 站点无关的画质目录：清单必须暴露**当前视频实际声明**的每一档，
// 不得退化成固定的五档列表。
const eightQualityCatalog = presentation.parseManifestQualities(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=180000,RESOLUTION=256x144
144/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=480000,RESOLUTION=426x240
240/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=640x360
360/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1300000,RESOLUTION=854x480
480/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720
720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4800000,RESOLUTION=1920x1080
1080/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=9000000,RESOLUTION=2560x1440
1440/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=18000000,RESOLUTION=3840x2160
2160/index.m3u8`, "m3u8");
if (eightQualityCatalog.join(",") !== "2160p,1440p,1080p,720p,480p,360p,240p,144p") {
    throw new Error("Quality catalogs must use every level advertised by the current video, not a fixed five-level list");
}

const hlsCatalog = presentation.parseManifestQualities(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=180000,RESOLUTION=256x144
144/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=480000,RESOLUTION=426x240
240/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=640x360
360/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1300000,RESOLUTION=854x480
480/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2400000,RESOLUTION=1280x720
720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4800000,RESOLUTION=1920x1080
1080/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=9000000,RESOLUTION=2560x1440
1440/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=18000000,RESOLUTION=3840x2160
2160/index.m3u8`, "m3u8");
if (hlsCatalog.join(",") !== "2160p,1440p,1080p,720p,480p,360p,240p,144p") {
    throw new Error("A generic HLS master must expose every resolution it actually advertises");
}

const dashCatalog = presentation.parseManifestQualities(`<MPD><Period><AdaptationSet mimeType="video/mp4">
<Representation id="one" width="7680" height="4320" />
<Representation id="two" width="3840" height="2160" />
<Representation id="three" width="1920" height="1080" />
</AdaptationSet></Period></MPD>`, "mpd");
if (dashCatalog.join(",") !== "4320p,2160p,1080p") {
    throw new Error("A generic DASH manifest must expose its own resolution count without a preset list");
}

const grouped = ui.groupCandidates([
    { tabId: 9, requestId: "v", url: "https://cdn.example/video.m4s", webUrl: "https://example.com/watch", role: "video", groupKey: "same-content", duration: 10 },
    { tabId: 9, requestId: "a", url: "https://cdn.example/audio.m4s", webUrl: "https://example.com/watch", role: "audio", groupKey: "same-content", duration: 10 }
]);
if (grouped.length !== 1) throw new Error("Toolbar count must use the same grouped-content model as the popup");

if (typeof ui.patchSidebarSelection !== "function") {
    throw new Error("Selecting a sidebar item must patch selection in place instead of rebuilding the scroll container");
}
const sidebarRows = ["first", "middle", "last"].map(id => ({
    dataset: { groupId: id },
    attributes: {},
    checkbox: { checked: false },
    setAttribute(name, value) { this.attributes[name] = String(value); }
}));
for (const row of sidebarRows) {
    row.closest = selector => selector === ".bridge-group-item"
        ? { querySelector: query => query === "[data-batch-group]" ? row.checkbox : null }
        : null;
}
const sidebarList = {
    scrollTop: 4820,
    querySelectorAll(selector) {
        if (selector !== "[data-group-id]") throw new Error(`Unexpected selector: ${selector}`);
        return sidebarRows;
    }
};
ui.patchSidebarSelection(sidebarList, "last");
if (sidebarList.scrollTop !== 4820) {
    throw new Error("Selecting the last sidebar item must not move the current scroll position");
}
if (sidebarRows[2].attributes["aria-current"] !== "true"
    || sidebarRows[2].attributes["aria-selected"] !== "true"
    || sidebarRows[0].attributes["aria-current"] !== "false") {
    throw new Error("In-place sidebar selection must update only the row accessibility state");
}
ui.patchSidebarSelection(sidebarList, "middle", {
    batchMode: true,
    selectedGroupIds: new Set(["first", "last"])
});
if (sidebarList.scrollTop !== 4820
    || sidebarRows[0].attributes["aria-selected"] !== "true"
    || sidebarRows[1].attributes["aria-selected"] !== "false"
    || !sidebarRows[2].checkbox.checked) {
    throw new Error("Batch selection must also update in place without rebuilding or scrolling the list");
}

(async () => {
    await Promise.all([recoveredDiscoveryPromise, alreadyReadyDiscoveryPromise, restrictedDiscoveryPromise]);
    let ready = false;
    let snapshot = { init: true };
    setTimeout(() => {
        snapshot = { 9: [{ requestId: "ready" }] };
        ready = true;
    }, 25);
    const result = await presentation.waitForSnapshot(() => ready, () => snapshot, 500, 5);
    if (result[9]?.[0]?.requestId !== "ready") {
        throw new Error("Initial popup read returned before the restored media cache was ready");
    }
    console.log("Cross-site candidate presentation and startup snapshot OK");
})().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
