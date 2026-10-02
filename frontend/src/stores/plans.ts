/**
 * 计划列表状态（zustand，按领域切分，[16 §7] 的 F2）。
 *
 * 三条来自 [04 §4] 的硬约束，全部落在这里：
 *
 *  1. **不轮询**（[09 `PF-1`]）：状态由 `planChanged` / `plansChanged` / `planProgress`
 *     三个事件驱动；只在启动与窗口重新聚焦时直接拉一次 `PlansList()` 对齐（[04 §4.4]）。
 *     `planProgress` 是首选路径（Go 侧 `EventSink` 实现了 `service.PlanViewPublisher`）；
 *     `planChanged` + 重查是它缺席时的退路，两条都接住，前端不需要知道后端走的是哪条。
 *  2. **进度直接覆盖**（[04 §4.2]）：`planProgress` 的载荷是**完整** `PlanView`，
 *     不再回查后端。
 *  3. **乱序防护**（[04 §4.3]）：为每个 id 维护本地请求序号，`PlanGet` 返回时
 *     序号已过期就丢弃。
 *
 * 订阅粒度（[16 §4.2] R4）：列表容器只订阅 `order`（id 顺序），每一行只订阅
 * `byId[id]`，因此一条进度推送只重渲染那一行。
 */
import { create } from 'zustand';

import * as bindings from '../bindings';
import type { PlanView } from '../bindings';

/**
 * 单次读取的条数上限（[16 §3.1] 的 B3：列表方法必须显式传 `offset`/`limit`）。
 *
 * 取值 = [03 §4.5] 的「单次返回的计划数 200」。后端会把更大的值夹到 200 并回显
 * 实际生效的 `limit`，所以这里直接取满，不猜。
 *
 * 这不是分页——B-310 明确禁止分页控件，界面始终是同一个连续滚动列表。
 * 超过 200 条时 `total` 会大于 `items.length`，界面**如实说明**只显示了最近的一批
 * （见 `truncated`），不做静默截断；要在一屏里看到全部 1000 条，需要先定
 * 「连续加载多页」还是「提高 [03 §4.5] 的上限」（两个都不能由前端单方面决定）。
 */
export const PLAN_LIST_LIMIT = 200;

const BACKEND_MISSING = '桌面端未连接';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

/** 列表被单次上限截断的信息（只在真的截断时存在）。 */
export interface Truncated {
  shown: number;
  total: number;
}

interface PlansState {
  /** 行顺序（后端给的顺序）。**只在增删时换引用**——列表容器订阅它。 */
  order: string[];
  /** 行的真身。进度推送只换这一条。 */
  byId: Record<string, PlanView>;
  /** 每个 id 的本地请求序号（[04 §4.3]）。 */
  seq: Record<string, number>;
  status: LoadStatus;
  /** 列表读取失败的原因。界面据此显示提示条，不假装"没有任务"。 */
  loadError: string | null;
  /** 被 [03 §4.5] 的单次上限截断时的说明（`null` 表示没有截断）。 */
  truncated: Truncated | null;
  /** 单条操作/重查失败的原因，按 id 挂在对应行上（不静默吞错）。 */
  rowErrors: Record<string, string>;
  /** 正在执行的 id：用于禁用按钮，避免连点造成重复提交（[04 §3.2] 幂等性一节）。 */
  pending: Record<string, boolean>;
  /** 待二次确认删除记录的计划 id（[09 §5]：破坏性操作必须二次确认）。 */
  confirmRemoveId: string | null;

  /** 启动对齐 + 事件订阅（可重复调用）。 */
  start(): void;
  /** 卸载时取消订阅与监听（[16 §4.1] `P-109`）。 */
  detach(): void;
  /** 直接拉一次列表对齐（[04 §4.4]）。 */
  align(): Promise<void>;
  /** 重新拉单个计划（`planChanged` 的处理，带乱序防护）。 */
  refreshOne(id: string): Promise<void>;
  /** 直接用事件载荷覆盖该条（[04 §4.2]）。 */
  applyProgress(view: PlanView): void;
  stop(id: string): Promise<void>;
  retry(id: string): Promise<void>;
  openFolder(id: string): Promise<void>;
  askRemove(id: string): void;
  cancelRemove(): void;
  remove(id: string): Promise<void>;
}

