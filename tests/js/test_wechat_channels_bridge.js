/*
 * 视频号网页 bridge 的回归。
 *
 * **注意被测对象不是浏览器扩展**：本文件测的是桌面端注入到微信内嵌页面的
 * `src/idm_eagle_bridge/assets/wechat_channels_bridge.js`（旧仓库确有该文件）。
 * 07 §8 把它列在"7 个 JS 测试"里，是因为它是既有的回归资产，而不是因为它
 * 测扩展代码。视频号捕获子系统属阶段 5（13 §6），新仓库尚无该资产。
 *
 * 本文件保留**全部原断言**，只在目标缺失时输出 SKIP 并以退出码 0 结束——
 * 既不虚报通过，也不删掉断言。阶段 5 把该 bridge 落到新仓库后，只需把下面的
 * 路径指过去，本文件即可恢复为真实门禁。
 */

const assert = require("assert");
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const bridgePath = path.join(__dirname, "..", "..", "internal", "wechat", "assets", "wechat_channels_bridge.js");
if (!fs.existsSync(bridgePath)) {
  process.stdout.write(
    "SKIP test_wechat_channels_bridge: 桌面端注入的 wechat_channels_bridge.js 尚未迁入新仓库"
    + "（视频号捕获属阶段 5）；本文件的全部断言原样保留，待资产落地后恢复为真实门禁\n"
  );
  process.exit(0);
}
const source = fs.readFileSync(bridgePath, "utf8");

function XMLHttpRequest() {}
XMLHttpRequest.prototype.open = function () {};
XMLHttpRequest.prototype.addEventListener = function () {};

const posted = [];
const documentEvents = {};
const context = {
  __DOWNLOAD_STATION_TEST__: true,
  console,
  document: {
    createElement() { return {}; },
    addEventListener(type, handler) { documentEvents[type] = handler; },
  },
  fetch: (_url, options) => {
    const payload = JSON.parse(options.body);
    posted.push(payload);
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve(payload.action === "download"
        ? { action: "download", plan: { id: "plan-test", delivery: "local" } }
        : {}),
    });
  },
  globalThis: null,
  location: { href: "https://channels.weixin.qq.com/web/pages/feed?objectId=feed-1234" },
  clearTimeout,
  setTimeout,
  URL,
  window: {
    addEventListener() {},
    innerWidth: 1650,
    innerHeight: 1000,
    getComputedStyle() { return { display: "block", visibility: "visible", opacity: "1" }; },
  },
  XMLHttpRequest,
};
context.globalThis = context;
vm.createContext(context);
vm.runInContext(source, context);

const api = context.__DOWNLOAD_STATION_WECHAT_TEST__;
assert(api, "test API should be exposed only in test mode");
assert(source.includes(".slides-item .click-box.op-item"));
assert(source.includes('action: "download"'));
assert(source.includes("variantId"));
assert(source.includes("download-station-wechat-control"));

const normalized = api.normalizeFeed({
  id: "feed-1234",
  objectDesc: {
    description: "每条内容自己的标题",
    media: [{
      url: "https://finder.video.qq.com/video.mp4?token=one",
      urlToken: "&extra=two",
      decodeKey: "123456789",
      width: 1920,
      height: 1080,
      videoPlayLen: 91,
      fileSize: 5000000,
      coverUrl: "https://finder.video.qq.com/cover.jpg",
      spec: [{
        fileFormat: "hd",
        width: 1920,
        height: 1080,
        durationMs: 91000,
        videoBitrate: 4000000,
      }],
    }],
  },
  contact: { nickname: "视频号作者", username: "author-id" },
});

assert.strictEqual(normalized.objectId, "feed-1234");
assert.strictEqual(normalized.title, "每条内容自己的标题");
assert.strictEqual(normalized.author, "视频号作者");
assert.strictEqual(normalized.media[0].height, 1080);
assert.strictEqual(normalized.media[0].decodeKey, "123456789");
assert.strictEqual(normalized.media[0].durationMs, 91000);
assert.strictEqual(normalized.media[0].specs[0].fileFormat, "hd");
assert(normalized.sourceUrl.includes("feed-1234"));
assert.strictEqual(api.normalizeFeed({ id: "no-media", objectDesc: { media: [] } }), null);

const refreshedAttachment = api.normalizeFeed({
  id: "feed-attachment-refresh",
  objectDesc: {
    description: "附件刷新后的媒体",
    media: [{
      url: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=stale",
      urlToken: "&token=stale-token",
    }],
  },
  attachmentList: {
    attachments: [{
      type: 1,
      video: { video: { desc: { media: [{
        url: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=fresh",
        urlToken: "&token=fresh-token",
        fileSize: 32697459,
        width: 1920,
        height: 1080,
      }] } } },
    }],
  },
});
assert(refreshedAttachment);
assert(refreshedAttachment.media[0].url.includes("encfilekey=fresh"));
assert.strictEqual(refreshedAttachment.media[0].fileSize, 32697459);

