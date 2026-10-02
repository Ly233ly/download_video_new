"use strict";

// 抖音适配器（L1）离线测试 —— [14 §8] 的 A-112/A-113 归位目标。
//
// 抖音是 **L1 声明式**适配器（[14 §10]）：它需要的每一项都在 adapter.json 里声明，
// 因此这里测的不是"某个站点的代码"，而是**通用声明式引擎 + 抖音声明**跑出来的结果。
// 引擎在 extension/js/eagle-bridge-candidate-logic.js（纯函数，不碰 document/chrome）。
//
// 断言来源（A-113：归位不得降低覆盖）：
//   - 旧 tests/js/test_candidate_presentation.js 的抖音部分（modal 规范化、类名取 ID、标题、选主播放器）
//   - 旧 tests/js/test_popup_logic.js 的抖音部分（标题优先于地址、同内容不同 CDN 归组由通用测试承担）
//   - 旧 tests/test_extension.py 的抖音断言（公共脚本里不得出现站点判定）

const assert = require("assert");
const fs = require("fs");
const path = require("path");

const repoRoot = path.resolve(__dirname, "..", "..");
const logic = require(path.join(repoRoot, "extension", "js", "eagle-bridge-candidate-logic.js"));

const adapterDir = path.join(repoRoot, "adapters", "douyin");
const sourcePath = path.join(adapterDir, "adapter.json");
const generatedPath = path.join(repoRoot, "extension", "site-adapters.json");

const source = JSON.parse(fs.readFileSync(sourcePath, "utf8"));
const generated = JSON.parse(fs.readFileSync(generatedPath, "utf8"));
const declared = generated.adapters.find((item) => item.id === "douyin");
const generic = generated.adapters.find((item) => item.id === "generic");

const ID = "7662692425235828009";
const OTHER_ID = "7653805516250025262";

/** 造一个"可用的播放器"：已就绪、有时长、在视口里。 */
function player(overrides = {}) {
    return {
        index: 0,
        rect: { left: 0, top: 0, width: 720, height: 1280 },
        viewport: { width: 1280, height: 800 },
        readyState: 4,
        duration: 60,
        currentTime: 0,
        paused: true,
        ended: false,
        ...overrides
    };
}

{
    // —— 声明层：抖音必须是 L1，且生成物与唯一事实源一致（A-101/A-104/A-105）——
    assert.strictEqual(source.id, path.basename(adapterDir), "id 必须等于目录名（A-114）");
    assert.strictEqual(source.codeReason, undefined, "L1 适配器不得有 codeReason（[14 §10]）");
    assert.strictEqual(fs.existsSync(path.join(adapterDir, "extension.js")), false,
        "L1 适配器不得有 extension.js（A-101：能用 L1 表达就禁止写 L2）");
    assert.strictEqual(generated.generated, true, "生成物必须带 generated 标记（A-105）");
    assert.strictEqual(generated.source, "adapters/", "生成物必须标明来源（A-105）");
    assert.ok(declared, "生成物里必须有 douyin");
    for (const field of ["id", "name", "version", "updatedFor", "match", "identity", "capture", "title"]) {
        assert.deepStrictEqual(declared[field], source[field], `生成物的 ${field} 应与 adapter.json 一致`);
    }
    assert.strictEqual(declared.resolve, undefined, "扩展侧不需要 resolve（[14 §4] 的字段分配）");
    assert.strictEqual(declared.errors, undefined, "扩展侧不需要 errors（[14 §4] 的字段分配）");
    console.log("A 声明层 OK");
}

{
    // —— 匹配范围：douyin.com 与全部子域，其它域名落回 generic（[14 §5.3]）——
    const hosts = ["douyin.com", "www.douyin.com", "WWW.DOUYIN.COM", "www.douyin.com:443", "v.douyin.com."];
    for (const host of hosts) {
        assert.strictEqual(logic.matchAdapter(generated.adapters, host)?.id, "douyin", `${host} 应命中抖音`);
    }
    for (const host of ["notdouyin.com", "douyin.com.evil.test", "example.com"]) {
        assert.strictEqual(logic.matchAdapter(generated.adapters, host)?.id, "generic", `${host} 应落回 generic`);
    }
    assert.strictEqual(logic.matchAdapter(generated.adapters, ""), null, "空主机名不匹配任何适配器");
    assert.strictEqual(logic.normalizeAdapterHost("www.douyin.com:8443"), "www.douyin.com", "端口要剥掉");
    console.log("B 匹配 OK");
}

