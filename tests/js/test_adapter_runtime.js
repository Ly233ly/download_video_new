"use strict";

// 声明式站点适配器的运行时门禁（14 §5 / §5.3 / §5.4）。
//
// 这里只判定"声明 → 行为"里**与浏览器无关**的那一半：把生成物
// `extension/site-adapters.json` 里的真实声明交给通用引擎和 content-script.js 的
// 纯函数层，检查选容器、取身份、定主播放器、放开直连流、拼标题的结果。
// DOM 采集与消息通道（chrome.runtime）不在本文件的射程内——本项目没有 jsdom、
// 也不新增依赖；那部分靠 `node --check` 与手工回归，见 extension/README.md。

const assert = require("assert");
const fs = require("fs");
const path = require("path");

const repoRoot = path.resolve(__dirname, "..", "..");
const logic = require(path.join(repoRoot, "extension", "js", "eagle-bridge-candidate-logic.js"));
const runtime = require(path.join(repoRoot, "extension", "js", "content-script.js"));

const generated = JSON.parse(fs.readFileSync(path.join(repoRoot, "extension", "site-adapters.json"), "utf8"));
const adapters = generated.adapters;

// 两份真实声明：严格的（要求 ID、有 urlRules/canonical、允许直连流、选当前播放器）
// 与兜底的（不要求 ID、没有 urlRules/canonical、只认 blob 源、全部上报）。
const strictAdapter = adapters.find(adapter => adapter.identity?.requireId === true && Array.isArray(adapter.identity?.urlRules));
const looseAdapter = adapters.find(adapter => adapter !== strictAdapter && adapter.identity?.requireId !== true);
assert.ok(strictAdapter, "生成物里应有一份 requireId 为真的声明");
assert.ok(looseAdapter, "生成物里应有一份 requireId 不为真的声明");

const strictHost = String(strictAdapter.match.hosts[0]).replace("*.", "www.");
const looseHost = "example.invalid";

/** 播放器快照工厂：字段与 content-script.js 交给 planAdapterDiscovery 的形状一致。 */
function player(overrides = {}) {
    return Object.assign({
        index: 0,
        sources: ["blob:https://example.invalid/6f3d"],
        readyState: 4,
        duration: 60,
        currentTime: 0,
        paused: true,
        ended: false,
        rect: { left: 0, top: 0, width: 720, height: 1280 },
        viewport: { width: 1280, height: 800 }
    }, overrides);
}

function planFor(adapter, input) {
    return runtime.planAdapterDiscovery(logic, adapter, Object.assign({
        pageUrl: `https://${strictHost}/`,
        videos: [player()],
        signals: [],
        fallbackTitles: {}
    }, input));
}

{
    // 声明选择器：只从声明里取，顺序就是数组顺序；没有声明（降级）时为空。
    assert.strictEqual(logic.matchAdapter(adapters, strictHost).id, strictAdapter.id);
    assert.strictEqual(logic.matchAdapter(adapters, looseHost).id, looseAdapter.id);
    assert.deepStrictEqual(runtime.adapterCaptureSelectors(strictAdapter), strictAdapter.capture.containers);
    assert.deepStrictEqual(runtime.adapterSignalSelectors(strictAdapter), strictAdapter.identity.domSignals);
    assert.deepStrictEqual(runtime.adapterCaptureSelectors(null), []);
    assert.deepStrictEqual(runtime.adapterSignalSelectors(undefined), []);
    console.log("A 段 OK：容器 / 身份信号选择器只来自声明");
}

{
    const declared = runtime.adapterCaptureSelectors(strictAdapter);
    assert.ok(declared.length >= 3, "严格声明应给出多个候选容器");
    const first = { name: "first" };
    const third = { name: "third" };

    // 声明里第 1 个命中就用第 1 个，哪怕更靠后的也命中。
    assert.strictEqual(runtime.pickAdapterContainer(strictAdapter, [
        { selector: declared[2], container: third },
        { selector: declared[0], container: first }
    ]).container, first);

    // 靠前的都没命中时，取声明顺序里最靠前的命中项。
    assert.strictEqual(runtime.pickAdapterContainer(strictAdapter, [
        { selector: declared[0], container: null },
        { selector: declared[1], container: null },
        { selector: declared[2], container: third }
    ]).selector, declared[2]);

    // 一个都没命中、或压根没有声明 → null，调用方退回通用容器策略。
    assert.strictEqual(runtime.pickAdapterContainer(strictAdapter, []), null);
    assert.strictEqual(runtime.pickAdapterContainer(strictAdapter, [{ selector: declared[0], container: null }]), null);
    assert.strictEqual(runtime.pickAdapterContainer(null, [{ selector: declared[0], container: first }]), null);

    // 没被声明的选择器即使命中也不算数。
    assert.strictEqual(runtime.pickAdapterContainer(strictAdapter, [{ selector: "video", container: first }]), null);
    console.log("B 段 OK：容器按声明顺序取第一个命中项");
}