const flowTitle = api.normalizeFeed({
  id: "feed-flow",
  objectDesc: {
    flowCardDesc: { description: "卡片内容标题" },
    media: [{ url: "https://finder.video.qq.com/flow.mp4" }],
  },
});
assert.strictEqual(flowTitle.title, "卡片内容标题");

const richTitle = api.normalizeFeed({
  id: "feed-rich",
  objectDesc: {
    finderNewlifeDesc: { richTextTitle: "富文本内容标题" },
    media: [{ url: "https://finder.video.qq.com/rich.mp4" }],
  },
});
assert.strictEqual(richTitle.title, "富文本内容标题");

const clearMedia = api.mediaItem({ url: "https://finder.video.qq.com/clear.mp4", decodeKey: 0, videoPlayLen: 41 });
assert.strictEqual(clearMedia.decodeKey, "");
assert.strictEqual(clearMedia.durationMs, 41000);

const reissuedOrigin = "https://finder.video.qq.com/251/20302/stodownload?encfilekey=origin-asset&token=origin-token&basedata=origin-catalog&sign=origin-signature";
const reissuedH264 = "https://finder.video.qq.com/251/20302/stodownload?encfilekey=h264-asset&token=h264-token&sign=h264-signature";
assert.deepStrictEqual(Array.from(api.sharedOriginalUrls({
  originVideoUrl: reissuedOrigin,
  videoUrl: reissuedOrigin,
  h264VideoInfo: { videoUrl: reissuedH264 },
  h265VideoInfo: { videoUrl: "https://evil.example/stodownload?encfilekey=bad&token=bad" },
})), [reissuedOrigin, reissuedH264]);
assert.strictEqual(
  api.issuedOriginalUrl("https://finder.video.qq.com/251/20302/stodownload?encfilekey=missing-token"),
  "",
);
const rolloutOriginals = Array.from(api.expandOriginalUrls([reissuedOrigin]));
assert(rolloutOriginals[0].includes("X-snsvideoflag=original"));
assert(rolloutOriginals.includes(reissuedOrigin));
assert.strictEqual(api.sharedFeedH5Url({
  data: { object: { feedH5Url: "https://weixin.qq.com/sph/AbCdEf12" } },
}), "https://weixin.qq.com/sph/AbCdEf12");
const exportedShareProfile = api.sharedFeedRequestProfile(
  "https://channels.weixin.qq.com/finder-preview/pages/feed?token=dynamic-general-token&eid=dynamic-export-id",
);
assert.strictEqual(exportedShareProfile.mode, "export");
assert.strictEqual(exportedShareProfile.body.baseReq.generalToken, "dynamic-general-token");
assert.strictEqual(exportedShareProfile.body.exportId, "dynamic-export-id");
const shortShareProfile = api.sharedFeedRequestProfile("https://weixin.qq.com/sph/AbCdEf12");
assert.strictEqual(shortShareProfile.mode, "short");
assert.strictEqual(shortShareProfile.body.shortUri, "AbCdEf12");
assert.strictEqual(api.sharedDynamicExportId({
  data: { sceneInfo: { dynamicExportId: "encrypted-feed-id" } },
}), "encrypted-feed-id");
const sharedDetailUrl = "https://finder.video.qq.com/251/20302/stodownload?encfilekey=shared-detail&token=shared-token&sign=shared-sign";
const sharedDetail = api.detailOriginalProfile({ data: { object: {
  id: "shared-detail-feed",
  objectDesc: {
    media: [{
      url: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=shared-detail",
      urlToken: "&token=shared-token&sign=shared-sign",
      decodeKey: "987654321",
    }],
  },
} } }, "shared-detail-feed");
assert(sharedDetail);
assert.deepStrictEqual(Array.from(sharedDetail.urls), [sharedDetailUrl]);
assert.strictEqual(sharedDetail.decodeKey, "987654321");
assert.strictEqual(api.originalIssuerAvailable(), false);

assert.strictEqual(typeof context.__DOWNLOAD_STATION_WECHAT_OBSERVE__, "function");
context.__DOWNLOAD_STATION_WECHAT_OBSERVE__({ data: { object: [{
  id: "feed-internal-api",
  objectDesc: {
    description: "内部 API 返回的视频",
    media: [{ url: "https://finder.video.qq.com/internal.mp4", videoPlayLen: 15 }],
  },
}] } }, "finderGetCommentDetail");
assert(posted.some((item) => item.objectId === "feed-internal-api"));
assert.strictEqual(api.activeObjectId(), "feed-internal-api");
context.location.href = "https://channels.weixin.qq.com/web/pages/feed";
assert.strictEqual(
  api.entryFromTrigger({ closest() { return null; }, parentElement: null }),
  null,
  "a sole seen candidate without current-page evidence must not be guessed",
);

