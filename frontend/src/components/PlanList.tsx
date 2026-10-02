import { useVirtualizer } from '@tanstack/react-virtual';
import { useMemo, useRef } from 'react';

import { usePlansStore } from '../stores/plans';
import { PLAN_ROW_HEIGHT, PlanRow } from './PlanRow';

/** 行数**超过**它才启用虚拟滚动（[16 §4.5]，与 [07 `P-105`] 的阈值一致）。 */
export const PLAN_VIRTUAL_THRESHOLD = 50;

/** 缓冲行用固定行数，禁止无限缓冲（[16 §4.5]）。 */
const OVERSCAN = 6;

/**
 * `estimateSize` 必须是**常量函数**（[16 §4.5]：禁止测量式），
 * 且引用稳定（[16 §5.3] M4）——所以定义在模块级，不在渲染里新建。
 */
const estimateRowSize = () => PLAN_ROW_HEIGHT;

/**
 * 计划列表容器。
 *
 * 两条渲染纪律：
 *
 *  - 容器**只订阅 `order`**（[16 §4.2] R4）：一条进度推送不动 `order` 的引用，
 *    因此容器不重渲染，只有那一行重渲染（[16 §4.6]）。
 *  - 行数超过阈值时只挂可见区间的行（[16 §4.5]）：DOM 节点数有上界（[09 `PF-2`]）。
 */
export function PlanList() {
  const order = usePlansStore((state) => state.order);
  const scrollRef = useRef<HTMLDivElement>(null);

  // opts 对象必须稳定（[16 §5.3] M4）：只随行数变化，不在每次渲染里新建。
  const options = useMemo(
    () => ({
      count: order.length,
      getScrollElement: () => scrollRef.current,
      estimateSize: estimateRowSize,
      overscan: OVERSCAN,
    }),
    [order.length],
  );
  const virtualizer = useVirtualizer(options);

  const virtual = order.length > PLAN_VIRTUAL_THRESHOLD;
  const virtualItems = virtual ? virtualizer.getVirtualItems() : [];

  return (
    <div ref={scrollRef} className="main-scroll min-h-0 flex-1 overflow-y-auto">
      <ul
        className="relative"
        style={virtual ? { height: `${virtualizer.getTotalSize()}px` } : undefined}
      >
        {virtual
          ? virtualItems.map((item) => (
              // key 用业务稳定 ID（[16 §4.2] R2），不用下标
              <PlanRow key={order[item.index]} id={order[item.index]} offset={item.start} />
            ))
          : order.map((id) => <PlanRow key={id} id={id} />)}
      </ul>
    </div>
  );
}
