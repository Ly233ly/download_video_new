/**
 * 计划视图的**纯函数**映射层（[16 §3] 的 S3：`lib/` 无 DOM、无副作用，便于单测）。
 *
 * 这里只做"把后端投影过来的字段翻译成界面文案"，**不做业务判断**：
 * 完成与否由 `plan.status` 决定（B-313），界面不自行推断。
 */
import type { MediaKind, PlanPhase, PlanStatus, PlanView } from '../bindings';

/** 状态中文标签（[09 §3.1] 行信息）。措辞与设计稿的徽标一致。 */
export const PLAN_STATUS_LABEL: Record<PlanStatus, string> = {
  queued: '排队中',
  running: '下载中',
  completed: '已完成',
  failed: '失败',
  canceled: '已取消',
};

/** 阶段说明（[05 §2.1]：`phase` 只在 `running` 内推进）。 */
export const PLAN_PHASE_LABEL: Record<Exclude<PlanPhase, ''>, string> = {
  downloading: '下载中',
  merging: '合并中',
  validating: '校验中',
};

/** 徽标语义（设计稿 `.b-run` / `.b-ok` / `.b-err` / `.b-wait` / `.b-mute`）。 */
export type StatusTone = 'run' | 'ok' | 'err' | 'wait' | 'mute';

export function statusLabel(status: PlanStatus): string {
  return PLAN_STATUS_LABEL[status] ?? '未知状态';
}

export function phaseLabel(phase: PlanPhase): string {
  return phase === '' ? '' : (PLAN_PHASE_LABEL[phase] ?? '');
}

export function statusTone(status: PlanStatus): StatusTone {
  switch (status) {
    case 'running':
      return 'run';
    case 'completed':
      return 'ok';
    case 'failed':
      return 'err';
    case 'queued':
      return 'wait';
    default:
      return 'mute';
  }
}

/** 终态不再自动调度（[05 §2.2]）。 */
export function isTerminal(status: PlanStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'canceled';
}

/**
 * 界面显示的百分比。
 *
 * **只有 `completed` 允许 100%**（B-312）：其余状态一律夹到 0–99，
 * 免得 `99.6` 四舍五入成 `100%`——那等于界面自己宣布完成（B-313）。
 * 若后端给出了非终态却 `progress = 100` 的脏数据，这里也不会显示 100%。
 */
export function progressPercent(plan: Pick<PlanView, 'status' | 'progress'>): number {
  if (plan.status === 'completed') {
    return 100;
  }
  const raw =
    typeof plan.progress === 'number' && Number.isFinite(plan.progress) ? plan.progress : 0;
  return Math.max(0, Math.min(99, Math.round(raw)));
}

/** 进度条：`failed` 行不画（设计稿的失败行只有原因与操作）。 */
export function showsProgressBar(status: PlanStatus): boolean {
  return status !== 'failed';
}

/**
 * 可删除记录：终态（非终态由「停止」承接）。
 *
 * ⚠ 停止 / 重试 / 打开所在文件夹**不在这里判断**——Go 侧已派生 `canStop` /
 * `canRetry` / `canOpen`（[02 B-313]：判断逻辑只存在于那里），界面直接用那几个字段。
 * `PlanView` 目前**没有** `canRemove`，所以"删除记录"仍是界面按 [05 §2.2] 的终态
 * 规则做的投影；后端补上派生字段后应改为用它。
 */
export function canRemove(status: PlanStatus): boolean {
  return isTerminal(status);
}

/** 来源展示：行里只放主机名（设计稿 `bilibili.com`），完整地址在详情里。 */
export function sourceHost(url: string): string {
  if (typeof url !== 'string' || url === '') {
    return '';
  }
  try {
    return new URL(url).hostname;
  } catch {
    return '';
  }
}

/** 字节数格式化（设计稿 `12.4 MB / 20.1 MB`）。非有限值返回空串，不显示 `NaN`。 */
export function formatBytes(bytes: number | null | undefined): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) {
    return '';
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return unit === 0 ? `${value} ${units[unit]}` : `${value.toFixed(1)} ${units[unit]}`;
}

/** 已下载 / 总量（任一为空则只显示另一个，都没有则空串）。 */
export function byteRange(downloaded: number | null, total: number | null): string {
  return [formatBytes(downloaded), formatBytes(total)].filter((part) => part !== '').join(' / ');
}

/** 时间戳（秒，见 [03 §2] 的 `REAL` 列）→ 本地 `HH:MM`。 */
export function formatClock(seconds: number | null | undefined): string {
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds <= 0) {
    return '';
  }
  const date = new Date(seconds * 1000);
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}

function joinParts(parts: Array<string | null | undefined>): string {
  return parts.filter((part) => typeof part === 'string' && part !== '').join(' · ');
}

