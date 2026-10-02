import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  plansList: vi.fn(),
  planGet: vi.fn(),
  planCreate: vi.fn(),
  planStop: vi.fn(),
  planRetry: vi.fn(),
  planRemove: vi.fn(),
  planOpenOutput: vi.fn(),
  hasPlanBindings: vi.fn(),
  subscribePlanChanged: vi.fn(),
  subscribePlansChanged: vi.fn(),
  subscribePlanProgress: vi.fn(),
}));

// 绑定层是唯一调用 Wails 的地方（[16 §3.1] 的 S1），所以 store 测试只替换它。
vi.mock('../bindings', () => mocks);

import type { PlanView, Result } from '../bindings';
import { makePlan as plan } from '../test/planFixture';
import { PLAN_LIST_LIMIT, usePlansStore } from './plans';

type Handler<T> = (value: T) => void;

let onPlanChanged: Handler<string> | null = null;
let onPlansChanged: Handler<void> | null = null;
let onPlanProgress: Handler<PlanView> | null = null;

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function resetStore(): void {
  usePlansStore.setState({
    order: [],
    byId: {},
    seq: {},
    status: 'idle',
    loadError: null,
    rowErrors: {},
    pending: {},
    confirmRemoveId: null,
  });
}

/** 启动 store 并等列表落地。`total` 用于模拟被单次上限截断的情形。 */
async function startWith(items: PlanView[], total = items.length): Promise<void> {
  mocks.plansList.mockResolvedValue({
    ok: true,
    value: { items, total, offset: 0, limit: PLAN_LIST_LIMIT },
  } satisfies Result<unknown>);
  usePlansStore.getState().start();
  await vi.waitFor(() => {
    expect(usePlansStore.getState().status).toBe('ready');
  });
}

