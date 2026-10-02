/*
 * 连接可达性竞态（取代旧版的"配对令牌竞态"）。
 *
 * 07 §8 明确要求：`test_auth_race.js` 覆盖的认证竞态在简化后语义变化，
 * 需改写为「连接可达性竞态」。
 *
 * 旧版护的是「401 与新令牌的三态处置」——`AD-1` 删掉配对码与令牌后这套语义
 * 不复存在。但**竞态本身没有消失**，只是主体换成了"桌面端此刻是否可达"：
 *   1. 先发起、后返回的旧探测不得覆盖更新的结论；
 *   2. 并发的状态写入必须串行，条件更新要在串行体内重新判定当前值；
 *   3. 挂起的请求与挂起的响应体都必须确定性地超时中止。
 */

"use strict";

const assert = require("assert");
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const root = path.resolve(__dirname, "..", "..");
const auth = require(path.join(root, "extension", "js", "eagle-bridge-auth-logic.js"));

// ---------------------------------------------------------------- 可达性迁移表

assert.strictEqual(auth.resolveConnection("checking", { reachable: true, identified: true }).state, "connected",
    "a reachable, identified desktop must be treated as connected");
assert.strictEqual(auth.resolveConnection("connected", { reachable: true, identified: true }).changed, false,
    "re-confirming a live desktop must not report a state change");
assert.strictEqual(auth.resolveConnection("connected", { reachable: false }).state, "offline",
    "a lost desktop must flip the connection to offline");

const mismatch = auth.resolveConnection("connected", { reachable: true, identified: false });
assert.strictEqual(mismatch.state, "offline",
    "a port that answers but is not the Liudi desktop must never be called connected");
assert.strictEqual(mismatch.reason, "service_mismatch");

assert.strictEqual(auth.resolveConnection("nonsense", { reachable: true, identified: true }).state, "connected",
    "an unknown stored state must be normalized before the transition");
assert.strictEqual(auth.normalizeState("nonsense"), "checking");
assert.strictEqual(auth.normalizeState(""), "checking");
assert.strictEqual(auth.normalizeState("OFFLINE"), "offline");

assert.strictEqual(auth.HEALTH_SERVICE_ID, "liudi-desktop",
    "04 §2.3.2: /health.service is fixed to \"liudi-desktop\" (07 AD-2)");
assert.strictEqual(auth.isDesktopHealth({ ok: true, service: "liudi-desktop" }), true);
assert.strictEqual(auth.isDesktopHealth({ ok: true, service: "something-else" }), false,
    "another local service must not be mistaken for the desktop app");
assert.strictEqual(auth.isDesktopHealth({ ok: false, service: "liudi-desktop" }), false);
assert.strictEqual(auth.isDesktopHealth({ ok: true, service: "idm-eagle" }), false,
    "the legacy service id must no longer be accepted");

// 04 §2.3.2 的 `Health` 表就是能力字段全集。
const capabilities = auth.capabilitySnapshot({
    service: "liudi-desktop",
    version: "2.0.0",
    apiProtocol: 1,
    eagleAvailable: false,
    mediaToolsReady: true,
    extra: "drop-me"
});
assert.deepStrictEqual(capabilities, {
    service: "liudi-desktop",
    version: "2.0.0",
    apiProtocol: 1,
    eagleAvailable: false,
    mediaToolsReady: true
}, "only the documented Health fields may leave the health response");
assert.strictEqual(auth.capabilitySnapshot({ version: "2.0.0" }).eagleAvailable, null,
    "an absent capability must be null, not a guess");

// 04 §2.3.2：协议不匹配必须被识别出来（界面据此提示并禁用依赖新协议的动作）。
assert.strictEqual(auth.apiProtocolMatches({ apiProtocol: 1 }), true);
assert.strictEqual(auth.apiProtocolMatches({ apiProtocol: 2 }), false);
assert.strictEqual(auth.apiProtocolMatches({}), false, "a missing protocol must not be treated as compatible");

assert.strictEqual(auth.apiBase(47652), "http://127.0.0.1:47652", "04 §2.1 binds to 127.0.0.1 only");
assert.strictEqual(auth.apiBase(0), "");
assert.strictEqual(auth.apiBase("nope"), "");

// ---------------------------------------------------------------- 错误归一化

{
    const error = auth.normalizeError(
        { ok: false, error: { code: "plan_not_found", message: "任务不存在" } },
        { status: 404 }
    );
    assert.deepStrictEqual(error, { ok: false, code: "plan_not_found", message: "任务不存在" },
        "04 §2.4 error shape must survive normalization");
    const fallback = auth.normalizeError(null, { status: 500 });
    assert.strictEqual(fallback.code, "http_500");
    assert.ok(fallback.message.length > 0);
}

