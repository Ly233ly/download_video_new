import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  getTheme,
  hasPlanBindings,
  planCreate,
  planGet,
  planOpenOutput,
  planRemove,
  plansList,
  planStop,
  subscribePlanChanged,
  subscribePlanProgress,
  subscribePlansChanged,
} from './index';

/**
 * 绑定层是**唯一**调用 Wails 的地方（[16 §3.1] 的 S1），所以这里用假的
 * `window.go.ui.UI` 与 `window.runtime` 覆盖它，不需要真实后端。
 */
interface FakeRuntime {
  EventsOn(name: string, callback: (...args: unknown[]) => void): () => void;
}

interface FakeGlobals {
  go?: { ui?: { UI?: Record<string, unknown> } };
  runtime?: FakeRuntime;
}

const globals = globalThis as unknown as FakeGlobals;

function setBackend(methods: Record<string, unknown>): void {
  globals.go = { ui: { UI: methods } };
  globals.runtime = { EventsOn: vi.fn(() => () => {}) };
}

afterEach(() => {
  delete globals.go;
  delete globals.runtime;
});

describe('绑定层 · 计划方法', () => {
  it('后端缺席时给出明确结果，不抛未捕获异常', async () => {
    delete globals.go;

    expect(hasPlanBindings()).toBe(false);
    expect(await plansList({ statuses: [], offset: 0, limit: 10 })).toEqual({
      ok: false,
      message: '桌面端未提供任务列表接口',
    });
  });

  it('后端方法未落地时 hasPlanBindings() 为 false（缺一个计划方法就算不可用）', () => {
    globals.runtime = { EventsOn: vi.fn(() => () => {}) };
    globals.go = { ui: { UI: { GetTheme: vi.fn() } } };

    expect(hasPlanBindings()).toBe(false);
  });

  it('方法齐全时 hasPlanBindings() 为 true', () => {
    setBackend({ PlansList: vi.fn(async () => ({ items: [] })) });

    expect(hasPlanBindings()).toBe(true);
  });

  it('异常被转成 Result 的失败分支，不静默吞错', async () => {
    setBackend({
      PlanStop: vi.fn(async () => {
        throw new Error('该任务当前不可停止');
      }),
    });

    expect(await planStop('a')).toEqual({ ok: false, message: '该任务当前不可停止' });
  });

  it('后端抛出非 Error 值时同样给出可读消息', async () => {
    setBackend({
      PlanRemove: vi.fn(async () => {
        throw '任务不存在';
      }),
    });

    expect(await planRemove('a')).toEqual({ ok: false, message: '任务不存在' });
  });

  it('列表载荷没有 items 数组时明确报错，而不是当成空列表', async () => {
    setBackend({ PlansList: vi.fn(async () => ({ rows: [] })) });

    expect(await plansList({ statuses: [], offset: 0, limit: 10 })).toEqual({
      ok: false,
      message: '桌面端返回了无法识别的任务列表',
    });
  });

  it('列表方法把查询参数原样传给后端（B3：必须显式给 offset/limit）', async () => {
    const PlansList = vi.fn(async () => ({ items: [], total: 0, offset: 0, limit: 200 }));
    setBackend({ PlansList });

    const result = await plansList({ statuses: ['failed'], offset: 0, limit: 200 });

    expect(result.ok).toBe(true);
    // 键名与 Go 侧 `PlansListRequest` 的 json tag 一致（statuses / offset / limit）
    expect(PlansList).toHaveBeenCalledWith({ statuses: ['failed'], offset: 0, limit: 200 });
  });

  it('单条记录缺少关键判别字段时明确报错（不把半成品当成功）', async () => {
    setBackend({ PlanGet: vi.fn(async () => ({ outputName: '缺 id 与 status' })) });

    expect(await planGet('a')).toEqual({
      ok: false,
      message: '桌面端返回了无法识别的任务记录',
    });
  });

  it('未知状态值也判为无法识别，不把脏数据当成功', async () => {
    setBackend({ PlanGet: vi.fn(async () => ({ id: 'a', status: 'done' })) });

    expect(await planGet('a')).toEqual({
      ok: false,
      message: '桌面端返回了无法识别的任务记录',
    });
  });

  it('创建计划把请求对象原样传给后端', async () => {
    const PlanCreate = vi.fn(async () => ({ id: 'a', status: 'queued' }));
    setBackend({ PlanCreate });
    // 键名与 Go 侧 `CreatePlanRequest` 的字段名一致（那边没有 json tag，按大小写不敏感匹配）
    const request = {
      url: 'https://example.com/a.mp4',
      mediaKind: 'direct' as const,
      outputName: 'a',
      outputContainer: 'mp4' as const,
      mergeMode: 'single' as const,
    };

    const result = await planCreate(request);

    expect(result.ok).toBe(true);
    expect(PlanCreate).toHaveBeenCalledWith(request);
  });

  it('打开所在文件夹在后端未落地该方法时明确报错（不假装成功）', async () => {
    setBackend({ PlanGet: vi.fn() });

    expect(await planOpenOutput('a')).toEqual({
      ok: false,
      message: '桌面端未提供打开所在文件夹接口',
    });
  });

  it('打开所在文件夹成功时返回 ok（失败必须让调用方看到）', async () => {
    setBackend({ PlanOpenOutput: vi.fn(async () => undefined) });

    expect(await planOpenOutput('a')).toEqual({ ok: true, value: undefined });
  });

  it('主题读取仍然工作（既有能力不被改坏）', async () => {
    setBackend({ GetTheme: vi.fn(async () => 'dark') });

    expect(await getTheme()).toEqual({ ok: true, value: 'dark' });
  });
});

