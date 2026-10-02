"use strict";

/*
 * 候选归组、选择与任务模型的跨站点回归。
 *
 * 迁移说明（对照 07 §7 `AD-1`~`AD-9` 与 §2）：
 *   - `validateSelection` 的选项从 `{ paired }` 改为 `{ desktopAvailable }`，
 *     失败码从 `not_paired` 改为 `desktop_unavailable`（AD-1/AD-2）；
 *   - `deliveryCapabilities` 读 `{ connection, desktopAvailable, eagleAvailable }`；
 *   - `replaceScrollableContent()` 已删除（它正是 P-102 禁止的整体重建），
 *     改用虚拟列表的行复用；只保留滚动位置相关的纯计算工具；
 *   - 站点专用解析（Instagram / 抖音 / Vimeo）已按 §2 移出扩展包，
 *     相关断言改写为**站点无关的等价断言**，覆盖强度不降低。
 */

const assert = require("assert");
const path = require("path");
const { performance } = require("perf_hooks");
const logic = require(path.resolve(__dirname, "..", "..", "extension", "js", "eagle-bridge-ui-logic.js"));

function candidate(overrides = {}) {
    return {
        requestId: overrides.requestId || `request-${Math.random()}`,
        tabId: 9,
        url: overrides.url || "https://media.example/video.m4s",
        webUrl: "https://www.bilibili.com/video/BV1test",
        title: "城市之光 - 哔哩哔哩",
        ext: "m4s",
        type: "video/mp4",
        role: "video",
        label: "1080P",
        codec: "avc1.640028",
        width: 1920,
        height: 1080,
        duration: 120,
        groupKey: "BV1test:123",
        estimatedSize: 1000,
        ...overrides
    };
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "v720", label: "720P", height: 720, estimatedSize: 600 }),
        candidate({ requestId: "v1080", label: "1080P", height: 1080, estimatedSize: 900 }),
        candidate({ requestId: "a64", role: "audio", type: "audio/mp4", label: "64K", codec: "mp4a.40.2", bitrate: 64000, estimatedSize: 100 }),
        candidate({ requestId: "a192", role: "audio", type: "audio/mp4", label: "192K", codec: "mp4a.40.2", bitrate: 192000, estimatedSize: 200 }),
        candidate({ requestId: "szh", role: "subtitle", type: "text/vtt", ext: "vtt", language: "zh-CN", label: "简体中文", url: "https://media.example/zh-CN.vtt" })
    ]);
    assert.strictEqual(groups.length, 1, "explicit Bilibili streams must form one content group");
    const selection = logic.createDefaultSelection(groups[0]);
    assert.strictEqual(selection.videoId, "9:v1080", "highest video quality should be recommended");
    assert.strictEqual(selection.audioId, "9:a192", "highest audio bitrate should be recommended");
    selection.subtitleIds = ["9:szh"];
    const selected = logic.selectedCandidates(groups[0], selection);
    assert.deepStrictEqual(selected.map(item => item.kind), ["video", "audio", "subtitle"], "checked subtitles must enter the download plan");
    assert.strictEqual(logic.groupSummary(groups[0], selection), "1080P + 192K");
    const validation = logic.validateSelection(groups[0], selection, { desktopAvailable: true, importToEagle: true });
    assert.strictEqual(validation.ok, true);
    assert.strictEqual(validation.outputContainer, "mp4");
    assert.strictEqual(validation.route, "desktop", "every selected media type must use the desktop downloader");
}

{
    const sharedUrl = "https://rr1---sn.example.googlevideo.com/videoplayback";
    const group = logic.groupCandidates([
        candidate({ requestId: "yt-271", webUrl: "https://www.youtube.com/watch?v=test", title: "YouTube catalog", groupKey: "youtube:test", streamId: "271", role: "video", label: "1440p", codec: "vp9", width: 2560, height: 1440, url: `${sharedUrl}?itag=271` }),
        candidate({ requestId: "yt-137", webUrl: "https://www.youtube.com/watch?v=test", title: "YouTube catalog", groupKey: "youtube:test", streamId: "137", role: "video", label: "1080p", codec: "avc1.640028", width: 1920, height: 1080, url: `${sharedUrl}?itag=137` }),
        candidate({ requestId: "yt-251", webUrl: "https://www.youtube.com/watch?v=test", title: "YouTube catalog", groupKey: "youtube:test", streamId: "251", role: "audio", type: "audio/webm", ext: "weba", label: "160K", codec: "opus", bitrate: 160000, url: `${sharedUrl}?itag=251` })
    ])[0];
    assert.strictEqual(group.videos.length, 2, "different YouTube itags on one CDN path must remain separate quality choices");
    assert.strictEqual(group.audios.length, 1);
    assert.strictEqual(group.videos[0].label, "1440p");
    assert.strictEqual(logic.outputContainer(group, logic.createDefaultSelection(group)), "mkv", "VP9 plus Opus must use a compatible MKV container");
}

{
    const candidates = [{ id: "first" }, { id: "second" }];
    const calls = [];
    const resolved = logic.resolveRawItems(candidates, function (candidate) {
        calls.push([...arguments]);
        return { id: candidate.id };
    });
    assert.deepStrictEqual(resolved, [{ id: "first" }, { id: "second" }]);
    assert.strictEqual(calls.every(call => call.length === 1), true, "raw item resolver must not receive Array.map index/array arguments");
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "video-one", groupKey: "BV1:1" }),
        candidate({ requestId: "audio-two", groupKey: "BV2:2", role: "audio", type: "audio/mp4" })
    ]);
    assert.strictEqual(groups.length, 2, "different explicit content IDs must never be merged");
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "plain-video", groupKey: "", role: undefined, duration: 0, type: "video/mp4" }),
        candidate({ requestId: "plain-audio", groupKey: "", role: undefined, duration: 0, type: "audio/mp4", url: "https://media.example/audio.m4a" })
    ]);
    assert.strictEqual(groups.length, 2, "unrelated generic direct resources must stay isolated");
}