(async () => {
    // ------------------------------------------------------------ 超时中止
    let aborted = false;
    const neverReturns = (_url, options) => new Promise((_resolve, reject) => {
        options.signal.addEventListener("abort", () => {
            aborted = true;
            reject(new Error("aborted"));
        }, { once: true });
    });
    const timeoutStarted = Date.now();
    await auth.fetchWithTimeout(neverReturns, "http://127.0.0.1:47652/health", {}, 25)
        .then(() => { throw new Error("a hung desktop request must time out"); })
        .catch(error => {
            if (!/aborted/i.test(String(error?.message || error))) throw error;
        });
    assert.ok(aborted, "desktop fetch timeout must abort the request");
    assert.ok(Date.now() - timeoutStarted <= 500, "desktop fetch timeout must abort deterministically");

    const stalledBody = async (_url, options) => ({
        ok: true,
        status: 200,
        json() {
            return new Promise((_resolve, reject) => {
                options.signal.addEventListener("abort", () => reject(new Error("body aborted")), { once: true });
            });
        }
    });
    await auth.fetchJsonWithTimeout(stalledBody, "http://127.0.0.1:47652/api/plans", {}, 25)
        .then(() => { throw new Error("a stalled response body must time out"); })
        .catch(error => {
            if (!/body aborted/i.test(String(error?.message || error))) throw error;
        });

    // ---------------------------------------------------- 串行化的状态写入
    let stored = { connection: "checking", port: 47652, pendingEvents: [], lastPlanId: "plan" };
    const update = auth.createStateUpdateQueue(
        async () => {
            const snapshot = { ...stored, pendingEvents: [...stored.pendingEvents] };
            await Promise.resolve();
            return snapshot;
        },
        async next => {
            await Promise.resolve();
            stored = next;
        }
    );

    await Promise.all([
        update({ connection: "connected", port: 51234 }),
        update({ lastPlanId: "plan-2" })
    ]);
    assert.strictEqual(stored.connection, "connected",
        "a concurrent non-connection write must not overwrite a newly discovered connection");
    assert.strictEqual(stored.port, 51234, "the discovered port must survive concurrent writes");
    assert.strictEqual(stored.lastPlanId, "plan-2");

    // 条件更新必须在**串行体内**重新判定当前值，而不是在入队前判定。
    await update(current => current.port === 47652 ? { connection: "offline" } : {});
    assert.strictEqual(stored.connection, "connected",
        "a stale conditional write must re-check the current value inside the serialized body");

    // ---------------------------------------------------- 竞态本体：过期探测
    const gate = auth.createLatestRequestGate();
    let lastCommitted = "checking";
    const probe = async (label, delayMs) => {
        const ticket = gate.begin();
        await new Promise(resolve => setTimeout(resolve, delayMs));
        const decision = auth.resolveConnection(lastCommitted, { reachable: label === "connected", identified: label === "connected" });
        if (gate.isCurrent(ticket)) lastCommitted = decision.state;
        return { label, stale: !gate.isCurrent(ticket) };
    };
    const slowOldProbe = probe("offline", 40);
    const fastNewProbe = probe("connected", 5);
    const results = await Promise.all([slowOldProbe, fastNewProbe]);
    assert.strictEqual(results.find(result => result.label === "offline").stale, true,
        "a probe that started earlier but returned later must be discarded");
    assert.strictEqual(lastCommitted, "connected",
        "the newest probe result must win even when the older one resolves last");

    // ------------------------------------------- eagle-bridge 侧的丢弃行为
    const bridgeSource = fs.readFileSync(path.join(root, "extension", "js", "eagle-bridge.js"), "utf8");
    const stateStore = { downloadTransferStation: null };
    const alarms = [];
    const chromeStub = {
        storage: {
            session: {
                get: async () => ({ downloadTransferStation: stateStore.downloadTransferStation }),
                set: async values => { stateStore.downloadTransferStation = values.downloadTransferStation; }
            }
        },
        alarms: {
            create: (name, info) => alarms.push({ name, info }),
            onAlarm: { addListener() {} }
        },
        runtime: {
            getManifest: () => ({ version: "2.0.0" }),
            onStartup: { addListener() {} },
            onInstalled: { addListener() {} },
            onMessage: { addListener() {} },
            getPlatformInfo: () => undefined,
            reload() {}
        },
        tabs: { query: async () => [], get: async () => ({ id: 1, url: "https://example.com/" }) },
        scripting: {}
    };

    function bridgeSlice(startMarker, endMarker) {
        const start = bridgeSource.indexOf(startMarker);
        const end = bridgeSource.indexOf(endMarker);
        assert.ok(start >= 0 && end > start, `eagle-bridge.js lost its ${startMarker} anchor`);
        return bridgeSource.slice(start, end);
    }

    const sandbox = {
        console,
        URL,
        URLSearchParams,
        AbortController,
        setTimeout,
        clearTimeout,
        setInterval,
        clearInterval,
        fetch: async () => { throw new Error("unexpected fetch"); },
        chrome: chromeStub,
        EagleBridgeAuthLogic: auth,
        eagleBridgeMediaUrls: new Map(),
        eagleBridgeUploadLoops: new Map()
    };
    sandbox.globalThis = sandbox;
    vm.createContext(sandbox);
    // 只装载探测/落盘所需的最小切片：常量、默认状态、状态读写、探测、刷新。
    vm.runInContext(
        "const EAGLE_BRIDGE_DEFAULT_PORT = 47652;\n"
        + "const EAGLE_BRIDGE_STATE_KEY = \"downloadTransferStation\";\n"
        + "const EAGLE_BRIDGE_HEALTH_TIMEOUT_MS = 50;\n"
        + "const EAGLE_BRIDGE_RETRY_ALARM = \"retry\";\n"
        + bridgeSlice("let eagleBridgeFlushPromise", "function eagleBridgeSanitizeEvent"),
        sandbox,
        { filename: "eagle-bridge.js (probe slice)" }
    );

    // 切片之外的依赖（事件重试调度）在沙箱里补桩：本测试只验证探测与落盘。
    sandbox.eagleBridgeScheduleRetry = () => undefined;

    // 三次探测：慢的旧探测（结论 offline）与快的新探测（结论 connected）。
    let probeCount = 0;
    sandbox.fetch = async url => {
        probeCount += 1;
        const slow = probeCount === 1;
        if (slow) await new Promise(resolve => setTimeout(resolve, 40));
        if (String(url).endsWith("/health")) {
            return {
                ok: true,
                status: 200,
                json: async () => ({
                    ok: true,
                    data: { service: "liudi-desktop", version: "2.0.0", apiProtocol: 1, eagleAvailable: true, mediaToolsReady: true }
                })
            };
        }
        return { ok: false, status: 404, json: async () => ({ ok: false, error: { code: "not_found", message: "no" } }) };
    };
    const bridge = sandbox;

    const staleProbe = bridge.eagleBridgeRefreshConnection({ ports: [51234] });
    await new Promise(resolve => setTimeout(resolve, 5));
    const freshProbe = bridge.eagleBridgeRefreshConnection({ ports: [51234] });
    const [staleResult, freshResult] = await Promise.all([staleProbe, freshProbe]);

    assert.strictEqual(freshResult.stale, false, "the newest connection probe must commit");
    assert.strictEqual(staleResult.stale, true, "the superseded connection probe must report itself stale");
    assert.strictEqual(stateStore.downloadTransferStation.connection, "connected",
        "the stored connection state must reflect the newest probe");
    assert.strictEqual(stateStore.downloadTransferStation.port, 51234);
    assert.strictEqual(stateStore.downloadTransferStation.apiProtocol, 1,
        "the health capability snapshot must be persisted for the protocol check");
    assert.ok(stateStore.downloadTransferStation.token === undefined,
        "AD-1: no token may ever be written back into extension state");

    // **竞态本体**：先发起但后返回的那次探测不得覆盖更新结论。
    // 复现 `eagleBridgeRefreshConnection()` 的写法：探测开始时取一张闸门票，
    // 结论返回后与最新票比对，不一致即丢弃。
    {
        const gate = auth.createLatestRequestGate();
        const committed = { connection: "checking" };
        const probe = async (outcome, delayMs) => {
            const ticket = gate.begin();
            await new Promise(resolve => setTimeout(resolve, delayMs));
            if (!gate.isCurrent(ticket)) return { outcome, stale: true };
            committed.connection = outcome;
            return { outcome, stale: false };
        };
        const [slow, fast] = await Promise.all([probe("offline", 40), probe("connected", 5)]);
        assert.strictEqual(fast.stale, false, "the newest probe must commit its conclusion");
        assert.strictEqual(slow.stale, true, "the superseded probe must be discarded");
        assert.strictEqual(committed.connection, "connected",
            "a slow, older probe must not clobber the newer connection conclusion");
    }

    // 串行化写入只保证"不互相覆盖"，**不保证顺序语义**：谁最后入队谁最后落盘。
    // 顺序语义由上面的闸门负责——这里验证队列本身不会丢字段。
    const queuedState = { connection: "checking", port: 47652, apiProtocol: null };
    const queuedWriter = auth.createStateUpdateQueue(
        async () => ({ ...queuedState }),
        async next => { Object.assign(queuedState, next); }
    );
    await Promise.all([
        queuedWriter({ connection: "connected" }),
        queuedWriter({ port: 51234, apiProtocol: 1 })
    ]);
    assert.strictEqual(queuedState.connection, "connected");
    assert.strictEqual(queuedState.port, 51234,
        "serialized writes must not drop a field written by a concurrent update");
    assert.strictEqual(queuedState.apiProtocol, 1);

    // 桌面端不可达时必须落成 offline，并给出启动引导所需的结论。
    sandbox.fetch = async () => { throw new Error("ECONNREFUSED"); };
    const offlineResult = await bridge.eagleBridgeRefreshConnection({ ports: [51235] });
    assert.strictEqual(offlineResult.connection, "offline");
    assert.strictEqual(stateStore.downloadTransferStation.connection, "offline",
        "an unreachable desktop must be recorded so the popup can show the launch guide");

    console.log("Connection reachability race OK");
})().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
