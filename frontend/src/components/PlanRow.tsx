import {
  canRemove,
  failureReason,
  progressCaption,
  progressPercent,
  rowMeta,
  showsProgressBar,
  statusLabel,
  statusTone,
  type StatusTone,
} from '../lib/planView';
import { usePlansStore } from '../stores/plans';
import { Button } from './Button';

/**
 * 固定行高（[16 §4.5]：`estimateSize` 必须是**常量函数**，禁止 `measureElement` 测量式）。
 *
 * 取"内容最高的一行"的高度：设计稿的一行含标题、来源、进度条、说明、路径与操作，
 * 按 [09 §4.5] 的字号算下来内容约 165px，加上下内边距 30px 定成 200px，留一点余量
 * 以免字体度量差异把操作行挤出可视区。具体像素值与行内布局属 [16 §7] 的 `F5`
 * （阶段 6 定稿）。
 */
export const PLAN_ROW_HEIGHT = 200;

/** 状态徽标配色（设计稿 `.b-run` / `.b-ok` / `.b-err` / `.b-wait` / `.b-mute`）。 */
const TONE_CLASS: Record<StatusTone, string> = {
  run: 'bg-accent-soft text-accent',
  ok: 'bg-ok/12 text-ok',
  err: 'bg-err/10 text-err',
  wait: 'bg-warn/12 text-warn',
  mute: 'bg-track text-content-2',
};

/**
 * 一行下载计划。**独立组件**（[16 §4.2] R1）：只有本行的状态变化才重渲染它，
 * 兄弟行与列表容器都不受影响（R4）。
 *
 * `offset` 只有虚拟滚动分支才传（绝对定位的行内偏移）。
 */
export function PlanRow({ id, offset }: { id: string; offset?: number }) {
  // 订阅粒度到行：本行的记录换了引用才重渲染（[16 §4.6]）。
  const plan = usePlansStore((state) => state.byId[id]);
  const rowError = usePlansStore((state) => state.rowErrors[id]);
  const pending = usePlansStore((state) => state.pending[id]);
  // store 的 action 是稳定引用，直接取来用（[16 §4.2] R3）。
  const stop = usePlansStore((state) => state.stop);
  const retry = usePlansStore((state) => state.retry);
  const openFolder = usePlansStore((state) => state.openFolder);
  const askRemove = usePlansStore((state) => state.askRemove);

  if (!plan) {
    // 行已被删除（order 与 byId 同步更新，这里只是防御）。
    return null;
  }

  const percent = progressPercent(plan);
  const caption = progressCaption(plan);
  const reason = failureReason(plan);
  const done = plan.status === 'completed';

  return (
    <li
      aria-label={plan.outputName}
      style={
        offset === undefined
          ? { height: PLAN_ROW_HEIGHT }
          : {
              height: PLAN_ROW_HEIGHT,
              position: 'absolute',
              top: 0,
              left: 0,
              width: '100%',
              transform: `translateY(${offset}px)`,
            }
      }
      // 设计稿 .row：内边距 15px 17px、行间 1px 描边、hover 次级表面（不用反白）
      className="flex items-start gap-3 border-b border-line px-[17px] py-[15px] transition-colors duration-100 hover:bg-surface-2"
    >
      {/* 设计稿 .thumb（64×40、圆角 6）：本阶段没有预览图（[04 §7] 的 I7），
          只放占位。设计稿的微渐变取值未登记在 [09 §4.4] 的 token 里，
          这里只用既有 token 的底色 + 1px 描边，不新造颜色。 */}
      <span
        aria-hidden
        className="h-10 w-16 flex-none rounded-[6px] border border-line bg-surface-2"
      />

      <div className="flex h-full min-w-0 flex-1 flex-col">
        <p className="truncate font-semibold">{plan.outputName}</p>
        <p className="mt-[3px] truncate text-[12.5px] text-content-3">{rowMeta(plan)}</p>

        {showsProgressBar(plan.status) && (
          <div
            role="progressbar"
            aria-label={`${plan.outputName} 的下载进度`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={percent}
            // 设计稿 .bar：高 6、全圆角、轨道色底
            className="mt-[9px] mb-[7px] h-[6px] overflow-hidden rounded-pill bg-track"
          >
            <span
              className={`block h-full rounded-pill transition-[width] duration-300 ${done ? 'bg-ok' : 'bg-accent'}`}
              style={{ width: `${percent}%` }}
            />
          </div>
        )}

        {/* 状态说明行。操作失败的原因**并入这一行**（而不是另起一行）：
            行高是固定常量（[16 §4.5]），多一行就会把下面的操作挤出可视区。
            这一行**不是** live region——进度每 200 ms 更新一次，做成 live region
            会把读屏器刷屏；只有失败信息那个 span 带 role="status"。 */}
        {(caption !== '' || rowError) && (
          <p className="truncate text-[12.5px] text-content-3">
            {caption}
            {caption !== '' && rowError ? ' · ' : ''}
            {rowError && (
              <span className="text-err" role="status">
                {rowError}
              </span>
            )}
          </p>
        )}

        {reason !== null && (
          <p className="mt-[6px] truncate text-[12.5px] text-content-3">{reason}</p>
        )}

        {/* B-309：`completed` 必须显示最终路径。等宽字体按 [09 §4.5]（路径用等宽）。 */}
        {done && plan.finalPath && (
          <p className="mt-[6px] truncate rounded-[6px] border border-line bg-surface-2 px-[9px] py-[4px] font-mono text-[12px] text-content-2">
            {plan.finalPath}
          </p>
        )}

        {/* 操作固定在行底：行高固定，内容多少都不改变按钮位置（[16 §4.5]） */}
        <div className="mt-auto flex flex-wrap items-center gap-2 pt-[10px]">
          {/* 可用性一律用后端派生字段（canStop/canRetry/canOpen，[02 B-313]）：
              重试还取决于错误码是否可重试、地址能否重建，前端无从判断，也不得猜。
              缺字段时按钮不显示——这是"只投影真实状态"的必然结果。 */}
          {plan.canStop && (
            <Button variant="danger" disabled={pending} onClick={() => void stop(id)}>
              停止
            </Button>
          )}
          {plan.canRetry && (
            <Button variant="primary" disabled={pending} onClick={() => void retry(id)}>
              重试
            </Button>
          )}
          {plan.canOpen && (
            <Button disabled={pending} onClick={() => void openFolder(id)}>
              打开所在文件夹
            </Button>
          )}
          {canRemove(plan.status) && (
            <Button variant="danger" disabled={pending} onClick={() => askRemove(id)}>
              删除记录
            </Button>
          )}
        </div>
      </div>

      <span
        className={`flex-none rounded-pill px-2 py-[2px] text-[11.5px] font-semibold ${TONE_CLASS[statusTone(plan.status)]}`}
      >
        {statusLabel(plan.status)}
      </span>
    </li>
  );
}