function attemptText(attemptCount: number): string {
  return typeof attemptCount === 'number' && attemptCount > 0 ? `第 ${attemptCount} 次尝试` : '';
}

/** 行副标题：来源主机 · 质量标签 · 容器（设计稿 `.meta`）。 */
export function rowMeta(plan: PlanView): string {
  return joinParts([sourceHost(plan.sourceUrl), plan.qualityLabel, plan.outputContainer]);
}

/**
 * 下载路径的中文名（[05 §4] 的 P1–P6）。
 *
 * 完整路径属于 [09 §3.1] 的「详情」展开，它与固定行高冲突、排在阶段 6（[16 §4.5]）；
 * 在那之前界面只在**发生改道时**用这个表（见 `routeNotice`）。
 */
export const MEDIA_KIND_LABEL: Record<MediaKind, string> = {
  direct: '直链',
  hls: 'HLS 清单',
  dash: 'DASH 清单',
  page: '页面解析',
  wechat: '视频号',
  browser: '浏览器中转',
};

/**
 * 路径中文名。**不认识的取值原样返回**：后端加了新路径而界面还没跟上时，
 * 显示 `wechat2` 也比显示「未知」有用。
 */
export function mediaKindLabel(kind: string): string {
  return (MEDIA_KIND_LABEL as Record<string, string | undefined>)[kind] ?? kind;
}

/**
 * 自动改道提示（B-316：实际路径必须对用户可见，**不得静默发生**）。
 *
 * 空串 = 没发生改道。`resolvedKind` 为 `null` 表示**还没开始执行**（[03 §2.1]），
 * 那不是改道，也不该提示。
 *
 * 落地位置：完整路径要等阶段 6 的详情展开；在那之前改道必须有个看得见的地方，
 * 所以并进状态说明行（不改变固定行高）。
 */
export function routeNotice(plan: Pick<PlanView, 'mediaKind' | 'resolvedKind'>): string {
  const actual = plan.resolvedKind;
  // `!actual` 同时挡住 null 与字段缺失两种"尚未执行"。
  if (!actual || actual === plan.mediaKind) {
    return '';
  }
  return `已自动改走${mediaKindLabel(actual)}（原判${mediaKindLabel(plan.mediaKind)}）`;
}

/**
 * 进度说明（设计稿 `.meta` 的第二行）。
 *
 * 相对时间（「30 秒后」）刻意不写：那要求一个每分钟跳动的定时器，
 * 与 [09 `PF-7`]/[09 `PF-1`] 的"空载无忙循环、前端不轮询"冲突，故用绝对时刻。
 */
export function progressCaption(plan: PlanView): string {
  switch (plan.status) {
    case 'completed':
      return joinParts([
        '已完成',
        `${progressPercent(plan)}%`,
        byteRange(plan.downloadedBytes, plan.totalBytes),
      ]);
    case 'running':
      return joinParts([
        phaseLabel(plan.phase) || '处理中',
        `${progressPercent(plan)}%`,
        byteRange(plan.downloadedBytes, plan.totalBytes),
        plan.phaseDetail,
      ]);
    case 'canceled':
      return joinParts([
        '已取消',
        `${progressPercent(plan)}%`,
        byteRange(plan.downloadedBytes, plan.totalBytes),
      ]);
    case 'queued':
      return joinParts([
        '排队中',
        attemptText(plan.attemptCount),
        plan.nextAttemptAt ? `下次重试 ${formatClock(plan.nextAttemptAt)}` : '',
      ]);
    default:
      // failed 的说明走失败原因行（设计稿），这里不重复。
      return '';
  }
}

/**
 * 失败原因（[09 §3.1]：`failed` 必须显示错误码对应的中文消息与质量档位）。
 *
 * **优先用后端给的 `errorMessage`**：它就是错误码对应的中文消息（[05 §7.4]），
 * 且由 Go 侧保证不含路径、URL 与秘密（[12 §3.3] 的 E3）。错误码→消息的表**不在前端复制**
 * ——那是 [12 §2]"一处事实，一处定义"要避免的第二份来源。缺消息时给通用兜底，
 * 原始错误码在详情里以等宽字体展示，便于排错。
 */
export function failureReason(
  plan: Pick<PlanView, 'status' | 'errorMessage' | 'attemptCount'>,
): string | null {
  if (plan.status !== 'failed') {
    return null;
  }
  const message =
    typeof plan.errorMessage === 'string' && plan.errorMessage.trim() !== ''
      ? plan.errorMessage.trim()
      : '下载未完成，请稍后重试';
  const retried =
    typeof plan.attemptCount === 'number' && plan.attemptCount > 0
      ? `已重试 ${plan.attemptCount} 次`
      : '';
  return joinParts([`失败原因：${message}`, retried]);
}
