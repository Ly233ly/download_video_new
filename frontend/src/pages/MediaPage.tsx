import { Inbox } from 'lucide-react';
import { useEffect } from 'react';

import { Button } from '../components/Button';
import { NoticeBar } from '../components/NoticeBar';
import { PageHeading } from '../components/PageHeading';
import { PlanList } from '../components/PlanList';
import { usePlansStore } from '../stores/plans';

/**
 * 下载任务页（默认页，[09 §2.1]）。
 *
 * 状态全部来自 `stores/plans`——它订阅 [04 §4] 的三个事件，**不轮询**
 * （[09 `PF-1`]）。本页只做投影：
 *
 *  - 只有 `completed` 显示 100%、显示最终路径与「打开所在文件夹」（B-309、B-312）；
 *  - 行状态一律照后端投影，不自行推断完成（B-313）；
 *  - 列表连续滚动，**无分页控件**（B-310）。
 *
 * 尚未落地（如实登记，避免看起来"做完了"）：
 *  - 摘要统计卡（活动任务数、总进度、缓存入口）属 [09 §9] 的 `U2`（阶段 6）；
 *  - 行详情的展开（[09 §3.1] 的「详情」）会改变行高，与 [16 §4.5] 的固定行高冲突，
 *    待阶段 6 连同 `F5`（行高与行内布局）一起定。
 */
export function MediaPage() {
  const rowCount = usePlansStore((state) => state.order.length);
  const loadError = usePlansStore((state) => state.loadError);
  const truncated = usePlansStore((state) => state.truncated);
  const confirmRemoveId = usePlansStore((state) => state.confirmRemoveId);
  const confirmName = usePlansStore((state) =>
    state.confirmRemoveId === null ? '' : (state.byId[state.confirmRemoveId]?.outputName ?? ''),
  );
  const cancelRemove = usePlansStore((state) => state.cancelRemove);
  const remove = usePlansStore((state) => state.remove);
  const start = usePlansStore((state) => state.start);
  const detach = usePlansStore((state) => state.detach);

  // 启动对齐 + 事件订阅；卸载时取消（[04 §4.4]、[16 §4.1] `P-109`）。
  // 刻意不在这里放任何定时器：前端不轮询（[09 `PF-1`]）。
  useEffect(() => {
    start();
    return () => {
      detach();
    };
  }, [start, detach]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeading title="下载任务" sub="连续滚动，无分页；进度由后端推送实时更新" />

      {/* 读取失败要说出来，不能让空列表冒充"没有任务"（B-313 的同一取向） */}
      {loadError !== null && (
        <NoticeBar tone="error">任务列表未能从桌面端读取：{loadError}</NoticeBar>
      )}

      {/* 单次读取有上限（[03 §4.5] 的 200 条）：被截断时必须说明，不做静默截断 */}
      {truncated !== null && (
        <NoticeBar>
          共 {truncated.total} 条任务记录，当前只读取并显示最近 {truncated.shown} 条。
        </NoticeBar>
      )}

      {/* [09 §5]：破坏性操作必须二次确认。确认条放在列表外，行高因此不受影响。 */}
      {confirmRemoveId !== null && (
        <NoticeBar>
          <span>
            {confirmName === '' ? '确认删除这条记录？' : `确认删除「${confirmName}」的记录？`}
          </span>
          <span className="ml-2 inline-flex gap-2 align-middle">
            <Button variant="danger" onClick={() => void remove(confirmRemoveId)}>
              确认删除
            </Button>
            <Button onClick={cancelRemove}>取消</Button>
          </span>
        </NoticeBar>
      )}

      {/* 卡片：设计稿 .card（主表面 + 1px 描边 + 圆角 12 + 极轻阴影） */}
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-card border border-line bg-surface shadow-card">
        {rowCount === 0 ? <EmptyPlans /> : <PlanList />}
      </div>
    </div>
  );
}

/**
 * 空态（[09 §3.1] 的列表区）。
 *
 * 读取失败时它也照旧渲染：此时"本地还没有记录"是实话，失败原因由上面的提示条说明。
 */
function EmptyPlans() {
  return (
    <div className="flex flex-1 items-center justify-center px-[17px] py-16">
      <div className="text-center">
        <Inbox className="mx-auto text-content-3" size={28} strokeWidth={1.6} />
        <p className="mt-3 text-[14px] font-semibold">还没有下载任务</p>
        <p className="mt-1 max-w-[380px] text-[12.5px] text-content-2">
          在浏览器里点扩展图标，选好画质与保存位置，任务就会出现在这里。
        </p>
      </div>
    </div>
  );
}
