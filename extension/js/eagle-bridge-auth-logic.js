(function (root, factory) {
    const api = factory();
    if (typeof module === "object" && module.exports) module.exports = api;
    root.EagleBridgeAuthLogic = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
    "use strict";

    /**
     * 连接可达性（取代旧版的配对令牌状态机）。
     *
     * 07 §7 `AD-1`：删除配对码与令牌逻辑，桌面端按 Origin 放行；
     * 07 §7 `AD-2`：找不到桌面端时显示启动引导，不再有"恢复令牌"概念。
     *
     * 因此本模块只回答一个问题：**桌面端此刻是否可达**。
     * 旧版 `unauthorizedAction()`（401 与新令牌的三态处置）随
     * `test_auth_race.js` 一并重写为"连接可达性竞态"。
     */

    const CONNECTION_STATES = Object.freeze({
        CHECKING: "checking",
        CONNECTED: "connected",
        OFFLINE: "offline"
    });

    // 04 §2.3.2 `GET /health`：`service` 固定 `"liudi-desktop"`，扩展据此确认
    // 对端是本软件，而不是占用同一端口的其他进程（07 AD-2）。
    const HEALTH_SERVICE_ID = "liudi-desktop";
    // 04 §2.3.2：`apiProtocol` 当前为 1。不兼容变更（增删端点、改字段语义、
    // 改取值域）必须 +1；不匹配时**提示**版本不匹配并禁用依赖新协议的动作，
    // **不得静默**。
    const EXPECTED_API_PROTOCOL = 1;

    const KNOWN_STATES = new Set(Object.values(CONNECTION_STATES));

    function normalizeState(value) {
        const state = String(value || "").trim().toLowerCase();
        return KNOWN_STATES.has(state) ? state : CONNECTION_STATES.CHECKING;
    }

    /**
     * 可达性迁移表。**纯函数**：同输入必得同输出，便于回归断言。
     *
     * @param {string} current       当前连接状态
     * @param {object} probe         一次探测的结果
     * @param {boolean} probe.reachable   请求是否成功往返
     * @param {boolean} probe.identified  响应是否确认为留底桌面端（service 字段匹配）
     * @param {string}  [probe.reason]    失败原因，仅用于诊断展示
     */
    function resolveConnection(current, probe = {}) {
        const previous = normalizeState(current);
        const reachable = Boolean(probe.reachable);
        const identified = Boolean(probe.identified);
        const reason = String(probe.reason || "");
        if (reachable && identified) {
            return {
                state: CONNECTION_STATES.CONNECTED,
                changed: previous !== CONNECTION_STATES.CONNECTED,
                reason: ""
            };
        }
        if (reachable) {
            // 端口上确实有 HTTP 服务，但不是留底桌面端：不能调用它，也不能
            // 谎报已连接——按"未找到桌面端"处理并给出启动引导（AD-2）。
            return {
                state: CONNECTION_STATES.OFFLINE,
                changed: previous !== CONNECTION_STATES.OFFLINE,
                reason: reason || "service_mismatch"
            };
        }
        return {
            state: CONNECTION_STATES.OFFLINE,
            changed: previous !== CONNECTION_STATES.OFFLINE,
            reason: reason || "unreachable"
        };
    }

    function isDesktopHealth(payload = {}) {
        return payload?.ok !== false && String(payload?.service || "") === HEALTH_SERVICE_ID;
    }

    /**
     * 能力快照（07 §7 `AD-4`：健康接口返回的字段）。
     * 字段与类型严格按 04 §2.3.2 的 `Health`（**该表就是全集**）；
     * 未知字段一律丢弃，避免把桌面端内部结构带进弹窗状态。
     */
    function capabilitySnapshot(payload = {}) {
        const source = payload && typeof payload === "object" ? payload : {};
        const apiProtocol = Number(source.apiProtocol);
        return {
            service: String(source.service || ""),
            version: String(source.version || "").slice(0, 40),
            apiProtocol: Number.isFinite(apiProtocol) ? Math.floor(apiProtocol) : null,
            eagleAvailable: typeof source.eagleAvailable === "boolean" ? source.eagleAvailable : null,
            mediaToolsReady: typeof source.mediaToolsReady === "boolean" ? source.mediaToolsReady : null
        };
    }

    /** 协议版本是否与本扩展期望的一致（不匹配时界面必须明示，不得静默）。 */
    function apiProtocolMatches(capabilities) {
        const protocol = Number(capabilities?.apiProtocol);
        return Number.isFinite(protocol) && protocol === EXPECTED_API_PROTOCOL;
    }

    /**
     * 桌面端地址（04 §2.1：仅 `127.0.0.1`）。
     * 端口发现的候选清单见 `eagle-bridge.js`——浏览器扩展读不到任意本地
     * 文件，只能按 `file://` 尝试，失败即视为该候选不可用。
     */
    function apiBase(port) {
        const normalized = Math.floor(Number(port));
        if (!Number.isInteger(normalized) || normalized < 1024 || normalized > 65535) return "";
        return `http://127.0.0.1:${normalized}`;
    }

    /**
     * 串行化状态写入（沿用旧版 `createStateUpdateQueue`）。
     *
     * 竞态来源没有消失，只是主体从"令牌"变成"可达性"：并发的健康探测与端口
     * 发现可能同时落盘，后返回的旧结果不得覆盖更新的结论。因此写入必须串行，
     * 且条件更新必须在**串行体内**重新判定当前值。
     */
    function createStateUpdateQueue(readState, writeState) {
        if (typeof readState !== "function" || typeof writeState !== "function") {
            throw new TypeError("State update queue requires read and write functions");
        }
        let tail = Promise.resolve();
        return function updateState(changes) {
            const operation = tail.then(async () => {
                const current = await readState();
                const patch = typeof changes === "function" ? await changes(current) : changes;
                const next = { ...current, ...(patch && typeof patch === "object" ? patch : {}) };
                await writeState(next);
                return next;
            });
            tail = operation.then(() => undefined, () => undefined);
            return operation;
        };
    }

    /**
     * 只接受最新一次探测的结论（沿用旧版同名工具）。
     * 这是"连接可达性竞态"的正面防护：慢的旧探测返回时已被作废。
     */
    function createLatestRequestGate() {
        let generation = 0;
        return {
            begin() {
                generation += 1;
                return generation;
            },
            isCurrent(ticket) {
                return ticket === generation;
            },
            invalidate() {
                generation += 1;
            }
        };
    }

    function withAbortTimeout(options = {}, timeoutMs) {
        const controller = new AbortController();
        const upstreamSignal = options?.signal;
        const abortFromUpstream = () => controller.abort();
        if (upstreamSignal?.aborted) controller.abort();
        else upstreamSignal?.addEventListener?.("abort", abortFromUpstream, { once: true });
        const timer = setTimeout(() => controller.abort(), Math.max(1, Number(timeoutMs) || 8000));
        return {
            signal: controller.signal,
            dispose() {
                clearTimeout(timer);
                upstreamSignal?.removeEventListener?.("abort", abortFromUpstream);
            }
        };
    }

    async function fetchWithTimeout(fetchImpl, input, options = {}, timeoutMs = 8000) {
        if (typeof fetchImpl !== "function") throw new TypeError("fetchWithTimeout requires fetch");
        const gate = withAbortTimeout(options, timeoutMs);
        try {
            return await fetchImpl(input, { ...options, signal: gate.signal });
        } finally {
            gate.dispose();
        }
    }

    async function fetchJsonWithTimeout(fetchImpl, input, options = {}, timeoutMs = 8000) {
        if (typeof fetchImpl !== "function") throw new TypeError("fetchJsonWithTimeout requires fetch");
        const gate = withAbortTimeout(options, timeoutMs);
        try {
            const response = await fetchImpl(input, { ...options, signal: gate.signal });
            let result = null;
            let jsonError = null;
            try {
                result = await response.json();
            } catch (error) {
                if (gate.signal.aborted) throw error;
                jsonError = error;
            }
            return { response, result, jsonError };
        } finally {
            gate.dispose();
        }
    }

    /**
     * 04 §2.4 的错误响应格式归一化：
     *   { "ok": false, "error": { "code": "...", "message": "..." } }
     * `message` 可直接展示，不含路径、堆栈或秘密。
     */
    function normalizeError(result, response, fallbackMessage = "留底桌面端调用失败") {
        const source = result?.error;
        const structured = source && typeof source === "object";
        const code = structured ? String(source.code || "") : String(source || "");
        const message = structured ? String(source.message || "") : "";
        return {
            ok: false,
            code: code.slice(0, 80) || `http_${Number(response?.status) || 0}`,
            message: (message || fallbackMessage).slice(0, 500)
        };
    }

    return {
        CONNECTION_STATES,
        HEALTH_SERVICE_ID,
        EXPECTED_API_PROTOCOL,
        normalizeState,
        resolveConnection,
        isDesktopHealth,
        capabilitySnapshot,
        apiProtocolMatches,
        apiBase,
        createStateUpdateQueue,
        createLatestRequestGate,
        fetchWithTimeout,
        fetchJsonWithTimeout,
        normalizeError
    };
});