{
    const rangeToken = Buffer.from("range=9207009-13484176").toString("base64url");
    const rangeFragment = candidate({
        requestId: "fixed-range-mp4",
        groupKey: "frame-0:embedded-player",
        role: undefined,
        duration: 0,
        ext: "mp4",
        type: "video/mp4",
        url: `https://media.example/v2/range/prot/${rangeToken}/avf/video-id.mp4`,
        _size: 4_277_168
    });
    const rangeOnlyGroup = logic.groupCandidates([rangeFragment])[0];
    assert.strictEqual(rangeOnlyGroup.segmentOnly, true, "a fixed byte-range MP4 URL must not be presented as a complete video");
    assert.strictEqual(
        logic.validateSelection(rangeOnlyGroup, logic.createDefaultSelection(rangeOnlyGroup), { desktopAvailable: true }).code,
        "segment_only",
        "a fixed byte-range MP4 URL must be blocked before plan creation"
    );

    const manifestGroup = logic.groupCandidates([
        rangeFragment,
        candidate({
            requestId: "complete-manifest",
            groupKey: "frame-0:embedded-player",
            role: undefined,
            duration: 40,
            ext: "m3u8",
            type: "application/vnd.apple.mpegurl",
            url: "https://media.example/v2/playlist/av/primary/playlist.m3u8"
        })
    ])[0];
    assert.strictEqual(manifestGroup.items.length, 1, "a complete manifest must hide fixed byte-range MP4 fragments from version choices");
    assert.strictEqual(manifestGroup.manifests.length, 1);
}

{
    const youtubeRange = candidate({
        requestId: "youtube-range-probe",
        webUrl: "https://www.youtube.com/watch?v=pIzs1qe-aBc",
        title: "Octane for Houdini - All About Instancing - YouTube",
        groupKey: "",
        role: undefined,
        duration: 0,
        ext: "mp4",
        type: "audio/mp4",
        url: "https://rr1---sn.example.googlevideo.com/videoplayback?itag=140&range=0-7134&clen=50000000",
        _size: 50_000_000
    });
    const group = logic.groupCandidates([youtubeRange])[0];
    assert.strictEqual(group.segmentOnly, true, "an explicit URL byte range stays a transport fragment even when Content-Range reports the full file size");

    const tinyProbe = candidate({
        requestId: "youtube-tiny-audio-probe",
        webUrl: "https://www.youtube.com/watch?v=pIzs1qe-aBc",
        title: "YouTube",
        groupKey: "",
        role: undefined,
        duration: 0,
        ext: "m4a",
        type: "audio/mp4",
        url: "https://www.youtube.com/player-audio-probe",
        _size: 7 * 1024
    });
    assert.strictEqual(logic.groupCandidates([tinyProbe])[0].segmentOnly, true, "tiny ungrouped media probes must stay out of the default content list");
}

{
    const instagramFragment = candidate({
        requestId: "instagram-byte-fragment",
        webUrl: "https://www.instagram.com/",
        title: "Instagram",
        groupKey: "",
        role: undefined,
        duration: 0,
        ext: "mp4",
        type: "video/mp4",
        url: "https://scontent-dfw5-2.cdninstagram.com/o1/v/t2/f2/m367/video.mp4?token=signed&bytestart=886&byteend=173864",
        _size: 172_979
    });
    const group = logic.groupCandidates([instagramFragment])[0];
    assert.strictEqual(group.segmentOnly, true, "bytestart/byteend requests are transport fragments, not complete Instagram videos");
    assert.strictEqual(
        logic.validateSelection(group, logic.createDefaultSelection(group), { desktopAvailable: true }).code,
        "segment_only",
        "raw Instagram byte fragments must never reach the desktop downloader"
    );
}

{
    const pageResolver = candidate({
        requestId: "generic-page-resolver",
        url: "https://example.com/video/2078821500099125405",
        webUrl: "https://example.com/",
        title: "内容页视频",
        groupKey: "page:content:2078821500099125405",
        resolver: "page",
        role: "video",
        label: "最佳可用",
        duration: 10.4,
        estimatedSize: 0
    });
    const group = logic.groupCandidates([pageResolver])[0];
    const selection = logic.createDefaultSelection(group);
    assert.strictEqual(selection.mode, "resolver");
    assert.strictEqual(selection.quality, "", "generic page resolution does not invent quality levels");
    const validation = logic.validateSelection(group, selection, { desktopAvailable: true });
    assert.strictEqual(validation.ok, true, "a stable content permalink must be eligible for desktop page resolution");
    assert.strictEqual(validation.resolver, "page");
    assert.match(logic.groupSummary(group, selection), /本机解析/);
}

