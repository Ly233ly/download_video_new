/*
 * 列表渲染性能门禁（`T-EXT-15` ~ `T-EXT-19`、`PF-A7`）。
 *
 * 07 §8：**必须有一条针对 P-101/P-102 的自动化检查**——对候选列表触发状态
 * 更新后，断言列表容器未被整体重建。"这条是防止卡顿回归的唯一可执行手段。"
 *
 * 本文件用**手写的最小 DOM 桩**在真实 DOM 语义下跑 `extension/js/virtual-list.js`
 * （仓库里没有 jsdom，也不为此引入依赖）。桩覆盖：`createElement` / `appendChild`
 * / `remove` / `insertBefore` / `querySelector` / `querySelectorAll` / `classList` /
 * `dataset` / `setAttribute` / `removeAttribute` / `style` / 文本节点 / 祖先链。
 *
 * **负向对照**：文件末尾有一段"如果渲染方式改成整体重建，检测器能不能抓到"
 * 的自检——检测器本身必须被证明有效，否则这条门禁就是假通过。
 */

"use strict";

const assert = require("assert");
const fs = require("fs");
const path = require("path");

const root = path.resolve(__dirname, "..", "..");
const extensionDir = path.join(root, "extension");
const list = require(path.join(extensionDir, "js", "virtual-list.js"));

// ---------------------------------------------------------------- 最小 DOM 桩

/** 记录"哪些元素被赋过 innerHTML"。出现一次 `P-102` 违反即失败。 */
const innerHtmlWrites = [];

class ClassList {
    constructor(node) { this.node = node; this.set = new Set(); }
    add(...names) { names.forEach(name => this.set.add(String(name))); }
    remove(...names) { names.forEach(name => this.set.delete(String(name))); }
    contains(name) { return this.set.has(String(name)); }
    toggle(name, force) {
        const on = force === undefined ? !this.set.has(String(name)) : Boolean(force);
        if (on) this.set.add(String(name)); else this.set.delete(String(name));
        return on;
    }
    toString() { return [...this.set].join(" "); }
}

class Element {
    constructor(tag) {
        this.tagName = String(tag).toUpperCase();
        this.childNodes = [];
        this.parentNode = null;
        this.ownerDocument = globalThis.document;
        this.attributes = new Map();
        this.dataset = {};
        this.style = {};
        this.classList = new ClassList(this);
        this.listeners = [];
        this._text = "";
        this.hidden = false;
        this.disabled = false;
        this.tabIndex = 0;
        this.value = "";
        this.checked = false;
        this._ownText = "";
    }

    get className() { return this.classList.toString(); }
    set className(value) {
        this.classList.set = new Set(String(value || "").split(/\s+/).filter(Boolean));
    }

    get textContent() {
        return this._ownText + this.childNodes.map(node => node.textContent || "").join("");
    }
    set textContent(value) { this._ownText = String(value ?? ""); this.childNodes = []; }

    // P-102 的检测点：任何元素被赋 `innerHTML` 都会被记账。
    get innerHTML() { return ""; }
    set innerHTML(value) {
        innerHtmlWrites.push({ node: this, value: String(value ?? "") });
        this.childNodes = [];
        throw new Error("P-102 violation: innerHTML assigned on a list element or its ancestor");
    }

    get firstChild() { return this.childNodes[0] || null; }
    get firstElementChild() {
        return this.childNodes.find(node => node instanceof Element) || null;
    }
    get lastElementChild() {
        const elements = this.childNodes.filter(node => node instanceof Element);
        return elements[elements.length - 1] || null;
    }
    get children() { return this.childNodes.filter(node => node instanceof Element); }
    get isConnected() {
        let node = this;
        while (node.parentNode) node = node.parentNode;
        return node === globalThis.__root;
    }