{
    // —— 身份：路径规则、modal_id 查询参数、类名文本规则、DOM 信号值 ——
    const cases = [
        [`https://www.douyin.com/video/${ID}`, [], ID],
        [`https://www.douyin.com/video/${ID}/`, [], ID],
        [`https://www.douyin.com/video/${ID}?from_page=feed`, [], ID],
        [`https://www.douyin.com/jingxuan?modal_id=${ID}&from_page=feed`, [], ID],
        ["https://www.douyin.com/jingxuan?modal_id=not-a-number", [], ""],
        [`https://www.douyin.com/video/123`, [], ""],
        ["https://www.douyin.com/", [`video_${ID}`], ID],
        [`https://www.douyin.com/`, [`  video_${ID}  `], ID],
        ["https://www.douyin.com/", [`video_${ID}_extra`], ""],
        [`https://www.douyin.com/`, [ID], ID],
        ["https://www.douyin.com/", ["", "   "], ""],
        [`https://www.douyin.com/`, [`https://www.douyin.com/video/${OTHER_ID}`], OTHER_ID],
        // 信号优先于地址栏：信息流的地址会随滚动变化，正在播的那个才是目标（[14 §5.4]）
        [`https://www.douyin.com/video/${ID}`, [`video_${OTHER_ID}`], OTHER_ID],
        [`https://www.douyin.com/jingxuan?modal_id=${ID}`, [`video_${OTHER_ID}`], OTHER_ID]
    ];
    for (const [pageUrl, signals, want] of cases) {
        assert.strictEqual(logic.videoIdFromAdapterSignals(declared, { pageUrl, signals }), want,
            `${pageUrl} + ${JSON.stringify(signals)} 应得到 ${want || "（空）"}`);
    }
    assert.strictEqual(logic.adapterRequiresId(declared), true, "抖音取不到 ID 就必须丢弃候选");
    assert.strictEqual(logic.adapterRequiresId(generic), false, "通用路径没有这条约束");
    console.log("C 身份 OK");
}

{
    // —— 页面地址规范化：有 ID 用 canonical，没 ID 原样退回 ——
    assert.strictEqual(logic.canonicalPageUrlFromAdapter(declared, ID, "https://www.douyin.com/jingxuan?modal_id=" + ID),
        `https://www.douyin.com/video/${ID}`);
    assert.strictEqual(logic.canonicalPageUrlFromAdapter(declared, "", "https://www.douyin.com/jingxuan"),
        "https://www.douyin.com/jingxuan");
    const empty = { id: "x", name: "x", identity: {}, match: {}, capture: {}, title: {} };
    assert.strictEqual(logic.canonicalPageUrlFromAdapter(empty, ID, "https://example.com/a"), "https://example.com/a");
    console.log("D 规范化 OK");
}

{
    // —— 标题：模板拼接、空缺占位符不留分隔符、兜底、长度上限 ——
    const cases = [
        [{ nickname: "@热话动漫", description: "这一集的瑞克，终于不像个疯子，像个外公 #动漫解说" },
            "@热话动漫 - 这一集的瑞克，终于不像个疯子，像个外公 #动漫解说"],
        [{ nickname: "@木板解说", description: "第7集：深度拆解瑞克和莫蒂 S6E4《夜晚家庭》" },
            "@木板解说 - 第7集：深度拆解瑞克和莫蒂 S6E4《夜晚家庭》"],
        [{ nickname: "@某人", description: "" }, "@某人"],
        [{ nickname: "", description: "只有描述" }, "只有描述"],
        [{ nickname: "  多   空白  ", description: "  描述  " }, "多 空白 - 描述"],
        [{ nickname: "@某人", description: "描述展开" }, "@某人 - 描述"],
        [{ nickname: "", description: "", videoId: ID }, `抖音视频 ${ID}`],
        [{ nickname: "", description: "", videoId: "" }, "抖音视频"],
        [{ nickname: "  ", description: "  " }, "抖音视频"]
    ];
    for (const [input, want] of cases) {
        assert.strictEqual(logic.adapterCandidateTitle(declared, input), want,
            `标题 ${JSON.stringify(input)} 应得到 ${want}`);
    }
    const long = logic.adapterCandidateTitle(declared, { nickname: "甲".repeat(300), description: "" });
    assert.strictEqual(long.length, 220, "标题上限 220（旧实现一致）");
    console.log("E 标题 OK");
}

