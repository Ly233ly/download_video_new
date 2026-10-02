"use strict";

/*
 * 站点专用注入脚本的回归（阶段 3 的目标）。
 *
 * 07 §2：`catch-script/` **已取消**——站点专用脚本的归处是
 * `adapters/<site>/extension.js`，而适配器体系属阶段 3（13 §6）。
 * 因此本阶段 `extension/catch-script/youtube.js` **不存在**：
 * 扩展包里已没有任何站点专用注入脚本。
 *
 * 本文件保留**全部原断言**，只在目标缺失时输出 SKIP 并以退出码 0 结束——
 * 既不虚报通过，也不删掉断言。阶段 3 把该脚本迁到
 * `adapters/youtube/extension.js` 后，只需把下面的 `scriptPath` 指过去，
 * 本文件即可恢复为真实门禁（07 §8：站点断言迁到 `test_adapter_<id>.js`，
 * 原断言数量全部保留、不得降低覆盖）。
 */

const fs = require("fs");
const path = require("path");
const vm = require("vm");

const scriptPath = path.resolve(__dirname, "..", "..", "extension", "catch-script", "youtube.js");
if (!fs.existsSync(scriptPath)) {
    process.stdout.write(
        "SKIP test_youtube: extension/catch-script/youtube.js 不存在"
        + "（07 §2 已取消 catch-script/，站点脚本属阶段 3 的 adapters/<site>/extension.js）；"
        + "本文件的全部断言原样保留，待阶段 3 指向适配器后恢复为真实门禁\n"
    );
    process.exit(0);
}
const script = fs.readFileSync(scriptPath, "utf8");

function runFixture(playerResponse) {
    let posted = null;
    const context = {
        URL,
        URLSearchParams,
        console,
        location: {
            href: "https://www.youtube.com/watch?v=pIzs1qe-aBc",
            origin: "https://www.youtube.com",
            hostname: "www.youtube.com"
        },
        document: {
            currentScript: { src: "chrome-extension://test/catch-script/youtube.js?channel=test-channel" },
            documentElement: {},
            title: "Octane for Houdini - All About Instancing - YouTube",
            addEventListener() {}
        },
        MutationObserver: class { observe() {} disconnect() {} },
        setInterval() { return 0; },
        setTimeout() { return 0; }
    };
    context.window = context;
    context.top = context;
    context.window.ytInitialPlayerResponse = playerResponse;
    context.window.addEventListener = () => {};
    context.window.postMessage = payload => { posted = payload; };
    vm.runInNewContext(script, context, { filename: scriptPath });
    return posted;
}

const details = {
    videoId: "pIzs1qe-aBc",
    title: "Octane for Houdini - All About Instancing",
    lengthSeconds: "3207",
    thumbnail: {
        thumbnails: [
            { url: "https://i.ytimg.com/vi/pIzs1qe-aBc/hqdefault.jpg", width: 480, height: 360 },
            { url: "https://i.ytimg.com/vi/pIzs1qe-aBc/maxresdefault.jpg", width: 1280, height: 720 }
        ]
    }
};

const directPosted = runFixture({
    videoDetails: details,
    streamingData: {
        adaptiveFormats: [
            {
                itag: 271,
                url: "https://rr1---sn.example.googlevideo.com/videoplayback?itag=271&mime=video%2Fwebm&clen=900000000",
                mimeType: 'video/webm; codecs="vp9"',
                width: 2560,
                height: 1440,
                fps: 30,
                bitrate: 2245000,
                contentLength: "900000000",
                qualityLabel: "1440p"
            },
            {
                itag: 137,
                url: "https://rr1---sn.example.googlevideo.com/videoplayback?itag=137&mime=video%2Fmp4&clen=500000000",
                mimeType: 'video/mp4; codecs="avc1.640028"',
                width: 1920,
                height: 1080,
                fps: 30,
                bitrate: 1200000,
                contentLength: "500000000",
                qualityLabel: "1080p"
            },
            {
                itag: 251,
                url: "https://rr1---sn.example.googlevideo.com/videoplayback?itag=251&mime=audio%2Fwebm&clen=50000000",
                mimeType: 'audio/webm; codecs="opus"',
                bitrate: 160000,
                contentLength: "50000000",
                audioQuality: "AUDIO_QUALITY_HIGH"
            },
            {
                itag: 999,
                signatureCipher: "url=https%3A%2F%2Fexample.invalid%2Fvideoplayback&s=encrypted",
                mimeType: 'video/mp4; codecs="avc1"',
                width: 640,
                height: 360,
                qualityLabel: "360p"
            }
        ]
    }
});