/**
 * 绑定层是否可用。
 *
 * 三个理由要检查而不是直接调用：
 *
 *  1. 后端未落地时 `window.go.ui.UI` 上没有这些方法（本阶段即如此）；
 *  2. 绑定层可能整体缺席（纯浏览器调试），[16 §3.1] 要求"给出明确结果，
 *     而不是抛未捕获异常"；
 *  3. 绑定层若被整体替换（测试），未定义的导出是**不可访问**的，`in` 探测
 *     比 `typeof` 安全——后者会直接抛错。
 */
function bindingsReady(): boolean {
  return 'hasPlanBindings' in bindings && bindings.hasPlanBindings();
}

/** 覆盖一行；未知 id 插到列表最前（新建的计划比已有记录新）。 */
function upsert(state: PlansState, view: PlanView): Partial<PlansState> {
  const known = Object.prototype.hasOwnProperty.call(state.byId, view.id);
  return {
    byId: { ...state.byId, [view.id]: view },
    order: known ? state.order : [view.id, ...state.order],
  };
}

/** 去掉一个 key；没有该 key 时返回原引用（避免无谓的重渲染）。 */
function without<T>(map: Record<string, T>, key: string): Record<string, T> {
  if (!Object.prototype.hasOwnProperty.call(map, key)) {
    return map;
  }
  const next = { ...map };
  delete next[key];
  return next;
}

/** 列表请求也要防乱序：两次对齐并发返回时，旧的不得覆盖新的。 */
let listSeq = 0;

/** 已安装的取消函数（模块级：store 状态变化不该引起订阅重建）。 */
let installed: Array<() => void> = [];

function onWindowFocus(): void {
  void usePlansStore.getState().align();
}