{
    // 旧版这里断言站点专用的"旧抖音信息流解析候选必须消失"。改写为站点无关的
    // 等价断言：**不是内容页**的 page 解析候选一律不得进入候选组。
    const staleFeedResolver = candidate({
        requestId: "stale-feed-resolver",
        url: "https://example.com/jingxuan",
        webUrl: "https://example.com/jingxuan?modal_id=7662692425235828009",
        title: "示例站点精选页",
        groupKey: "page:stale-player",
        resolver: "page",
        role: "video",
        duration: 49.041
    });
    assert.deepStrictEqual(
        logic.groupCandidates([staleFeedResolver]),
        [],
        "a feed page must not survive as a retryable page resolver after an extension reload"
    );
    // 带明确数字内容 ID 的内容页仍然可用（同一判据的正向对照）。
    const contentPageResolver = candidate({
        requestId: "content-page-resolver",
        url: "https://example.com/video/7662692425235828009",
        webUrl: "https://example.com/",
        title: "内容页视频",
        groupKey: "page:content-player",
        resolver: "page",
        role: "video",
        duration: 49.041
    });
    assert.strictEqual(
        logic.groupCandidates([contentPageResolver]).length,
        1,
        "a numeric content permalink must remain eligible for desktop page resolution"
    );
}

{
    const unboundNetworkCandidate = candidate({
        requestId: "unbound-network-media",
        url: "https://cdn.example/raw/preload.mp4?token=signed",
        webUrl: "https://example.com/feed",
        title: "标签页标题",
        groupKey: "",
        role: undefined,
        ext: "mp4",
        type: "video/mp4",
        estimatedSize: 18_100_000,
        duration: 223.234,
        unboundPageMedia: true
    });
    const groups = logic.groupCandidates([unboundNetworkCandidate]);
    assert.strictEqual(groups.length, 1);
    assert.strictEqual(groups[0].segmentOnly, true, "an unbound network request must stay in the technical-fragment partition");
    assert.strictEqual(logic.partitionGroups(groups).visible.length, 0, "an unbound request must not appear as a fake video named after the tab");
}

{
    const distinctItems = logic.groupCandidates([
        candidate({
            requestId: "current-7662692425235828009",
            url: "https://example.com/video/7662692425235828009",
            webUrl: "https://example.com/feed?modal_id=7662692425235828009",
            title: "@热话动漫 · 这一集的瑞克，终于不像个疯子，像个外公",
            groupKey: "explicit:7662692425235828009",
            resolver: "page",
            role: "video",
            duration: 223.234
        }),
        candidate({
            requestId: "feed-7653805516250025262",
            url: "https://example.com/video/7653805516250025262",
            webUrl: "https://example.com/feed?modal_id=7662692425235828009",
            title: "@木板解说 · 第7集：深度拆解瑞克和莫蒂 S6E4",
            groupKey: "explicit:7653805516250025262",
            resolver: "page",
            role: "video",
            duration: 532.9
        })
    ]);
    assert.strictEqual(distinctItems.length, 2, "different explicit content IDs must remain separate content groups");
    assert.deepStrictEqual(distinctItems.map(group => group.title).sort(), [
        "@热话动漫 · 这一集的瑞克，终于不像个疯子，像个外公",
        "@木板解说 · 第7集：深度拆解瑞克和莫蒂 S6E4"
    ].sort());
}

{
    const reconstructedFallback = candidate({
        requestId: "reconstructed-track",
        webUrl: "https://example.com/",
        title: "重建后的完整视频 · 10 秒",
        groupKey: "explicit:18421119973183903",
        reconstructedRange: true,
        role: "video",
        duration: 10,
        ext: "mp4",
        url: "https://cdn.example/o1/complete.mp4?token=signed"
    });
    const pageResolver = candidate({
        requestId: "content-page-resolver",
        url: "https://example.com/p/18421119973183903",
        webUrl: "https://example.com/",
        title: "真实帖子视频",
        groupKey: "page:content:18421119973183903",
        resolver: "page",
        role: "video",
        duration: 10,
        estimatedSize: 0
    });
    const groups = logic.groupCandidates([reconstructedFallback, pageResolver]);
    const partitioned = logic.partitionGroups(groups);
    assert.deepStrictEqual(
        partitioned.visible.map(group => group.title).sort(),
        ["重建后的完整视频 · 10 秒", "真实帖子视频"].sort(),
        "a page resolver must not hide another complete media candidate without a shared content identity"
    );
    assert.strictEqual(partitioned.hiddenSegmentCount, 0, "a reconstructed complete media URL is not a transport fragment");
    assert.strictEqual(logic.partitionGroups(groups, { showSegments: true }).visible.length, 2);
}