if (!directPosted || directPosted.source !== "liudi-downloader-youtube") throw new Error("No YouTube format catalog was posted");
if (directPosted.channel !== "test-channel") throw new Error("Channel mismatch");
if (directPosted.streams.length !== 3) throw new Error(`Expected 3 direct streams, got ${directPosted.streams.length}`);
const videos = directPosted.streams.filter(stream => stream.role === "video");
const audio = directPosted.streams.find(stream => stream.role === "audio");
if (videos.map(stream => stream.label).join(",") !== "1440p,1080p") throw new Error("Video quality catalog is incomplete");
if (videos[0].extension !== "webm" || videos[1].extension !== "mp4") throw new Error("Video containers are incorrect");
if (!audio || audio.extension !== "weba" || audio.codec !== "opus") throw new Error("Audio metadata is incomplete");
if (!directPosted.streams.every(stream => stream.groupKey === "youtube:pIzs1qe-aBc")) throw new Error("YouTube tracks were not grouped");
if (!directPosted.streams.every(stream => stream.title === details.title)) throw new Error("YouTube title is missing");
if (!directPosted.streams.every(stream => stream.duration === 3207)) throw new Error("YouTube duration is missing");
if (directPosted.streams[0].thumbnailUrl !== "https://i.ytimg.com/vi/pIzs1qe-aBc/maxresdefault.jpg") throw new Error("Largest thumbnail was not selected");
if (directPosted.streams.some(stream => String(stream.url).includes("example.invalid"))) throw new Error("Undeciphered signature URL leaked into the catalog");

const sabrQualities = [1440, 1080, 720, 480, 360, 240, 144];
const sabrPosted = runFixture({
    videoDetails: details,
    streamingData: {
        serverAbrStreamingUrl: "https://rr1---sn.example.googlevideo.com/videoplayback?sabr=1",
        adaptiveFormats: sabrQualities.map((height, index) => ({
            itag: 400 - index,
            mimeType: 'video/mp4; codecs="av01.0.08M.08"',
            width: Math.round(height * 16 / 9),
            height,
            bitrate: 200000 + height * 1000,
            contentLength: String(1000000 + height * 1000),
            qualityLabel: `${height}p`
        })),
        formats: [{
            itag: 18,
            url: "https://rr1---sn.example.googlevideo.com/videoplayback?itag=18",
            mimeType: 'video/mp4; codecs="avc1.42001E, mp4a.40.2"',
            width: 640,
            height: 360,
            bitrate: 650000,
            contentLength: "90400000",
            qualityLabel: "360p"
        }]
    }
});

if (!sabrPosted || sabrPosted.streams.length !== 1) throw new Error("SABR catalog must become one desktop-resolved choice");
const resolver = sabrPosted.streams[0];
if (resolver.resolver !== "youtube") throw new Error("SABR catalog is missing the YouTube resolver marker");
if (resolver.url !== "https://www.youtube.com/watch?v=pIzs1qe-aBc") throw new Error("Resolver must use the canonical watch page");
if (resolver.availableQualities.join(",") !== "1440p,1080p,720p,480p,360p,240p,144p") {
    throw new Error(`SABR qualities are incomplete: ${resolver.availableQualities.join(",")}`);
}
if (resolver.qualitySource !== "youtube_player_catalog") throw new Error("SABR catalog source is incorrect");
if (resolver.extension !== "mp4" || resolver.role !== "video") throw new Error("Resolver output metadata is invalid");

process.stdout.write("YouTube structured format bridge OK\n");