const currentVariant = api.selectDefaultVariant({
  variants: [
    { id: "hd", deliverySpec: "hd", quality: "1080p · hd" },
    { id: "current", deliverySpec: "", isOriginal: true, quality: "720p · 原始视频" },
  ],
}, "https://finder.video.qq.com/video.mp4");
assert.strictEqual(currentVariant.id, "hd");

const declaredSourceVariant = api.selectDefaultVariant({
  variants: [
    { id: "source", width: 1920, height: 1080, fileSize: 32697459, isOriginal: true },
    { id: "xwt111", width: 1280, height: 720, deliverySpec: "xWT111" },
  ],
}, "https://finder.video.qq.com/video.mp4?X-snsvideoflag=xWT111");
assert.strictEqual(
  declaredSourceVariant.id,
  "source",
  "a larger source asset must be attempted instead of silently accepting 720p",
);
assert.strictEqual(api.sourceUpgradeRequired({
  feed: { media: [{ width: 1920, height: 1080, fileSize: 32697459 }] },
  candidate: { variants: [
    { width: 1280, height: 720, deliverySpec: "xWT111" },
  ] },
}), true);
assert.strictEqual(api.sourceUpgradeRequired({
  feed: { media: [{ width: 1920, height: 1080, fileSize: 32697459 }] },
  candidate: { variants: [
    { width: 1920, height: 1080, fileSize: 32697459, isOriginal: true },
    { width: 1280, height: 720, deliverySpec: "xWT111" },
  ] },
}), false);

const originalOnlyVariant = api.selectDefaultVariant({
  variants: [
    { id: "original", deliverySpec: "", isOriginal: true, quality: "原始视频" },
  ],
}, "https://finder.video.qq.com/video.mp4");
assert.strictEqual(originalOnlyVariant.id, "original");

const selectedVariant = api.selectDefaultVariant({
  variants: [
    { id: "hd", deliverySpec: "hd", quality: "1080p · hd" },
    { id: "current", deliverySpec: "", isOriginal: true, quality: "720p · 原始视频" },
  ],
}, "https://finder.video.qq.com/video.mp4?X-snsvideoflag=hd");
assert.strictEqual(selectedVariant.id, "hd");

const transcodeOnlyVariant = api.selectDefaultVariant({
  variants: [
    { id: "highest", deliverySpec: "xWT156", quality: "1080p · xWT156" },
    { id: "playing", deliverySpec: "xWT111", quality: "720p · xWT111" },
  ],
}, "https://finder.video.qq.com/video.mp4?X-snsvideoflag=xWT111");
assert.strictEqual(transcodeOnlyVariant.id, "highest");

assert.strictEqual(
  api.downloadStartedMessage({ delivery: "local" }, "本机视频"),
  "Eagle 未连接，已改为下载到电脑并保留文件：本机视频",
);
assert.strictEqual(
  api.downloadStartedMessage({ delivery: "eagle" }, "归档视频"),
  "已开始下载并导入 Eagle：归档视频",
);
assert.strictEqual(
  api.downloadStartedMessage({}, "兼容旧版"),
  "已开始下载：兼容旧版",
);