{
    const pageResolvers = [
        ["2078821500099125405", "我单方面宣布，Joy-Con 才是 Voice Coding 的最佳神器，没有之一。", 47.647],
        ["2078673094940745759", "28 个真实可跑项目！AI Agent 实战圣经开源", 25.966],
        ["2078466405369102834", "看完汗毛直立，提问和回答的水平都是超一流的", 3485.936]
    ].map(([id, title, duration], index) => candidate({
        requestId: `page-resolver-${id}`,
        url: `https://x.com/creator/status/${id}`,
        webUrl: "https://x.com/home",
        title,
        groupKey: `page:player-${id}`,
        streamId: `page:player-${id}`,
        resolver: "page",
        role: "video",
        duration,
        thumbnailUrl: `https://images.example/${id}.jpg`,
        getTime: 1_000 + index
    }));
    const unboundPlayback = Array.from({ length: 12 }, (_, index) => candidate({
        requestId: `unbound-hls-${index}`,
        url: `https://video-cdn-${index % 3}.example/playback/${index}/master.m3u8?token=${index}`,
        webUrl: "https://x.com/home",
        title: "主页 / X",
        groupKey: "",
        role: undefined,
        duration: 0,
        ext: "m3u8",
        type: "application/vnd.apple.mpegurl",
        getTime: 2_000 + index
    }));
    const groups = logic.groupCandidates([...pageResolvers, ...unboundPlayback]);
    assert.strictEqual(groups.length, 15, "the raw capture still keeps auditable network resources before presentation partitioning");

    const defaultView = logic.partitionGroups(groups);
    assert.strictEqual(defaultView.visible.length, 15, "a multi-video page must keep complete manifests visible beside page resolvers");
    assert.deepStrictEqual(defaultView.visible.slice(0, 3).map(group => group.title), pageResolvers.map(item => item.title));
    assert.strictEqual(defaultView.hiddenSegmentCount, 0, "a complete manifest must not be hidden by an unrelated page resolver");

    const diagnosticView = logic.partitionGroups(groups, { showSegments: true });
    const technical = diagnosticView.visible.filter(group => group.technicalOnly);
    assert.strictEqual(technical.length, 0, "complete media must remain selectable instead of being downgraded to diagnostics");
    assert.strictEqual(
        logic.validateSelection(defaultView.visible[3], logic.createDefaultSelection(defaultView.visible[3]), { desktopAvailable: true }).route,
        "desktop",
        "a user-selected complete manifest must remain downloadable"
    );

    const manifestWithoutBoundContent = logic.groupCandidates([unboundPlayback[0]]);
    assert.strictEqual(
        logic.partitionGroups(manifestWithoutBoundContent).visible.length,
        1,
        "a site without a content-bound alternative must keep its only complete manifest usable"
    );
}

{
    const sharedPath = "/video/tos/cn/tos-cn-ve-15/oc1ACobO9BZIaAie2AsEWtLQ0LAdgzfN9yaBiP/";
    const groups = logic.groupCandidates([
        candidate({
            requestId: "douyin-player",
            webUrl: "https://www.douyin.com/jingxuan",
            title: "抖音精选电脑版",
            groupKey: "frame-0:player-current",
            role: undefined,
            duration: 228.345,
            ext: "mp4",
            type: "video/mp4",
            url: `https://v95-web-sz.douyinvod.com${sharedPath}?signature=one`,
            _size: 54_525_952,
            thumbnailUrl: "https://p3-sign.douyinpic.com/current-frame.webp"
        }),
        candidate({
            requestId: "douyin-cdn-alias",
            webUrl: "https://www.douyin.com/jingxuan",
            title: "douyin.com/jingxuan",
            groupKey: "",
            role: undefined,
            duration: 0,
            ext: "mp4",
            type: "video/mp4",
            url: `https://v3-web.douyinvod.com${sharedPath}?signature=two`,
            _size: 54_525_952,
            thumbnailUrl: ""
        })
    ]);
    assert.strictEqual(groups.length, 1, "one media file exposed through CDN aliases must form one content group");
    assert.strictEqual(groups[0].items.length, 1, "CDN aliases must collapse to one useful download version");
    assert.strictEqual(groups[0].title, "抖音精选电脑版", "the richer player title must win over a URL fallback");
    assert.strictEqual(groups[0].thumbnailUrl, "https://p3-sign.douyinpic.com/current-frame.webp", "the real player preview must be shared by the merged candidate");
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "same-size-a", groupKey: "", role: undefined, duration: 0, url: "https://cdn-a.example/video/first-content.mp4", _size: 54_525_952 }),
        candidate({ requestId: "same-size-b", groupKey: "", role: undefined, duration: 0, url: "https://cdn-b.example/video/second-content.mp4", _size: 54_525_952 })
    ]);
    assert.strictEqual(groups.length, 2, "file size alone must never merge different media");
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "digest-player", groupKey: "frame-0:player-digest", role: undefined, duration: 228, url: "https://cdn-a.example/opaque/first", _size: 54_525_952, contentIdentity: "md5:6af621f29f1d8a9f" }),
        candidate({ requestId: "digest-alias", groupKey: "", role: undefined, duration: 0, url: "https://cdn-b.example/completely/different/path", _size: 54_525_952, contentIdentity: "md5:6af621f29f1d8a9f" })
    ]);
    assert.strictEqual(groups.length, 1, "matching strong response identities must merge even when CDN paths differ");
    assert.strictEqual(groups[0].items.length, 1, "strong response aliases must expose one download version");
}

{
    const firstUrl = "https://media.example/first-video.mp4?signature=one";
    const secondUrl = "https://media.example/second-video.mp4?signature=two";
    const group = logic.groupCandidates([
        candidate({ requestId: "preview-first", groupKey: "same-player", role: undefined, duration: 0, ext: "mp4", type: "video/mp4", url: firstUrl, thumbnailUrl: "" }),
        candidate({ requestId: "preview-second", groupKey: "same-player", role: undefined, duration: 0, ext: "mp4", type: "video/mp4", url: secondUrl, thumbnailUrl: "" })
    ])[0];
    const selection = logic.createDefaultSelection(group);
    selection.directId = group.items.find(item => item.url === secondUrl).id;
    assert.strictEqual(
        logic.previewMediaUrl(group, selection),
        secondUrl,
        "a missing thumbnail must preview the exact direct URL selected for download"
    );
    selection.directId = group.items.find(item => item.url === firstUrl).id;
    assert.strictEqual(logic.previewMediaUrl(group, selection), firstUrl, "changing the selected version must change the preview source");

    const audio = logic.groupCandidates([candidate({ requestId: "preview-audio", role: "audio", type: "audio/mp4", ext: "m4a", url: "https://media.example/audio.m4a", thumbnailUrl: "" })])[0];
    assert.strictEqual(logic.previewMediaUrl(audio, logic.createDefaultSelection(audio)), "", "audio must not be rendered as a video preview");
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "behance-1", webUrl: "https://www.behance.net/gallery/239656477/Song-of-the-Stars-TXT", title: "Song of the Stars - TXT :: Behance", groupKey: "frame-8:vimeo-player", role: undefined, duration: 91, ext: "mp4", type: "video/mp4", url: "https://vod.example/720/video.mp4?token=one", _size: 4_400_000 }),
        candidate({ requestId: "behance-2", webUrl: "https://www.behance.net/gallery/239656477/Song-of-the-Stars-TXT", title: "Song of the Stars - TXT :: Behance", groupKey: "frame-8:vimeo-player", role: undefined, duration: 91, ext: "mp4", type: "video/mp4", url: "https://vod.example/720/video.mp4?token=two", _size: 4_500_000 }),
        candidate({ requestId: "behance-3", webUrl: "https://www.behance.net/gallery/239656477/Song-of-the-Stars-TXT", title: "Song of the Stars - TXT :: Behance", groupKey: "frame-8:vimeo-player", role: undefined, duration: 91, ext: "mp4", type: "video/mp4", url: "https://vod.example/1080/video.mp4?token=three", _size: 8_500_000 })
    ]);
    assert.strictEqual(groups.length, 1, "requests from one Behance/Vimeo player must stay in one content group");
    assert.strictEqual(groups[0].items.length, 2, "rotating signatures for the same stream must not keep adding versions");
}

