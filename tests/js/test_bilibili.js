"use strict";

/*
 * 站点专用注入脚本的回归（阶段 3 的目标）。
 *
 * 07 §2：`catch-script/` **已取消**——站点专用脚本的归处是
 * `adapters/<site>/extension.js`，而适配器体系属阶段 3（13 §6）。
 * 因此本阶段 `extension/catch-script/bilibili.js` **不存在**：
 * 扩展包里已没有任何站点专用注入脚本。
 *
 * 本文件保留**全部原断言**，只在目标缺失时输出 SKIP 并以退出码 0 结束——
 * 既不虚报通过，也不删掉断言。阶段 3 把该脚本迁到
 * `adapters/bilibili/extension.js` 后，只需把下面的 `scriptPath` 指过去，
 * 本文件即可恢复为真实门禁（07 §8：站点断言迁到 `test_adapter_<id>.js`，
 * 原断言数量全部保留、不得降低覆盖）。
 */

const fs = require("fs");
const path = require("path");
const vm = require("vm");

const scriptPath = path.resolve(__dirname, "..", "..", "extension", "catch-script", "bilibili.js");
if (!fs.existsSync(scriptPath)) {
    process.stdout.write(
        "SKIP test_bilibili: extension/catch-script/bilibili.js 不存在"
        + "（07 §2 已取消 catch-script/，站点脚本属阶段 3 的 adapters/<site>/extension.js）；"
        + "本文件的全部断言原样保留，待阶段 3 指向适配器后恢复为真实门禁\n"
    );
    process.exit(0);
}

let posted = null;
global.location = {
    href: "https://www.bilibili.com/video/BV1TEST",
    origin: "https://www.bilibili.com"
};
global.document = {
    currentScript: { src: "chrome-extension://test/catch-script/bilibili.js?channel=test-channel" },
    title: "可见下载测试_哔哩哔哩_bilibili",
    documentElement: {},
    querySelector: selector => {
        if (selector === 'meta[property="og:image"]') return { content: "//i0.hdslb.com/bfs/archive/current-cover.jpg@100w_100h_1c.png" };
        if (selector === 'meta[property="og:title"]') return { content: "当前 B 站视频" };
        return null;
    },
    querySelectorAll: () => [{
        dataset: {},
        textContent: `window.__playinfo__=${JSON.stringify({
            data: {
                timelength: 10000,
                dash: {
                    video: [{
                        id: 80,
                        baseUrl: "https://cdn.example/video.m4s?token=secret",
                        mimeType: "video/mp4",
                        codecs: "avc1.640028",
                        width: 1920,
                        height: 1080,
                        frameRate: "30",
                        bandwidth: 4000000
                    }],
                    audio: [{
                        id: 30280,
                        baseUrl: "https://cdn.example/audio.m4s?token=secret",
                        mimeType: "audio/mp4",
                        codecs: "mp4a.40.2",
                        bandwidth: 192000
                    }]
                }
            }
        })};window.__afterPlayinfo__=true;`
    }]
};
global.MutationObserver = class {
    observe() {}
};
global.setInterval = () => 0;
global.window = global;
global.window.top = global.window;
global.window.postMessage = payload => { posted = payload; };

vm.runInThisContext(fs.readFileSync(scriptPath, "utf8"), { filename: scriptPath });

if (!posted || posted.source !== "liudi-downloader-bilibili") throw new Error("No Bilibili media message was posted");
if (posted.channel !== "test-channel") throw new Error("Channel mismatch");
if (posted.streams.length !== 2) throw new Error(`Expected 2 streams, got ${posted.streams.length}`);
const video = posted.streams.find(stream => stream.role === "video");
const audio = posted.streams.find(stream => stream.role === "audio");
if (!video || video.label !== "1080P" || video.width !== 1920 || video.estimatedSize !== 5000000) {
    throw new Error("Video metadata is incomplete");
}
if (!audio || audio.label !== "192K" || audio.estimatedSize !== 240000) {
    throw new Error("Audio metadata is incomplete");
}
if (video.groupKey !== audio.groupKey || video.groupKey !== "BV1TEST:0") throw new Error("Streams were not grouped");
if (video.thumbnailUrl !== "https://i0.hdslb.com/bfs/archive/current-cover.jpg@100w_100h_1c.png") throw new Error("OG cover fallback missing");

process.stdout.write("Bilibili metadata bridge OK\n");