{
    const signals = [{ index: 0, values: ["ignored"] }, { index: 1, values: ["a", "b"] }];
    assert.deepStrictEqual(runtime.adapterSignalValues(signals, 1), ["a", "b"]);
    assert.deepStrictEqual(runtime.adapterSignalValues(signals, 9), []);
    assert.deepStrictEqual(runtime.adapterSignalValues(undefined, 0), []);
    assert.deepStrictEqual(runtime.adapterSignalValues([{ index: 0 }], 0), []);
    console.log("C 段 OK：身份信号按播放器下标取用");
}

{
    // 页面上取不到 ID（地址不是内容页，也没有 DOM 信号）→ 丢弃，不上报。
    assert.strictEqual(strictAdapter.identity.requireId, true);
    assert.deepStrictEqual(planFor(strictAdapter, {
        pageUrl: `https://${strictHost}/discover`,
        signals: [{ index: 0, values: [] }]
    }), []);

    // 地址里带 ID → 上报规范化后的内容页地址（不是当前地址）。
    const source = `https://${strictHost}/?modal_id=7300000000000000000`;
    const fromUrl = planFor(strictAdapter, { pageUrl: source });
    assert.strictEqual(fromUrl.length, 1);
    assert.ok(fromUrl[0].videoId);
    assert.strictEqual(fromUrl[0].pageUrl, strictAdapter.identity.canonical.replace("{id}", fromUrl[0].videoId));
    assert.notStrictEqual(fromUrl[0].pageUrl, source);

    // 地址不是内容页，但 DOM 信号里有 ID → 同样上报规范化地址（14 §5.4：先 DOM 信号）。
    const fromSignal = planFor(strictAdapter, { signals: [{ index: 0, values: ["ignored", `video_7300000000000000001`] }] });
    assert.strictEqual(fromSignal.length, 1);
    assert.strictEqual(fromSignal[0].videoId, "7300000000000000001");
    assert.strictEqual(fromSignal[0].pageUrl, strictAdapter.identity.canonical.replace("{id}", "7300000000000000001"));
    console.log("D 段 OK：严格声明下无 ID 丢弃候选、有 ID 规范化页面地址");
}

{
    // 兜底声明不要求 ID：取不到也照报，地址保持原样（没有 canonical 规则）。
    const loose = planFor(looseAdapter, { pageUrl: `https://${looseHost}/watch` });
    assert.strictEqual(loose.length, 1);
    assert.strictEqual(loose[0].videoId, "");
    assert.strictEqual(loose[0].pageUrl, `https://${looseHost}/watch`);

    // 兜底声明只认 blob 源：直连流不给候选。
    const directOnly = planFor(looseAdapter, {
        pageUrl: `https://${looseHost}/watch`,
        videos: [player({ sources: [`https://cdn.${looseHost}/v.mp4`] })]
    });
    assert.deepStrictEqual(directOnly, []);
    console.log("E 段 OK：兜底声明不丢弃无 ID 候选、不认直连流");
}

{
    // 严格声明允许直连流：只有 http(s) 源也算候选。
    assert.strictEqual(strictAdapter.capture.allowDirectStream, true);
    assert.strictEqual(strictAdapter.capture.requireBlobSource, false);
    const direct = planFor(strictAdapter, {
        videos: [player({ sources: ["https://cdn.example.invalid/v.mp4"] })],
        signals: [{ index: 0, values: ["video_7300000000000000002"] }]
    });
    assert.strictEqual(direct.length, 1);
    assert.strictEqual(direct[0].hasDirectSource, true);
    assert.strictEqual(direct[0].hasBlobSource, false);

    // 没有声明（通道不可用时的降级路径）等价于"只认 blob 源"。
    const degraded = planFor(null, {
        videos: [player({ sources: ["https://cdn.example.invalid/v.mp4"] })],
        signals: [{ index: 0, values: ["video_7300000000000000002"] }]
    });
    assert.deepStrictEqual(degraded, []);
    console.log("F 段 OK：直连流候选只由声明放开");
}