{
    const fragments = Array.from({ length: 21 }, (_, index) => candidate({
        requestId: `vimeo-fragment-${index}`,
        webUrl: "https://www.behance.net/gallery/239656477/Song-of-the-Stars-TXT",
        title: "Song of the Stars - TXT :: Behance",
        groupKey: "frame-8:vimeo:1143783367",
        role: undefined,
        label: "",
        width: 0,
        height: 0,
        playerWidth: 1920,
        playerHeight: 1080,
        duration: 103,
        ext: "mp4",
        type: "video/mp4",
        url: `https://vod.example/segment/${index}.mp4`,
        _size: index % 2 ? 74_000 : 2_000_000 + index * 120_000
    }));
    const manifest = candidate({
        requestId: "vimeo-master",
        webUrl: "https://www.behance.net/gallery/239656477/Song-of-the-Stars-TXT",
        title: "Song of the Stars - TXT :: Behance",
        groupKey: "frame-8:vimeo:1143783367",
        role: undefined,
        label: "最高 1080p",
        width: 0,
        height: 0,
        playerWidth: 1920,
        playerHeight: 1080,
        duration: 103,
        ext: "m3u8",
        type: "application/vnd.apple.mpegurl",
        url: "https://vod.example/master/playlist.m3u8",
        availableQualities: ["360p", "1080p", "720p"]
    });
    const group = logic.groupCandidates([...fragments, manifest])[0];
    assert.strictEqual(group.items.length, 1, "a complete manifest must replace the player's captured MP4 fragments");
    assert.strictEqual(group.manifests.length, 1);
    assert.deepStrictEqual(group.manifests[0].availableQualities, ["1080p", "720p", "360p"]);
    assert.strictEqual(group.playbackQuality, "1080p", "player dimensions should be shown as current playback quality, not a stream guess");
    assert.strictEqual(logic.manifestLabel(group.manifests[0]), "HLS · 最高 1080p · 自动选择最佳");
    const selection = logic.createDefaultSelection(group);
    assert.strictEqual(selection.mode, "manifest");
    assert.strictEqual(selection.quality, "1080p", "the highest advertised quality should be selected by default");
}

{
    const advertised = ["144p", "240p", "360p", "480p", "720p", "1080p", "1440p", "2160p"];
    const group = logic.groupCandidates([candidate({
        requestId: "eight-quality-master",
        groupKey: "player-eight-quality",
        role: undefined,
        ext: "m3u8",
        type: "application/vnd.apple.mpegurl",
        url: "https://vod.example/eight/master.m3u8",
        availableQualities: advertised
    })])[0];
    assert.deepStrictEqual(group.availableQualities, [...advertised].reverse(), "every quality advertised by this video must remain selectable");
    assert.deepStrictEqual(logic.qualityCatalogInfo(group.availableQualities), {
        count: 8,
        highest: "2160p",
        lowest: "144p"
    });
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "manifest", groupKey: "player-one", role: undefined, duration: 91, ext: "mpd", type: "application/dash+xml", url: "https://vod.example/master.mpd" }),
        ...Array.from({ length: 12 }, (_, index) => candidate({ requestId: `segment-${index}`, groupKey: "player-one", role: undefined, duration: 91, ext: "m4s", type: "video/mp4", url: `https://vod.example/segment-${index}.m4s` }))
    ]);
    assert.strictEqual(groups.length, 1);
    assert.strictEqual(groups[0].items.length, 1, "transport fragments must not appear as twelve downloadable videos");
    assert.strictEqual(logic.createDefaultSelection(groups[0]).mode, "manifest");
}

