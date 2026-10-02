(function (root, factory) {
    const api = factory();
    if (typeof module === "object" && module.exports) module.exports = api;
    root.EagleBridgeVirtualList = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
    "use strict";

    /**
     * 候选/任务列表的虚拟滚动与节点复用。
     *
     * 本模块就是 07 §6.2 的 `P-101` ~ `P-107` 的落地处。旧版
     * `js/eagle-bridge-ui.js` 的 9 处 `innerHTML` 整体重建、0 次
     * `createElement`/`appendChild`、1.2 秒轮询触发 `renderTasks()` 全量重建，
     * 是列表卡顿的实测根因。这里的纪律是：
     *
     *   P-101 只有**集合**真正变化才增删行；状态变化只改该行文本与属性
     *   P-102 **禁止**对列表容器或其祖先赋 `innerHTML`（出现一次即违反）
     *   P-103 以稳定 ID（候选组 ID）建立节点索引并复用
     *   P-104 监听只挂在列表容器上，行内不挂监听
     *   P-105 行数 > 50 时只渲染可见窗口 + 固定上下缓冲
     *   P-106 同帧多次更新合并，每帧最多渲染一次
     *   P-107 缩略图用图片 URL，只对可视行赋 `src`，并发上限 4
     *
     * 固定尺寸取值来自 P-105（**不得**改成测量式）：
     */

    const ROW_HEIGHT_PX = 56;
    const VISIBLE_ROWS = 12;
    const OVERSCAN_ABOVE_ROWS = 3;
    const OVERSCAN_BELOW_ROWS = 5;
    // 行数不超过该阈值时全部渲染（P-105 的触发条件是"超过 50"）。
    const VIRTUALIZE_THRESHOLD = 50;
    // P-107：图片请求并发上限。
    const IMAGE_CONCURRENCY = 4;

    function number(value) {
        const parsed = Number(value);
        return Number.isFinite(parsed) ? parsed : 0;
    }

    /**
     * P-105 的窗口计算。**纯函数**，便于单元测试：
     * 只有固定行高才算得出稳定的起始索引（§10 `E6` 已因此关闭）。
     */
    function visibleWindow(options = {}) {
        const total = Math.max(0, Math.floor(number(options.total)));
        const rowHeight = Math.max(1, number(options.rowHeight) || ROW_HEIGHT_PX);
        const viewportHeight = Math.max(0, number(options.viewportHeight));
        const scrollTop = Math.max(0, number(options.scrollTop));
        const visibleRows = Math.max(1, Math.floor(number(options.visibleRows) || VISIBLE_ROWS));
        const above = Math.max(0, Math.floor(number(options.overscanAbove ?? OVERSCAN_ABOVE_ROWS)));
        const below = Math.max(0, Math.floor(number(options.overscanBelow ?? OVERSCAN_BELOW_ROWS)));
        if (!total) return { start: 0, end: 0, first: 0, last: 0, totalHeight: 0, virtual: false };
        const virtual = total > VIRTUALIZE_THRESHOLD;
        if (!virtual) {
            return {
                start: 0,
                end: total,
                first: 0,
                last: Math.max(0, total - 1),
                totalHeight: total * rowHeight,
                virtual: false
            };
        }
        // 视口高度按整行折算：`clientHeight` 常带零头（内边距、边框、缩放），
        // 直接用它会多渲染一行，把 `PF-A7` 的 60 节点上界顶破。
        const wholeRows = Math.max(1, Math.floor(viewportHeight / rowHeight) || visibleRows);
        const first = Math.max(0, Math.min(total - 1, Math.floor(scrollTop / rowHeight)));
        const last = Math.max(first, Math.min(total - 1, first + wholeRows - 1));
        const start = Math.max(0, first - above);
        const end = Math.min(total, last + 1 + below);
        return {
            start,
            end,
            first,
            last,
            totalHeight: total * rowHeight,
            virtual: true
        };
    }

    /**
     * P-107：只对可视区内的行赋 `src`。
     * 该行的 `img` 不在可视区时清空 `src`（交给浏览器回收），绝不留 base64。
     */
    function shouldLoadImage(index, window) {
        return index >= window.first && index <= window.last;
    }

    function isImageUrl(value) {
        const text = String(value || "");
        if (!text) return false;
        // P-107：必须是图片 URL。`data:` 一律拒绝——base64 常驻内存正是要治的病。
        if (/^data:/i.test(text)) return false;
        return /^https?:\/\//i.test(text) || /^chrome-extension:\/\//i.test(text);
    }

    /** P-107：图片请求并发闸门（上限 4）。 */
    function createImageLoader(limit = IMAGE_CONCURRENCY) {
        const maximum = Math.max(1, Math.floor(number(limit)) || IMAGE_CONCURRENCY);
        const queue = [];
        const pending = new Map();
        let active = 0;

        function pump() {
            while (active < maximum && queue.length) {
                const job = queue.shift();
                active += 1;
                job.run().then(job.resolve, job.resolve).finally(() => {
                    active -= 1;
                    pump();
                });
            }
        }

        return {
            get activeCount() {
                return active;
            },
            get queuedCount() {
                return queue.length;
            },
            /**
             * @returns {Promise<boolean>} 该图片是否成功加载（失败即用占位图）
             */
            request(key, url) {
                const normalizedKey = String(key || "");
                if (!normalizedKey || !isImageUrl(url)) return Promise.resolve(false);
                if (pending.has(normalizedKey)) return pending.get(normalizedKey);
                const promise = new Promise(resolve => {
                    queue.push({
                        resolve,
                        run: async () => {
                            if (typeof Image !== "function") {
                                // 没有图片解码环境（例如纯逻辑测试）：不阻断渲染，
                                // 直接判定为"未加载"，由调用方回落占位图。
                                resolve(false);
                                return;
                            }
                            try {
                                await new Promise((done, fail) => {
                                    const image = new Image();
                                    image.decoding = "async";
                                    image.onload = () => done(true);
                                    image.onerror = () => fail(new Error("image_load_failed"));
                                    image.src = String(url);
                                });
                                resolve(true);
                            } catch (_error) {
                                resolve(false);
                            }
                        }
                    });
                    pump();
                }).finally(() => pending.delete(normalizedKey));
                pending.set(normalizedKey, promise);
                return promise;
            },
            /** 关闭列表时清空排队任务，避免残留工作。 */
            dispose() {
                queue.length = 0;
                pending.clear();
                active = 0;
            }
        };
    }

    /**
     * 创建列表控制器。
     *
     * @param {object} options
     * @param {HTMLElement} options.container  列表容器（`role="listbox"`）——**唯一**挂监听的元素
     * @param {HTMLElement} options.viewport   滚动视口
     * @param {(item:object, row:HTMLElement)=>void} options.patchRow  原位更新一行（只改文本与属性）
     * @param {(row:HTMLElement)=>void} [options.createRowContent]     首次创建行时构建一次行内结构
     * @param {(index:number)=>string} [options.getImageUrl]           行的缩略图 URL（P-107）
     * @param {(index:number)=>string} [options.getItemId]             稳定 ID（P-103）
     */
    function create(options = {}) {
        const container = options.container;
        const viewport = options.viewport || container;
        if (!container || !viewport) throw new TypeError("virtual list requires a container and a viewport");

        // P-103：稳定 ID → 行节点。索引键与节点键都用同一个 ID。
        const nodesById = new Map();
        const idByIndex = [];
        const imageLoader = createImageLoader(options.imageConcurrency || IMAGE_CONCURRENCY);
        const listeners = [];

        let items = [];
        let frameHandle = 0;
        let pending = false;
        let disposed = false;
        let lastWindow = visibleWindow({ total: 0, viewportHeight: 0 });

        // 占位元素：只创建一次，之后只改 `height`（P-102 的替代做法）。
        const spacer = container.ownerDocument.createElement("div");
        spacer.className = "bridge-list-spacer";
        spacer.setAttribute("aria-hidden", "true");
        container.appendChild(spacer);

        function viewportHeight() {
            return Math.max(0, number(viewport.clientHeight) || number(viewport.offsetHeight));
        }

        function scheduleRender() {
            // P-106：同一帧内的多次更新合并，每帧最多渲染一次。
            if (disposed || pending) return;
            pending = true;
            const raf = typeof requestAnimationFrame === "function"
                ? requestAnimationFrame
                : callback => setTimeout(callback, 16);
            frameHandle = raf(() => {
                pending = false;
                frameHandle = 0;
                render();
            });
        }

        function currentWindow() {
            return visibleWindow({
                total: items.length,
                rowHeight: ROW_HEIGHT_PX,
                viewportHeight: viewportHeight(),
                scrollTop: number(viewport.scrollTop),
                visibleRows: VISIBLE_ROWS,
                overscanAbove: OVERSCAN_ABOVE_ROWS,
                overscanBelow: OVERSCAN_BELOW_ROWS
            });
        }

        function createRow(item, index) {
            const row = container.ownerDocument.createElement("div");
            row.className = "bridge-row";
            row.setAttribute("role", "option");
            // 行内**不挂**任何监听（P-104）：全部事件由容器委托处理。
            if (typeof options.createRowContent === "function") options.createRowContent(row, item, index);
            container.appendChild(row);
            return row;
        }

        function placeRow(row, index) {
            row.style.height = `${ROW_HEIGHT_PX}px`;
            row.dataset.index = String(index);
            if (lastWindow.virtual) {
                row.style.position = "absolute";
                row.style.top = "0";
                row.style.left = "0";
                row.style.right = "0";
                row.style.transform = `translateY(${index * ROW_HEIGHT_PX}px)`;
            } else {
                row.style.position = "";
                row.style.top = "";
                row.style.left = "";
                row.style.right = "";
                row.style.transform = "";
            }
        }

        /**
         * P-107：缩略图一律用**图片 URL**。
         *
         * 这里刻意用 `background-image` 而不是 `<img>` 元素：`PF-A7` 把 200 条
         * 候选下的 DOM 节点总数硬性限定在 **60**，而 20 行窗口下每行只摊到 3 个
         * 节点。`background-image` 同样是"把图片 URL 交给浏览器缓存"——浏览器
         * 照常发起请求、照常命中 HTTP 缓存，只是不再多占一个 DOM 节点。
         * "只对可视区内的行赋 src"在此等价为"只对可视区内的行赋 background-image"，
         * 离开窗口即清空。**绝不接受 `data:`**（base64 常驻内存正是要治的病）。
         */
        function patchImage(index, row, window) {
            if (typeof options.getImageUrl !== "function") return;
            // 缩略图载体可以是行本身（`PF-A7` 的 60 节点上界要求行尽可能扁），
            // 也可以是行内的专用载体元素。
            const holder = row.matches?.("[data-row-image]") ? row : row.querySelector("[data-row-image]");
            if (!holder) return;
            const url = options.getImageUrl(index);
            if (!shouldLoadImage(index, window)) {
                // 不在可视区：立刻清空，不留悬挂请求也不留 base64。
                if (holder.style.backgroundImage) holder.style.backgroundImage = "";
                holder.dataset.imageUrl = "";
                return;
            }
            if (holder.dataset.imageUrl === url) return;
            holder.dataset.imageUrl = String(url || "");
            const safe = isImageUrl(url) ? String(url) : String(options.placeholderUrl || "");
            if (!safe) {
                holder.style.backgroundImage = "";
                return;
            }
            // P-107：图片请求并发上限 4，由 loader 统一排队（浏览器缓存由同一
            // URL 命中；loader 只负责"同时在飞的不超过 4 个"）。
            imageLoader
                .request(`${options.listId || "list"}:${safe}`, safe)
                .then(loaded => {
                    if (disposed) return;
                    if (holder.dataset.imageUrl !== String(url || "")) return;
                    holder.style.backgroundImage = loaded
                        ? `url("${safe}")`
                        : `url("${String(options.placeholderUrl || "")}")`;
                })
                .catch(() => undefined);
        }

        function render() {
            if (disposed) return;
            const window = lastWindow = currentWindow();
            const totalHeight = window.totalHeight;
            if (spacer.style.height !== `${totalHeight}px`) spacer.style.height = `${totalHeight}px`;
            container.dataset.virtual = window.virtual ? "true" : "false";
            container.dataset.windowStart = String(window.start);
            container.dataset.windowEnd = String(window.end);

            // P-101：先把超出窗口的行**移出可见集合**，但节点留在索引里复用。
            // 用窗口区间做 O(窗口大小) 的判定，避免逐行线性查找。
            for (const entry of nodesById.values()) {
                const inside = entry.index >= window.start && entry.index < window.end;
                if (inside) continue;
                if (entry.row.isConnected) entry.row.remove();
                entry.detached = true;
            }

            for (let index = window.start; index < window.end; index += 1) {
                const id = idByIndex[index];
                let entry = nodesById.get(id);
                if (!entry) {
                    // P-103：只在**该稳定 ID 从未出现过**时创建节点。
                    const row = createRow(items[index], index);
                    entry = { row, detached: false, index };
                    nodesById.set(id, entry);
                } else if (entry.detached || !entry.row.isConnected) {
                    container.appendChild(entry.row);
                    entry.detached = false;
                }
                entry.index = index;
                placeRow(entry.row, index);
                if (typeof options.patchRow === "function") options.patchRow(entry.row, items[index], index);
                patchImage(index, entry.row, window);
            }
        }

        return {
            /** 集合变化入口：只有 ID 序列真的不同才动 DOM（P-101）。 */
            setItems(nextItems) {
                const next = Array.isArray(nextItems) ? nextItems : [];
                const nextIds = next.map((item, index) => String(options.getItemId ? options.getItemId(item, index) : item?.id ?? index));
                const changed = nextIds.length !== idByIndex.length
                    || nextIds.some((id, index) => id !== idByIndex[index]);
                items = next;
                if (!changed) {
                    // 集合未变：不增删行，交给 patchRow 原位更新。
                    scheduleRender();
                    return false;
                }
                idByIndex.length = 0;
                idByIndex.push(...nextIds);
                // 移除已经不在集合里的节点（这是唯一合法的删行时机）。
                const alive = new Set(nextIds);
                for (const [id, entry] of nodesById) {
                    if (alive.has(id)) continue;
                    entry.row.remove();
                    nodesById.delete(id);
                }
                scheduleRender();
                return true;
            },
            /** 状态变化入口：不清集合，只请求原位重绘。 */
            update() {
                scheduleRender();
            },
            render,
            on(target, type, handler, opts) {
                target.addEventListener(type, handler, opts);
                listeners.push([target, type, handler, opts]);
            },
            get window() {
                return lastWindow;
            },
            get nodeCount() {
                return nodesById.size;
            },
            get imageActivity() {
                return { active: imageLoader.activeCount, queued: imageLoader.queuedCount };
            },
            /** P-109：关闭即停——取消挂起的帧、清空图片队列、摘掉监听。 */
            dispose() {
                disposed = true;
                if (frameHandle) {
                    const cancel = typeof cancelAnimationFrame === "function" ? cancelAnimationFrame : clearTimeout;
                    cancel(frameHandle);
                    frameHandle = 0;
                }
                pending = false;
                imageLoader.dispose();
                for (const [target, type, handler, opts] of listeners) {
                    target.removeEventListener(type, handler, opts);
                }
                listeners.length = 0;
                for (const [, entry] of nodesById) entry.row.remove();
                nodesById.clear();
                idByIndex.length = 0;
                items = [];
            }
        };
    }

    return {
        ROW_HEIGHT_PX,
        VISIBLE_ROWS,
        OVERSCAN_ABOVE_ROWS,
        OVERSCAN_BELOW_ROWS,
        VIRTUALIZE_THRESHOLD,
        IMAGE_CONCURRENCY,
        visibleWindow,
        shouldLoadImage,
        isImageUrl,
        createImageLoader,
        create
    };
});