    setAttribute(name, value) {
        const key = String(name);
        this.attributes.set(key, String(value));
        if (key.startsWith("data-")) {
            const camel = key.slice(5).replace(/-([a-z])/g, (_m, c) => c.toUpperCase());
            this.dataset[camel] = String(value);
        } else if (key === "hidden") this.hidden = true;
        else if (key === "disabled") this.disabled = true;
    }
    getAttribute(name) {
        const key = String(name);
        if (key === "src") return this._src ?? null;
        return this.attributes.has(key) ? this.attributes.get(key) : null;
    }
    removeAttribute(name) {
        const key = String(name);
        if (key === "src") { this._src = undefined; return; }
        this.attributes.delete(key);
    }
    get src() { return this._src || ""; }
    set src(value) { this._src = String(value ?? ""); }

    appendChild(node) {
        if (node.parentNode) node.parentNode.removeChild(node);
        node.parentNode = this;
        this.childNodes.push(node);
        return node;
    }
    insertBefore(node, reference) {
        if (node.parentNode) node.parentNode.removeChild(node);
        node.parentNode = this;
        const index = reference ? this.childNodes.indexOf(reference) : -1;
        if (index < 0) this.childNodes.push(node);
        else this.childNodes.splice(index, 0, node);
        return node;
    }
    removeChild(node) {
        const index = this.childNodes.indexOf(node);
        if (index >= 0) this.childNodes.splice(index, 1);
        node.parentNode = null;
        return node;
    }
    remove() { this.parentNode?.removeChild(this); }

    addEventListener(type, handler, opts) { this.listeners.push({ type, handler, opts }); }
    removeEventListener(type, handler) {
        const index = this.listeners.findIndex(entry => entry.type === type && entry.handler === handler);
        if (index >= 0) this.listeners.splice(index, 1);
    }
    dispatch(type, event = {}) {
        for (const entry of [...this.listeners]) {
            if (entry.type !== type) continue;
            entry.handler({ target: this, ...event });
        }
    }

    matches(selector) {
        return matchesSelector(this, selector);
    }
    closest(selector) {
        let node = this;
        while (node) {
            if (node instanceof Element && matchesSelector(node, selector)) return node;
            node = node.parentNode;
        }
        return null;
    }
    querySelector(selector) {
        for (const child of this.childNodes) {
            if (!(child instanceof Element)) continue;
            if (matchesSelector(child, selector)) return child;
            const nested = child.querySelector(selector);
            if (nested) return nested;
        }
        return null;
    }
    querySelectorAll(selector) {
        const found = [];
        const walk = node => {
            for (const child of node.childNodes) {
                if (!(child instanceof Element)) continue;
                if (matchesSelector(child, selector)) found.push(child);
                walk(child);
            }
        };
        walk(this);
        return found;
    }
}

function matchesSelector(node, selector) {
    const text = String(selector).trim();
    if (!text) return false;
    // 组合选择器按空白拆分，逐段从右往左匹配（覆盖本文件用到的全部形态）。
    const parts = text.split(/\s+/);
    if (parts.length > 1) {
        let current = node;
        for (let index = parts.length - 1; index >= 0; index -= 1) {
            while (current && !matchesSimple(current, parts[index])) current = current.parentNode;
            if (!current) return false;
            if (index > 0) current = current.parentNode;
        }
        return true;
    }
    return matchesSimple(node, parts[0]);
}