{
    const group = logic.groupCandidates([
        ...Array.from({ length: 6 }, (_, index) => candidate({ requestId: `orphan-${index}`, groupKey: "player-two", role: undefined, duration: 91, ext: "m4s", type: "video/mp4", url: `https://vod.example/orphan-${index}.m4s` }))
    ])[0];
    const validation = logic.validateSelection(group, logic.createDefaultSelection(group), { desktopAvailable: true, importToEagle: true });
    assert.strictEqual(validation.code, "segment_only", "a partial fragment must never be presented as a complete download");
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "downloadable-old", groupKey: "player-old", role: undefined, duration: 30, ext: "mp4", type: "video/mp4", url: "https://vod.example/old.mp4", getTime: 1000 }),
        candidate({ requestId: "fragment-new", groupKey: "player-fragment", role: undefined, duration: 30, ext: "m4s", type: "video/mp4", url: "https://vod.example/new.m4s", getTime: 3000 }),
        candidate({ requestId: "downloadable-latest", groupKey: "player-latest", role: undefined, duration: 30, ext: "mp4", type: "video/mp4", url: "https://vod.example/latest.mp4", getTime: 5000 })
    ]);
    assert.strictEqual(groups.at(-1).newest, 5000, "the newest captured content must be the last row");
    const defaultView = logic.partitionGroups(groups, { showSegments: false });
    assert.deepStrictEqual(defaultView.visible.map(group => group.newest), [1000, 5000]);
    assert.strictEqual(defaultView.hiddenSegmentCount, 1, "transport-only groups must be hidden by default");
    assert.strictEqual(logic.defaultActiveGroupId(defaultView.visible, ""), defaultView.visible.at(-1).id, "opening the popup must select the newest visible row");
    const diagnosticView = logic.partitionGroups(groups, { showSegments: true });
    assert.strictEqual(diagnosticView.visible.length, 3, "the filter option must reveal transport-only rows for diagnostics");
}

{
    const groups = logic.groupCandidates([
        candidate({ requestId: "manifest", groupKey: "", role: undefined, duration: 0, ext: "m3u8", type: "application/vnd.apple.mpegurl", url: "https://media.example/master.m3u8" }),
        candidate({ requestId: "direct", groupKey: "", role: undefined, duration: 0, ext: "mp4", type: "video/mp4", url: "https://media.example/full.mp4" })
    ]);
    assert.strictEqual(groups.length, 2, "manifest and direct media must not share a fallback group");
    const manifest = groups.find(group => group.manifests.length);
    const selection = logic.createDefaultSelection(manifest);
    const validation = logic.validateSelection(manifest, selection, { desktopAvailable: true, importToEagle: true });
    assert.strictEqual(validation.route, "desktop");
}

{
    const group = logic.groupCandidates([candidate({ requestId: "drm", drm: true })])[0];
    const validation = logic.validateSelection(group, logic.createDefaultSelection(group), { desktopAvailable: true, importToEagle: true });
    assert.strictEqual(validation.code, "blocked_drm");
}

{
    // AD-1 / AD-2：没有配对码也没有令牌，只有"桌面端是否可达"。
    const group = logic.groupCandidates([candidate({ requestId: "desktop-offline" })])[0];
    const selection = logic.createDefaultSelection(group);
    assert.strictEqual(logic.validateSelection(group, selection, { desktopAvailable: false, importToEagle: true }).code, "desktop_unavailable");
    assert.strictEqual(logic.validateSelection(group, selection, { desktopAvailable: false, importToEagle: false }).code, "desktop_unavailable");
    assert.strictEqual(logic.validateSelection(group, selection, { desktopAvailable: true, importToEagle: true }).ok, true,
        "a reachable desktop must be enough to start a download — no pairing step exists");
}

async function testValidatedTaskStart() {
    let createCalls = 0;
    const rejected = await logic.startValidatedTask(
        { ok: false, message: "请先连接本机软件" },
        async () => {
            createCalls += 1;
            return { id: "must-not-exist" };
        }
    );
    assert.deepStrictEqual(rejected, {
        started: false,
        plan: null,
        error: "请先连接本机软件"
    });
    assert.strictEqual(createCalls, 0, "an unreachable-desktop action must never create a download plan");

    const accepted = await logic.startValidatedTask(
        { ok: true },
        async () => {
            createCalls += 1;
            return { id: "created-plan" };
        }
    );
    assert.strictEqual(accepted.started, true);
    assert.strictEqual(accepted.plan.id, "created-plan");
    assert.strictEqual(createCalls, 1);
}

{
    const qualities = ["1440p", "1080p", "720p", "480p", "360p", "240p", "144p"];
    const group = logic.groupCandidates([candidate({
        requestId: "page-resolver-catalog",
        url: "https://example.com/video/2078821500099125405",
        webUrl: "https://example.com/video/2078821500099125405",
        title: "Octane for Houdini - All About Instancing",
        groupKey: "page:resolver-123456789012345",
        streamId: "resolver-123456789012345",
        resolver: "page",
        ext: "mp4",
        type: "video/mp4",
        role: "video",
        label: "1440p",
        availableQualities: qualities,
        qualitySource: "player_catalog",
        width: 0,
        height: 1440,
        estimatedSize: 0
    })])[0];
    assert.strictEqual(group.resolvers.length, 1, "a player catalog must become one desktop-resolved choice");
    assert.deepStrictEqual(group.availableQualities, qualities);
    const selection = logic.createDefaultSelection(group);
    assert.strictEqual(selection.mode, "resolver");
    assert.strictEqual(selection.quality, "1440p", "highest advertised quality should be selected by default");
    const selected = logic.selectedCandidates(group, selection);
    assert.strictEqual(selected.length, 1);
    assert.strictEqual(selected[0].resolver, "page");
    assert.strictEqual(logic.outputContainer(group, selection), "mp4");
    const validation = logic.validateSelection(group, selection, { desktopAvailable: true, importToEagle: true });
    assert.strictEqual(validation.ok, true);
    assert.strictEqual(validation.resolver, "page");
    // 站点无关：解析候选的摘要只说"本机解析并合并"，不暴露站点名。
    assert.strictEqual(logic.groupSummary(group, selection), "1440p · 本机解析并合并");
}