{
    // —— 选主播放器：正在播的胜过预加载的；不可用的不参与；返回候选自己声明的下标 ——
    const playing = player({ index: 7, rect: { left: 0, top: 0, width: 720, height: 1280 }, currentTime: 12, paused: false });
    const preloaded = player({ index: 9, rect: { left: 0, top: 0, width: 720, height: 1280 } });
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, [preloaded, playing]), 7,
        "正在播放的那个是主播放器，即使它排在后面");
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, [playing, preloaded]), 7);
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, [player({ index: 3 }), player({ index: 4 })]), 3,
        "并列时取先遇到的");
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, [player({ index: 1, readyState: 1 })]), -1,
        "未就绪的不算");
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, [player({ index: 1, duration: 0 })]), -1,
        "没有时长的不算");
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, [player({ index: 1, rect: { left: 2000, top: 0, width: 720, height: 1280 } })]), -1,
        "完全在视口外的不算");
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, []), -1, "没有播放器时返回 -1");
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, undefined), -1);
    const visible = player({ index: 2, rect: { left: 0, top: 0, width: 720, height: 1280 } });
    const partial = player({ index: 5, rect: { left: 1200, top: 0, width: 720, height: 1280 } });
    assert.strictEqual(logic.selectPrimaryVideoIndex(declared, [partial, visible]), 2,
        "可见面积大的胜出");
    const bare = { ...declared, capture: { ...declared.capture, primarySelection: "first" } };
    assert.strictEqual(logic.selectPrimaryVideoIndex(bare, [player({ index: 4 }), player({ index: 5 })]), 4);
    const all = { ...declared, capture: { ...declared.capture, primarySelection: "all" } };
    assert.strictEqual(logic.selectPrimaryVideoIndex(all, [player({ index: 4 })]), -1,
        "all 表示全部上报，没有唯一主播放器");
    assert.strictEqual(logic.primaryVideoScore(player({ index: 1, paused: false })) > 0, true);
    console.log("F 选主 OK");
}

{
    // —— 源约束：抖音允许直连流，通用路径仍要求 blob ——
    assert.strictEqual(logic.adapterAllowsCapture(declared, { hasDirectSource: true, hasBlobSource: false }), true,
        "抖音的直连流可以下载（放宽 blob 约束）");
    assert.strictEqual(logic.adapterAllowsCapture(declared, { hasDirectSource: false, hasBlobSource: true }), true);
    assert.strictEqual(logic.adapterAllowsCapture(declared, {}), false, "两者都没有就不该上报");
    assert.strictEqual(logic.adapterAllowsCapture(generic, { hasDirectSource: true, hasBlobSource: false }), false,
        "通用路径必须没有直连源");
    assert.strictEqual(logic.adapterAllowsCapture(generic, { hasBlobSource: true }), true);
    assert.strictEqual(logic.adapterPrimarySelection(declared), "current-player");
    assert.strictEqual(logic.adapterPrimarySelection(generic), "all", "没声明时默认全部上报");
    console.log("G 源约束 OK");
}

{
    // —— A-102/T-ADP-01：公共发现代码里不得出现站点名判断（旧 test_extension.py 的抖音断言）——
    const stripComments = (text) => text
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .replace(/(^|[^:])\/\/[^\n]*/g, "$1");
    for (const file of ["eagle-bridge-candidate-logic.js", "content-script.js"]) {
        const text = stripComments(fs.readFileSync(path.join(repoRoot, "extension", "js", file), "utf8"));
        for (const site of ["douyin", "bilibili", "youtube", "vimeo"]) {
            assert.strictEqual(new RegExp(site, "i").test(text), false,
                `${file} 里不得出现站点名 ${site}（站点判定只能待在适配器目录，A-102）`);
        }
    }
    console.log("H 公共代码整洁 OK");
}

console.log("Douyin adapter (L1 declarative) OK");