describe('计划 store', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    resetStore();

    mocks.hasPlanBindings.mockReturnValue(true);
    mocks.subscribePlanChanged.mockImplementation((handler: Handler<string>) => {
      onPlanChanged = handler;
      return () => {
        onPlanChanged = null;
      };
    });
    mocks.subscribePlansChanged.mockImplementation((handler: Handler<void>) => {
      onPlansChanged = handler;
      return () => {
        onPlansChanged = null;
      };
    });
    mocks.subscribePlanProgress.mockImplementation((handler: Handler<PlanView>) => {
      onPlanProgress = handler;
      return () => {
        onPlanProgress = null;
      };
    });
  });

  afterEach(() => {
    usePlansStore.getState().detach();
  });

  it('启动时订阅三个事件，并直接拉一次列表对齐（[04 §4.4]）', async () => {
    await startWith([plan({ id: 'a' }), plan({ id: 'b' })]);

    expect(mocks.subscribePlanChanged).toHaveBeenCalledTimes(1);
    expect(mocks.subscribePlansChanged).toHaveBeenCalledTimes(1);
    expect(mocks.subscribePlanProgress).toHaveBeenCalledTimes(1);
    // B3（[16 §3.1]）：列表方法必须显式给 offset/limit；B-310 不分页，所以取满单次上限
    // 键名与 Go 侧 `PlansListRequest` 对齐（statuses / offset / limit）
    expect(mocks.plansList).toHaveBeenCalledWith({
      statuses: [],
      offset: 0,
      limit: PLAN_LIST_LIMIT,
    });
    expect(usePlansStore.getState().order).toEqual(['a', 'b']);
    expect(mocks.plansList).toHaveBeenCalledTimes(1); // 只对齐一次，不轮询（[09 PF-1]）
    expect(usePlansStore.getState().truncated).toBeNull();
  });

  it('planProgress 直接覆盖该条，不发起任何重查（[04 §4.2]）', async () => {
    await startWith([plan({ id: 'a', progress: 10 })]);
    mocks.plansList.mockClear();

    onPlanProgress?.(plan({ id: 'a', progress: 42, phaseDetail: '12.4 MB / 20.1 MB' }));

    expect(usePlansStore.getState().byId['a'].progress).toBe(42);
    expect(usePlansStore.getState().byId['a'].phaseDetail).toBe('12.4 MB / 20.1 MB');
    expect(mocks.planGet).not.toHaveBeenCalled();
    expect(mocks.plansList).not.toHaveBeenCalled();
  });

  it('进度推送按整条覆盖：字段被整体替换，不做增量合并', async () => {
    await startWith([plan({ id: 'a', status: 'running' })]);

    onPlanProgress?.(
      plan({ id: 'a', status: 'completed', progress: 100, finalPath: 'D:\\已完成\\输出名.mp4' }),
    );

    const row = usePlansStore.getState().byId['a'];
    expect(row.status).toBe('completed');
    expect(row.finalPath).toBe('D:\\已完成\\输出名.mp4');
  });

  it('未知 id 的进度也能落地（插到列表最前），不会永久丢失', async () => {
    await startWith([plan({ id: 'a' })]);

    onPlanProgress?.(plan({ id: 'new', progress: 3 }));

    expect(usePlansStore.getState().order).toEqual(['new', 'a']);
  });

  it('planChanged 触发 PlanGet 并覆盖该条（[04 §4.1]）', async () => {
    await startWith([plan({ id: 'a', status: 'running' })]);
    mocks.plansList.mockClear();
    mocks.planGet.mockResolvedValue({
      ok: true,
      value: plan({ id: 'a', status: 'completed', progress: 100, finalPath: 'D:\\a.mp4' }),
    });

    onPlanChanged?.('a');

    await vi.waitFor(() => {
      expect(usePlansStore.getState().byId['a'].status).toBe('completed');
    });
    expect(mocks.planGet).toHaveBeenCalledWith('a');
    // 单条变化不得触发整列表重查（[11 PF-A2]：单次更新触发的主列表查询 ≤ 2 次）
    expect(mocks.plansList).not.toHaveBeenCalled();
  });

  it('plansChanged 触发重新拉列表（[04 §4.1]）', async () => {
    await startWith([plan({ id: 'a' })]);
    mocks.plansList.mockClear();
    mocks.plansList.mockResolvedValue({
      ok: true,
      value: { items: [plan({ id: 'a' }), plan({ id: 'b' })] },
    });

    onPlansChanged?.();

    await vi.waitFor(() => {
      expect(usePlansStore.getState().order).toEqual(['a', 'b']);
    });
    expect(mocks.plansList).toHaveBeenCalledTimes(1);
  });

  it('乱序防护：旧序号的 PlanGet 响应必须被丢弃（[04 §4.3]）', async () => {
    await startWith([plan({ id: 'a', progress: 10 })]);
    const first = deferred<Result<PlanView>>();
    const second = deferred<Result<PlanView>>();
    mocks.planGet.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);

    const slow = usePlansStore.getState().refreshOne('a');
    const fast = usePlansStore.getState().refreshOne('a');

    // 后发的先返回：生效
    second.resolve({ ok: true, value: plan({ id: 'a', progress: 80 }) });
    await fast;
    expect(usePlansStore.getState().byId['a'].progress).toBe(80);

    // 先发的后返回：序号已过期，丢弃
    first.resolve({ ok: true, value: plan({ id: 'a', progress: 20 }) });
    await slow;
    expect(usePlansStore.getState().byId['a'].progress).toBe(80);
  });

  it('乱序防护同样覆盖列表请求：旧列表不得覆盖新列表', async () => {
    const first = deferred<Result<{ items: PlanView[] }>>();
    const second = deferred<Result<{ items: PlanView[] }>>();
    mocks.plansList.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);

    const older = usePlansStore.getState().align();
    const newer = usePlansStore.getState().align();

    second.resolve({ ok: true, value: { items: [plan({ id: 'new' })] } });
    await newer;
    expect(usePlansStore.getState().order).toEqual(['new']);

    first.resolve({ ok: true, value: { items: [plan({ id: 'stale' })] } });
    await older;
    expect(usePlansStore.getState().order).toEqual(['new']);
  });

  it('重查失败留下可读消息，不静默吞错', async () => {
    await startWith([plan({ id: 'a' })]);
    mocks.planGet.mockResolvedValue({ ok: false, message: '任务不存在' });

    await usePlansStore.getState().refreshOne('a');

    expect(usePlansStore.getState().rowErrors['a']).toBe('任务不存在');
  });

  it('停止成功后用后端返回的记录覆盖，不自行推测状态（B-313）', async () => {
    await startWith([plan({ id: 'a', status: 'running' })]);
    mocks.planStop.mockResolvedValue({ ok: true, value: plan({ id: 'a', status: 'canceled' }) });

    await usePlansStore.getState().stop('a');

    expect(mocks.planStop).toHaveBeenCalledWith('a');
    expect(usePlansStore.getState().byId['a'].status).toBe('canceled');
    expect(usePlansStore.getState().rowErrors['a']).toBeUndefined();
  });

  it('操作失败时记录消息且不改动该行', async () => {
    await startWith([plan({ id: 'a', status: 'failed' })]);
    mocks.planRetry.mockResolvedValue({ ok: false, message: '该任务当前不可重试' });

    await usePlansStore.getState().retry('a');

    expect(usePlansStore.getState().rowErrors['a']).toBe('该任务当前不可重试');
    expect(usePlansStore.getState().byId['a'].status).toBe('failed');
    expect(usePlansStore.getState().pending['a']).toBe(false);
  });

  it('删除记录走二次确认，成功后从列表移除（[09 §5]）', async () => {
    await startWith([plan({ id: 'a' }), plan({ id: 'b' })]);
    mocks.planRemove.mockResolvedValue({ ok: true, value: undefined });

    usePlansStore.getState().askRemove('a');
    expect(usePlansStore.getState().confirmRemoveId).toBe('a');

    usePlansStore.getState().cancelRemove();
    expect(usePlansStore.getState().confirmRemoveId).toBeNull();

    usePlansStore.getState().askRemove('a');
    await usePlansStore.getState().remove('a');

    expect(mocks.planRemove).toHaveBeenCalledWith('a');
    expect(usePlansStore.getState().order).toEqual(['b']);
    expect(usePlansStore.getState().byId['a']).toBeUndefined();
    expect(usePlansStore.getState().confirmRemoveId).toBeNull();
  });

  it('删除失败时保留该行并说明原因', async () => {
    await startWith([plan({ id: 'a' }), plan({ id: 'b' })]);
    mocks.planRemove.mockResolvedValue({ ok: false, message: '该文件不属于本程序，已保留' });

    await usePlansStore.getState().remove('a');

    expect(usePlansStore.getState().order).toEqual(['a', 'b']);
    expect(usePlansStore.getState().rowErrors['a']).toBe('该文件不属于本程序，已保留');
  });

  it('窗口重新聚焦时直接拉一次列表对齐（[04 §4.4]）', async () => {
    await startWith([plan({ id: 'a' })]);
    mocks.plansList.mockClear();

    window.dispatchEvent(new Event('focus'));

    await vi.waitFor(() => {
      expect(mocks.plansList).toHaveBeenCalledTimes(1);
    });
  });

  it('卸载后取消订阅：事件不再生效（[16 §4.1] P-109）', async () => {
    await startWith([plan({ id: 'a', progress: 10 })]);
    mocks.plansList.mockClear();

    usePlansStore.getState().detach();
    expect(onPlanProgress).toBeNull();

    window.dispatchEvent(new Event('focus'));
    expect(mocks.plansList).not.toHaveBeenCalled();
  });

  it('绑定层缺席时给出明确结果，不抛未捕获异常（[16 §3.1]）', () => {
    mocks.hasPlanBindings.mockReturnValue(false);

    expect(() => usePlansStore.getState().start()).not.toThrow();
    expect(usePlansStore.getState().loadError).toBe('桌面端未连接');
    expect(mocks.plansList).not.toHaveBeenCalled();
  });

  it('列表读取失败时如实记录原因，不假装成空列表', async () => {
    mocks.plansList.mockResolvedValue({ ok: false, message: '桌面端返回了无法识别的任务列表' });

    await usePlansStore.getState().align();

    expect(usePlansStore.getState().status).toBe('error');
    expect(usePlansStore.getState().loadError).toBe('桌面端返回了无法识别的任务列表');
  });

  it('总数超过单次上限时记录截断信息（不静默截断）', async () => {
    await startWith([plan({ id: 'a' })], 250);

    expect(usePlansStore.getState().truncated).toEqual({ shown: 1, total: 250 });
  });

  it('没有被截断时不留截断提示', async () => {
    await startWith([plan({ id: 'a' }), plan({ id: 'b' })]);

    expect(usePlansStore.getState().truncated).toBeNull();
  });
});