{
    assert.strictEqual(logic.hasActiveTasks([{ status: "merging" }]), true);
    assert.strictEqual(logic.hasActiveTasks([{ status: "imported" }, { status: "canceled" }]), false);
    const view = logic.taskView({ id: "p1", output_name: "video.mp4", status: "downloading", progress: 50, phase_detail: "本机软件正在下载" });
    assert.strictEqual(view.progress, 50);
    assert.strictEqual(view.active, true);
    assert.strictEqual(view.detail, "本机软件正在下载");

    const completed = logic.taskView({
        id: "p2",
        output_name: "finished.mp4",
        status: "completed_local",
        progress: 0,
        final_path: "C:\\Users\\Tester\\Downloads\\留底下载器\\已完成\\finished.mp4",
        preview_path: "C:\\Users\\Tester\\Downloads\\留底下载器\\预览\\p2.png"
    });
    assert.strictEqual(completed.progress, 100, "a completed desktop task must never remain at 0% in the popup");
    assert.strictEqual(completed.canOpenOutput, true);
    assert.strictEqual(completed.canImportExisting, true, "a download-only result must be importable without downloading again");
    assert.strictEqual(completed.finalPath.endsWith("finished.mp4"), true);
    assert.strictEqual(completed.hasLocalPreview, true);

    const validating = logic.taskView({ status: "validating", progress: 100 });
    assert.strictEqual(validating.progress, 99, "validation must not look complete before local delivery");
    const named = logic.normalizeOutputName("bad<>name.mp4");
    assert.strictEqual(named.ok, true);
    assert.strictEqual(named.value, "bad__name.mp4");
    assert.strictEqual(logic.normalizeOutputName("...").ok, false);
}

{
    // `replaceScrollableContent()` 已删除（07 §6.2 `P-102` 禁止对列表容器赋
    // `innerHTML`）。这里保留等价覆盖：滚动位置相关的纯计算工具仍然正确，
    // 且整个逻辑模块**不再导出**任何整体重建入口。
    assert.strictEqual(
        typeof logic.replaceScrollableContent,
        "undefined",
        "the whole-container rebuild helper must be gone — P-102 forbids it"
    );

    const rerenderedTasks = { scrollTop: 0, scrollHeight: 1600, clientHeight: 500 };
    logic.restoreScrollPosition(rerenderedTasks, 840);
    assert.strictEqual(rerenderedTasks.scrollTop, 840, "task polling must preserve the user's position in a long task list");
    logic.restoreScrollPosition(rerenderedTasks, 2000);
    assert.strictEqual(rerenderedTasks.scrollTop, 1100, "task scroll restoration must clamp to the refreshed list height");

    const shortList = { scrollTop: 900, scrollHeight: 600, clientHeight: 400 };
    logic.restoreScrollPosition(shortList, 900);
    assert.strictEqual(shortList.scrollTop, 200, "a shorter refreshed list must clamp the restored position to its new scroll range");

    assert.strictEqual(logic.isNearScrollEnd({ scrollTop: 600, scrollHeight: 1000, clientHeight: 400 }), true);
    assert.strictEqual(
        logic.isNearScrollEnd({ scrollTop: 400, scrollHeight: 1000, clientHeight: 400 }),
        false,
        "new candidates must not drag a user who manually scrolled upward back to the bottom"
    );
}

{
    // AD-1 / AD-2 / AD-4：能力来自"可达性 + Eagle 能力"，没有配对概念。
    const offline = logic.deliveryCapabilities({ connection: "connected", desktopAvailable: true, eagleAvailable: false });
    assert.strictEqual(offline.canDownload, true, "Eagle-less users must still be able to download");
    assert.strictEqual(offline.canImport, false, "Eagle import must be disabled while Eagle is unavailable");
    assert.strictEqual(offline.preferLocal, true, "download-only must become the primary action without Eagle");

    const online = logic.deliveryCapabilities({ connection: "connected", desktopAvailable: true, eagleAvailable: true });
    assert.strictEqual(online.canDownload, true);
    assert.strictEqual(online.canImport, true);
    assert.strictEqual(online.preferLocal, false);

    const legacyDesktop = logic.deliveryCapabilities({ connection: "connected" });
    assert.strictEqual(legacyDesktop.canDownload, true);
    assert.strictEqual(legacyDesktop.canImport, true, "desktop versions that predate eagleAvailable must keep their original import behavior");

    const desktopOffline = logic.deliveryCapabilities({
        desktopAvailable: false,
        connection: "offline",
        eagleAvailable: true
    });
    assert.strictEqual(desktopOffline.canDownload, false, "an unreachable desktop must not enable downloads");
    assert.strictEqual(desktopOffline.canImport, false, "desktop disconnect must disable imports even if the last Eagle probe succeeded");
    assert.strictEqual(desktopOffline.preferLocal, false, "desktop disconnect is not the same condition as Eagle being unavailable");

    const checking = logic.deliveryCapabilities({ connection: "checking", eagleAvailable: false });
    assert.strictEqual(checking.canDownload, false, "no action may be offered before reachability is known");
    assert.strictEqual(checking.canImport, false);
    assert.strictEqual(checking.preferLocal, false);
}

{
    const gate = logic.createLatestRequestGate();
    const first = gate.begin();
    const second = gate.begin();
    assert.strictEqual(gate.isCurrent(first), false, "an older asynchronous response must not overwrite newer connection/task state");
    assert.strictEqual(gate.isCurrent(second), true);
    gate.invalidate();
    assert.strictEqual(gate.isCurrent(second), false, "popup teardown must invalidate in-flight work");
}