{
    // current-player：两个播放器里只报正在播的那个。
    assert.strictEqual(strictAdapter.capture.primarySelection, "current-player");
    const pausedSmall = player({ index: 0, paused: true, rect: { left: 0, top: 0, width: 100, height: 100 } });
    const playing = player({ index: 1, paused: false, currentTime: 12, rect: { left: 0, top: 0, width: 360, height: 640 } });
    const signals = [
        { index: 0, values: ["video_7000000000000000000"] },
        { index: 1, values: ["video_7000000000000000001"] }
    ];
    const primary = planFor(strictAdapter, { videos: [pausedSmall, playing], signals });
    assert.strictEqual(primary.length, 1);
    assert.strictEqual(primary[0].index, 1);
    assert.strictEqual(primary[0].videoId, "7000000000000000001");

    // all：每个播放器各自成为独立候选（维持既有行为）。
    assert.strictEqual(looseAdapter.capture.primarySelection, "all");
    const all = planFor(looseAdapter, {
        pageUrl: `https://${looseHost}/watch`,
        videos: [player({ index: 0 }), player({ index: 1 })]
    });
    assert.strictEqual(all.length, 2);
    assert.deepStrictEqual(all.map(item => item.index), [0, 1]);
    console.log("G 段 OK：current-player 只报正在播的那个，all 全部上报");
}

{
    // 有 title 声明时标题由模板渲染；没有 title 声明时保持"从页面取标题"的行为。
    const declared = planFor(strictAdapter, {
        signals: [{ index: 0, values: ["video_7300000000000000003"] }],
        fallbackTitles: { 0: "页面启发式标题" }
    });
    assert.strictEqual(declared.length, 1);
    assert.strictEqual(declared[0].title, logic.adapterCandidateTitle(strictAdapter, {
        description: "页面启发式标题",
        videoId: declared[0].videoId
    }));
    assert.ok(declared[0].title.length > 0);

    const fallbackOnly = planFor(looseAdapter, {
        pageUrl: `https://${looseHost}/watch`,
        fallbackTitles: { 0: "页面启发式标题" }
    });
    assert.strictEqual(fallbackOnly[0].title, "页面启发式标题");

    // 模板渲染为空时退回声明里的兜底文案（由数据渲染，不在代码里写死站点名）。
    const emptyTitle = logic.adapterCandidateTitle(strictAdapter, { videoId: "" });
    assert.strictEqual(emptyTitle, String(strictAdapter.title.fallback).replace("{id}", "").trim());
    console.log("H 段 OK：标题走声明模板，没有声明时用页面标题");
}

{
    // 声明是站点差异的唯一来源：公共代码里不允许出现任何适配器 id。
    const stripComments = text => text.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/[^\n]*/g, "$1");
    const ids = adapters.map(adapter => String(adapter.id || "")).filter(Boolean);
    assert.ok(ids.length >= 2, "生成物里应有多份声明");
    for (const relative of ["extension/js/content-script.js", "extension/js/background.js"]) {
        const code = stripComments(fs.readFileSync(path.join(repoRoot, relative), "utf8"));
        for (const id of ids) {
            assert.ok(!code.includes(id), `${relative} 里不应出现站点名「${id}」`);
        }
    }
    console.log("I 段 OK：公共代码里没有任何站点名");
}

{
    // 降级路径：声明不可用时 matchAdapter 给 null，通用行为照跑（14 §5.3）。
    const input = {
        pageUrl: `https://${strictHost}/?modal_id=7300000000000000004`,
        videos: [player()],
        signals: [],
        fallbackTitles: {}
    };
    assert.strictEqual(logic.matchAdapter([], strictHost), null);
    assert.strictEqual(logic.matchAdapter(null, strictHost), null);
    const degraded = planFor(null, input);
    assert.strictEqual(degraded.length, 1);
    assert.strictEqual(degraded[0].videoId, "");
    assert.strictEqual(degraded[0].pageUrl, input.pageUrl);
    assert.strictEqual(degraded[0].title, "");

    // 引擎缺少必需能力时宁可什么都不报，也不半途抛错。
    assert.deepStrictEqual(runtime.planAdapterDiscovery(null, strictAdapter, input), []);
    assert.deepStrictEqual(runtime.planAdapterDiscovery({}, strictAdapter, input), []);
    console.log("J 段 OK：声明缺失 / 引擎不完整时按通用行为降级");
}

console.log("Site adapter runtime (declaration-driven discovery) OK");