// A late response for a preloaded next feed must never make the download
// control inside an ambiguous current slide submit that other feed.  This is
// the intermittent production shape: WeChat may expose a blob currentSrc and
// the visible overlay text may not contain the feed description.
context.location.href = "https://channels.weixin.qq.com/web/pages/feed";
const visibleFeed = api.normalizeFeed({
  id: "visible-feed",
  objectDesc: {
    description: "用户当前看见的视频",
    media: [{ url: "https://finder.video.qq.com/visible.mp4" }],
  },
});
const preloadedRaw = {
  id: "preloaded-feed",
  objectDesc: {
    description: "下一条预加载视频",
    media: [{ url: "https://finder.video.qq.com/preloaded.mp4" }],
  },
};
api.accept(visibleFeed);
api.scan({ data: { object: [preloadedRaw] } }, "goToNextFlowFeed");
assert.notStrictEqual(
  api.activeObjectId(),
  "preloaded-feed",
  "a next-feed response may be a preload and must not activate before playback",
);
api.setCandidate("visible-feed", {
  objectId: "visible-feed",
  variants: [{ id: "visible-original", deliverySpec: "", quality: "原始视频" }],
});
api.setCandidate("preloaded-feed", {
  objectId: "preloaded-feed",
  variants: [{ id: "preloaded-original", deliverySpec: "", quality: "原始视频" }],
});
const ambiguousSlide = {
  textContent: "当前播放画面没有接口返回的描述文本",
  querySelectorAll(selector) {
    return selector === "video"
      ? [{ currentSrc: "blob:https://channels.weixin.qq.com/current", src: "", querySelectorAll() { return []; } }]
      : [];
  },
};
const label = { textContent: "下载" };
const ambiguousTrigger = {
  style: {},
  closest(selector) { return selector === ".slides-item" ? ambiguousSlide : null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
const toastNode = { style: {}, textContent: "", __downloadStationTimer: 0 };
context.document.getElementById = (id) => id === "download-station-wechat-toast" ? toastNode : null;
posted.length = 0;
api.requestDownload(ambiguousTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "an ambiguous slide must not fall back to a different active/preloaded feed",
);
assert.strictEqual(toastNode.textContent, "未能确认当前视频，请先播放当前视频后重试");
clearTimeout(toastNode.__downloadStationTimer);

// The safety guard must not disable a strongly matched current slide.  Even
// with the neighbour already preloaded, the visible media URL owns the click.
ambiguousSlide.querySelectorAll = function (selector) {
  return selector === "video"
    ? [{ currentSrc: "https://finder.video.qq.com/visible.mp4?token=fresh", src: "", querySelectorAll() { return []; } }]
    : [];
};
posted.length = 0;
api.requestDownload(ambiguousTrigger, "");
const matchedDownload = posted.find((item) => item.action === "download");
assert(matchedDownload, "a strongly matched visible slide should still download");
assert.strictEqual(matchedDownload.objectId, "visible-feed");
assert.strictEqual(matchedDownload.variantId, "visible-original");

// Production finder URLs frequently share the same stodownload path.  The
// stable encfilekey must identify the current video while short-lived tokens
// and signatures are allowed to rotate.
const sharedA = api.normalizeFeed({
  id: "shared-path-a",
  objectDesc: {
    description: "共享路径甲",
    media: [{
      url: "https://finder.video.qq.com/251/20302/stodownload?token=old-a",
      urlToken: "&encfilekey=stable-a&sign=old-sign",
    }],
  },
});
const sharedB = api.normalizeFeed({
  id: "shared-path-b",
  objectDesc: {
    description: "共享路径乙",
    media: [{
      url: "https://finder.video.qq.com/251/20302/stodownload?token=old-b",
      urlToken: "&encfilekey=stable-b&sign=old-sign",
    }],
  },
});
api.accept(sharedA);
api.accept(sharedB);
api.setCandidate("shared-path-a", {
  objectId: "shared-path-a",
  variants: [{ id: "shared-a-hd", deliverySpec: "hd", quality: "1080p · hd" }],
});
api.setCandidate("shared-path-b", {
  objectId: "shared-path-b",
  variants: [{ id: "shared-b-hd", deliverySpec: "hd", quality: "1080p · hd" }],
});
const sharedVideo = {
  currentSrc: "https://finder.video.qq.com/251/20302/stodownload?token=fresh&ENCFILEKEY=stable-a&sign=fresh-sign",
  src: "",
  paused: false,
  ended: false,
  querySelectorAll() { return []; },
};
const sharedSlide = {
  textContent: "不依赖标题",
  querySelectorAll(selector) {
    if (selector === "video") return [sharedVideo];
    return [];
  },
};
const sharedTrigger = {
  style: {},
  closest(selector) { return selector === ".slides-item" ? sharedSlide : null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
posted.length = 0;
api.requestDownload(sharedTrigger, "");
const sharedDownload = posted.find((item) => item.action === "download");
assert(sharedDownload, "encfilekey should disambiguate a shared production path");
assert.strictEqual(sharedDownload.objectId, "shared-path-a");

// A generic stodownload path without encfilekey is not media identity, even
// when only one captured neighbour happens to use that exact path.
const keylessNeighbour = api.normalizeFeed({
  id: "keyless-neighbour",
  objectDesc: {
    description: "仅预加载邻居",
    media: [{ url: "https://finder.video.qq.com/999/888/stodownload?token=neighbour" }],
  },
});
api.accept(keylessNeighbour);
api.setCandidate("keyless-neighbour", {
  objectId: "keyless-neighbour",
  variants: [{ id: "keyless-hd", deliverySpec: "hd", quality: "1080p · hd" }],
});
const keylessSlide = {
  textContent: "当前内容尚未捕获",
  querySelectorAll(selector) {
    return selector === "video" ? [{
      currentSrc: "https://finder.video.qq.com/999/888/stodownload?token=current-no-key",
      src: "",
      paused: false,
      ended: false,
      querySelectorAll() { return []; },
    }] : [];
  },
};
const keylessTrigger = {
  style: {},
  closest(selector) { return selector === ".slides-item" ? keylessSlide : null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
posted.length = 0;
api.requestDownload(keylessTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "keyless stodownload paths must not select the only preloaded neighbour",
);

// Author and duration alone are not authoritative.  The screenshot's MSE
// blob shape must wait for a trusted current-detail binding instead of
// accidentally selecting a preloaded neighbour mentioned in visible text.
const blobCurrent = api.normalizeFeed({
  id: "blob-current",
  objectDesc: {
    description: "页面可能没有显示这段完整描述",
    media: [{
      url: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=blob-current-key",
      durationMs: 18000,
    }],
  },
  contact: { nickname: "_CHENGYUAN-" },
});
const blobNeighbour = api.normalizeFeed({
  id: "blob-neighbour",
  objectDesc: {
    description: "相邻预加载内容",
    media: [{
      url: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=blob-neighbour-key",
      durationMs: 18000,
    }],
  },
  contact: { nickname: "另一位作者" },
});
api.accept(blobCurrent);
api.accept(blobNeighbour);
api.setCandidate("blob-current", {
  objectId: "blob-current",
  variants: [{ id: "blob-hd", deliverySpec: "hd", quality: "1080p · hd" }],
});
const blobVideo = {
  currentSrc: "blob:https://channels.weixin.qq.com/playing",
  src: "",
  duration: 18,
  paused: false,
  ended: false,
  querySelectorAll() { return []; },
};
const blobSlide = {
  textContent: "音乐 小邓不抽烟 等2个朋友♡ _CHENGYUAN- +关注",
  querySelectorAll(selector) {
    if (selector === "video") return [blobVideo];
    return [];
  },
};
const blobTrigger = {
  style: {},
  closest(selector) { return selector === ".slides-item" ? blobSlide : null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
posted.length = 0;
api.requestDownload(blobTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "visible author and duration alone must not select an MSE blob video",
);

// A duplicate author and duration remains ambiguous and must still refuse.
const blobDuplicate = api.normalizeFeed({
  id: "blob-duplicate",
  objectDesc: {
    description: "同作者同长度的另一条",
    media: [{
      url: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=blob-duplicate-key",
      durationMs: 18000,
    }],
  },
  contact: { nickname: "_CHENGYUAN-" },
});
api.accept(blobDuplicate);
posted.length = 0;
api.requestDownload(blobTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "duplicate visible metadata must not guess between neighbouring feeds",
);

// finderGetCommentDetail is an authoritative current-feed response.  Bind it
// only to the sole visible playing video; a later next-feed preload cannot
// steal that binding, and no .slides-item ancestor is required.
const authoritativeVideo = {
  currentSrc: "blob:https://channels.weixin.qq.com/authoritative",
  src: "",
  duration: 27,
  paused: false,
  ended: false,
  getClientRects() { return [{ left: 0, top: 0, right: 1200, bottom: 800, width: 1200, height: 800 }]; },
  querySelectorAll() { return []; },
};
const offscreenVideo = {
  currentSrc: "blob:https://channels.weixin.qq.com/offscreen-preload",
  src: "",
  duration: 27,
  paused: false,
  ended: false,
  getClientRects() { return [{ left: 1800, top: 0, right: 3000, bottom: 800, width: 1200, height: 800 }]; },
  querySelectorAll() { return []; },
};
const ancestorScope = {
  textContent: "截断文案",
  parentElement: null,
  querySelectorAll(selector) {
    if (selector === "video") return [authoritativeVideo];
    return [];
  },
};
context.document.querySelectorAll = (selector) => selector === "video"
  ? [authoritativeVideo, offscreenVideo]
  : [];
api.scan({ data: {
  // Related feeds may be traversed before the direct object by BFS.  They are
  // candidates only and must never participate in current-detail selection.
  recommendations: [{
    id: "detail-related-first",
    objectDesc: {
      description: "详情里的关联推荐",
      media: [{ url: "https://finder.video.qq.com/detail-related-first", durationMs: 27000 }],
    },
  }],
  object: [{
    id: "authoritative-current",
    objectDesc: {
      description: "完整标题不会出现在页面",
      media: [{ url: "https://finder.video.qq.com/current-authoritative", durationMs: 27000 }],
    },
  }],
} }, "finderGetCommentDetail");
assert.strictEqual(
  api.activeObjectId(),
  "authoritative-current",
  "only data.object may activate current when detail also contains recommendations",
);
api.setCandidate("authoritative-current", {
  objectId: "authoritative-current",
  variants: [{ id: "authoritative-hd", deliverySpec: "hd", quality: "1080p · hd" }],
});
api.scan({ data: { object: [{
  id: "late-next-feed",
  objectDesc: {
    description: "晚到的下一条",
    media: [{ url: "https://finder.video.qq.com/late-next", durationMs: 27000 }],
  },
}] } }, "goToNextFlowFeed");
const noSlideTrigger = {
  style: {},
  parentElement: ancestorScope,
  closest() { return null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
posted.length = 0;
api.requestDownload(noSlideTrigger, "");
const authoritativeDownload = posted.find((item) => item.action === "download");
assert(authoritativeDownload, "authoritative detail should bind to the playing blob video");
assert.strictEqual(authoritativeDownload.objectId, "authoritative-current");

// Reusing the same video node and blob URL must not retain A after media
// lifecycle invalidation.  It becomes downloadable again only after B's
// authoritative detail arrives.
assert(documentEvents.loadstart, "the bridge must invalidate bindings on loadstart");
documentEvents.loadstart({ target: authoritativeVideo });
posted.length = 0;
api.requestDownload(noSlideTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "a reused blob player must refuse while the next detail is still unknown",
);
api.scan({ data: { object: [{
  id: "authoritative-next",
  objectDesc: {
    description: "下一条完整标题也不可见",
    media: [{ url: "https://finder.video.qq.com/authoritative-next", durationMs: 27000 }],
  },
}] } }, "finderGetCommentDetail");
api.setCandidate("authoritative-next", {
  objectId: "authoritative-next",
  variants: [{ id: "authoritative-next-hd", deliverySpec: "hd", quality: "1080p · hd" }],
});
posted.length = 0;
api.requestDownload(noSlideTrigger, "");
const authoritativeNextDownload = posted.find((item) => item.action === "download");
assert(authoritativeNextDownload, "the next authoritative detail should replace the old binding");
assert.strictEqual(authoritativeNextDownload.objectId, "authoritative-next");

// If the authoritative object field itself contains two distinct feeds there
// is no current identity.  Related traversal order and the previously active
// feed must not turn that ambiguity into a download.
documentEvents.loadstart({ target: authoritativeVideo });
api.scan({ data: { object: [{
  id: "detail-direct-a",
  objectDesc: {
    description: "直接对象甲",
    media: [{ url: "https://finder.video.qq.com/detail-direct-a", durationMs: 27000 }],
  },
}, {
  id: "detail-direct-b",
  objectDesc: {
    description: "直接对象乙",
    media: [{ url: "https://finder.video.qq.com/detail-direct-b", durationMs: 27000 }],
  },
}] } }, "finderGetCommentDetail");
posted.length = 0;
api.requestDownload(noSlideTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "two direct detail objects must refuse instead of reusing the prior binding",
);

// A scope exposing two known object IDs is ambiguous regardless of DOM order.
const idNode = (value) => ({
  href: "",
  getAttribute(name) { return name === "data-object-id" ? value : ""; },
});
const ambiguousIdSlide = {
  textContent: "无完整标题",
  querySelectorAll(selector) {
    if (selector.includes("[data-object-id]")) {
      return [idNode("shared-path-a")]
        .concat(Array.from({ length: 601 }, () => idNode("")))
        .concat([idNode("shared-path-b")]);
    }
    if (selector === "video") return [{
      currentSrc: "blob:https://channels.weixin.qq.com/ambiguous-ids",
      src: "",
      paused: false,
      ended: false,
      querySelectorAll() { return []; },
    }];
    return [];
  },
};
const ambiguousIdTrigger = {
  style: {},
  closest(selector) { return selector === ".slides-item" ? ambiguousIdSlide : null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
posted.length = 0;
api.requestDownload(ambiguousIdTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "two object IDs inside one scope must not pick the first DOM node",
);

// A stale SPA address for A conflicts with an explicit current-card B and
// must refuse instead of letting location win by ordering.
ambiguousIdSlide.querySelectorAll = function (selector) {
  if (selector.includes("[data-object-id]")) return [idNode("shared-path-b")];
  if (selector === "video") return [{
    currentSrc: "blob:https://channels.weixin.qq.com/location-conflict",
    src: "",
    paused: false,
    ended: false,
    querySelectorAll() { return []; },
  }];
  return [];
};
context.location.href = "https://channels.weixin.qq.com/web/pages/feed?objectId=shared-path-a";
posted.length = 0;
api.requestDownload(ambiguousIdTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "stale location and current-card object IDs must conflict instead of picking A",
);
context.location.href = "https://channels.weixin.qq.com/web/pages/feed";

// With a playing blob video, stale location A is not sufficient by itself
// when B has not yet produced any corroborating evidence.
const staleLocationSlide = {
  textContent: "新视频还没有结构化候选",
  querySelectorAll(selector) {
    if (selector === "video") return [{
      currentSrc: "blob:https://channels.weixin.qq.com/new-unknown",
      src: "",
      paused: false,
      ended: false,
      querySelectorAll() { return []; },
    }];
    return [];
  },
};
const staleLocationTrigger = {
  style: {},
  closest(selector) { return selector === ".slides-item" ? staleLocationSlide : null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
context.location.href = "https://channels.weixin.qq.com/web/pages/feed?objectId=shared-path-a";
posted.length = 0;
api.requestDownload(staleLocationTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "stale location alone must not authorize while a new blob video is playing",
);
context.location.href = "https://channels.weixin.qq.com/web/pages/feed";

// A unique whitelisted card object ID is strong evidence, while unrelated
// attributes are ignored.
ambiguousIdSlide.querySelectorAll = function (selector) {
  if (selector.includes("[data-object-id]")) return [idNode("shared-path-b")];
  if (selector === "video") return [{
    currentSrc: "blob:https://channels.weixin.qq.com/card-id",
    src: "",
    paused: false,
    ended: false,
    querySelectorAll() { return []; },
  }];
  return [];
};
posted.length = 0;
api.requestDownload(ambiguousIdTrigger, "");
const cardIdDownload = posted.find((item) => item.action === "download");
assert(cardIdDownload, "a unique whitelisted card object ID should resolve the feed");
assert.strictEqual(cardIdDownload.objectId, "shared-path-b");

// video.poster is accepted as a scoped cover identity.  Generic page images
// are intentionally not scanned, and a duplicated poster remains ambiguous.
const posterA = api.normalizeFeed({
  id: "poster-feed-a",
  objectDesc: {
    description: "封面甲",
    media: [{
      url: "https://finder.video.qq.com/poster-a-video",
      coverUrl: "https://finder.video.qq.com/covers/current-a.jpg?token=old",
    }],
  },
});
api.accept(posterA);
api.setCandidate("poster-feed-a", {
  objectId: "poster-feed-a",
  variants: [{ id: "poster-a-hd", deliverySpec: "hd", quality: "1080p · hd" }],
});
const posterVideo = {
  currentSrc: "blob:https://channels.weixin.qq.com/poster-current",
  src: "",
  poster: "https://finder.video.qq.com/covers/current-a.jpg?token=fresh",
  paused: false,
  ended: false,
  querySelectorAll() { return []; },
};
const posterSlide = {
  textContent: "无标题",
  querySelectorAll(selector) {
    if (selector === "video") return [posterVideo];
    if (selector === "img") return [{ src: "https://finder.video.qq.com/ignored-neighbour.jpg" }];
    return [];
  },
};
const posterTrigger = {
  style: {},
  closest(selector) { return selector === ".slides-item" ? posterSlide : null; },
  querySelector(selector) { return selector === '[data-role="download-label"]' ? label : null; },
  setAttribute() {},
};
posted.length = 0;
api.requestDownload(posterTrigger, "");
const posterDownload = posted.find((item) => item.action === "download");
assert(posterDownload, "a unique video poster should resolve the feed");
assert.strictEqual(posterDownload.objectId, "poster-feed-a");

const preloadedPosterVideo = {
  currentSrc: "blob:https://channels.weixin.qq.com/poster-preload",
  src: "",
  poster: "https://finder.video.qq.com/covers/current-a.jpg?token=preloaded",
  paused: true,
  ended: false,
  querySelectorAll() { return []; },
};
posterVideo.poster = "";
posterSlide.querySelectorAll = function (selector) {
  if (selector === "video") return [posterVideo, preloadedPosterVideo];
  return [];
};
posted.length = 0;
api.requestDownload(posterTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "a paused preloaded video's poster must not identify the current blob player",
);
posterVideo.poster = "https://finder.video.qq.com/covers/current-a.jpg?token=fresh";
posterSlide.querySelectorAll = function (selector) {
  if (selector === "video") return [posterVideo];
  return [];
};
const posterDuplicate = api.normalizeFeed({
  id: "poster-feed-b",
  objectDesc: {
    description: "封面乙",
    media: [{
      url: "https://finder.video.qq.com/poster-b-video",
      coverUrl: "https://finder.video.qq.com/covers/current-a.jpg?another=token",
    }],
  },
});
api.accept(posterDuplicate);
posted.length = 0;
api.requestDownload(posterTrigger, "");
assert.strictEqual(
  posted.some((item) => item.action === "download"),
  false,
  "a duplicated poster path must remain ambiguous",
);
delete context.document.querySelectorAll;

const refreshedDetailCalls = [];
const internalApiContext = {
  finderGetFeedH5Url(payload) { return Promise.resolve({ data: { payload } }); },
  finderGetCommentDetail(payload) {
    refreshedDetailCalls.push(payload);
    if (payload.objectid === "refresh-fallback" && payload.pullScene !== 8) {
      return Promise.resolve({ data: { object: {
        id: "different-feed",
        objectDesc: { media: [{ url: "https://finder.video.qq.com/different.mp4" }] },
      } } });
    }
    return Promise.resolve({ data: { object: {
      id: payload.objectid || (payload.encrypted_objectid ? "shared-direct-feed" : ""),
      objectDesc: {
        description: "刷新后的当前视频",
        media: [{
          url: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=refreshed",
          urlToken: "&token=refreshed-token&sign=refreshed-sign",
          decodeKey: "24680",
        }],
      },
    } } });
  },
};
context.__DOWNLOAD_STATION_WECHAT_OBSERVE__({}, "finderGetRecommend", internalApiContext);
assert.strictEqual(api.originalIssuerAvailable(), true);
const refreshObject = {
  data: { object: {
    id: "refresh-feed",
    objectDesc: {
      description: "当前视频",
      media: [{ url: "https://finder.video.qq.com/current.mp4" }],
    },
  } },
};
api.captureCurrentDetailPayload(
  refreshObject,
  "finderGetCommentDetail",
  [{
    objectid: "refresh-feed",
    finderUsername: "dynamic-current-account",
    scene: 7,
    direction: 2,
    objectNonceId: "dynamic-nonce",
    identityScene: 2,
    pullScene: 7,
    lastBuffer: "dynamic-buffer",
    traceBuffer: { rollout: "current-account" },
  }],
);
api.requestCurrentDetailRefresh("refresh-feed").then((profile) => {
  assert(profile.urls[0].includes("encfilekey=refreshed"));
  assert.strictEqual(refreshedDetailCalls[0].pullScene, 7);
  assert.strictEqual(refreshedDetailCalls[0].lastBuffer, "dynamic-buffer");
  assert.strictEqual(refreshedDetailCalls[0].traceBuffer.rollout, "current-account");
  assert.strictEqual(refreshedDetailCalls[0].finderUsername, "dynamic-current-account");
  assert.strictEqual(refreshedDetailCalls[0].objectNonceId, "dynamic-nonce");
  const beforeSharedDetail = refreshedDetailCalls.length;
  return api.requestSharedDetail({
    data: {
      feedInfo: {
        originVideoUrl: reissuedOrigin,
        decodeKey: "112233",
      },
      sceneInfo: { dynamicExportId: "shared-dynamic-export" },
    },
  }, "shared-direct-feed").then((sharedProfile) => {
    assert.strictEqual(refreshedDetailCalls.length, beforeSharedDetail + 1);
    const sharedCall = refreshedDetailCalls.at(-1);
    assert.strictEqual(sharedCall.encrypted_objectid, "shared-dynamic-export");
    assert.strictEqual(sharedCall.pullScene, 8);
    assert.strictEqual(sharedCall.finderUsername, "dynamic-current-account");
    assert.strictEqual(sharedCall.traceBuffer.rollout, "current-account");
    assert.strictEqual(Object.prototype.hasOwnProperty.call(sharedCall, "objectid"), false);
    assert.strictEqual(Object.prototype.hasOwnProperty.call(sharedCall, "objectNonceId"), false);
    assert(sharedProfile.urls.some((url) => url.includes("encfilekey=refreshed")));
    assert(sharedProfile.urls.includes(reissuedOrigin));
  });
}).then(() => {
  const fallbackFeed = api.normalizeFeed({
    id: "refresh-fallback",
    objectNonceId: "fallback-dynamic-nonce",
    objectDesc: {
      media: [{ url: "https://finder.video.qq.com/fallback.mp4" }],
    },
  });
  api.accept(fallbackFeed);
  const beforeFallback = refreshedDetailCalls.length;
  return api.requestCurrentDetailRefresh("refresh-fallback").then((profile) => {
    assert(profile.urls[0].includes("encfilekey=refreshed"));
    const fallbackCalls = refreshedDetailCalls.slice(beforeFallback);
    assert(fallbackCalls.length >= 2);
    assert.strictEqual(fallbackCalls.at(-1).pullScene, 8);
    assert.strictEqual(fallbackCalls.at(-1).finderUsername, "dynamic-current-account");
    assert.strictEqual(fallbackCalls.at(-1).objectNonceId, "fallback-dynamic-nonce");
  });
}).then(() => {
  console.log("wechat channels bridge tests passed");
}).catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
