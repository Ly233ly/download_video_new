(function () {
    var _framePreviewCache = new Map();

    // ——— 声明式站点适配器的运行时（纯函数，不碰 document / chrome）———
    //
    // 声明数据只有背景侧读得到：内容脚本读不到扩展包内的文件（`manifest.json` 里没有、
    // 也不该为这个数据文件加 `web_accessible_resources`，[14 §5.3]）。这里只做一件事：
    // 把拿到的声明折算成"报哪些候选、报成什么样"。**本文件不含任何站点名**——站点差异
    // 全部来自声明的 `match` / `identity` / `capture` / `title` 四组字段（契约 A-102）。
    //
    // 降级：声明没拿到（通道失败 / 文件缺失 / 解析失败）时 `matchAdapter` 返回 null，
    // 下面每个函数对 null 声明都退回通用行为——这是 [14 §5.3] 要求的"没有适配器"退化，
    // 不是错误分支。

    const ADAPTER_SIGNAL_ELEMENTS_PER_SELECTOR = 4;
    const ADAPTER_SIGNAL_VALUE_LIMIT = 32;
    const ADAPTER_DECLARATION_TIMEOUT_MS = 400;
    const ADAPTER_REQUIRED_LOGIC = [
        "adapterPrimarySelection",
        "selectPrimaryVideoIndex",
        "adapterAllowsCapture",
        "videoIdFromAdapterSignals",
        "adapterRequiresId",
        "canonicalPageUrlFromAdapter",
        "adapterCandidateTitle"
    ];

    function adapterStringList(values) {
        return (Array.isArray(values) ? values : [])
            .map(value => String(value === undefined || value === null ? "" : value).trim())
            .filter(Boolean);
    }

    /** `capture.containers`：候选容器选择器，按声明顺序逐个尝试。 */
    function adapterCaptureSelectors(adapter) {
        return adapterStringList(adapter?.capture?.containers);
    }

    /** `identity.domSignals`：身份信号选择器（命中元素的属性值当信号）。 */
    function adapterSignalSelectors(adapter) {
        return adapterStringList(adapter?.identity?.domSignals);
    }

    /**
     * 按声明顺序取第一个命中的容器；声明为空或都没命中时返回 null，由调用方退回
     * 通用容器策略（[14 §5]）。
     *
     * `groups` 是纯数据 `[{ selector, container }]`：与 DOM 无关，因此可离线判定。
     */
    function pickAdapterContainer(adapter, groups) {
        const declared = adapterCaptureSelectors(adapter);
        if (!declared.length) return null;
        const resolved = new Map();
        for (const group of Array.isArray(groups) ? groups : []) {
            if (!group || typeof group !== "object") continue;
            resolved.set(String(group.selector || ""), group.container || null);
        }
        for (const selector of declared) {
            const container = resolved.get(selector);
            if (container) return { selector, container };
        }
        return null;
    }

    /** 取某个播放器的身份信号值；没有就返回空数组。 */
    function adapterSignalValues(signals, index) {
        for (const entry of Array.isArray(signals) ? signals : []) {
            if (!entry || Number(entry.index) !== Number(index)) continue;
            return Array.isArray(entry.values) ? entry.values : [];
        }
        return [];
    }

    /**
     * 播放器快照 → 候选计划（纯函数）。
     *
     * `input.videos[i]` 须带 `index`（文档内下标）、`sources`、`readyState`、`duration`、
     * `currentTime`、`paused`、`ended`、`rect`、`viewport`，可带 `pageUrl`（就近推导出的
     * 内容页地址，缺省用 `input.pageUrl`）。`input.signals` 是 `[{ index, values }]`
     * （容器内在前、整篇文档在后，[14 §5.4]）；`input.fallbackTitles` 是
     * `{ [index]: 通用启发式标题 }`。
     */
    function planAdapterDiscovery(logic, adapter, input) {
        if (!logic || ADAPTER_REQUIRED_LOGIC.some(name => typeof logic[name] !== "function")) return [];
        const settings = input && typeof input === "object" ? input : {};
        const videos = Array.isArray(settings.videos) ? settings.videos : [];
        const fallbackTitles = settings.fallbackTitles && typeof settings.fallbackTitles === "object"
            ? settings.fallbackTitles : {};
        const selection = logic.adapterPrimarySelection(adapter);
        const primaryIndex = logic.selectPrimaryVideoIndex(adapter, videos);
        const plans = [];
        for (let position = 0; position < videos.length; position += 1) {
            const video = videos[position] || {};
            const index = Number.isInteger(video.index) ? video.index : position;
            // `all`（含"没有适配器"的降级）时全部上报为独立候选；其余取值只认声明选出的
            // 主播放器，选不出来（-1）就没有候选。
            if (selection !== "all" && index !== primaryIndex) continue;
            const sources = Array.isArray(video.sources) ? video.sources.map(value => String(value || "")) : [];
            const hasBlobSource = sources.some(source => source.startsWith("blob:"));
            const hasDirectSource = sources.some(source => /^https?:/i.test(source));
            if (!logic.adapterAllowsCapture(adapter, { hasBlobSource, hasDirectSource })) continue;
            const pageUrl = String(video.pageUrl || settings.pageUrl || "");
            if (!pageUrl) continue;
            const videoId = logic.videoIdFromAdapterSignals(adapter, {
                pageUrl,
                signals: adapterSignalValues(settings.signals, index)
            });
            // `identity.requireId`：取不到 ID 就丢弃，避免产生无法归属的条目（[14 §5]）
            if (!videoId && logic.adapterRequiresId(adapter)) continue;
            const resolvedPageUrl = logic.canonicalPageUrlFromAdapter(adapter, videoId, pageUrl);
            if (!resolvedPageUrl) continue;
            const fallbackTitle = String(fallbackTitles[index] || "");
            const declaredTitle = logic.adapterCandidateTitle(adapter, { description: fallbackTitle, videoId });
            plans.push({
                index,
                videoId,
                pageUrl: resolvedPageUrl,
                title: declaredTitle || fallbackTitle,
                hasBlobSource,
                hasDirectSource,
                primary: index === primaryIndex
            });
        }
        return plans;
    }

    if (typeof module === "object" && module.exports) {
        // 离线测试入口（`tests/js/test_adapter_runtime.js`）：只导出上面这层纯函数，
        // 不执行下面的 DOM / chrome 代码。
        module.exports = {
            adapterCaptureSelectors,
            adapterSignalSelectors,
            pickAdapterContainer,
            adapterSignalValues,
            planAdapterDiscovery
        };
        return;
    }

    // 背景侧下发的适配器声明：null = 尚未拿到（先按"没有适配器"跑），数组 = 已确定
    // （空数组表示背景侧也不可用）。[14 §5.3]
    var _siteAdapters = null;
    var _siteAdaptersPending = false;
    var _siteAdaptersWarned = false;
    var _siteAdaptersWaiters = [];

    /** 声明通道失败时只提醒一次（[14 §5.3]），不刷屏。 */
    function warnAdapterFallback(reason) {
        if (_siteAdaptersWarned) return;
        _siteAdaptersWarned = true;
        console.warn("[留底] 站点适配器声明不可用，本页按通用发现行为工作：", reason);
    }

    /**
     * 向背景侧要一次声明。拿到之前按"没有适配器"跑；明确失败时记空并提醒一次，
     * **不阻塞**候选发现。
     */
    function requestSiteAdapters(done) {
        if (_siteAdapters !== null) {
            if (done) done();
            return;
        }
        if (done) _siteAdaptersWaiters.push(done);
        if (_siteAdaptersPending) return;
        _siteAdaptersPending = true;
        const settle = (adapters) => {
            _siteAdaptersPending = false;
            _siteAdapters = adapters;
            const waiters = _siteAdaptersWaiters.splice(0, _siteAdaptersWaiters.length);
            for (const waiter of waiters) waiter();
        };
        try {
            chrome.runtime.sendMessage({ Message: "getSiteAdapters" }, function (response) {
                if (chrome.runtime.lastError) {
                    warnAdapterFallback(chrome.runtime.lastError.message || "send_failed");
                    settle([]);
                    return;
                }
                if (Array.isArray(response?.adapters)) {
                    settle(response.adapters);
                    return;
                }
                warnAdapterFallback(String((response && response.reason) || "no_adapters"));
                settle([]);
            });
        } catch (error) {
            warnAdapterFallback(String((error && error.message) || "send_failed"));
            settle([]);
        }
    }

    /** 当前页面命中的适配器声明；没有（含"声明没拿到"的降级）返回 null。 */
    function adapterForCurrentPage(logic) {
        if (typeof logic.matchAdapter !== "function" || !_siteAdapters) return null;
        return logic.matchAdapter(_siteAdapters, location.hostname) || null;
    }

    function closestContainer(element, selector) {
        try { return element.closest(selector); } catch (_error) { return null; }
    }

    /** 通用容器策略：没有声明、或声明的容器一个都没命中时用它（保持既有行为）。 */
    function defaultCandidateContainer(video) {
        return video.closest("article, [role='article'], [role='dialog'], figure") || video.parentElement || video;
    }

    /** 候选容器：先按 `capture.containers` 的声明顺序取，取不到再退回通用策略。 */
    function adapterCandidateContainer(video, adapter) {
        const declared = adapterCaptureSelectors(adapter);
        if (!declared.length) return defaultCandidateContainer(video);
        const picked = pickAdapterContainer(adapter, declared.map(selector => ({
            selector,
            container: closestContainer(video, selector)
        })));
        return picked ? picked.container : defaultCandidateContainer(video);
    }

    function elementSignalValues(element) {
        const values = [];
        const className = String(element.getAttribute?.("class") || "").trim();
        if (className) values.push(className);
        for (const attribute of Array.from(element.attributes || [])) {
            const value = String(attribute.value || "").trim();
            if (value) values.push(value);
        }
        return values;
    }

    /**
     * 身份信号值：`identity.domSignals` 的每个选择器命中的元素，取其属性值（含类名）。
     * 收集顺序是"候选容器内 → 整篇文档"，与 [14 §5.4] 的"先逐个 DOM 信号、再页面地址"
     * 一致；同一个值只留一次，总量有界（[07 §6.1]）。
     */
    function collectAdapterSignals(container, adapter) {
        const selectors = adapterSignalSelectors(adapter);
        const values = [];
        if (!selectors.length) return values;
        const seen = new Set();
        const scopes = [];
        if (container && container !== document.documentElement) scopes.push(container);
        scopes.push(document.documentElement || document);
        for (const scope of scopes) {
            for (const selector of selectors) {
                let elements;
                try {
                    elements = Array.from(scope.querySelectorAll(selector));
                } catch (_error) {
                    continue; // 声明里的选择器非法：跳过它，不影响其它选择器
                }
                if (typeof scope.matches === "function") {
                    try {
                        if (scope.matches(selector)) elements.unshift(scope);
                    } catch (_error) { /* 同上：非法选择器跳过 */ }
                }
                for (const element of elements.slice(0, ADAPTER_SIGNAL_ELEMENTS_PER_SELECTOR)) {
                    for (const value of elementSignalValues(element)) {
                        if (seen.has(value)) continue;
                        seen.add(value);
                        values.push(value);
                        if (values.length >= ADAPTER_SIGNAL_VALUE_LIMIT) return values;
                    }
                }
            }
        }
        return values;
    }

    function absoluteImageUrl(value) {
        try {
            const url = new URL(String(value || ""), location.href);
            return ["http:", "https:"].includes(url.protocol) ? url.href : "";
        } catch (_error) {
            return "";
        }
    }

    function elementScore(element) {
        const rect = element.getBoundingClientRect();
        const width = Math.max(0, Math.min(rect.right, innerWidth) - Math.max(rect.left, 0));
        const height = Math.max(0, Math.min(rect.bottom, innerHeight) - Math.max(rect.top, 0));
        const visibleArea = width * height;
        const style = getComputedStyle(element);
        if (style.display === "none" || style.visibility === "hidden" || Number(style.opacity) === 0) return 0;
        return visibleArea || Math.max(0, rect.width * rect.height * 0.05);
    }

    function videoSources(video) {
        return [video.currentSrc, video.src, ...Array.from(video.querySelectorAll("source"), source => source.src)]
            .map(value => {
                try { return new URL(value, location.href).href; } catch (_error) { return ""; }
            })
            .filter(Boolean);
    }

    function videoArtwork(video) {
        return absoluteImageUrl(video.poster || video.getAttribute("poster"));
    }

    function backgroundArtwork(element) {
        for (let current = element; current && current !== document.documentElement; current = current.parentElement) {
            const value = getComputedStyle(current).backgroundImage || "";
            const match = value.match(/url\((?:["']?)(https?:[^"')]+)(?:["']?)\)/i);
            if (match) return absoluteImageUrl(match[1]);
        }
        return "";
    }

    function metadataArtwork() {
        const values = Array.from(document.querySelectorAll(
            'meta[property="og:image"], meta[property="og:image:secure_url"], meta[name="twitter:image"], meta[itemprop="thumbnailUrl"], link[rel="image_src"]'
        ), element => element.content || element.href || "");
        for (const script of document.querySelectorAll('script[type="application/ld+json"]')) {
            try {
                const parsed = JSON.parse(script.textContent || "null");
                const records = Array.isArray(parsed) ? parsed : [parsed];
                for (const record of records) {
                    const thumbnail = record?.thumbnailUrl || record?.thumbnail?.contentUrl;
                    if (Array.isArray(thumbnail)) values.push(...thumbnail);
                    else if (thumbnail) values.push(thumbnail);
                }
            } catch (_error) { /* Ignore unrelated or malformed JSON-LD. */ }
        }
        return values.map(absoluteImageUrl).filter(Boolean);
    }

    function metadataContent(selector) {
        const element = document.querySelector(selector);
        return String(element?.getAttribute?.("content") || element?.getAttribute?.("href") || "").trim();
    }

    function discoverStructuredPageResolver(logic) {
        const pageUrl = logic?.chooseStructuredVideoPageUrl?.(location.href, {
            ogType: metadataContent('meta[property="og:type"]'),
            twitterCard: metadataContent('meta[name="twitter:card"]'),
            canonicalUrl: metadataContent('meta[property="og:url"], link[rel="canonical"]'),
            playerUrl: metadataContent('meta[name="twitter:player"]'),
            videoUrl: metadataContent('meta[property="og:video"], meta[property="og:video:url"], meta[property="og:video:secure_url"]')
        }) || "";
        if (!pageUrl || _pageResolverSent.has(pageUrl)) return false;
        const duration = Number(metadataContent('meta[property="video:duration"], meta[property="og:video:duration"]')) || 0;
        const width = Number(metadataContent('meta[property="og:video:width"]')) || 0;
        const height = Number(metadataContent('meta[property="og:video:height"]')) || 0;
        const groupKey = `page:${logic.stableVisualKey(pageUrl, 0, duration)}`;
        const title = metadataContent('meta[property="og:title"], meta[name="twitter:title"]')
            || String(document.title || "网页视频").slice(0, 220);
        const thumbnailUrl = absoluteImageUrl(metadataContent(
            'meta[property="og:image"], meta[property="og:image:secure_url"], meta[name="twitter:image"]'
        ));
        _pageResolverSent.add(pageUrl);
        chrome.runtime.sendMessage({
            Message: "addMedia",
            url: pageUrl,
            href: location.href,
            extraExt: "mp4",
            mime: "video/mp4",
            requestId: `page-resolver-metadata-${groupKey}`,
            requestHeaders: {
                referer: location.href,
                origin: location.origin,
                "user-agent": navigator.userAgent
            },
            mediaMeta: {
                resolver: "page",
                role: "video",
                streamId: groupKey,
                title: title.slice(0, 220),
                label: "页面视频 · 最佳可用",
                width,
                height,
                duration,
                thumbnailUrl,
                groupKey,
                qualitySource: "structured_page_metadata",
                separateAv: true,
                drm: false
            }
        }, () => { void chrome.runtime.lastError; });
        return true;
    }

    function captureVideoFrame(video) {
        const logic = globalThis.EagleBridgeCandidateLogic;
        if (!logic || !video || video.readyState < 2 || !video.videoWidth || !video.videoHeight) return "";
        try {
            const scale = Math.min(1, 360 / video.videoWidth, 202 / video.videoHeight);
            const canvas = document.createElement("canvas");
            canvas.width = Math.max(1, Math.round(video.videoWidth * scale));
            canvas.height = Math.max(1, Math.round(video.videoHeight * scale));
            const context = canvas.getContext("2d", { alpha: false });
            if (!context) return "";
            context.drawImage(video, 0, 0, canvas.width, canvas.height);
            return logic.safeFrameDataUrl(canvas.toDataURL("image/jpeg", 0.72));
        } catch (_error) {
            // Cross-origin players can taint a canvas. Poster/metadata remains
            // available, but a generic website icon must never pretend to be
            // a frame from the video.
            return "";
        }
    }

    function cachedVideoFrame(video, groupKey) {
        if (!video || !groupKey) return "";
        const cached = _framePreviewCache.get(groupKey);
        if (cached && Date.now() - cached.capturedAt < 1500) return cached.dataUrl;
        const dataUrl = captureVideoFrame(video);
        if (dataUrl) {
            _framePreviewCache.set(groupKey, { dataUrl, capturedAt: Date.now() });
            if (_framePreviewCache.size > 8) {
                const oldest = [..._framePreviewCache.entries()].sort((left, right) => left[1].capturedAt - right[1].capturedAt)[0];
                if (oldest) _framePreviewCache.delete(oldest[0]);
            }
        }
        return dataUrl;
    }

    function collectMediaVisualContext(mediaUrl) {
        const logic = globalThis.EagleBridgeCandidateLogic;
        if (!logic) return { thumbnailUrl: "" };
        let target = "";
        try { target = new URL(String(mediaUrl || ""), location.href).href; } catch (_error) { /* keep empty */ }
        const videos = Array.from(document.querySelectorAll("video"))
            .map((video, ordinal) => ({
                video,
                ordinal,
                sources: videoSources(video),
                artwork: videoArtwork(video),
                score: elementScore(video),
                playing: !video.paused && !video.ended && video.readyState >= 2
            }))
            .sort((left, right) => (Number(right.playing) - Number(left.playing)) || right.score - left.score);
        const visualMatch = logic.resolveVisualMatch(videos, target);
        const exactVideos = visualMatch.matches;
        const selected = visualMatch.selected;
        const exact = exactVideos.map(item => item.artwork).filter(Boolean);
        const nearby = [];
        for (const item of exactVideos.slice(0, 3)) {
            const container = item.video.closest("figure, article, [role='dialog'], [class*='player'], [class*='video']") || item.video.parentElement;
            if (!container) continue;
            const images = Array.from(container.querySelectorAll("img"))
                .filter(image => Math.max(image.naturalWidth, image.width) >= 160 && Math.max(image.naturalHeight, image.height) >= 90)
                .sort((left, right) => elementScore(right) - elementScore(left));
            nearby.push(...images.map(image => absoluteImageUrl(image.currentSrc || image.src)).filter(Boolean));
        }
        const duration = Number(selected?.video?.duration);
        const selectedSource = selected?.sources?.[0] || selected?.video?.currentSrc || location.href;
        // 分组键只由"媒体来源 + 播放器序号 + 时长"决定（站点无关）。
        // 旧版还会用站点 DOM 特征（`data-e2e` 等）取站点自身的视频 ID；
        // 那属站点专用逻辑，已按 07 §2 移出扩展包（阶段 3 的适配器）。
        const groupKey = selected
            ? logic.stableVisualKey(selectedSource, selected.ordinal, duration)
            : "";
        const rect = selected?.video?.getBoundingClientRect?.();
        const selectedContainer = selected?.video?.closest?.("article, [role='article'], [role='dialog'], figure")
            || selected?.video?.parentElement;
        const captureRect = rect && rect.width >= 48 && rect.height >= 27
            && rect.bottom > 0 && rect.right > 0 && rect.top < innerHeight && rect.left < innerWidth
            ? {
                x: Math.max(0, rect.left),
                y: Math.max(0, rect.top),
                width: Math.min(innerWidth, rect.right) - Math.max(0, rect.left),
                height: Math.min(innerHeight, rect.bottom) - Math.max(0, rect.top),
                viewportWidth: innerWidth,
                viewportHeight: innerHeight
            }
            : null;
        return {
            thumbnailUrl: selected ? logic.selectThumbnail({ exact, nearby, metadata: metadataArtwork() }) : "",
            frameDataUrl: cachedVideoFrame(selected?.video, groupKey),
            groupKey,
            duration: Number.isFinite(duration) && duration > 0 ? duration : 0,
            width: Number(selected?.video?.videoWidth) || 0,
            height: Number(selected?.video?.videoHeight) || 0,
            title: selected ? pageResolverTitle(selected.video, selectedContainer) : "",
            captureRect,
            visualMatch: visualMatch.kind
        };
    }

    function embeddingFrameRect(frameUrl) {
        let target = "";
        try { target = new URL(String(frameUrl || ""), location.href).href; } catch (_error) { return null; }
        const frames = Array.from(document.querySelectorAll("iframe"));
        const exact = frames.find(frame => {
            try { return new URL(frame.src, location.href).href === target; } catch (_error) { return false; }
        });
        const sameDocument = exact || frames.find(frame => {
            try {
                const left = new URL(frame.src, location.href);
                const right = new URL(target);
                return left.origin === right.origin && left.pathname === right.pathname;
            } catch (_error) { return false; }
        });
        const rect = sameDocument?.getBoundingClientRect?.();
        if (!rect || rect.width < 48 || rect.height < 27) return null;
        return {
            x: Math.max(0, rect.left),
            y: Math.max(0, rect.top),
            width: Math.min(innerWidth, rect.right) - Math.max(0, rect.left),
            height: Math.min(innerHeight, rect.bottom) - Math.max(0, rect.top),
            viewportWidth: innerWidth,
            viewportHeight: innerHeight
        };
    }

    var _pageResolverSent = new Set();
    var _pageResolverSchedule = null;
    var _pageResolverStarted = false;
    var _pageResolverObserving = false;
    var _pageLocationTimer = null;
    var _pageLocationTracker = null;

    function pageResolverTitle(video, container) {
        const textValues = selector => Array.from(container?.querySelectorAll?.(selector) || [], element =>
            element.getAttribute?.("content") || element.getAttribute?.("aria-label") || element.textContent || ""
        ).slice(0, 24);
        const captions = textValues([
            "figcaption", "[itemprop='caption']", "[itemprop='description']",
            "[itemprop='headline']", "[aria-description]"
        ].join(","));
        const headings = textValues("h1, h2, h3, [role='heading'], [itemprop='name']");
        const lines = String(container?.innerText || container?.textContent || "")
            .split(/[\r\n]+/).map(value => value.trim()).filter(Boolean).slice(0, 60);
        const imageAlts = Array.from(container?.querySelectorAll?.("img[alt]") || [], image => image.getAttribute("alt") || "").slice(0, 12);
        return globalThis.EagleBridgeCandidateLogic.selectContentTitle?.({
            captions,
            headings,
            lines,
            imageAlts,
            fallback: document.title || "网页视频"
        }) || String(document.title || "网页视频").slice(0, 220);
    }

    function nearbyVideoContent(video, logic) {
        const currentPageUrl = logic.chooseContentPageUrl(location.href, []);
        let element = video;
        for (let depth = 0; element && depth < 16; depth += 1, element = element.parentElement) {
            const links = [];
            if (element.matches?.("a[href]") && element.href) links.push(element.href);
            links.push(...Array.from(element.querySelectorAll?.("a[href]") || [], link => link.href).slice(0, 64));
            const linkedPageUrl = logic.chooseNearbyContentPageUrl?.(location.href, [links]) || "";
            if (linkedPageUrl && linkedPageUrl !== currentPageUrl) {
                return {
                    container: element.matches?.("a[href]") ? element.parentElement || element : element,
                    pageUrl: linkedPageUrl
                };
            }
        }
        const container = video.closest("article, [role='article'], [role='dialog'], figure") || video.parentElement;
        return { container, pageUrl: currentPageUrl };
    }

    /**
     * 页面解析器发现（站点无关 + 声明驱动）。
     *
     * 按 07 §2，扩展源码里**禁止**出现站点名判断：站点差异全部来自声明数据
     * （包内的 `site-adapters.json`，[14 §5]）——先用 `matchAdapter` 选中当前页面的
     * 适配器，再按它的 `capture` / `identity` / `title` 决定报哪些候选、报成什么样。
     * 没有声明（含"声明没拿到"的降级）时 `matchAdapter` 返回 null，下面每一步都退回
     * 通用行为（[14 §5.3]）。
     *
     * 通用规则仍然只处理"页面自己供给媒体"的播放器：能否直连由声明的
     * `allowDirectStream` / `requireBlobSource` 决定，未声明时等价于"只认 blob 源"。
     */
    function discoverPageResolvers() {
        if (window.top !== window) return;
        const logic = globalThis.EagleBridgeCandidateLogic;
        if (!logic?.chooseContentPageUrl) return;
        discoverStructuredPageResolver(logic);

        const adapter = adapterForCurrentPage(logic);
        const candidates = [];
        for (const video of Array.from(document.querySelectorAll("video")).slice(0, 32)) {
            if (video.readyState < 2) continue;
            const rect = video.getBoundingClientRect();
            const content = nearbyVideoContent(video, logic);
            candidates.push({
                video,
                container: adapterCandidateContainer(video, adapter),
                pageUrl: content.pageUrl,
                snapshot: {
                    index: candidates.length,
                    sources: videoSources(video),
                    readyState: video.readyState,
                    duration: Number(video.duration) || 0,
                    currentTime: Number(video.currentTime) || 0,
                    paused: video.paused,
                    ended: video.ended,
                    rect: {
                        left: Number(rect.left) || 0,
                        top: Number(rect.top) || 0,
                        width: Number(rect.width) || 0,
                        height: Number(rect.height) || 0
                    },
                    viewport: { width: innerWidth, height: innerHeight }
                }
            });
        }
        const fallbackTitles = {};
        candidates.forEach((candidate, index) => {
            fallbackTitles[index] = pageResolverTitle(candidate.video, candidate.container);
        });
        const plans = planAdapterDiscovery(logic, adapter, {
            pageUrl: location.href,
            videos: candidates.map(candidate => candidate.snapshot),
            signals: candidates.map((candidate, index) => ({
                index,
                values: collectAdapterSignals(candidate.container, adapter)
            })),
            fallbackTitles
        });
        for (const plan of plans) {
            const candidate = candidates[plan.index];
            if (!candidate || _pageResolverSent.has(plan.pageUrl)) continue;
            const video = candidate.video;
            const duration = Number(video.duration);
            const groupKey = `page:${logic.stableVisualKey(plan.pageUrl, 0, duration)}`;
            const frameDataUrl = captureVideoFrame(video);
            const thumbnailUrl = videoArtwork(video) || backgroundArtwork(video);
            _pageResolverSent.add(plan.pageUrl);
            chrome.runtime.sendMessage({
                Message: "addMedia",
                url: plan.pageUrl,
                href: location.href,
                extraExt: "mp4",
                mime: "video/mp4",
                requestId: `page-resolver-${groupKey}`,
                requestHeaders: {
                    referer: location.href,
                    origin: location.origin,
                    "user-agent": navigator.userAgent
                },
                mediaMeta: {
                    resolver: "page",
                    role: "video",
                    streamId: groupKey,
                    title: plan.title,
                    label: "页面已加载 · 最佳可用",
                    width: Number(video.videoWidth) || 0,
                    height: Number(video.videoHeight) || 0,
                    duration: Number.isFinite(duration) && duration > 0 ? duration : 0,
                    thumbnailUrl,
                    frameDataUrl,
                    groupKey,
                    qualitySource: "desktop_page_resolver",
                    separateAv: true,
                    drm: false
                }
            }, () => { void chrome.runtime.lastError; });
        }
    }

    function schedulePageResolverDiscovery() {
        const logic = globalThis.EagleBridgeCandidateLogic;
        if (!_pageResolverSchedule) {
            _pageResolverSchedule = logic?.createBoundedScheduler
                ? logic.createBoundedScheduler(discoverPageResolvers, 250)
                : (() => {
                    let pending = false;
                    return () => {
                        if (pending) return;
                        pending = true;
                        setTimeout(() => {
                            pending = false;
                            discoverPageResolvers();
                        }, 250);
                    };
                })();
        }
        _pageResolverSchedule();
    }

    function beginPageResolverDiscovery() {
        if (_pageResolverObserving) return;
        _pageResolverObserving = true;
        discoverPageResolvers();
        document.addEventListener("play", schedulePageResolverDiscovery, true);
        document.addEventListener("loadedmetadata", schedulePageResolverDiscovery, true);
        const observer = new MutationObserver(schedulePageResolverDiscovery);
        observer.observe(document.documentElement || document, { childList: true, subtree: true });
    }

    /**
     * 启动发现。**先要一次声明再开始**：声明驱动与通用路径给出的候选地址不同，
     * 先按通用路径报一批、再按声明报一批会产生重复候选。声明通道不可用时由这里的
     * 超时兜底开跑——[14 §5.3] 要求下发失败**不得**让候选发现整体失效。
     */
    function startPageResolverDiscovery() {
        if (window.top !== window || _pageResolverStarted) return;
        _pageResolverStarted = true;
        requestSiteAdapters(beginPageResolverDiscovery);
        setTimeout(beginPageResolverDiscovery, ADAPTER_DECLARATION_TIMEOUT_MS);
    }

    function resetPageDiscovery(nextUrl, _previousUrl, context = {}) {
        _framePreviewCache.clear();
        _pageResolverSent.clear();

        if (window.top !== window) return;
        discoverPageResolvers();
        [250, 900, 1800].forEach(delay => {
            setTimeout(schedulePageResolverDiscovery, delay);
        });
        if (context.notifyBackground) {
            chrome.runtime.sendMessage({
                Message: "notifyPageLocationChanged",
                url: nextUrl
            }, () => { void chrome.runtime.lastError; });
        }
    }

    function initializePageLocationTracking() {
        if (window.top !== window || _pageLocationTracker) return;
        const logic = globalThis.EagleBridgeCandidateLogic;
        _pageLocationTracker = logic?.createLocationChangeTracker
            ? logic.createLocationChangeTracker(location.href, resetPageDiscovery)
            : (() => {
                let currentUrl = String(location.href || "");
                return (nextUrl, context) => {
                    const normalizedUrl = String(nextUrl || "");
                    if (!normalizedUrl || normalizedUrl === currentUrl) return false;
                    const previousUrl = currentUrl;
                    currentUrl = normalizedUrl;
                    resetPageDiscovery(normalizedUrl, previousUrl, context);
                    return true;
                };
            })();
        _pageLocationTimer = setInterval(() => {
            _pageLocationTracker(location.href, { notifyBackground: true });
        }, 750);
    }

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", () => {
            startPageResolverDiscovery();
            initializePageLocationTracking();
        }, { once: true });
    } else {
        startPageResolverDiscovery();
        initializePageLocationTracking();
    }

    chrome.runtime.onMessage.addListener(function (Message, sender, sendResponse) {
        if (chrome.runtime.lastError) { return; }
        if (Message.Message == "getMediaVisualContext") {
            sendResponse({ ...collectMediaVisualContext(Message.url), frameUrl: location.href });
            return true;
        }
        if (Message.Message == "getEmbeddingFrameRect") {
            sendResponse(embeddingFrameRect(Message.frameUrl));
            return true;
        }
        if (Message.Message == "discoverPageResolvers") {
            discoverPageResolvers();
            sendResponse({ ok: true });
            return true;
        }
        if (Message.Message == "pageLocationChanged") {
            if (_pageLocationTracker) {
                _pageLocationTracker(Message.url || location.href, { notifyBackground: false });
            } else {
                resetPageDiscovery(Message.url || location.href, "", { notifyBackground: false });
            }
            sendResponse({ ok: true });
            return true;
        }
    });

    // Heart Beat
    var Port;
    function connect() {
        Port = chrome.runtime.connect(chrome.runtime.id, { name: "HeartBeat" });
        Port.postMessage("HeartBeat");
        Port.onMessage.addListener(function (message, Port) { return true; });
        Port.onDisconnect.addListener(connect);
    }
    connect();

    const sendAddMedia = (data) => {
        chrome.runtime.sendMessage({
            Message: "addMedia",
            url: data.url,
            href: data.href ?? location.href,
            extraExt: data.ext,
            mime: data.mime,
            requestHeaders: { referer: data.referer },
            requestId: data.requestId
        });
    };
    window.addEventListener("message", (event) => {
        const action = ["downloadTransferAddMedia"];
        if (!event.data || !event.data.action || event.origin !== window.location.origin || !action.includes(event.data.action)) { return; }
        event.stopPropagation();
        event.stopImmediatePropagation();

        if (event.data.action == "downloadTransferAddMedia") {
            if (!event.data.url) { return; }

            sendAddMedia(event.data);
        }

    }, { capture: true });
})();