{
    const gate = logic.createKeyedActionGate();
    assert.strictEqual(gate.begin("plan-1"), true);
    assert.strictEqual(gate.begin("plan-1"), false, "rapid double clicks must not submit one task action twice");
    assert.strictEqual(gate.begin("plan-2"), true, "independent task rows may still operate concurrently");
    assert.strictEqual(gate.isBusy("plan-1"), true);
    gate.end("plan-1");
    assert.strictEqual(gate.begin("plan-1"), true, "the task action must become available after completion");
    gate.end("plan-1");
    gate.end("plan-2");
    assert.strictEqual(gate.any(), false);
}

async function testOutOfOrderLatestResponse() {
    const gate = logic.createLatestRequestGate();
    let resolveOld;
    let resolveNew;
    const oldResponse = new Promise(resolve => { resolveOld = resolve; });
    const newResponse = new Promise(resolve => { resolveNew = resolve; });
    let committed = "";
    const issue = promise => {
        const ticket = gate.begin();
        return promise.then(value => {
            if (gate.isCurrent(ticket)) committed = value;
        });
    };
    const oldWork = issue(oldResponse);
    const newWork = issue(newResponse);
    resolveNew("new page");
    await newWork;
    resolveOld("old page");
    await oldWork;
    assert.strictEqual(committed, "new page", "a delayed response from the previous page must not overwrite the latest page state");
}

async function testBoundedAsyncMapping() {
    let active = 0;
    let peak = 0;
    const completed = await logic.mapWithConcurrency(
        Array.from({ length: 17 }, (_, index) => index),
        4,
        async value => {
            active += 1;
            peak = Math.max(peak, active);
            await new Promise(resolve => setTimeout(resolve, 2));
            active -= 1;
            return value * 2;
        }
    );
    assert.ok(peak <= 4, `preview loading exceeded its concurrency budget: ${peak}`);
    assert.deepStrictEqual(completed, Array.from({ length: 17 }, (_, index) => index * 2));
}

{
    let urlReads = 0;
    const raw = Array.from({ length: 20 }, (_, index) => ({
        requestId: `normalization-${index}`,
        tabId: 9,
        get url() {
            urlReads += 1;
            return `https://media.example/${index}.mp4`;
        },
        webUrl: "https://example.com/watch",
        title: `Video ${index}`,
        ext: "mp4",
        type: "video/mp4",
        role: "video",
        groupKey: `normalization-${index}`
    }));
    const filtered = logic.filterCandidates(raw, { mediaType: "video" });
    const readsAfterFiltering = urlReads;
    logic.groupCandidates(filtered);
    assert.strictEqual(
        urlReads,
        readsAfterFiltering,
        "grouping filtered candidates must reuse their validated normalization instead of reparsing every URL"
    );
}

{
    const raw = Array.from({ length: 1000 }, (_, index) => ({
        requestId: `stress-${index}`,
        tabId: 10,
        url: `https://media.example/${index}.mp4`,
        webUrl: "https://example.com/watch",
        title: `Stress video ${index}`,
        ext: "mp4",
        type: "video/mp4",
        role: "video",
        height: 720,
        duration: 60,
        groupKey: `stress-${index}`
    }));
    const started = performance.now();
    const filtered = logic.filterCandidates(raw, { mediaType: "video", query: "stress" });
    const groups = logic.partitionGroups(logic.groupCandidates(filtered), { showSegments: false }).visible;
    const elapsed = performance.now() - started;
    assert.strictEqual(groups.length, 1000);
    assert.ok(elapsed < 500, `1000-candidate pipeline took ${elapsed.toFixed(1)} ms`);
}

{
    const items = [
        candidate({ requestId: "filter-video", ext: "mp4", type: "video/mp4", role: undefined, groupKey: "", duration: 0, _size: 20 * 1024 * 1024 }),
        candidate({ requestId: "filter-audio", ext: "m4a", type: "audio/mp4", role: undefined, groupKey: "", duration: 0, url: "https://media.example/audio.m4a", _size: 3 * 1024 * 1024 }),
        candidate({ requestId: "filter-duplicate", ext: "mp4", type: "video/mp4", role: undefined, groupKey: "", duration: 0, name: "same.mp4", url: "https://media.example/same-1.mp4" }),
        candidate({ requestId: "filter-duplicate-2", ext: "mp4", type: "video/mp4", role: undefined, groupKey: "", duration: 0, name: "same.mp4", url: "https://media.example/same-2.mp4" })
    ];
    assert.strictEqual(logic.filterCandidates(items, { mediaType: "video" }).length, 3);
    assert.strictEqual(logic.filterCandidates(items, { extension: "m4a" }).length, 1);
    assert.strictEqual(logic.filterCandidates(items, { minimumSize: 10 * 1024 * 1024 }).length, 1);
    assert.strictEqual(logic.filterCandidates(items, { regex: "audio\\.m4a$" }).length, 1);
    assert.strictEqual(logic.filterCandidates(items, { dedupe: true }).length, 3);
    assert.strictEqual(logic.isSafeFilterRegex("(a+)+$"), false, "nested quantifiers must be rejected");
    assert.strictEqual(logic.isSafeFilterRegex("video|audio"), true);
}

Promise.all([testValidatedTaskStart(), testOutOfOrderLatestResponse(), testBoundedAsyncMapping()]).then(
    () => console.log("Popup grouping and task logic OK"),
    error => {
        console.error(error);
        process.exitCode = 1;
    }
);