function matchesSimple(node, simple) {
    for (const branch of String(simple).split(",")) {
        const part = branch.trim();
        if (!part) continue;
        const attrMatches = [...part.matchAll(/\[([^\]]+)\]/g)];
        const withoutAttrs = part.replace(/\[[^\]]+\]/g, "");
        const tag = withoutAttrs.replace(/^[.#].*$/, "").trim();
        if (tag && node.tagName !== tag.toUpperCase()) continue;
        const classes = [...withoutAttrs.matchAll(/\.([a-zA-Z0-9_-]+)/g)].map(match => match[1]);
        if (classes.some(name => !node.classList.contains(name))) continue;
        const id = withoutAttrs.match(/#([a-zA-Z0-9_-]+)/)?.[1];
        if (id && node.attributes.get("id") !== id) continue;
        let ok = true;
        for (const [, expression] of attrMatches.map(match => [match[0], match[1]])) {
            const [rawName, rawValue] = expression.split("=");
            const name = rawName.trim();
            if (rawValue === undefined) {
                if (name === "data-row-image") {
                    if (node.dataset.rowImage === undefined) { ok = false; break; }
                } else if (!node.attributes.has(name) && node[name] === undefined) { ok = false; break; }
                continue;
            }
            const expected = rawValue.trim().replace(/^['"]|['"]$/g, "");
            const actual = name === "data-row-image" ? node.dataset.rowImage
                : name.startsWith("data-")
                    ? node.dataset[name.slice(5).replace(/-([a-z])/g, (_m, c) => c.toUpperCase())]
                    : node.attributes.get(name) ?? node[name];
            if (String(actual) !== expected) { ok = false; break; }
        }
        if (ok) return true;
    }
    return false;
}

const documentStub = {
    createElement(tag) { return new Element(tag); },
    createTextNode(value) { return { textContent: String(value), parentNode: null, remove() { this.parentNode?.removeChild(this); } }; }
};

globalThis.document = documentStub;
// 不提供 `Image`：图片加载器在无解码环境下降级为"未加载"，由调用方回落占位图。
// 这正是 `virtual-list.js` 声明的行为，也让本测试不依赖任何真实网络。

// 帧调度：手动驱动，保证"同帧合并"可断言。
let frameQueue = [];
globalThis.requestAnimationFrame = callback => { frameQueue.push(callback); return frameQueue.length; };
globalThis.cancelAnimationFrame = () => { frameQueue = []; };
function flushFrame() {
    const queued = frameQueue;
    frameQueue = [];
    for (const callback of queued) callback();
    return queued.length;
}

// ---------------------------------------------------------------- 夹具

function buildContainer(viewportHeight) {
    const domRoot = new Element("body");
    globalThis.__root = domRoot;
    const viewport = new Element("div");
    viewport.clientHeight = viewportHeight;
    viewport.offsetHeight = viewportHeight;
    viewport.scrollTop = 0;
    const container = new Element("div");
    container.setAttribute("id", "bridgeGroupList");
    container.setAttribute("role", "listbox");
    viewport.appendChild(container);
    domRoot.appendChild(viewport);
    return { domRoot, viewport, container };
}

function makeGroups(count) {
    return Array.from({ length: count }, (_, index) => ({
        id: `group-${index}`,
        title: `候选 ${index}`,
        thumbnailUrl: `https://cdn.example/thumb-${index}.webp`,
        summary: `1080P · ${index} 个可下载内容`,
        duration: 60 + index
    }));
}

function makeController(container, viewport, patchLog) {
    return list.create({
        listId: "candidates",
        container,
        viewport,
        placeholderUrl: "chrome-extension://test/icons/icon-128.png",
        getItemId: item => item.id,
        getImageUrl: index => index >= 0 ? `https://cdn.example/thumb-${index}.webp` : "",
        createRowContent: row => {
            // 与 `eagle-bridge-ui.js` 的 `buildCandidateRow` 同构：
            // 行**恰好 2 个节点**（行本身 + 一个文本节点）——`PF-A7` 在 20 行
            // 窗口下只摊得出这么多；缩略图由行自身的 `background-image` 承担。
            row.dataset.rowImage = "1";
            const text = documentStub.createElement("span");
            text.classList.add("bridge-row-text");
            row.appendChild(text);
        },
        patchRow: (row, item) => {
            patchLog.push(item?.id || "");
            const text = row.querySelector(".bridge-row-text");
            if (text) text.textContent = `${item.title} · ${item.summary}`;
        }
    });
}

/** 行的缩略图载体是行自身或行内的 `[data-row-image]`（P-107 见 virtual-list.js）。 */
function thumbBackground(row) {
    const holder = row.dataset?.rowImage !== undefined ? row : row.querySelector("[data-row-image]");
    return holder?.style.backgroundImage || "";
}

function hasImageUrl(row) {
    const value = thumbBackground(row);
    const url = value.match(/^url\(["']?(.*?)["']?\)$/)?.[1] || "";
    return list.isImageUrl(url);
}

function domNodeCount(container) {
    let count = 0;
    const walk = node => {
        for (const child of node.childNodes) {
            count += 1;
            if (child instanceof Element) walk(child);
        }
    };
    walk(container);
    return count;
}

// ================================================================ T-EXT-15

{
    innerHtmlWrites.length = 0;
    const { viewport, container } = buildContainer(672);
    const patchLog = [];
    const controller = makeController(container, viewport, patchLog);
    const groups = makeGroups(200);

    controller.setItems(groups);
    flushFrame();
    // P-105 的窗口是 [first - 3, last + 1 + 5]。窗口高度取决于视口高度：
    // 672px 视口（= 12 × 56px）里"完全可见"12 行，且第 13 行也有一部分露在
    // 视口内，所以窗口 = 13 + 3 + 5 = 21 行。这里断言的是**不变量**：
    // 窗口不得超过 可见 + 上下缓冲 + 那一行跨界行，且行数远小于总数 200。
    assert.strictEqual(controller.window.start, 0);
    const windowSize = controller.window.end - controller.window.start;
    const renderedRows = container.querySelectorAll(".bridge-row");
    assert.ok(windowSize >= list.VISIBLE_ROWS && windowSize <= list.VISIBLE_ROWS + list.OVERSCAN_ABOVE_ROWS + list.OVERSCAN_BELOW_ROWS + 1,
        `P-105: the window must respect the fixed visible/buffer budget; got ${windowSize}`);
    assert.ok(windowSize < 30, `P-105: a 200-row list must not render ${windowSize} rows`);
    assert.strictEqual(renderedRows.length, windowSize,
        `P-105: exactly the window may be rendered; window=${windowSize} rows=${renderedRows.length}`);

    // 记录每一行的节点对象引用；随后对**同一集合**触发 20 次状态更新。
    const identityBefore = renderedRows.map(row => row);
    const childCountBefore = container.childNodes.length;
    const patchCallsBefore = patchLog.length;
    for (let round = 0; round < 20; round += 1) {
        groups[0].summary = `1080P · 第 ${round} 轮更新`;
        groups[1].summary = `720P · 第 ${round} 轮更新`;
        assert.strictEqual(controller.setItems(groups), false,
            "P-101: an unchanged ID set must not be reported as a collection change");
        flushFrame();
    }
    const identityAfter = container.querySelectorAll(".bridge-row");

    assert.strictEqual(identityAfter.length, identityBefore.length);
    for (let index = 0; index < identityAfter.length; index += 1) {
        assert.strictEqual(identityAfter[index], identityBefore[index],
            `P-103: row ${index} was rebuilt instead of reused (node identity changed)`);
    }
    assert.strictEqual(container.childNodes.length, childCountBefore,
        "P-101: a status update must not add or remove rows");
    assert.ok(patchLog.length > patchCallsBefore, "patchRow must still run so the row text is updated in place");
    assert.strictEqual(groups[0].summary, "1080P · 第 19 轮更新");
    assert.ok(identityAfter[0].querySelector(".bridge-row-text").textContent.includes("第 19 轮更新"),
        "the visible row must carry the newest text after an in-place update");

    // T-EXT-15 的布尔判据：列表容器及其祖先链上不得出现整体重建。
    assert.deepStrictEqual(innerHtmlWrites, [],
        `P-102: the list container or an ancestor was rebuilt ${innerHtmlWrites.length} time(s)`);

    // 集合真变时仍必须增删行（否则 P-101 会被误读成"永不更新"）。
    assert.strictEqual(controller.setItems(groups.slice(0, 100)), true,
        "a real collection change must be reported as such");
    flushFrame();

    // 集合缩小到 100 条后，窗口仍然只渲染窗口内的行，且节点被复用而非重建。
    const afterShrink = container.querySelectorAll(".bridge-row");
    assert.strictEqual(afterShrink.length, windowSize);
    assert.strictEqual(afterShrink[0], identityBefore[0], "surviving rows must keep their nodes");
}

// ================================================================ T-EXT-17

{
    innerHtmlWrites.length = 0;
    const { viewport, container } = buildContainer(672);
    const controller = makeController(container, viewport, []);
    const groups = makeGroups(200);
    controller.setItems(groups);
    flushFrame();

    // 固定行高 56px × 可见 12 行 = 672px 视口；上下缓冲固定为 3 / 5。
    assert.strictEqual(list.ROW_HEIGHT_PX, 56, "P-105: the row height is a fixed 56px");
    assert.strictEqual(list.VISIBLE_ROWS, 12, "P-105: exactly 12 rows are visible");
    assert.strictEqual(list.OVERSCAN_ABOVE_ROWS, 3, "P-105: exactly 3 buffer rows above");
    assert.strictEqual(list.OVERSCAN_BELOW_ROWS, 5, "P-105: exactly 5 buffer rows below");
    assert.strictEqual(list.VIRTUALIZE_THRESHOLD, 50, "P-105: virtualization starts above 50 rows");

    const check = (scrollTop, expectedFirst, expectedStart, expectedEnd) => {
        viewport.scrollTop = scrollTop;
        controller.render();
        const window = controller.window;
        assert.strictEqual(window.first, expectedFirst, `window.first wrong at scrollTop=${scrollTop}`);
        assert.strictEqual(window.start, expectedStart, `window.start wrong at scrollTop=${scrollTop}`);
        assert.strictEqual(window.end, expectedEnd, `window.end wrong at scrollTop=${scrollTop}`);
        assert.strictEqual(container.querySelectorAll(".bridge-row").length, expectedEnd - expectedStart,
            `rendered row count wrong at scrollTop=${scrollTop}`);
    };
    check(0, 0, 0, 17);              // 顶部：first=0、last=12（第 13 行部分可见）、无上方缓冲
    check(56 * 50, 50, 47, 67);      // 中部：first=50、last=61、上 3 下 5 全部生效
    check(56 * 199, 199, 196, 200);  // 末尾：窗口被总数夹住，不错位

    // PF-A7：200 条候选下 DOM 节点总数 ≤ 60（20 行 + 容器 + 哨兵仍在上界内）。
    viewport.scrollTop = 56 * 100;
    controller.render();
    const nodes = domNodeCount(container);
    assert.ok(nodes <= 60, `PF-A7: DOM node count must stay ≤ 60 for a 200-row list; got ${nodes}`);

    // 50 条及以下不虚拟化，但窗口计算仍然正确。
    const small = list.visibleWindow({ total: 50, rowHeight: 56, viewportHeight: 672, scrollTop: 0 });
    assert.strictEqual(small.virtual, false);
    assert.strictEqual(small.end, 50);
    const large = list.visibleWindow({ total: 51, rowHeight: 56, viewportHeight: 672, scrollTop: 0 });
    assert.strictEqual(large.virtual, true);
    assert.strictEqual(large.end, 17);
}

// ================================================================ T-EXT-16

{
    // P-106：同一帧内的多次更新只渲染一次。
    innerHtmlWrites.length = 0;
    const { viewport, container } = buildContainer(672);
    const patchLog = [];
    const controller = makeController(container, viewport, patchLog);
    controller.setItems(makeGroups(20));
    flushFrame();
    const baseline = patchLog.length;
    for (let round = 0; round < 25; round += 1) controller.update();
    assert.strictEqual(frameQueue.length, 1, "P-106: 25 updates inside one frame must be merged into one render");
    const rendered = flushFrame();
    assert.strictEqual(rendered, 1, "P-106: exactly one frame callback may be scheduled");
    assert.strictEqual(patchLog.length - baseline, 20,
        "P-106: one render patches each row exactly once");

    // P-104：监听只挂在容器上，行内不挂监听。
    assert.ok(container.listeners.length >= 0);
    for (const row of container.querySelectorAll(".bridge-row")) {
        assert.strictEqual(row.listeners.length, 0,
            "P-104: rows must not carry their own listeners; delegate to the container");
    }
    const source = fs.readFileSync(path.join(extensionDir, "js", "virtual-list.js"), "utf8");
    const createRowBody = source.slice(source.indexOf("function createRow("), source.indexOf("function placeRow("));
    assert.ok(!createRowBody.includes("addEventListener"),
        "P-104: the row-creation path must not register listeners");
    assert.ok(!/innerHTML\s*=/.test(source),
        "P-102: the list module must not contain any innerHTML assignment");
}

// ================================================================ P-107

// 缩略图的 `background-image` 由图片加载器在微任务里落地（并发闸门是异步的），
// 所以这一段是 async 的：断言前先让微任务队列排空。
async function testThumbnails() {
    assert.strictEqual(list.isImageUrl("https://cdn.example/a.webp"), true);
    assert.strictEqual(list.isImageUrl("chrome-extension://abc/icons/icon-128.png"), true);
    assert.strictEqual(list.isImageUrl(`data:image/jpeg;base64,${"A".repeat(64)}`), false,
        "P-107: base64 data URLs must never be used as list thumbnails");
    assert.strictEqual(list.isImageUrl(""), false);
    assert.strictEqual(list.IMAGE_CONCURRENCY, 4, "P-107: at most 4 image requests in flight");

    const settle = () => new Promise(resolve => setImmediate(resolve));

    // 只对可视区内的行赋缩略图；滚动后旧行立刻清空（P-107）。
    // 缩略图载体是**行本身**（`PF-A7` 的 60 节点上界要求每行只摊到 2 个节点），
    // 所以这里断言 `style.backgroundImage`。
    innerHtmlWrites.length = 0;
    const { viewport, container } = buildContainer(672);
    const controller = makeController(container, viewport, []);
    controller.setItems(makeGroups(200));
    flushFrame();
    await settle();
    await settle();

    const rows = container.querySelectorAll(".bridge-row");
    const window = controller.window;
    for (let index = 0; index < rows.length; index += 1) {
        const absoluteIndex = window.start + index;
        const inside = absoluteIndex >= window.first && absoluteIndex <= window.last;
        if (!inside) continue;
        assert.ok(hasImageUrl(rows[index]),
            `P-107: visible row ${absoluteIndex} must carry an image URL (got ${thumbBackground(rows[index]) || "nothing"})`);
    }
    for (const row of rows) {
        const value = thumbBackground(row);
        if (!value) continue;
        const url = value.match(/^url\(["']?(.*?)["']?\)$/)?.[1] || "";
        assert.ok(list.isImageUrl(url), `P-107: every assigned background-image must be an image URL, got ${url}`);
        assert.ok(!/^data:/i.test(url), "P-107: no base64 may be assigned");
    }

    // 滚动到远处：离开窗口的行必须被清空（不留悬挂请求）。
    const firstRow = rows[0];
    viewport.scrollTop = 56 * 100;
    controller.render();
    await settle();
    assert.ok(!firstRow.isConnected || !thumbBackground(firstRow),
        "P-107: a row that left the window must drop its background-image");

    // 无可用图片时用占位图，且绝不退回 base64。
    const placeholderBox = buildContainer(672);
    const placeholderController = list.create({
        listId: "placeholder",
        container: placeholderBox.container,
        viewport: placeholderBox.viewport,
        placeholderUrl: "chrome-extension://test/icons/icon-128.png",
        getItemId: item => item.id,
        getImageUrl: () => "",
        createRowContent: row => {
            row.dataset.rowImage = "1";
            row.appendChild(documentStub.createElement("span"));
        },
        patchRow: () => undefined
    });
    placeholderController.setItems([{ id: "no-thumb" }]);
    flushFrame();
    await settle();
    await settle();
    const placeholderRow = placeholderBox.container.querySelector(".bridge-row");
    assert.ok(placeholderRow, "the placeholder case must still render a row");
    const placeholderBackground = thumbBackground(placeholderRow);
    const placeholderUrl = placeholderBackground.match(/^url\(["']?(.*?)["']?\)$/)?.[1] || "";
    assert.ok(list.isImageUrl(placeholderUrl),
        `P-107: a missing thumbnail must fall back to a placeholder image URL; got ${placeholderBackground}`);
}

// ================================================================ T-EXT-19

{
    const uiSource = fs.readFileSync(path.join(extensionDir, "js", "eagle-bridge-ui.js"), "utf8");
    const pollMin = Number(uiSource.match(/TASK_POLL_MIN_MS\s*=\s*(\d+)/)?.[1]);
    assert.ok(Number.isFinite(pollMin), "the popup must declare its task poll floor");
    assert.ok(pollMin >= 2000, `P-108: the task poll interval must be at least 2000 ms; got ${pollMin}`);
    assert.ok(/TASK_POLL_ACTIVE_MS\s*=\s*(\d+)/.test(uiSource));
    const activePoll = Number(uiSource.match(/TASK_POLL_ACTIVE_MS\s*=\s*(\d+)/)[1]);
    assert.ok(activePoll >= 2000, `P-108: even the active-task interval must respect the 2000 ms floor; got ${activePoll}`);
    assert.ok(!/setInterval\s*\(/.test(uiSource),
        "P-108/P-109: the popup must not use a resident setInterval");
    assert.ok(/dispose\(\)/.test(uiSource) && /clearTimeout\(taskPollTimer\)/.test(uiSource),
        "P-109: dispose() must clear the task poll timer");
    assert.ok(/pagehide/.test(uiSource) && /beforeunload/.test(uiSource),
        "P-109: both pagehide and beforeunload must tear the popup down");

    // P-109：dispose 之后控制器不得再渲染。
    innerHtmlWrites.length = 0;
    const { viewport, container } = buildContainer(672);
    const patchLog = [];
    const controller = makeController(container, viewport, patchLog);
    controller.setItems(makeGroups(30));
    flushFrame();
    const before = patchLog.length;
    controller.dispose();
    controller.update();
    const frames = flushFrame();
    assert.strictEqual(frames, 0, "P-109: a disposed list must cancel its pending frame");
    assert.strictEqual(patchLog.length, before, "P-109: a disposed list must not render anymore");
    assert.strictEqual(controller.nodeCount, 0, "P-109: dispose() must release the row nodes");
}

// ======================================================== 负向对照（检测器自检）

{
    // 如果渲染方式改回"整体重建"，检测器必须能抓到——否则上面的通过毫无意义。
    innerHtmlWrites.length = 0;
    const { container } = buildContainer(672);
    let caught = false;
    try {
        container.innerHTML = "<div class=\"bridge-row\">rebuilt</div>";
    } catch (_error) {
        caught = true;
    }
    assert.strictEqual(caught, true, "the P-102 detector must trip when a container is rebuilt");
    assert.strictEqual(innerHtmlWrites.length, 1, "the P-102 detector must record exactly one violation");

    // 祖先链上的整体重建同样必须被抓到（容器本身的父节点）。
    innerHtmlWrites.length = 0;
    const { viewport: parentViewport } = buildContainer(672);
    let ancestorCaught = false;
    try {
        parentViewport.innerHTML = "<div id=\"bridgeGroupList\"></div>";
    } catch (_error) {
        ancestorCaught = true;
    }
    assert.strictEqual(ancestorCaught, true, "the P-102 detector must also cover the list container's ancestors");
    assert.strictEqual(innerHtmlWrites.length, 1);

    // 反向对照：正常的节点复用路径**不得**产生任何 innerHTML 记账。
    innerHtmlWrites.length = 0;
    const { viewport, container: reusedContainer } = buildContainer(672);
    const controller = makeController(reusedContainer, viewport, []);
    controller.setItems(makeGroups(80));
    flushFrame();
    controller.setItems(makeGroups(80));
    flushFrame();
    controller.dispose();
    assert.deepStrictEqual(innerHtmlWrites, [],
        "the reuse path must be provably free of container rebuilds");
}

testThumbnails()
    .then(() => {
        process.stdout.write("Extension list rendering (P-101..P-109) OK\n");
    })
    .catch(error => {
        console.error(error);
        process.exitCode = 1;
    });