describe('绑定层 · 事件订阅', () => {
  /** 装一个假的事件通道，返回"按事件名发载荷"的入口。 */
  function captureEvents(): (eventName: string, ...args: unknown[]) => void {
    const handlers = new Map<string, (...args: unknown[]) => void>();
    globals.runtime = {
      EventsOn: (name, callback) => {
        handlers.set(name, callback);
        return () => {};
      },
    };
    return (eventName, ...args) => {
      handlers.get(eventName)?.(...args);
    };
  }

  it('三个事件各自订阅，返回取消函数（[16 §3.1]）', () => {
    const off = vi.fn();
    const subscribed: string[] = [];
    const EventsOn = vi.fn((name: string, callback: (...args: unknown[]) => void) => {
      subscribed.push(name);
      expect(typeof callback).toBe('function');
      return off;
    });
    globals.runtime = { EventsOn };

    expect(subscribePlanChanged(() => {})).toBe(off);
    expect(subscribePlansChanged(() => {})).toBe(off);
    expect(subscribePlanProgress(() => {})).toBe(off);
    expect(subscribed).toEqual(['planChanged', 'plansChanged', 'planProgress']);
  });

  it('planChanged 只把 id 交给上层（[04 §4.1]）', () => {
    const emit = captureEvents();
    const handler = vi.fn();
    subscribePlanChanged(handler);

    emit('planChanged', { id: 'plan-1' });

    expect(handler).toHaveBeenCalledWith('plan-1');
  });

  it('载荷非法时忽略这一次，不让坏载荷打断订阅', () => {
    const emit = captureEvents();
    const changed = vi.fn();
    const progress = vi.fn();
    subscribePlanChanged(changed);
    subscribePlanProgress(progress);

    emit('planChanged', { nope: true });
    emit('planChanged', null);
    emit('planChanged', { id: '' });
    emit('planProgress', { id: 'a', status: 'unknown' });
    emit('planProgress', 'not-an-object');

    expect(changed).not.toHaveBeenCalled();
    expect(progress).not.toHaveBeenCalled();
  });

  it('planProgress 的完整记录直接交给上层（[04 §4.2]）', () => {
    const emit = captureEvents();
    const handler = vi.fn();
    subscribePlanProgress(handler);
    const view = { id: 'a', status: 'running', progress: 42 };

    emit('planProgress', view);

    expect(handler).toHaveBeenCalledWith(view);
  });

  it('事件通道缺席时返回可调用的空取消函数（卸载时不会炸）', () => {
    delete globals.runtime;

    const off = subscribePlanChanged(() => {});

    expect(() => off()).not.toThrow();
  });
});