export const usePlansStore = create<PlansState>((set, get) => {
  /**
   * 单条操作的公共流程：可用性 → 置忙 → 调用 → 失败落到该行 → 复位忙。
   * 返回值是后端给的权威 `PlanView`（`PlanRemove` 之类没有返回值时为 null）。
   */
  async function callAction(
    id: string,
    call: () => Promise<bindings.Result<PlanView>>,
  ): Promise<PlanView | null> {
    if (!bindingsReady()) {
      set((state) => ({ rowErrors: { ...state.rowErrors, [id]: BACKEND_MISSING } }));
      return null;
    }
    set((state) => ({
      pending: { ...state.pending, [id]: true },
      rowErrors: without(state.rowErrors, id),
      confirmRemoveId: null,
    }));
    const result = await call();
    set((state) => ({ pending: { ...state.pending, [id]: false } }));
    if (!result.ok) {
      set((state) => ({ rowErrors: { ...state.rowErrors, [id]: result.message } }));
      return null;
    }
    return result.value;
  }

  return {
    order: [],
    byId: {},
    seq: {},
    status: 'idle',
    loadError: null,
    truncated: null,
    rowErrors: {},
    pending: {},
    confirmRemoveId: null,

    start() {
      if (!bindingsReady()) {
        set({ status: 'error', loadError: BACKEND_MISSING });
        return;
      }
      if (installed.length === 0) {
        installed = [
          bindings.subscribePlanChanged((id) => {
            void get().refreshOne(id);
          }),
          bindings.subscribePlansChanged(() => {
            void get().align();
          }),
          bindings.subscribePlanProgress((view) => {
            get().applyProgress(view);
          }),
          () => {
            window.removeEventListener('focus', onWindowFocus);
          },
        ];
        window.addEventListener('focus', onWindowFocus);
      }
      void get().align();
    },

    detach() {
      for (const off of installed) {
        off();
      }
      installed = [];
    },

    async align() {
      if (!bindingsReady()) {
        set({ status: 'error', loadError: BACKEND_MISSING });
        return;
      }
      const mine = listSeq + 1;
      listSeq = mine;
      set((state) => ({ status: state.status === 'ready' ? state.status : 'loading' }));
      // 列表方法必须显式传 offset/limit（[16 §3.1] 的 B3），空 statuses = 不筛选。
      const result = await bindings.plansList({
        statuses: [],
        offset: 0,
        limit: PLAN_LIST_LIMIT,
      });
      if (mine !== listSeq) {
        return; // 更新的列表请求已经发出，本次结果作废
      }
      if (!result.ok) {
        set({ status: 'error', loadError: result.message });
        return;
      }
      const items = result.value.items;
      const total = typeof result.value.total === 'number' ? result.value.total : items.length;
      set({
        order: items.map((item) => item.id),
        byId: Object.fromEntries(items.map((item) => [item.id, item])),
        status: 'ready',
        loadError: null,
        // 只显示了一部分就如实说明（不静默截断）
        truncated: total > items.length ? { shown: items.length, total } : null,
      });
    },

    async refreshOne(id) {
      if (!bindingsReady()) {
        set((state) => ({ rowErrors: { ...state.rowErrors, [id]: BACKEND_MISSING } }));
        return;
      }
      // [04 §4.3]：发起时 seq++，返回时若已被更新的请求超过就丢弃。
      const mine = (get().seq[id] ?? 0) + 1;
      set((state) => ({ seq: { ...state.seq, [id]: mine } }));
      const result = await bindings.planGet(id);
      if ((get().seq[id] ?? 0) > mine) {
        return;
      }
      if (!result.ok) {
        set((state) => ({ rowErrors: { ...state.rowErrors, [id]: result.message } }));
        return;
      }
      set((state) => upsert(state, result.value));
    },

    applyProgress(view) {
      // 直接覆盖，不回查（[04 §4.2]：推送由同一 goroutine 串行发出，后到的必然更新）。
      //
      // 刻意**不**在这里动 `seq`：进度推送每 200 ms 一条，写坏了下一跳自愈；
      // 而"一条进度盖掉一次更新的 PlanGet 结果"同样自愈，反过来丢掉一次
      // 终态重查（如刚转为 canceled/failed）却没有自愈路径。所以 [04 §4.3] 的
      // 序号只用于 PlanGet 之间。
      set((state) => upsert(state, view));
    },

    async stop(id) {
      const view = await callAction(id, () => bindings.planStop(id));
      if (view) {
        set((state) => upsert(state, view));
      }
    },

    async retry(id) {
      const view = await callAction(id, () => bindings.planRetry(id));
      if (view) {
        set((state) => upsert(state, view));
      }
    },

    async openFolder(id) {
      if (!bindingsReady()) {
        set((state) => ({ rowErrors: { ...state.rowErrors, [id]: BACKEND_MISSING } }));
        return;
      }
      set((state) => ({ rowErrors: without(state.rowErrors, id) }));
      const result = await bindings.planOpenOutput(id);
      if (!result.ok) {
        set((state) => ({ rowErrors: { ...state.rowErrors, [id]: result.message } }));
      }
    },

    askRemove(id) {
      set({ confirmRemoveId: id });
    },

    cancelRemove() {
      set({ confirmRemoveId: null });
    },

    async remove(id) {
      if (!bindingsReady()) {
        set((state) => ({ rowErrors: { ...state.rowErrors, [id]: BACKEND_MISSING } }));
        return;
      }
      set((state) => ({
        pending: { ...state.pending, [id]: true },
        rowErrors: without(state.rowErrors, id),
        confirmRemoveId: null,
      }));
      const result = await bindings.planRemove(id);
      set((state) => {
        if (!result.ok) {
          return {
            pending: { ...state.pending, [id]: false },
            rowErrors: { ...state.rowErrors, [id]: result.message },
          };
        }
        const byId = { ...state.byId };
        delete byId[id];
        return {
          byId,
          order: state.order.filter((item) => item !== id),
          pending: without(state.pending, id),
          rowErrors: without(state.rowErrors, id),
        };
      });
    },
  };
});
