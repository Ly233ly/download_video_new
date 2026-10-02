/*
 * 留底浏览器扩展 · 弹窗界面（重写版）。
 *
 * 结构与呈现按 docs/09-UI.md §4.8：
 *   标题栏（图标 + 名称 + 连接状态）· **标签页** · 内容区（滚动）· 底部操作栏
 *   标签页 = 候选 / 任务 / 设置，数字角标表示该类的活动条目数。
 *   设置页按标准设置项写（名称 + 一行说明 + 右侧控件，分组 下载 / 连接 / 诊断）。
 *
 * 渲染纪律按 docs/07-EXTENSION.md §6.2 的 `P-101` ~ `P-110`：
 *   - 列表容器与其祖先**绝不**赋 `innerHTML`（P-102）；
 *   - 行节点以稳定 ID 复用，只有集合真变才增删（P-101/P-103）；
 *   - 事件只委托到列表容器（P-104）；
 *   - 超过 50 行走虚拟滚动，固定行高 56px / 可见 12 行 / 上 3 下 5 缓冲（P-105）；
 *   - 同帧多次更新合并（P-106）；
 *   - 缩略图用图片 URL、只对可视行赋 `src`、并发上限 4（P-107）；
 *   - 任务列表轮询不低于 2 秒、弹窗关闭立即停止（P-108/P-109）。
 */

