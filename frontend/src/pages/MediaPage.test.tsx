import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
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

vi.mock('../bindings', () => mocks);

import type { PlanView, Result } from '../bindings';
import { PLAN_ROW_HEIGHT } from '../components/PlanRow';
import { usePlansStore } from '../stores/plans';
import { makePlan } from '../test/planFixture';
import { MediaPage } from './MediaPage';

function listResult(
  items: PlanView[],
  total = items.length,
): Result<{
  items: PlanView[];
  total: number;
  offset: number;
  limit: number;
}> {
  return { ok: true, value: { items, total, offset: 0, limit: 200 } };
}

/** 让虚拟滚动在 jsdom 里有可用的视口尺寸（`getRect` 读的是 offsetWidth/offsetHeight）。 */
beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
    configurable: true,
    get: () => 600,
  });
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
    configurable: true,
    get: () => 900,
  });
});

describe('下载任务页', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    usePlansStore.setState({
      order: [],
      byId: {},
      seq: {},
      status: 'idle',
      loadError: null,
      truncated: null,
      rowErrors: {},
      pending: {},
      confirmRemoveId: null,
    });
    mocks.hasPlanBindings.mockReturnValue(true);
    mocks.subscribePlanChanged.mockReturnValue(() => {});
    mocks.subscribePlansChanged.mockReturnValue(() => {});
    mocks.subscribePlanProgress.mockReturnValue(() => {});
    mocks.planStop.mockResolvedValue({
      ok: true,
      value: makePlan({ id: 'a', outputName: '下载中的.mp4', status: 'canceled' }),
    });
    mocks.planRetry.mockResolvedValue({
      ok: true,
      value: makePlan({ id: 'b', outputName: '失败的.mp4', status: 'queued' }),
    });
    mocks.planRemove.mockResolvedValue({ ok: true, value: undefined });
    mocks.planOpenOutput.mockResolvedValue({ ok: true, value: undefined });
  });

  afterEach(() => {
    usePlansStore.getState().detach();
  });

  async function renderWith(items: PlanView[], total = items.length): Promise<void> {
    mocks.plansList.mockResolvedValue(listResult(items, total));
    render(<MediaPage />);
    if (items.length > 0) {
      await screen.findByRole('listitem', { name: items[0].outputName });
    } else {
      await screen.findByText('还没有下载任务');
    }
  }

  function rowOf(name: string): HTMLElement {
    return screen.getByRole('listitem', { name });
  }

  it('空列表时保留空态提示', async () => {
    await renderWith([]);

    expect(screen.getByText('还没有下载任务')).toBeInTheDocument();
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument();
  });

  it('按行展示输出名、来源、质量档位、状态与进度（[09 §3.1] 行信息）', async () => {
    await renderWith([
      makePlan({
        id: 'a',
        outputName: '小米澎湃 OS4 宣发视频.mp4',
        sourceUrl: 'https://www.bilibili.com/video/BV1',
        qualityLabel: '1080P',
        outputContainer: 'mp4',
        status: 'running',
        phase: 'downloading',
        progress: 62,
        downloadedBytes: 13_002_342,
        totalBytes: 21_076_787,
      }),
    ]);

    const row = rowOf('小米澎湃 OS4 宣发视频.mp4');
    expect(within(row).getByText('小米澎湃 OS4 宣发视频.mp4')).toBeInTheDocument();
    expect(within(row).getByText('www.bilibili.com · 1080P · mp4')).toBeInTheDocument();
    expect(within(row).getByText('下载中')).toBeInTheDocument();
    expect(within(row).getByText(/62%/)).toBeInTheDocument();
    expect(within(row).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '62');
  });

  it('只有 completed 显示 100%（B-312），非 completed 即使后端给 100 也不显示', async () => {
    await renderWith([
      makePlan({ id: 'a', outputName: '下载中的.mp4', status: 'running', progress: 100 }),
      makePlan({ id: 'b', outputName: '已完成的.mp4', status: 'completed', progress: 100 }),
    ]);

    const running = rowOf('下载中的.mp4');
    expect(within(running).queryByText(/100%/)).not.toBeInTheDocument();
    expect(within(running).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '99');
    expect(screen.getAllByText(/100%/)).toHaveLength(1);
    expect(within(rowOf('已完成的.mp4')).getByText(/100%/)).toBeInTheDocument();
  });

  it('completed 显示最终路径与「打开所在文件夹」入口（B-309）', async () => {
    await renderWith([
      makePlan({
        id: 'a',
        outputName: '已完成.mp4',
        status: 'completed',
        progress: 100,
        finalPath: 'D:\\Users\\MSI\\Downloads\\留底下载器\\已完成\\已完成.mp4',
      }),
      makePlan({ id: 'b', outputName: '下载中.mp4', status: 'running' }),
    ]);

    const done = rowOf('已完成.mp4');
    expect(
      within(done).getByText('D:\\Users\\MSI\\Downloads\\留底下载器\\已完成\\已完成.mp4'),
    ).toBeInTheDocument();
    expect(within(done).getByRole('button', { name: '打开所在文件夹' })).toBeInTheDocument();

    // 未完成的行不得出现路径与打开入口
    const running = rowOf('下载中.mp4');
    expect(
      within(running).queryByRole('button', { name: '打开所在文件夹' }),
    ).not.toBeInTheDocument();
    expect(within(running).queryByText(/已完成\\/)).not.toBeInTheDocument();
  });

  it('点击「打开所在文件夹」调用 PlanOpenOutput', async () => {
    await renderWith([
      makePlan({
        id: 'a',
        outputName: '已完成.mp4',
        status: 'completed',
        progress: 100,
        finalPath: 'D:\\已完成\\已完成.mp4',
      }),
    ]);

    fireEvent.click(screen.getByRole('button', { name: '打开所在文件夹' }));

    await waitFor(() => {
      expect(mocks.planOpenOutput).toHaveBeenCalledWith('a');
    });
  });

  it('failed 行显示失败原因与质量档位，且没有进度条（[09 §3.1]）', async () => {
    await renderWith([
      makePlan({
        id: 'a',
        outputName: '失败的.mp4',
        status: 'failed',
        qualityLabel: '原画',
        progress: 62,
        errorCode: 'download_failed',
        errorMessage: '下载未完成，请稍后重试',
        attemptCount: 5,
      }),
    ]);

    const row = rowOf('失败的.mp4');
    expect(within(row).getByText('失败')).toBeInTheDocument();
    expect(within(row).getByText(/原画/)).toBeInTheDocument();
    expect(
      within(row).getByText('失败原因：下载未完成，请稍后重试 · 已重试 5 次'),
    ).toBeInTheDocument();
    expect(within(row).queryByRole('progressbar')).not.toBeInTheDocument();
    expect(within(row).getByRole('button', { name: '重试' })).toBeInTheDocument();
  });

  it('列表不分页（B-310）：不出现页码或翻页控件', async () => {
    const items = Array.from({ length: 60 }, (_, index) =>
      makePlan({ id: `p${index}`, outputName: `任务 ${index}.mp4` }),
    );
    await renderWith(items);

    for (const label of ['下一页', '上一页', '加载更多', '首页', '末页']) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
    }
    expect(screen.queryByText(/\d+\s*\/\s*\d+\s*页/)).not.toBeInTheDocument();
  });

  it('停止按钮调用 PlanStop；重试按钮调用 PlanRetry（按钮由状态决定）', async () => {
    await renderWith([
      makePlan({ id: 'a', outputName: '下载中的.mp4', status: 'running' }),
      makePlan({ id: 'b', outputName: '失败的.mp4', status: 'failed' }),
    ]);

    // 非终态不给「删除记录」；终态不给「停止」（[05 §2.1]/[05 §2.2] 的状态机投影）
    expect(within(rowOf('下载中的.mp4')).queryByRole('button', { name: '删除记录' })).toBeNull();
    expect(within(rowOf('失败的.mp4')).queryByRole('button', { name: '停止' })).toBeNull();

    fireEvent.click(within(rowOf('下载中的.mp4')).getByRole('button', { name: '停止' }));
    await waitFor(() => {
      expect(mocks.planStop).toHaveBeenCalledWith('a');
    });

    fireEvent.click(within(rowOf('失败的.mp4')).getByRole('button', { name: '重试' }));
    await waitFor(() => {
      expect(mocks.planRetry).toHaveBeenCalledWith('b');
    });
  });

  it('删除记录必须二次确认（[09 §5]），确认后才调用 PlanRemove', async () => {
    await renderWith([makePlan({ id: 'a', outputName: '失败的.mp4', status: 'failed' })]);

    fireEvent.click(screen.getByRole('button', { name: '删除记录' }));

    expect(mocks.planRemove).not.toHaveBeenCalled();
    expect(screen.getByText('确认删除「失败的.mp4」的记录？')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '确认删除' }));

    await waitFor(() => {
      expect(mocks.planRemove).toHaveBeenCalledWith('a');
    });
  });

  it('操作失败时把原因显示在该行（不静默吞错）', async () => {
    mocks.planRetry.mockResolvedValue({ ok: false, message: '该任务当前不可重试' });
    await renderWith([makePlan({ id: 'a', outputName: '失败的.mp4', status: 'failed' })]);

    fireEvent.click(screen.getByRole('button', { name: '重试' }));

    expect(await screen.findByText('该任务当前不可重试')).toBeInTheDocument();
  });

  it('按钮可用性只认后端的派生字段（B-313）：canRetry=false 的失败行不给「重试」', async () => {
    await renderWith([
      makePlan({
        id: 'a',
        outputName: '不可重试的失败.mp4',
        status: 'failed',
        errorCode: 'context_expired',
        errorMessage: '任务上下文已失效，请重新创建任务',
        // 后端判定：错误码不可重试 / 地址无法重建
        canRetry: false,
      }),
    ]);

    const row = rowOf('不可重试的失败.mp4');
    expect(within(row).queryByRole('button', { name: '重试' })).not.toBeInTheDocument();
    // 失败原因照实显示，不给一个按不了又不说原因的按钮
    expect(within(row).getByText(/任务上下文已失效/)).toBeInTheDocument();
  });

  it('被单次读取上限截断时如实说明（[03 §4.5] 的 200 条），不静默截断', async () => {
    await renderWith([makePlan({ id: 'a', outputName: '最近的一条.mp4' })], 250);

    expect(screen.getByText('共 250 条任务记录，当前只读取并显示最近 1 条。')).toBeInTheDocument();
  });

  it('列表读取失败时说明原因，而不是假装没有任务', async () => {
    mocks.plansList.mockResolvedValue({ ok: false, message: '桌面端未提供任务列表接口' });

    render(<MediaPage />);

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '任务列表未能从桌面端读取：桌面端未提供任务列表接口',
    );
  });

  it('行数超过阈值时启用虚拟滚动：DOM 行数有上界，且容器不被整体重建（[16 §4.5]）', async () => {
    const items = Array.from({ length: 1000 }, (_, index) =>
      makePlan({ id: `p${index}`, outputName: `任务 ${index}.mp4` }),
    );
    await renderWith(items);

    const rows = screen.getAllByRole('listitem');
    // 视口 600 / 行高 200 ≈ 3 行可见；即便加上缓冲也远小于 1000（[09 PF-2]）
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(40);
    // 行高固定：容器高度按 1000 × 常量行高算出来，而不是测量出来的
    const list = screen.getByRole('list');
    expect(list).toHaveStyle({ height: `${1000 * PLAN_ROW_HEIGHT}px` });
  });

  it('启动时直接对齐一次列表，之后由事件驱动（不轮询）', async () => {
    await renderWith([makePlan({ id: 'a', outputName: '下载中的.mp4' })]);

    expect(mocks.plansList).toHaveBeenCalledTimes(1);
    expect(mocks.subscribePlanProgress).toHaveBeenCalledTimes(1);
  });
});