(function () {
    "use strict";

    const root = document.getElementById("eagleBridgeRoot");
    const logic = globalThis.EagleBridgeUILogic;
    const virtualList = globalThis.EagleBridgeVirtualList;
    if (!root || !logic || !virtualList) return;

    const POPUP_REQUEST_TIMEOUT_MS = 12000;
    const TASK_POLL_MIN_MS = 2000;          // P-108 的下限
    const TASK_POLL_ACTIVE_MS = 2500;       // 有进行中任务
    const TASK_POLL_IDLE_MS = 6000;         // 全部终态
    const CANDIDATE_COALESCE_MS = 120;      // 候选突发的数据侧合并
    const TABS = ["candidates", "tasks", "settings"];
    const FALLBACK_ICON = "icons/icon-128.png";
    // 04 §2.3.2：`Health.apiProtocol` 当前为 1。不匹配时提示并禁用依赖新协议的动作。
    const EXPECTED_API_PROTOCOL = 1;

    const zhHans = {
        product: "留底下载器", candidates: "候选", tasks: "任务", settings: "设置",
        checking: "正在连接", connected: "已连接", offline: "软件未启动",
        desktopNotFoundBody: "未检测到留底桌面端。请先启动软件，然后点下面的按钮重新连接。",
        connect: "连接留底桌面端", connecting: "正在连接…", connectionDone: "已连接留底桌面端",
        connectFailed: "仍未能连接。请确认已安装并启动最新版留底桌面端。",
        noMedia: "暂未发现媒体",
        noMediaBody: "播放视频后再打开此窗口。",
        captured: "已捕获 {count} 个内容",
        hiddenSegments: "已隐藏 {count} 个技术资源",
        refresh: "刷新", clearMedia: "清空当前页媒体", clearConfirm: "清空当前页面捕获到的全部媒体？",
        clearMediaDone: "已清空当前页媒体",
        batch: "批量", exitBatch: "退出批量", selectAll: "全选", invert: "反选",
        batchSelected: "已选择 {count} 个内容",
        notGrouped: "未归组资源", technicalResource: "技术",
        segmentOnlyTitle: "无法确认归属的播放资源",
        selectVersion: "视频质量", uploadVersion: "上传版本", recommendedQuality: "推荐",
        currentQuality: "当前播放 {quality}",
        audio: "音频", noAudio: "不选择音频", subtitles: "字幕（将单独下载）",
        filename: "文件名", technicalInfo: "技术信息", copyLink: "复制链接", copied: "链接已复制",
        batchImport: "批量导入 Eagle", batchDownload: "批量仅下载",
        downloadToEagle: "下载到 Eagle", downloadToComputer: "下载到电脑",
        desktopUnavailable: "桌面端未连接", desktopUnavailableHint: "请先启动留底桌面端；连接恢复后即可继续下载。",
        protocolMismatch: "桌面端 API 协议为 v{actual}，本扩展期望 v{expected}；请更新到同一版本后再下载。",
        eagleUnavailable: "Eagle 未连接",
        eagleOptionalHint: "Eagle 未安装或未启动，不影响下载；文件会保留在电脑中，启动 Eagle 后可从任务列表补导。",
        localDownloadInfo: "所选内容将在本机下载、合并并保留，不需要 Eagle。",
        mergeInfo: "视频与音频将在本机无损合并；本机下载文件会保留。",
        manifestInfo: "HLS/DASH 清单将在本机下载、合并、校验；本机下载文件会保留。",
        directInfo: "所选文件将在本机下载；本机下载文件会保留。",
        resolverInfo: "本机软件将从当前内容页面识别最佳可用媒体并完成下载、合并。",
        taskStarted: "下载已开始", taskStopped: "任务已停止", taskRemoved: "任务记录已清理",
        tasksCleared: "已清除 {count} 条任务记录", batchPartial: "已启动 {count} 个任务，另有任务失败。",
        noTasks: "还没有下载任务。",
        openFolder: "打开所在文件夹", folderOpened: "已打开下载文件夹", openSource: "打开来源网页",
        importExisting: "导入 Eagle", importQueued: "已加入 Eagle 导入队列；本机文件会保留",
        stop: "停止", retry: "重试", removeTask: "清理记录",
        removeTaskConfirm: "将停止并清理这条任务记录；已下载的本机文件和 Eagle 内容会保留。是否继续？",
        clearTasks: "清除完成", clearTasksConfirm: "只清除已完成、失败和已停止的任务记录；进行中的任务与下载文件都会保留。是否继续？",
        refreshTasks: "刷新",
        processed: "已处理 {current} / {total}",
        outputLocation: "保存位置：{path}",
        activeTaskCount: "{active} 个进行中，共 {count} 个任务",
        taskCount: "共 {count} 个任务",
        syncInterrupted: "任务状态同步中断；本机下载仍可能继续，正在自动重连。",
        groupDownload: "下载任务", taskSubtitle: "浏览器与视频号任务统一显示在这里。",
        settingsDownload: "下载", settingsConnection: "连接", settingsDiagnostics: "诊断",
        browserDownloadMode: "浏览器下载模式",
        browserDownloadModeHint: "用浏览器自身的会话取字节再转给桌面端",
        browserDownloadModeInfo: "开启后，下载由浏览器凭自身会话取字节并中转给桌面端；关闭时由桌面端出站获取。部分站点对直链握手校验严格，只有浏览器会话能取到。该模式不适用于视频号。模式在创建任务时决定，运行中改开关不影响已创建的任务。",
        desktopAddress: "桌面端", desktopVersion: "版本", desktopVersionValue: "扩展 {ext} · 桌面端 {desktop}",
        diagnostics: "诊断信息", exportDiagnostics: "复制", diagnosticsExported: "诊断信息已复制",
        legal: "请只下载你有权保存的内容。",
        requestTimeout: "本机操作等待超时，请确认留底桌面端正在运行后重试。",
        connectionError: "无法连接本机留底桌面端",
        invalidOutputName: "请输入有效的 Windows 文件名",
        enhancedDiscovery: "增强发现",
        enhancedDiscoveryHint: "站点专用注入脚本不在本版本内提供，后续阶段随站点适配器启用。",
        batchTitle: "批量操作", batchBody: "每个内容会创建独立任务，不会把不同视频的音轨混在一起。",
        selectAllImport: "批量导入 Eagle", selectAllLocal: "批量仅下载", copySelected: "复制链接"
    };

    const strings = { ...zhHans };
    const t = (key, values = {}) => {
        let text = strings[key] || key;
        for (const [name, value] of Object.entries(values)) text = text.replaceAll(`{${name}}`, String(value));
        return text;
    };

    const state = {
        view: "candidates",
        tab: null,
        // `AD-1`/`AD-2`：没有配对与令牌，只有"桌面端是否可达"。
        connection: "checking",
        desktopAvailable: false,
        desktopVersion: "",
        port: 0,
        eagleAvailable: null,
        apiProtocol: null,
        browserDownloadMode: false,
        modeBusy: false,
        candidates: [],
        groups: [],
        activeGroupId: "",
        selections: new Map(),
        drafts: new Map(),
        hiddenSegmentCount: 0,
        plans: [],
        taskSyncError: "",
        taskActionBusyKey: "",
        batchMode: false,
        selectedGroupIds: new Set(),
        busy: false,
        disposed: false
    };

    const candidateList = { controller: null };
    const taskList = { controller: null };

    let toastTimer = null;
    let taskPollTimer = null;
    let candidateCoalesceTimer = null;
    let snapshotCoalesceTimer = null;

    const frames = { tabTicket: 0, connectionTicket: 0, candidateTicket: 0, plansTicket: 0 };

    // ---------------------------------------------------------------- 基础工具

    function el(tag, options = {}) {
        const node = document.createElement(tag);
        if (options.className) node.className = options.className;
        if (options.text !== undefined && options.text !== null) node.textContent = String(options.text);
        if (options.attrs) for (const [k, v] of Object.entries(options.attrs)) node.setAttribute(k, String(v));
        if (options.dataset) for (const [k, v] of Object.entries(options.dataset)) node.dataset[k] = String(v);
        if (options.children) for (const child of options.children) if (child) node.appendChild(child);
        return node;
    }

    function asset(path) {
        return chrome.runtime.getURL(path);
    }

    function contact(top, ...rest) {
        for (const child of rest) if (child) top.appendChild(child);
        return top;
    }

    function clearChildren(node) {
        while (node.firstChild) node.removeChild(node.firstChild);
        return node;
    }

    function showToast(message, kind = "info", timeout = 2400) {
        if (state.disposed) return;
        const node = root.querySelector("#bridgeToast");
        if (!node) return;
        clearTimeout(toastTimer);
        node.textContent = String(message || "");
        node.dataset.kind = kind;
        node.hidden = false;
        toastTimer = setTimeout(() => { node.hidden = true; }, timeout);
    }

    function send(payload) {
        return new Promise((resolve, reject) => {
            let settled = false;
            const finish = (callback, value) => {
                if (settled) return;
                settled = true;
                clearTimeout(timer);
                callback(value);
            };
            const timer = setTimeout(() => finish(reject, new Error(t("requestTimeout"))), POPUP_REQUEST_TIMEOUT_MS);
            try {
                chrome.runtime.sendMessage(payload, response => {
                    const runtimeError = chrome.runtime.lastError;
                    if (settled) return;
                    if (runtimeError) {
                        finish(reject, new Error(runtimeError.message));
                        return;
                    }
                    finish(resolve, response);
                });
            } catch (error) {
                finish(reject, error);
            }
        });
    }

    async function ask(payload) {
        const response = await send(payload);
        if (!response?.ok) throw new Error(response?.error || t("connectionError"));
        return response.data;
    }

    function currentDomain() {
        try { return new URL(state.tab?.url || "").hostname; } catch (_error) { return ""; }
    }

    function activeGroup() {
        return state.groups.find(group => group.id === state.activeGroupId) || null;
    }

    function taskViews() {
        return state.plans.map(plan => logic.taskView(plan));
    }

    /**
     * P-107：行的缩略图一律是**图片 URL**。
     * `data:`（旧版的 base64 帧缓存）在这里被主动丢弃——那正是要治的内存常驻。
     */
    function candidateThumbUrl(group) {
        const url = String(group?.thumbnailUrl || "");
        return /^https?:\/\//i.test(url) ? url : asset(FALLBACK_ICON);
    }

    // ---------------------------------------------------------------- 骨架构建

    function buildHeader() {
        const lockup = el("div", {
            className: "bridge-brand-lockup",
            children: [
                el("img", { className: "bridge-brand-icon", attrs: { src: asset("icons/icon-32.png"), alt: "" } }),
                el("h1", { className: "bridge-brand", text: t("product") })
            ]
        });
        const context = el("div", {
            className: "bridge-page-context",
            children: [
                el("strong", { className: "bridge-page-title", attrs: { id: "bridgePageTitle" } }),
                el("span", { className: "bridge-domain", attrs: { id: "bridgeDomain" } })
            ]
        });
        const connection = el("button", {
            className: "bridge-connection",
            attrs: { id: "bridgeConnection", type: "button", "data-state": "checking" },
            dataset: { action: "connect" },
            children: [
                el("span", { className: "bridge-connection-dot", attrs: { "aria-hidden": "true" } }),
                el("span", { attrs: { id: "bridgeConnectionLabel" }, text: t("checking") })
            ]
        });
        return el("header", { className: "bridge-header", children: [lockup, context, connection] });
    }

    function buildTabs() {
        const nav = el("nav", {
            className: "bridge-nav",
            attrs: { role: "tablist", "aria-label": t("product") }
        });
        const labels = { candidates: t("candidates"), tasks: t("tasks"), settings: t("settings") };
        for (const name of TABS) {
            const button = el("button", {
                className: "bridge-nav-button",
                attrs: {
                    type: "button",
                    role: "tab",
                    "data-view": name,
                    "aria-selected": String(name === state.view),
                    "aria-controls": `bridgePanel-${name}`
                }
            });
            button.appendChild(el("span", { text: labels[name] }));
            if (name !== "settings") {
                const badge = el("span", {
                    className: "bridge-tab-badge",
                    attrs: { id: `bridgeBadge-${name}` },
                    dataset: { badge: name }
                });
                badge.hidden = true;
                button.appendChild(badge);
            }
            nav.appendChild(button);
        }
        return nav;
    }

    function buildPanel(name) {
        return el("section", {
            className: "bridge-panel",
            attrs: { id: `bridgePanel-${name}`, role: "tabpanel" },
            dataset: { panel: name }
        });
    }

    function buildCandidatePanel() {
        const panel = buildPanel("candidates");
        const sidebarHeader = el("div", {
            className: "bridge-sidebar-header",
            children: [
                el("span", { className: "bridge-sidebar-title", attrs: { id: "bridgeSidebarTitle" } }),
                el("button", {
                    className: "bridge-filter-button",
                    attrs: { id: "bridgeBatchButton", type: "button", "aria-pressed": "false" },
                    dataset: { action: "batch" },
                    text: t("batch")
                }),
                el("button", {
                    className: "bridge-filter-button",
                    attrs: { type: "button" },
                    dataset: { action: "refresh" },
                    text: t("refresh")
                })
            ]
        });

        // 滚动视口与列表容器分离：视口负责滚动，容器只承载行节点。
        // 两者的祖先都不会被赋 `innerHTML`（P-102）。
        const list = el("div", {
            className: "bridge-group-list",
            attrs: { id: "bridgeGroupList", role: "listbox", "aria-label": t("candidates") }
        });
        const sidebar = el("aside", {
            className: "bridge-sidebar",
            children: [sidebarHeader, el("div", { className: "bridge-list-viewport", children: [list] })]
        });
        const inspector = el("section", {
            className: "bridge-inspector",
            attrs: { id: "bridgeInspector", "aria-label": t("selectVersion") }
        });
        const batchBar = el("div", {
            className: "bridge-batch-bar",
            attrs: { id: "bridgeBatchBar" },
            dataset: { batchBar: "1" }
        });
        batchBar.hidden = true;

        const footer = el("footer", {
            className: "bridge-footer",
            attrs: { id: "bridgeCandidateFooter" },
            dataset: { footer: "candidates" }
        });
        contact(panel,
            el("div", { className: "bridge-media-layout", children: [sidebar, inspector] }),
            batchBar,
            footer);
        return panel;
    }

    function buildTaskPanel() {
        const panel = buildPanel("tasks");
        const header = el("div", {
            className: "bridge-section-header",
            children: [
                el("div", {
                    children: [
                        el("h2", { text: t("groupDownload") }),
                        el("p", { text: t("taskSubtitle") })
                    ]
                }),
                el("div", { className: "bridge-section-actions", attrs: { id: "bridgeTaskActions" } })
            ]
        });
        const warning = el("div", {
            className: "bridge-sync-warning",
            attrs: { id: "bridgeSyncWarning", role: "status" }
        });
        warning.hidden = true;
        const list = el("div", {
            className: "bridge-task-list",
            attrs: { id: "bridgeTaskList", role: "list", "aria-label": t("tasks") }
        });
        const body = el("div", {
            className: "bridge-section-view",
            children: [header, warning, el("div", { className: "bridge-list-viewport", children: [list] })]
        });
        const footer = el("footer", {
            className: "bridge-footer",
            attrs: { id: "bridgeTaskFooter" },
            dataset: { footer: "tasks" }
        });
        contact(panel, body, footer);
        return panel;
    }

    function buildSettingsPanel() {
        const panel = buildPanel("settings");
        // 设置页的呈现规范（09 §4.8）：一行一项，左侧「名称 + 一行说明」，
        // 右侧控件；详细解释收进 ⓘ；分组用 sect 小节标题（下载 / 连接 / 诊断）。
        contact(panel, el("div", { className: "bridge-settings", attrs: { id: "bridgeSettingsBody" } }));
        return panel;
    }

    function initShell() {
        const content = el("div", {
            className: "bridge-content",
            children: [buildCandidatePanel(), buildTaskPanel(), buildSettingsPanel()]
        });
        const toast = el("div", {
            className: "bridge-toast",
            attrs: { id: "bridgeToast", role: "status", "aria-live": "polite" }
        });
        toast.hidden = true;
        content.appendChild(toast);

        const shell = el("div", {
            className: "bridge-app",
            children: [buildHeader(), buildTabs(), content]
        });
        contact(clearChildren(root), shell);

        root.setAttribute("aria-busy", "false");
        buildLists();
        bindDelegatedEvents();
        switchView("candidates", { silent: true });
    }

    // ---------------------------------------------------------------- 虚拟列表

    function buildLists() {
        candidateList.controller = virtualList.create({
            listId: "candidates",
            container: root.querySelector("#bridgeGroupList"),
            viewport: root.querySelector(".bridge-sidebar .bridge-list-viewport"),
            placeholderUrl: asset(FALLBACK_ICON),
            getItemId: item => String(item?.id || ""),
            getImageUrl: index => candidateThumbUrl(state.groups[index]),
            createRowContent: row => buildCandidateRow(row),
            patchRow: patchCandidateRow
        });

        taskList.controller = virtualList.create({
            listId: "tasks",
            container: root.querySelector("#bridgeTaskList"),
            viewport: root.querySelector("#bridgeTaskList").parentElement,
            placeholderUrl: asset(FALLBACK_ICON),
            getItemId: item => String(item?.id || ""),
            getImageUrl: index => {
                const task = taskViews()[index];
                return /^https?:\/\//i.test(task?.thumbnailUrl || "") ? task.thumbnailUrl : asset(FALLBACK_ICON);
            },
            createRowContent: row => buildTaskRow(row),
            patchRow: patchTaskRow
        });
    }

    function buildCandidateRow(row) {
        // 行内节点数受 `PF-A7`（200 条候选下 DOM 节点总数 ≤ **60**）硬约束。
        // 20 行窗口下每行只摊到 2 个节点，所以行本身承担缩略图与状态类，
        // 唯一的子节点承载全部文本：
        //   `<div class="bridge-row">`  →  `background-image` 缩略图（P-107）
        //     └── `<span class="bridge-row-text">` → 一行候选信息（B-202 必显信息）
        // 这与 `css/eagle-bridge.css` 末尾的 `.bridge-row*` 规则配套。
        const text = el("span", { className: "bridge-row-text" });
        // 缩略图载体就是行本身：`patchImage` 找 `[data-row-image]`。
        row.dataset.rowImage = "1";
        row.appendChild(text);
        row.dataset.groupId = "";
    }

    /** P-101：集合未变时只走这里——只改该行的文本与属性，不碰结构。 */
    function patchCandidateRow(row, group) {
        if (!group) return;
        const selection = state.selections.get(group.id);
        const selected = state.batchMode
            ? state.selectedGroupIds.has(group.id)
            : group.id === state.activeGroupId;
        const technical = Boolean(group.segmentOnly || group.technicalOnly);

        const duration = logic.formatDuration(group.duration);
        const source = [group.sourceDomain || t("candidates"), duration].filter(Boolean).join(" · ");
        const text = row.querySelector(".bridge-row-text");
        if (text) {
            // B-202：标题、类型/清晰度摘要、来源与条目数、时长都在这一行里。
            text.textContent = [
                group.title || t("notGrouped"),
                logic.groupSummary(group, selection),
                `${source} · ${group.items?.length || 0}`
            ].filter(Boolean).join(" · ");
        }

        row.setAttribute("aria-selected", String(selected));
        row.setAttribute("aria-current", String(group.id === state.activeGroupId));
        row.setAttribute("aria-label", `${group.title || t("notGrouped")} · ${logic.groupSummary(group, selection)}`);
        row.classList.toggle("bridge-row-selected", selected);
        row.classList.toggle("bridge-segment-only", technical);
        row.classList.toggle("bridge-row-technical", technical);
        row.dataset.groupId = group.id;
        row.tabIndex = 0;
    }

    function buildTaskRow(row) {
        const thumb = el("span", { className: "bridge-task-thumb", dataset: { rowImage: "1" } });
        const copy = el("div", {
            className: "bridge-task-copy",
            children: [
                el("div", { className: "bridge-task-name" }),
                el("div", {
                    className: "bridge-task-state",
                    children: [
                        el("span"),
                        el("span", { className: "bridge-progress-track", children: [el("span", { className: "bridge-progress-value" })] }),
                        el("span")
                    ]
                }),
                el("div", { className: "bridge-task-meta" }),
                el("div", { className: "bridge-task-detail" }),
                el("div", { className: "bridge-task-path" }),
                el("div", { className: "bridge-task-error" })
            ]
        });
        const actions = el("div", { className: "bridge-task-actions" });
        contact(row, thumb, copy, actions);
    }

    function patchTaskRow(row, task) {
        if (!task) return;
        const busy = state.taskActionBusyKey === task.id || state.taskActionBusyKey === "__all__";
        const desktopAvailable = state.desktopAvailable;
        row.dataset.taskId = task.id;
        row.setAttribute("aria-busy", String(busy));

        const name = row.querySelector(".bridge-task-name");
        if (name) {
            name.textContent = task.title;
            name.title = task.title;
        }
        const stateSpans = row.querySelectorAll(".bridge-task-state > span");
        if (stateSpans[0]) stateSpans[0].textContent = task.statusLabel;
        const progressValue = row.querySelector(".bridge-progress-value");
        if (progressValue) progressValue.style.width = `${task.progress}%`;
        if (stateSpans[2]) stateSpans[2].textContent = `${Math.round(task.progress)}%`;
        if (stateSpans[1]) stateSpans[1].setAttribute("aria-label", `${Math.round(task.progress)}%`);
        const meta = row.querySelector(".bridge-task-meta");
        if (meta) meta.textContent = t("processed", { current: task.processed, total: task.total });
        const detail = row.querySelector(".bridge-task-detail");
        if (detail) {
            detail.textContent = task.detail || "";
            detail.hidden = !task.detail;
        }
        const path = row.querySelector(".bridge-task-path");
        if (path) {
            path.textContent = task.finalPath ? t("outputLocation", { path: task.finalPath }) : "";
            path.hidden = !task.finalPath;
        }
        const error = row.querySelector(".bridge-task-error");
        if (error) {
            error.textContent = task.error || "";
            error.hidden = !task.error;
        }

        const actions = row.querySelector(".bridge-task-actions");
        if (!actions) return;
        const signature = [
            task.id, task.status, busy ? "1" : "0", desktopAvailable ? "1" : "0",
            task.canOpenOutput ? "1" : "0", task.canOpenSource ? "1" : "0",
            task.canImportExisting ? "1" : "0", task.canRetry ? "1" : "0",
            state.eagleAvailable === false ? "0" : "1"
        ].join("|");
        if (actions.dataset.signature === signature) return;
        actions.dataset.signature = signature;
        clearChildren(actions);

        if (task.canImportExisting) {
            if (state.eagleAvailable === false) {
                actions.appendChild(el("button", {
                    className: "bridge-small-button",
                    attrs: { type: "button", disabled: "disabled", title: t("eagleOptionalHint") },
                    text: t("eagleUnavailable")
                }));
            } else {
                actions.appendChild(taskActionButton(task.id, "import-task", t("importExisting"), busy));
            }
        }
        if (task.canOpenOutput) actions.appendChild(taskActionButton(task.id, "open-task-folder", t("openFolder"), busy || !desktopAvailable));
        if (task.canOpenSource) actions.appendChild(taskActionButton(task.id, "open-task-source", t("openSource"), busy));
        if (task.active) actions.appendChild(taskActionButton(task.id, "stop-task", t("stop"), busy || !desktopAvailable));
        else if (task.canRetry) actions.appendChild(taskActionButton(task.id, "retry-task", t("retry"), busy || !desktopAvailable));
        actions.appendChild(taskActionButton(task.id, "remove-task", t("removeTask"), busy || !desktopAvailable, "bridge-danger-button"));
    }

    function taskActionButton(planId, action, label, disabled, extraClass = "") {
        const attrs = { type: "button", "data-action": action, "data-plan-id": planId };
        if (disabled) attrs.disabled = "disabled";
        return el("button", { className: `bridge-small-button ${extraClass}`.trim(), attrs, text: label });
    }

    // ---------------------------------------------------------------- 候选渲染

    function rebuildGroups() {
        const previousId = state.activeGroupId;
        const previousLatestId = state.groups.at(-1)?.id || "";
        const listViewport = root.querySelector(".bridge-sidebar .bridge-list-viewport");
        const followLatest = (!previousId || previousId === previousLatestId) && logic.isNearScrollEnd(listViewport);
        const partition = logic.partitionGroups(logic.groupCandidates(state.candidates), { showSegments: false });
        state.groups = partition.visible;
        state.hiddenSegmentCount = partition.hiddenSegmentCount;
        for (const group of state.groups) {
            state.selections.set(group.id, logic.createDefaultSelection(group, state.selections.get(group.id)));
            if (!state.drafts.has(group.id)) {
                state.drafts.set(group.id, { outputName: logic.defaultOutputName(group, state.selections.get(group.id)) });
            }
        }
        state.selectedGroupIds = new Set([...state.selectedGroupIds].filter(id => state.groups.some(group => (
            group.id === id && !group.segmentOnly && !group.technicalOnly
        ))));
        state.activeGroupId = followLatest
            ? logic.defaultActiveGroupId(state.groups, "")
            : logic.defaultActiveGroupId(state.groups, previousId);

        // P-101/P-103：由控制器判定"集合是否真的变了"；不变就只原位重绘。
        candidateList.controller?.setItems(state.groups);
        // B-206：滚动位置保持——只有跟随最新时才滚到底部。
        if (followLatest && state.groups.length) {
            requestAnimationFrame(() => {
                if (state.disposed || !listViewport) return;
                listViewport.scrollTop = listViewport.scrollHeight;
            });
        }
        patchSidebarTitle();
        renderBatchBar();
        renderInspector();
        renderCandidateFooter();
        patchBadges();
    }

    function patchSidebarTitle() {
        const title = root.querySelector("#bridgeSidebarTitle");
        if (!title) return;
        let text = t("captured", { count: state.groups.length });
        if (state.hiddenSegmentCount) text += ` · ${t("hiddenSegments", { count: state.hiddenSegmentCount })}`;
        title.textContent = text;
        const batchButton = root.querySelector("#bridgeBatchButton");
        if (batchButton) {
            batchButton.textContent = state.batchMode ? t("exitBatch") : t("batch");
            batchButton.setAttribute("aria-pressed", String(state.batchMode));
        }
    }

    function renderBatchBar() {
        const bar = root.querySelector("#bridgeBatchBar");
        if (!bar) return;
        bar.hidden = !state.batchMode;
        if (!state.batchMode) {
            clearChildren(bar);
            bar.dataset.signature = "";
            return;
        }
        const signature = `${state.selectedGroupIds.size}|${state.busy ? 1 : 0}`;
        if (bar.dataset.signature === signature) {
            const count = bar.querySelector("[data-batch-count]");
            if (count) count.textContent = t("batchSelected", { count: state.selectedGroupIds.size });
            return;
        }
        bar.dataset.signature = signature;
        clearChildren(bar);
        const count = state.selectedGroupIds.size;
        bar.appendChild(el("span", { className: "bridge-batch-count", dataset: { batchCount: "1" }, text: t("batchSelected", { count }) }));
        const disabled = !count || state.busy;
        for (const [action, label] of [
            ["select-all", t("selectAll")],
            ["invert", t("invert")],
            ["copy", t("copySelected")]
        ]) {
            bar.appendChild(el("button", {
                className: "bridge-small-button",
                attrs: { type: "button", "data-batch-action": action, ...(action === "copy" && !count ? { disabled: "disabled" } : {}) },
                text: label
            }));
        }
        bar.appendChild(el("button", {
            className: "bridge-primary-button",
            attrs: { type: "button", "data-batch-action": "select-all-download", ...(disabled ? { disabled: "disabled" } : {}) },
            text: t("selectAllImport")
        }));
        bar.appendChild(el("button", {
            className: "bridge-secondary-button",
            attrs: { type: "button", "data-batch-action": "select-all-local", ...(disabled ? { disabled: "disabled" } : {}) },
            text: t("selectAllLocal")
        }));
    }

    function renderCandidateFooter() {
        const footer = root.querySelector("#bridgeCandidateFooter");
        if (!footer) return;
        const delivery = logic.deliveryCapabilities(state);
        const group = activeGroup();
        const selection = state.selections.get(group?.id);
        // 04 §2.3.2：协议不匹配时禁用依赖新协议的动作，并明示原因，不得静默。
        const blocked = !delivery.canDownload || protocolMismatch();
        const validation = blocked
            ? { ok: false }
            : logic.validateSelection(group, selection, { desktopAvailable: true });
        const disabled = !validation.ok || state.busy;
        const signature = [
            group?.id || "", disabled ? "1" : "0", state.busy ? "1" : "0",
            delivery.preferLocal ? "local" : delivery.canImport ? "eagle" : "none",
            state.batchMode ? "1" : "0", protocolMismatch() ? "proto" : "ok"
        ].join("|");
        if (footer.dataset.signature === signature) return;
        footer.dataset.signature = signature;
        clearChildren(footer);
        if (state.batchMode) {
            footer.hidden = true;
            return;
        }
        footer.hidden = false;

        const primary = el("button", {
            className: "bridge-primary-button",
            attrs: {
                type: "button",
                "data-action": delivery.preferLocal ? "download-local" : "download-eagle",
                ...(disabled ? { disabled: "disabled" } : {})
            },
            text: delivery.preferLocal ? t("downloadToComputer") : t("downloadToEagle")
        });
        const secondary = el("button", {
            className: "bridge-secondary-button",
            attrs: { type: "button", "data-action": "download-local", ...(disabled || delivery.preferLocal ? { disabled: "disabled" } : {}) },
            text: t("downloadToComputer")
        });
        if (delivery.preferLocal) secondary.hidden = true;
        contact(footer, primary, secondary);

        const note = protocolMismatch() ? t("protocolMismatch", { expected: EXPECTED_API_PROTOCOL, actual: state.apiProtocol })
            : !delivery.canDownload ? t("desktopUnavailableHint")
                : delivery.preferLocal ? t("eagleOptionalHint") : "";
        if (note) footer.appendChild(el("p", { className: "bridge-legal-note", text: note }));
    }

    function renderInspector() {
        const inspector = root.querySelector("#bridgeInspector");
        if (!inspector) return;
        const group = activeGroup();
        if (!group) {
            clearChildren(inspector);
            inspector.appendChild(el("div", {
                className: "bridge-empty-state",
                children: [
                    el("h2", { text: t("noMedia") }),
                    el("p", { text: t("noMediaBody") })
                ]
            }));
            return;
        }
        const selection = state.selections.get(group.id);
        const draft = state.drafts.get(group.id) || { outputName: logic.defaultOutputName(group, selection) };
        const delivery = logic.deliveryCapabilities(state);
        const validation = delivery.canDownload
            ? logic.validateSelection(group, selection, { desktopAvailable: true })
            : { ok: false, message: t("desktopUnavailableHint") };
        const outputValidation = logic.normalizeOutputName(draft.outputName);
        const effective = validation.ok && !outputValidation.ok
            ? { ok: false, message: outputValidation.message || t("invalidOutputName") }
            : validation;

        // 检查器不是列表容器，也不在候选列表的祖先链上——它可以整体重绘。
        // 列表区域的唯一渲染纪律见 P-102。
        clearChildren(inspector);
        if (group.segmentOnly || group.technicalOnly) {
            inspector.appendChild(el("div", {
                className: "bridge-segment-inspector",
                attrs: { role: "status" },
                children: [
                    el("div", { className: "bridge-segment-glyph", text: t("technicalResource") }),
                    el("div", {
                        children: [
                            el("h2", { text: t("segmentOnlyTitle") }),
                            el("p", { text: effective.message || "" })
                        ]
                    })
                ]
            }));
            return;
        }

        const selected = logic.selectedCandidates(group, selection);
        const duration = logic.formatDuration(group.duration);
        const source = [
            group.sourceDomain,
            duration,
            group.playbackQuality ? t("currentQuality", { quality: group.playbackQuality }) : ""
        ].filter(Boolean).join(" · ");

        inspector.appendChild(el("figure", {
            className: "bridge-inspector-preview",
            children: [
                el("img", { className: "bridge-inspector-media", attrs: { src: candidateThumbUrl(group), alt: group.title } }),
                ...(duration ? [el("figcaption", { text: duration })] : [])
            ]
        }));
        inspector.appendChild(el("h2", { className: "bridge-inspector-title", text: group.title, attrs: { title: group.title } }));
        inspector.appendChild(el("div", {
            className: "bridge-inspector-meta",
            children: [
                el("span", { text: source || t("candidates") }),
                el("span", { text: "·" }),
                el("span", { text: logic.groupSummary(group, selection) })
            ]
        }));
        for (const field of buildSelectionFields(group, selection)) inspector.appendChild(field);

        const summaryText = !delivery.canDownload ? t("desktopUnavailableHint")
            : delivery.preferLocal ? t("localDownloadInfo")
                : selection.mode === "resolver" ? t("resolverInfo")
                    : selection.mode === "manifest" ? t("manifestInfo")
                        : selection.mode === "tracks" && selected.length > 1 ? t("mergeInfo") : t("directInfo");
        inspector.appendChild(el("div", { className: "bridge-action-summary", children: [el("span", { text: summaryText })] }));
        if (!effective.ok) {
            inspector.appendChild(el("div", {
                className: "bridge-field-error",
                attrs: { role: "alert" },
                text: effective.message || ""
            }));
        }

        inspector.appendChild(el("label", {
            className: "bridge-field",
            children: [
                el("span", { className: "bridge-field-label", text: t("filename") }),
                el("input", {
                    className: "bridge-field-input",
                    attrs: { maxlength: "160", value: draft.outputName },
                    dataset: { draft: "outputName" }
                })
            ]
        }));
        inspector.appendChild(buildTechnicalDetails(group, selected));
        inspector.appendChild(el("p", { className: "bridge-legal-note", text: t("legal") }));
    }

    function optionEl(value, selected, label) {
        const attrs = { value };
        if (selected) attrs.selected = "selected";
        return el("option", { attrs, text: label });
    }

    function buildSelectionFields(group, selection) {
        const fields = [];
        if (selection.mode === "tracks") {
            if (group.videos.length) {
                const select = el("select", { className: "bridge-field-select", dataset: { selection: "videoId" } });
                group.videos.forEach((item, index) => select.appendChild(optionEl(item.id, selection.videoId === item.id, logic.videoLabel(item, index === 0))));
                fields.push(el("label", {
                    className: "bridge-field",
                    children: [el("span", { className: "bridge-field-label", text: t("selectVersion") }), select]
                }));
            }
            if (group.audios.length) {
                const select = el("select", { className: "bridge-field-select", dataset: { selection: "audioId" } });
                select.appendChild(optionEl("", !selection.audioId, t("noAudio")));
                for (const item of group.audios) select.appendChild(optionEl(item.id, selection.audioId === item.id, logic.audioLabel(item)));
                fields.push(el("label", {
                    className: "bridge-field",
                    children: [el("span", { className: "bridge-field-label", text: t("audio") }), select]
                }));
            }
            return fields;
        }
        if (selection.mode === "manifest") {
            if (group.manifests.length > 1) {
                const select = el("select", { className: "bridge-field-select", dataset: { selection: "manifestId" } });
                for (const item of group.manifests) select.appendChild(optionEl(item.id, selection.manifestId === item.id, logic.manifestLabel(item)));
                fields.push(el("label", {
                    className: "bridge-field",
                    children: [el("span", { className: "bridge-field-label", text: t("uploadVersion") }), select]
                }));
            }
            return fields;
        }
        if (selection.mode === "resolver") {
            fields.push(el("div", {
                className: "bridge-field",
                children: [
                    el("span", { className: "bridge-field-label", text: t("selectVersion") }),
                    el("div", { className: "bridge-selection-summary", text: logic.directLabel(group.resolvers[0], true) })
                ]
            }));
            return fields;
        }
        const directItems = group.items.filter(item => !item.drm);
        if (!directItems.length) return fields;
        if (directItems.length === 1) {
            fields.push(el("div", {
                className: "bridge-field",
                children: [
                    el("span", { className: "bridge-field-label", text: t("selectVersion") }),
                    el("div", { className: "bridge-selection-summary", text: logic.directLabel(directItems[0], true) })
                ]
            }));
            return fields;
        }
        const select = el("select", { className: "bridge-field-select", dataset: { selection: "directId" } });
        directItems.forEach((item, index) => select.appendChild(optionEl(item.id, selection.directId === item.id, logic.directLabel(item, index === 0))));
        fields.push(el("label", {
            className: "bridge-field",
            children: [el("span", { className: "bridge-field-label", text: t("selectVersion") }), select]
        }));
        return fields;
    }

    function technicalUrl(value) {
        try {
            const url = new URL(String(value || ""));
            url.search = "";
            url.hash = "";
            return url.href;
        } catch (_error) {
            return "";
        }
    }

    function buildTechnicalDetails(group, selected) {
        const details = el("details", { className: "bridge-advanced" });
        details.appendChild(el("summary", { text: t("technicalInfo") }));
        const body = el("div", { className: "bridge-advanced-body" });
        const selection = state.selections.get(group.id);

        if (group.subtitles.length) {
            const list = el("div", {
                className: "bridge-subtitle-list",
                children: [el("span", { className: "bridge-field-label", text: t("subtitles") })]
            });
            for (const item of group.subtitles) {
                const checkbox = el("input", {
                    attrs: { type: "checkbox", ...((selection.subtitleIds || []).includes(item.id) ? { checked: "checked" } : {}) },
                    dataset: { subtitleId: item.id }
                });
                list.appendChild(el("label", {
                    className: "bridge-check-row",
                    children: [checkbox, el("span", { text: item.language || item.label || item.name || item.extension.toUpperCase() })]
                }));
            }
            body.appendChild(list);
        }

        const technical = el("div", { className: "bridge-technical-list" });
        for (const item of selected) {
            const summary = [item.kind?.toUpperCase(), item.extension?.toUpperCase(), item.codec, item.sourceDomain, technicalUrl(item.url)]
                .filter(Boolean).join(" · ");
            technical.appendChild(el("div", { className: "bridge-technical-row", text: summary }));
        }
        body.appendChild(technical);
        body.appendChild(el("div", {
            className: "bridge-technical-actions",
            children: [el("button", {
                className: "bridge-small-button",
                attrs: { type: "button", "data-candidate-action": "copy" },
                text: t("copyLink")
            })]
        }));
        details.appendChild(body);
        return details;
    }

    // ---------------------------------------------------------------- 任务渲染

    function renderTaskHeader() {
        const actions = root.querySelector("#bridgeTaskActions");
        if (!actions) return;
        const delivery = logic.deliveryCapabilities(state);
        const busy = Boolean(state.taskActionBusyKey);
        const signature = `${delivery.canDownload ? 1 : 0}|${busy ? 1 : 0}`;
        if (actions.dataset.signature === signature) return;
        actions.dataset.signature = signature;
        clearChildren(actions);
        const disabled = !delivery.canDownload || busy;
        actions.appendChild(el("button", {
            className: "bridge-small-button",
            attrs: { type: "button", "data-action": "refresh-tasks", ...(disabled ? { disabled: "disabled" } : {}) },
            text: t("refreshTasks")
        }));
        actions.appendChild(el("button", {
            className: "bridge-small-button bridge-danger-button",
            attrs: { type: "button", "data-action": "clear-tasks", ...(disabled ? { disabled: "disabled" } : {}) },
            text: t("clearTasks")
        }));
    }

    function renderTaskFooter() {
        const footer = root.querySelector("#bridgeTaskFooter");
        if (!footer) return;
        const disabled = !state.desktopAvailable || state.taskActionBusyKey === "__all__";
        const signature = disabled ? "1" : "0";
        if (footer.dataset.signature === signature) return;
        footer.dataset.signature = signature;
        clearChildren(footer);
        footer.appendChild(el("button", {
            className: "bridge-primary-button",
            attrs: { type: "button", "data-action": "open-download-folder", ...(disabled ? { disabled: "disabled" } : {}) },
            text: t("openFolder")
        }));
        footer.appendChild(el("button", {
            className: "bridge-secondary-button",
            attrs: { type: "button", "data-action": "refresh-tasks", ...(disabled ? { disabled: "disabled" } : {}) },
            text: t("refreshTasks")
        }));
    }

    function renderTasks() {
        const tasks = taskViews();
        taskList.controller?.setItems(tasks);
        const warning = root.querySelector("#bridgeSyncWarning");
        if (warning) {
            warning.textContent = state.taskSyncError || "";
            warning.hidden = !state.taskSyncError;
        }
        const list = root.querySelector("#bridgeTaskList");
        if (list) list.dataset.empty = tasks.length ? "false" : "true";
        if (list && !tasks.length) {
            // 空态不进入虚拟列表：只在容器里放一个提示节点，任务出现时移除。
            if (!list.querySelector(".bridge-task-empty")) {
                list.appendChild(el("div", { className: "bridge-task-empty bridge-empty-state", children: [el("h2", { text: t("noTasks") })] }));
            }
        } else {
            list?.querySelector(".bridge-task-empty")?.remove();
        }
        renderTaskHeader();
        renderTaskFooter();
    }

    // ---------------------------------------------------------------- 设置渲染

    function settingsRow(name, hint, control, infoText = "") {
        const nameLine = el("span", { className: "bridge-setting-name", text: name });
        if (infoText) {
            nameLine.appendChild(el("span", {
                className: "bridge-info",
                attrs: { tabindex: "0", role: "button", "aria-label": infoText },
                dataset: { tooltip: infoText },
                text: "ⓘ"
            }));
        }
        return el("div", {
            className: "bridge-setting-row",
            children: [
                el("span", {
                    className: "bridge-setting-copy",
                    children: [nameLine, el("span", { className: "bridge-setting-hint", text: hint })]
                }),
                control
            ]
        });
    }

    function renderSettings() {
        const body = root.querySelector("#bridgeSettingsBody");
        if (!body) return;
        clearChildren(body);

        // 分组 1：下载
        body.appendChild(el("h2", { className: "bridge-sect", text: t("settingsDownload") }));
        body.appendChild(settingsRow(
            t("browserDownloadMode"),
            t("browserDownloadModeHint"),
            el("span", {
                className: "bridge-switch",
                children: [el("input", {
                    attrs: {
                        type: "checkbox",
                        role: "switch",
                        "data-setting": "browserDownloadMode",
                        ...(state.browserDownloadMode ? { checked: "checked" } : {}),
                        ...(state.desktopAvailable && !state.modeBusy ? {} : { disabled: "disabled" })
                    }
                })]
            }),
            t("browserDownloadModeInfo")
        ));

        // 分组 2：连接
        body.appendChild(el("h2", { className: "bridge-sect", text: t("settingsConnection") }));
        body.appendChild(settingsRow(
            t("desktopAddress"),
            t("desktopVersionValue", {
                ext: chrome.runtime.getManifest().version,
                desktop: state.desktopVersion || "—"
            }),
            el("span", {
                className: "bridge-pill",
                dataset: { state: state.connection },
                text: state.connection === "connected" ? t("connected")
                    : state.connection === "checking" ? t("checking") : t("offline")
            })
        ));
        // 04 §2.3.2：协议不匹配必须明示，不得静默。
        if (protocolMismatch()) {
            body.appendChild(el("p", {
                className: "bridge-sync-warning",
                attrs: { role: "alert" },
                text: t("protocolMismatch", { expected: EXPECTED_API_PROTOCOL, actual: state.apiProtocol })
            }));
        }
        if (!state.desktopAvailable) {
            body.appendChild(el("div", {
                className: "bridge-connect-box",
                children: [
                    el("p", { text: t("desktopNotFoundBody") }),
                    el("button", {
                        className: "bridge-primary-button",
                        attrs: { type: "button", "data-action": "connect" },
                        text: t("connect")
                    })
                ]
            }));
        }

        // 分组 3：诊断
        body.appendChild(el("h2", { className: "bridge-sect", text: t("settingsDiagnostics") }));
        body.appendChild(settingsRow(
            t("enhancedDiscovery"),
            t("enhancedDiscoveryHint"),
            el("button", {
                className: "bridge-small-button",
                attrs: { type: "button", disabled: "disabled" },
                text: "—"
            })
        ));
        body.appendChild(settingsRow(
            t("diagnostics"),
            state.port ? `127.0.0.1:${state.port}` : t("desktopAddress"),
            el("button", {
                className: "bridge-small-button",
                attrs: { type: "button", "data-action": "copy-diagnostics" },
                text: t("exportDiagnostics")
            })
        ));
        body.appendChild(el("p", { className: "bridge-settings-legal", text: t("legal") }));
    }

    // ---------------------------------------------------------------- 请求

    async function refreshTab() {
        const ticket = ++frames.tabTicket;
        try {
            const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
            if (state.disposed || ticket !== frames.tabTicket) return false;
            state.tab = tab || null;
            return true;
        } catch (_error) {
            return false;
        }
    }

    function patchHeader() {
        const pageTitle = root.querySelector("#bridgePageTitle");
        const domain = root.querySelector("#bridgeDomain");
        if (pageTitle) {
            pageTitle.textContent = state.tab?.title || "";
            pageTitle.title = state.tab?.title || "";
        }
        if (domain) {
            domain.textContent = currentDomain() || "";
            domain.title = state.tab?.url || "";
        }
        const connection = root.querySelector("#bridgeConnection");
        if (connection) connection.dataset.state = state.connection;
        const label = root.querySelector("#bridgeConnectionLabel");
        if (label) {
            label.textContent = state.connection === "connected" ? t("connected")
                : state.connection === "checking" ? t("checking") : t("offline");
        }
        patchBadges();
    }

    function patchBadges() {
        const candidateBadge = root.querySelector("[data-badge='candidates']");
        if (candidateBadge) {
            const count = state.groups.length;
            candidateBadge.hidden = !count;
            candidateBadge.textContent = String(Math.min(count, 99));
        }
        const taskBadge = root.querySelector("[data-badge='tasks']");
        if (taskBadge) {
            const tasks = taskViews();
            const active = tasks.filter(task => task.active).length;
            taskBadge.hidden = !tasks.length;
            taskBadge.textContent = String(Math.min(active || tasks.length, 99));
            taskBadge.title = active
                ? t("activeTaskCount", { active, count: tasks.length })
                : t("taskCount", { count: tasks.length });
        }
    }

    /**
     * `AD-1` / `AD-2`：连接可达性探测。
     * 没有配对、没有令牌；找不到桌面端就显示启动引导。
     */
    async function refreshConnection() {
        const ticket = ++frames.connectionTicket;
        if (state.connection === "checking") patchHeader();
        let next;
        try {
            const data = await ask({ eagleBridge: "connect" });
            next = {
                connection: data?.connection === "connected" ? "connected" : "offline",
                desktopAvailable: data?.connection === "connected",
                version: String(data?.version || ""),
                port: Number(data?.port || 0),
                apiProtocol: Number.isFinite(Number(data?.apiProtocol)) ? Number(data.apiProtocol) : null,
                eagleAvailable: typeof data?.eagleAvailable === "boolean" ? data.eagleAvailable : null,
                browserDownloadMode: typeof data?.browserDownloadMode === "boolean" ? data.browserDownloadMode : null
            };
        } catch (_error) {
            next = { connection: "offline", desktopAvailable: false };
        }
        if (state.disposed || ticket !== frames.connectionTicket) return false;
        state.connection = next.connection;
        state.desktopAvailable = next.desktopAvailable;
        state.desktopVersion = next.version || "";
        state.port = next.port || 0;
        if (next.apiProtocol !== undefined) state.apiProtocol = next.apiProtocol;
        if (next.eagleAvailable !== undefined && next.eagleAvailable !== null) state.eagleAvailable = next.eagleAvailable;
        if (next.browserDownloadMode !== undefined && next.browserDownloadMode !== null) state.browserDownloadMode = next.browserDownloadMode;
        patchHeader();
        renderSettings();
        renderCandidateFooter();
        renderTaskHeader();
        renderTaskFooter();
        return state.desktopAvailable;
    }

    /**
     * 04 §2.3.2：`apiProtocol` 与本扩展期望值不等时必须**提示**并禁用依赖新协议
     * 的动作，**不得静默**。
     */
    function protocolMismatch() {
        if (!state.desktopAvailable) return false;
        if (state.apiProtocol === null) return false;
        return state.apiProtocol !== EXPECTED_API_PROTOCOL;
    }

    async function refreshCandidates() {
        const ticket = ++frames.candidateTicket;
        const requestedTabId = String(state.tab?.id || "");
        const data = await ask({ Message: "getAllData" }).catch(() => null);
        if (state.disposed || ticket !== frames.candidateTicket) return false;
        if (requestedTabId !== String(state.tab?.id || "")) return false;
        const cache = data && typeof data === "object" && !Array.isArray(data) ? data : {};
        const items = Array.isArray(cache[requestedTabId]) ? cache[requestedTabId].slice(-400) : [];
        state.candidates = items.map(item => ({ ...item, __scope: "current" }));
        rebuildGroups();
        return true;
    }

    async function refreshPlans() {
        const ticket = ++frames.plansTicket;
        if (!state.desktopAvailable) {
            renderTasks();
            return false;
        }
        try {
            // `GET /api/plans` 返回 `Paged<PlanView>`（04 §3.3.1）。
            const page = await ask({ eagleBridge: "plans" });
            if (state.disposed || ticket !== frames.plansTicket) return false;
            state.plans = Array.isArray(page?.items) ? page.items : [];
            state.taskSyncError = "";
            renderTasks();
            patchBadges();
            return true;
        } catch (error) {
            if (state.disposed || ticket !== frames.plansTicket) return false;
            state.taskSyncError = t("syncInterrupted");
            renderTasks();
            throw error;
        }
    }

    /**
     * `AD-7` / `P-108`：任务列表只在弹窗打开期间轮询，间隔不低于 2 秒。
     * `P-109`：弹窗关闭立即停止——`dispose()` 会清掉这个定时器。
     */
    function scheduleTaskPoll() {
        if (state.disposed) return;
        clearTimeout(taskPollTimer);
        const active = logic.hasActiveTasks(state.plans);
        const connectionStale = state.connection !== "connected";
        const delay = connectionStale || !active
            ? Math.max(TASK_POLL_MIN_MS, TASK_POLL_IDLE_MS)
            : TASK_POLL_ACTIVE_MS;
        taskPollTimer = setTimeout(async () => {
            if (state.disposed) return;
            if (connectionStale) await refreshConnection().catch(() => undefined);
            if (state.disposed) return;
            if (state.desktopAvailable) await refreshPlans().catch(() => undefined);
            if (state.disposed) return;
            scheduleTaskPoll();
        }, delay);
    }

    async function refreshAll() {
        root.setAttribute("aria-busy", "true");
        try {
            await refreshTab();
            patchHeader();
            await Promise.allSettled([
                refreshCandidates(),
                refreshConnection(),
                ask({ eagleBridge: "ensureDiscovery", tabId: state.tab?.id }).catch(() => undefined)
            ]);
            if (state.disposed) return;
            scheduleTaskPoll();
        } finally {
            if (!state.disposed) root.setAttribute("aria-busy", "false");
        }
    }

    // ---------------------------------------------------------------- 交互

    function switchView(view, options = {}) {
        if (!TABS.includes(view)) return;
        state.view = view;
        for (const button of root.querySelectorAll("[data-view][role='tab']")) {
            button.setAttribute("aria-selected", String(button.dataset.view === view));
        }
        for (const panel of root.querySelectorAll("[data-panel]")) {
            panel.hidden = panel.dataset.panel !== view;
        }
        if (view === "candidates" && !options.silent) {
            // B-206：切回候选页只原位重绘，不重置列表滚动位置。
            candidateList.controller?.update();
            renderInspector();
            renderCandidateFooter();
        }
        if (view === "tasks") renderTasks();
        if (view === "settings") renderSettings();
    }

    function rawItem(candidate, group, selection) {
        if (!candidate) return null;
        const raw = { ...(candidate.raw || {}) };
        delete raw.frameDataUrl;
        raw.downFileName ||= candidate.name || logic.defaultOutputName(group, selection);
        raw.parsing ||= candidate.kind === "hls" ? "m3u8" : candidate.kind === "dash" ? "mpd" : false;
        raw._size ||= candidate.size;
        if (candidate.kind === "hls" || candidate.kind === "dash") raw.preferredQuality = selection?.quality || "";
        if (candidate.kind === "resolver") {
            raw.resolver = candidate.resolver;
            raw.preferredQuality = selection?.quality || "";
        }
        return raw;
    }

    function selectedRawItemsForGroup(group) {
        const selection = state.selections.get(group?.id);
        return logic.selectedCandidates(group, selection)
            .map(candidate => rawItem(candidate, group, selection))
            .filter(Boolean);
    }

    async function createPlanForGroup(group, importToEagle) {
        const delivery = logic.deliveryCapabilities(state);
        if (!delivery.canDownload) throw new Error(t("desktopUnavailableHint"));
        if (importToEagle && !delivery.canImport) throw new Error(t("eagleOptionalHint"));
        const selection = state.selections.get(group?.id);
        const validation = logic.validateSelection(group, selection, { desktopAvailable: true });
        if (!validation.ok) throw new Error(validation.message);
        const outputName = logic.normalizeOutputName(
            state.drafts.get(group.id)?.outputName || logic.defaultOutputName(group, selection)
        );
        if (!outputName.ok) throw new Error(outputName.message || t("invalidOutputName"));
        state.drafts.set(group.id, { ...(state.drafts.get(group.id) || {}), outputName: outputName.value });
        const plan = await ask({
            eagleBridge: "createPlan",
            items: selectedRawItemsForGroup(group),
            options: {
                outputName: outputName.value,
                outputContainer: validation.outputContainer,
                importToEagle,
                // 弹窗承诺本机下载文件会保留；删除文件是桌面端单独的显式动作。
                deleteAfterImport: false,
                // 模式在**创建计划时**决定（04 §2.5）。
                browserDownloadMode: state.browserDownloadMode === true
            }
        });
        if (state.browserDownloadMode === true && plan?.id) enqueueBrowserDownload(plan, group);
        return plan;
    }

    /**
     * `AD-8`：浏览器下载模式的中转上传。
     * 上传循环交给 Service Worker（与弹窗解耦，B-222）；这里只发起。
     */
    function enqueueBrowserDownload(plan, group) {
        const items = logic.selectedCandidates(group, state.selections.get(group?.id));
        items.forEach((item, index) => {
            const track = ["video", "audio"].includes(item.role)
                ? item.role
                : items.length === 1 ? "main" : index === 0 ? "video" : "audio";
            send({
                eagleBridge: "runUpload",
                task: {
                    planId: plan.id,
                    track,
                    declaredBytes: Number(item.size || 0),
                    contentType: item.type || "application/octet-stream",
                    sourceUrl: item.url
                }
            }).catch(() => undefined);
        });
    }

    async function downloadForActiveGroup(importToEagle) {
        const group = activeGroup();
        if (!group || state.busy) return;
        if (!logic.deliveryCapabilities(state).canDownload) {
            showToast(t("desktopUnavailableHint"), "error", 4200);
            return;
        }
        const validation = logic.validateSelection(group, state.selections.get(group.id), { desktopAvailable: true });
        if (!validation.ok) {
            showToast(validation.message, "error");
            return;
        }
        state.busy = true;
        renderCandidateFooter();
        try {
            const plan = await createPlanForGroup(group, importToEagle);
            state.plans = [plan, ...state.plans.filter(item => item.id !== plan.id)];
            showToast(t("taskStarted"));
            switchView("tasks");
            scheduleTaskPoll();
        } catch (error) {
            showToast(error.message || error, "error", 4200);
        } finally {
            state.busy = false;
            if (!state.disposed) renderCandidateFooter();
        }
    }

    async function runTaskAction(planId, operation) {
        if (state.taskActionBusyKey) return false;
        state.taskActionBusyKey = String(planId || "");
        renderTasks();
        try {
            await operation();
            return true;
        } finally {
            state.taskActionBusyKey = "";
            if (!state.disposed) renderTasks();
        }
    }

    async function stopTask(planId) {
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        return runTaskAction(planId, async () => {
            try {
                await ask({ eagleBridge: "stopPlan", planId });
                showToast(t("taskStopped"));
                await refreshPlans();
            } catch (error) {
                showToast(error.message || error, "error");
            }
        });
    }

    async function retryTask(planId) {
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        return runTaskAction(planId, async () => {
            try {
                await ask({ eagleBridge: "retryPlan", planId });
                showToast(t("taskStarted"));
                await refreshPlans();
                scheduleTaskPoll();
            } catch (error) {
                showToast(error.message || error, "error", 4200);
            }
        });
    }

    async function removeTask(planId) {
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        if (!planId || !window.confirm(t("removeTaskConfirm"))) return;
        return runTaskAction(planId, async () => {
            try {
                await ask({ eagleBridge: "removePlan", planId });
                await refreshPlans();
                showToast(t("taskRemoved"));
            } catch (error) {
                showToast(error.message || error, "error", 4200);
            }
        });
    }

    async function openTaskFolder(planId) {
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        return runTaskAction(planId, async () => {
            try {
                await ask({ eagleBridge: "openPlanOutput", planId });
                showToast(t("folderOpened"));
            } catch (error) {
                showToast(error.message || error, "error", 4200);
            }
        });
    }

    async function openTaskSource(planId) {
        const task = taskViews().find(item => item.id === String(planId || ""));
        if (!task?.pageUrl) return;
        try {
            await chrome.tabs.create({ url: task.pageUrl });
        } catch (error) {
            showToast(error.message || error, "error", 4200);
        }
    }

    async function importExistingTask(planId) {
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        if (state.eagleAvailable === false) { showToast(t("eagleOptionalHint"), "error", 4200); return; }
        return runTaskAction(planId, async () => {
            try {
                await ask({ eagleBridge: "importPlan", planId });
                showToast(t("importQueued"));
                await refreshPlans();
                scheduleTaskPoll();
            } catch (error) {
                showToast(error.message || error, "error", 4200);
            }
        });
    }

    async function clearTasks() {
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        if (!window.confirm(t("clearTasksConfirm"))) return;
        return runTaskAction("__all__", async () => {
            try {
                const terminal = state.plans.filter(plan => logic.taskView(plan).terminal);
                await Promise.allSettled(terminal.map(plan => ask({ eagleBridge: "removePlan", planId: plan.id })));
                await refreshPlans();
                showToast(t("tasksCleared", { count: terminal.length }));
            } catch (error) {
                showToast(error.message || error, "error", 4200);
            }
        });
    }

    async function openDownloadFolder() {
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        const task = taskViews().find(item => item.canOpenOutput);
        if (!task) { showToast(t("noTasks")); return; }
        return openTaskFolder(task.id);
    }

    async function changeBrowserDownloadMode(enabled) {
        if (!state.desktopAvailable || state.modeBusy) {
            renderSettings();
            return;
        }
        state.modeBusy = true;
        renderSettings();
        try {
            state.browserDownloadMode = Boolean(await ask({ eagleBridge: "writeMode", enabled }));
        } catch (error) {
            showToast(error.message || error, "error", 4200);
        } finally {
            state.modeBusy = false;
            if (!state.disposed) renderSettings();
        }
    }

    async function connectDesktop() {
        const label = root.querySelector("#bridgeConnectionLabel");
        if (label) label.textContent = t("connecting");
        try {
            const connected = await refreshConnection();
            showToast(connected ? t("connectionDone") : t("connectFailed"), connected ? "info" : "error", 4200);
            if (connected) {
                await refreshPlans().catch(() => undefined);
                await refreshPlans().catch(() => undefined);
                scheduleTaskPoll();
            }
        } finally {
            if (!state.disposed) renderSettings();
        }
    }

    function setBatchSelection(mode) {
        const selectable = state.groups.filter(group => !group.segmentOnly && !group.technicalOnly);
        if (mode === "select-all") state.selectedGroupIds = new Set(selectable.map(group => group.id));
        else state.selectedGroupIds = new Set(selectable.filter(group => !state.selectedGroupIds.has(group.id)).map(group => group.id));
        candidateList.controller?.update();
        renderBatchBar();
        renderInspector();
    }

    async function bulkCreatePlans(importToEagle) {
        const groups = state.groups.filter(group => state.selectedGroupIds.has(group.id));
        if (!groups.length || state.busy) return;
        if (!state.desktopAvailable) { showToast(t("desktopUnavailableHint"), "error", 4200); return; }
        state.busy = true;
        renderBatchBar();
        renderCandidateFooter();
        const plans = [];
        let failures = 0;
        for (const group of groups) {
            try {
                plans.push(await createPlanForGroup(group, importToEagle));
            } catch (_error) {
                failures += 1;
            }
        }
        state.busy = false;
        if (plans.length) {
            const ids = new Set(plans.map(plan => plan.id));
            state.plans = [...plans, ...state.plans.filter(plan => !ids.has(plan.id))];
            switchView("tasks");
            scheduleTaskPoll();
        } else {
            renderBatchBar();
            renderCandidateFooter();
        }
        showToast(failures ? t("batchPartial", { count: plans.length }) : t("taskStarted"), failures ? "error" : "info", 4200);
    }

    function copySelectedLinks() {
        const groups = state.groups.filter(group => state.selectedGroupIds.has(group.id));
        const urls = groups.flatMap(group => selectedRawItemsForGroup(group).map(item => item.url)).filter(Boolean);
        if (!urls.length) return;
        navigator.clipboard.writeText([...new Set(urls)].join("\n"))
            .then(() => showToast(t("copied")))
            .catch(error => showToast(error.message, "error"));
    }

    function copyDiagnostics() {
        const lines = [
            `extension=${chrome.runtime.getManifest().version}`,
            `connection=${state.connection}`,
            `port=${state.port || "-"}`,
            `desktop=${state.desktopVersion || "-"}`,
            `eagleAvailable=${state.eagleAvailable === null ? "unknown" : state.eagleAvailable}`,
            `candidates=${state.groups.length}`,
            `hiddenSegments=${state.hiddenSegmentCount}`,
            `plans=${state.plans.length}`,
            `browserDownloadMode=${state.browserDownloadMode}`,
            `candidateRows=${candidateList.controller?.nodeCount || 0}`
        ];
        navigator.clipboard.writeText(lines.join("\n"))
            .then(() => showToast(t("diagnosticsExported")))
            .catch(error => showToast(error.message, "error"));
    }

    async function clearCurrentMedia() {
        if (!window.confirm(t("clearConfirm"))) return;
        frames.candidateTicket += 1;
        await send({ Message: "clearData", tabId: state.tab?.id, type: true }).catch(() => undefined);
        await send({ Message: "ClearIcon", type: true, tabId: state.tab?.id }).catch(() => undefined);
        state.candidates = [];
        state.selections.clear();
        state.drafts.clear();
        state.selectedGroupIds.clear();
        state.activeGroupId = "";
        rebuildGroups();
        showToast(t("clearMediaDone"));
    }

    // ---------------------------------------------------------------- 事件委托

    function groupIdFrom(target) {
        return target.closest("[data-group-id]")?.dataset.groupId || "";
    }

    function onCandidateClick(event) {
        const groupId = groupIdFrom(event.target);
        if (!groupId) return;
        const group = state.groups.find(item => item.id === groupId);
        if (!group) return;
        if (state.batchMode) {
            if (group.segmentOnly || group.technicalOnly) return;
            if (state.selectedGroupIds.has(groupId)) state.selectedGroupIds.delete(groupId);
            else state.selectedGroupIds.add(groupId);
            renderBatchBar();
        } else {
            state.activeGroupId = groupId;
        }
        // P-101：只原位更新受影响的行，不重建列表。
        candidateList.controller?.update();
        renderInspector();
        renderCandidateFooter();
    }

    function onCandidateKeydown(event) {
        if (!["Enter", " "].includes(event.key)) return;
        if (!groupIdFrom(event.target)) return;
        event.preventDefault();
        onCandidateClick(event);
    }

    function bindDelegatedEvents() {
        // P-104：候选列表的全部监听挂在**容器**上，行内不挂任何监听。
        const groupList = root.querySelector("#bridgeGroupList");
        groupList.addEventListener("click", onCandidateClick);
        groupList.addEventListener("keydown", onCandidateKeydown);

        // P-104：任务列表同理，动作按钮由容器委托处理。
        root.querySelector("#bridgeTaskList").addEventListener("click", event => {
            const button = event.target.closest("[data-action]");
            if (!button || button.disabled) return;
            const planId = button.dataset.planId || "";
            const action = button.dataset.action;
            if (action === "stop-task") stopTask(planId);
            else if (action === "retry-task") retryTask(planId);
            else if (action === "remove-task") removeTask(planId);
            else if (action === "import-task") importExistingTask(planId);
            else if (action === "open-task-folder") openTaskFolder(planId);
            else if (action === "open-task-source") openTaskSource(planId);
        });

        root.addEventListener("click", event => {
            if (event.target.closest("#bridgeGroupList") || event.target.closest("#bridgeTaskList")) return;
            const batchAction = event.target.closest("[data-batch-action]")?.dataset.batchAction;
            if (batchAction) {
                if (batchAction === "select-all" || batchAction === "invert") setBatchSelection(batchAction);
                else if (batchAction === "copy") copySelectedLinks();
                else if (batchAction === "select-all-download") bulkCreatePlans(true);
                else if (batchAction === "select-all-local") bulkCreatePlans(false);
                return;
            }
            const candidateAction = event.target.closest("[data-candidate-action]")?.dataset.candidateAction;
            if (candidateAction === "copy") {
                const group = activeGroup();
                const first = logic.selectedCandidates(group, state.selections.get(group?.id))[0];
                if (first?.url) {
                    navigator.clipboard.writeText(first.url)
                        .then(() => showToast(t("copied")))
                        .catch(error => showToast(error.message, "error"));
                }
                return;
            }
            const view = event.target.closest("[data-view]")?.dataset.view;
            if (view && !event.target.closest("[data-action]") && !event.target.closest("[data-batch-action]")) {
                switchView(view);
                return;
            }
            const action = event.target.closest("[data-action]")?.dataset.action;
            if (!action) return;
            if (action === "connect") connectDesktop();
            else if (action === "refresh") refreshAll();
            else if (action === "batch") {
                state.batchMode = !state.batchMode;
                if (!state.batchMode) state.selectedGroupIds.clear();
                candidateList.controller?.update();
                patchSidebarTitle();
                renderBatchBar();
                renderInspector();
                renderCandidateFooter();
            } else if (action === "download-eagle") downloadForActiveGroup(true);
            else if (action === "download-local") downloadForActiveGroup(false);
            else if (action === "refresh-tasks") { if (state.desktopAvailable) refreshPlans().catch(error => showToast(error.message, "error")); }
            else if (action === "clear-tasks") clearTasks();
            else if (action === "open-download-folder") openDownloadFolder();
            else if (action === "clear-media") clearCurrentMedia();
            else if (action === "copy-diagnostics") copyDiagnostics();
        });

        root.addEventListener("input", event => {
            if (!event.target.matches("[data-draft]")) return;
            const group = activeGroup();
            if (!group) return;
            const draft = state.drafts.get(group.id) || {};
            draft[event.target.dataset.draft] = event.target.value;
            draft.outputNameTouched = true;
            state.drafts.set(group.id, draft);
        });

        root.addEventListener("change", event => {
            if (event.target.matches("[data-setting='browserDownloadMode']")) {
                changeBrowserDownloadMode(event.target.checked);
                return;
            }
            const select = event.target.closest("[data-selection]");
            if (select) {
                const group = activeGroup();
                if (!group) return;
                const selection = state.selections.get(group.id);
                selection[select.dataset.selection] = select.value;
                state.selections.set(group.id, selection);
                const draft = state.drafts.get(group.id);
                if (draft && !draft.outputNameTouched) draft.outputName = logic.defaultOutputName(group, selection);
                // B-206：改质量不得回滚滚动位置——只原位更新行文本与检查器。
                candidateList.controller?.update();
                renderInspector();
                renderCandidateFooter();
                return;
            }
            const checkbox = event.target.closest("[data-subtitle-id]");
            if (checkbox) {
                const group = activeGroup();
                if (!group) return;
                const selection = state.selections.get(group.id);
                const ids = new Set(selection.subtitleIds || []);
                if (checkbox.checked) ids.add(checkbox.dataset.subtitleId);
                else ids.delete(checkbox.dataset.subtitleId);
                selection.subtitleIds = [...ids];
            }
        });

        // 滚动只触发窗口重算，不重建节点（P-105 + P-107 的"滚动停止后再补相邻行"）。
        candidateList.controller?.on(root.querySelector(".bridge-sidebar .bridge-list-viewport"), "scroll",
            () => candidateList.controller.update(), { passive: true });
        taskList.controller?.on(root.querySelector("#bridgeTaskList").parentElement, "scroll",
            () => taskList.controller.update(), { passive: true });

        document.addEventListener("keydown", event => {
            if (event.key !== "Escape") return;
            root.querySelector(".bridge-tooltip")?.remove();
        });

        // 工具提示（09 §4.7：详细说明用自定义 tooltip，不用原生 title）。
        root.addEventListener("mouseover", event => {
            const info = event.target.closest?.("[data-tooltip]");
            if (info) showTooltip(info);
        });
        root.addEventListener("mouseout", event => {
            if (event.target.closest?.("[data-tooltip]")) root.querySelector(".bridge-tooltip")?.remove();
        });
    }

    function showTooltip(anchor) {
        root.querySelector(".bridge-tooltip")?.remove();
        const bubble = el("div", { className: "bridge-tooltip", text: anchor.dataset.tooltip || "" });
        document.body.appendChild(bubble);
        const rect = anchor.getBoundingClientRect();
        bubble.style.position = "fixed";
        bubble.style.left = `${Math.max(4, Math.min(rect.left, window.innerWidth - 268))}px`;
        bubble.style.top = `${rect.bottom + 6}px`;
    }

    // ---------------------------------------------------------------- 消息与生命周期

    chrome.runtime.onMessage.addListener(message => {
        if (state.disposed) return;
        if (message?.Message === "tabLocationChanged") {
            if (Number(message.tabId) !== Number(state.tab?.id)) return;
            resetTabScopedUi();
            refreshAll().catch(() => undefined);
            return;
        }
        if (message?.Message !== "popupAddData") return;
        const data = message.data;
        if (!state.tab || Number(data?.tabId) !== Number(state.tab.id)) return;
        frames.candidateTicket += 1;
        const index = state.candidates.findIndex(item => String(item.requestId) === String(data.requestId));
        const item = { ...data, __scope: "current" };
        delete item.frameDataUrl;
        if (index >= 0) state.candidates[index] = item;
        else state.candidates.push(item);
        // 数据侧合并突发捕获；渲染仍在下一帧统一发生（P-106）。
        clearTimeout(candidateCoalesceTimer);
        candidateCoalesceTimer = setTimeout(() => {
            if (state.disposed) return;
            rebuildGroups();
        }, CANDIDATE_COALESCE_MS);
    });

    chrome.storage.onChanged.addListener(changes => {
        if (state.disposed || !changes.MediaData) return;
        clearTimeout(snapshotCoalesceTimer);
        snapshotCoalesceTimer = setTimeout(() => {
            if (state.disposed) return;
            refreshCandidates().catch(() => undefined);
        }, CANDIDATE_COALESCE_MS);
    });

    function resetTabScopedUi() {
        frames.candidateTicket += 1;
        frames.plansTicket += 1;
        state.candidates = [];
        state.selections.clear();
        state.drafts.clear();
        state.selectedGroupIds.clear();
        state.activeGroupId = "";
        state.batchMode = false;
    }

    /** P-109：关闭即停——不得有任何渲染或定时任务残留。 */
    function dispose() {
        state.disposed = true;
        clearTimeout(toastTimer);
        clearTimeout(taskPollTimer);
        clearTimeout(candidateCoalesceTimer);
        clearTimeout(snapshotCoalesceTimer);
        taskPollTimer = null;
        frames.tabTicket += 1;
        frames.connectionTicket += 1;
        frames.candidateTicket += 1;
        frames.plansTicket += 1;
        candidateList.controller?.dispose();
        taskList.controller?.dispose();
        window.__eagleBridgeDisposed = true;
    }

    window.addEventListener("pagehide", dispose);
    window.addEventListener("beforeunload", dispose);

    // 暴露最小只读测试面：性能门禁（T-EXT-15 / PF-A7）需要在真实 DOM 上断言
    // "状态更新后列表容器未被整体重建"、固定行高与窗口大小。不改变任何行为。
    window.__eagleBridgePopup = {
        createVirtualList: virtualList.create,
        constants: {
            rowHeight: virtualList.ROW_HEIGHT_PX,
            visibleRows: virtualList.VISIBLE_ROWS,
            overscanAbove: virtualList.OVERSCAN_ABOVE_ROWS,
            overscanBelow: virtualList.OVERSCAN_BELOW_ROWS,
            taskPollMinMs: TASK_POLL_MIN_MS
        },
        state: () => state,
        listContainer: () => root.querySelector("#bridgeGroupList"),
        render: () => {
            candidateList.controller?.update();
            renderInspector();
            renderCandidateFooter();
        },
        setGroups: groups => {
            state.groups = groups;
            candidateList.controller?.setItems(groups);
        },
        dispose
    };

    (async () => {
        initShell();
        await refreshAll();
    })().catch(error => {
        if (state.disposed) return;
        const inspector = root.querySelector("#bridgeInspector");
        if (!inspector) return;
        clearChildren(inspector);
        inspector.appendChild(el("div", {
            className: "bridge-empty-state",
            children: [
                el("h2", { text: t("connectionError") }),
                el("p", { text: String(error?.message || error) })
            ]
        }));
    });
})();
