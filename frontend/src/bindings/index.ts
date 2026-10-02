/**
 * **唯一**调用 Wails 的地方（[16 §3.1] 的 S1）。
 *
 * 组件与 store **不得**直接访问 `window.go` 或 Wails 运行时——否则绑定层一变，
 * 全仓都要改。所有方法在这里做三件事：
 *   1. 类型化包装；
 *   2. 把后端的失败转成 `Result`（**不得**把失败静默当成功，[16 §3.1]）；
 *   3. 后端缺席（例如纯浏览器里跑测试）时给出明确结果，而不是抛未捕获异常。
 *
 * 本文件只搬数据，**不做业务判断**：完成与否、能不能重试，判断在 Go 侧
 * （[02 B-313]：界面只投影真实状态；[16 §3.1]）。
 */
import { isTheme, type Theme } from '../theme/theme';

export type Result<T> = { ok: true; value: T } | { ok: false; message: string };

/* ------------------------------------------------------------------ *
 * 计划的线上类型
 *
 * 字段**逐条对齐 Go 侧的投影**：`internal/service/plans.go` 的 `PlanView` / `Paged`
 * 与 `plans_validate.go` 的 `CreatePlanRequest`（[04 §7] 的 I2 已由那边定稿）。
 * 命名即 Go 的 json tag；没有 tag 的结构体（`CreatePlanRequest`）按字段名的大小写
 * 不敏感匹配，所以这里用 camelCase。
 * ------------------------------------------------------------------ */

/** 计划状态（[03 §3.1]、[05 §2]）。终态：completed / failed / canceled。 */
export type PlanStatus = 'queued' | 'running' | 'completed' | 'failed' | 'canceled';

/** 阶段，只在 `running` 内推进（[05 §2.1]）。 */
export type PlanPhase = '' | 'downloading' | 'merging' | 'validating';

/** 媒体类型（[03 §3.3]）。 */
export type MediaKind = 'direct' | 'hls' | 'dash' | 'page' | 'wechat' | 'browser';

/** 合并方式（[03 §3.3]）。 */
export type MergeMode = 'single' | 'av' | 'subtitles';

/** 输出容器（[03 §3.3]）。 */
export type OutputContainer = 'mp4' | 'mkv' | 'webm' | 'm4a' | 'mp3' | 'ts';

/**
 * 计划视图（[04 §3.2] 各计划方法的返回值）。
 *
 * 带 `?` 的字段在 Go 侧是 `omitempty`（空值时整个键不出现），因此**必须**按
 * "可能不存在"处理，不能用 `=== null` 判空。
 */
export interface PlanView {
  id: string;
  /** 归一化且去敏感后的来源地址（[03 §2.1]）；媒体地址不落库（B-722）。 */
  sourceUrl: string;
  sourceTitle: string;
  mediaKind: MediaKind;
  outputName: string;
  outputContainer: OutputContainer;
  mergeMode: MergeMode;
  qualityLabel: string;

  status: PlanStatus;
  phase: PlanPhase;
  /** 0–100。**只有 `completed` 允许是 100**（B-312）。 */
  progress: number;
  downloadedBytes: number;
  /** `null` 表示长度未知，**不得**当成 0（[03 §2.1]）。 */
  totalBytes: number | null;
  phaseDetail: string;

  /** `completed` 时必须非空（B-309、[03 §2]）。 */
  finalPath?: string;
  previewPath?: string;

  attemptCount: number;
  nextAttemptAt?: number;
  errorCode?: string;
  errorMessage?: string;

  importToEagle: boolean;
  deleteAfterImport: boolean;
  createdAt: number;
  updatedAt: number;
  completedAt?: number;

  /**
   * 后端派生布尔（[02 B-313]：判断逻辑**只存在于 Go 侧**，界面只投影）。
   *
   * 按钮可用性一律用它们，界面**不得**再从 `status` 自行推断：例如 `canRetry`
   * 还取决于错误码是否可重试、媒体地址能否重建，这些前端都不知道。
   */
  canRetry: boolean;
  canStop: boolean;
  /** `completed` 且最终路径非空——对应 B-309 的「打开所在文件夹」。 */
  canOpen: boolean;
  /** 补导 Eagle（B-211：Eagle 不可用时为 false）。阶段 4 用。 */
  canImport: boolean;
}

/**
 * 分页信封（[04 §3.2] 的 `Paged<T>`）。
 *
 * `limit` 是**实际生效**的值：后端会把它夹进 [03 §4.5] 的上限并回显（单次最多 200 条）。
 */
export interface Paged<T> {
  items: T[];
  total: number;
  offset: number;
  limit: number;
}

/**
 * `PlansList` 入参（[04 §3.2]、Go 侧 `PlansListRequest`）。
 *
 * B-310 禁止分页控件，但绑定层必须显式给 `offset`/`limit`（[16 §3.1] 的 B3）。
 */
export interface PlansListQuery {
  /** 空数组表示不按状态过滤。 */
  statuses: PlanStatus[];
  offset: number;
  limit: number;
}

/** 轨道选择（[05 §3.1]、Go 侧 `StreamTrack`）。 */
export interface StreamTrack {
  kind: 'video' | 'audio' | 'subtitle';
  index: number;
  quality?: string;
  bitrate?: number;
}

/**
 * `PlanCreate` 入参（[05 §3.1]、Go 侧 `CreatePlanRequest`）。
 *
 * **刻意不暴露 `headers` 与 `wechat`**：那是会话凭据与视频号解密上下文（B-303、B-722），
 * 桌面界面永远不持有它们——走那条路的是捕获链路自己。`url` 同样只在内存使用，
 * 落库的是后端归一化且去敏感后的 `sourceUrl`。
 */
export interface CreatePlanRequest {
  /** 媒体地址或页面地址，按 `mediaKind` 解释。 */
  url: string;
  pageUrl?: string;
  mediaKind: MediaKind;
  sourceTitle?: string;
  outputName: string;
  outputContainer: OutputContainer;
  mergeMode: MergeMode;
  qualityLabel?: string;
  /** 省略表示由下载引擎自行解析；显式空数组会被后端按 `missing_media` 拒绝。 */
  tracks?: StreamTrack[];
  importToEagle?: boolean;
  deleteAfterImport?: boolean;
}

/* ------------------------------------------------------------------ *
 * 事件名与载荷（[04 §4.1]/[04 §4.2]，同名常量在 Go 侧 `internal/ui/events.go`）
 * ------------------------------------------------------------------ */

const EventPlanChanged = 'planChanged';
const EventPlansChanged = 'plansChanged';
const EventPlanProgress = 'planProgress';

/** Go 侧 `window.go.ui.UI`（Wails 绑定路径）。方法可能缺席——后端未落地时就是如此。 */
interface UiBinding {
  GetTheme(): Promise<string>;
  SetTheme(theme: string): Promise<void>;
  PlansList(query: PlansListQuery): Promise<unknown>;
  PlanGet(id: string): Promise<unknown>;
  PlanCreate(request: CreatePlanRequest): Promise<unknown>;
  PlanStop(id: string): Promise<unknown>;
  PlanRetry(id: string): Promise<unknown>;
  PlanRemove(id: string): Promise<void>;
  PlanOpenOutput(id: string): Promise<void>;
}

/** Wails 注入的事件通道（`frontend/wailsjs/runtime/runtime.js` 用的就是 `window.runtime`）。 */
interface WailsRuntime {
  EventsOn(eventName: string, callback: (...args: unknown[]) => void): () => void;
}

function backend(): Partial<UiBinding> | null {
  const wails = (globalThis as { go?: { ui?: { UI?: Partial<UiBinding> } } }).go;
  return wails?.ui?.UI ?? null;
}

function wailsRuntime(): WailsRuntime | null {
  return (globalThis as { runtime?: WailsRuntime }).runtime ?? null;
}

/** 后端是否可用（纯前端调试与测试时为 false）。 */
export function hasBackend(): boolean {
  return backend() !== null;
}

function failure(error: unknown): Result<never> {
  if (typeof error === 'string' && error.trim() !== '') {
    // Wails 把 Go 侧 `error` 直接当拒绝原因抛出，可能不是 Error 实例。
    return { ok: false, message: error };
  }
  const message = error instanceof Error ? error.message : '后端调用失败';
  return { ok: false, message };
}

/** 方法是否已在绑定对象上就绪。后端未落地时 Wails 不会生成这些方法，调用会抛 TypeError。 */
function method<K extends keyof UiBinding>(name: K): UiBinding[K] | null {
  const api = backend();
  const fn = api?.[name];
  return typeof fn === 'function' ? (fn as UiBinding[K]) : null;
}

/**
 * 计划接口是否可用。
 *
 * 两样东西缺一不可：方法本身与事件通道。缺方法（后端未落地）时界面只能说明
 * "未连接"；缺事件通道时列表拉一次就再也不会更新，同样不可用。
 */
export function hasPlanBindings(): boolean {
  return backend() !== null && method('PlansList') !== null && wailsRuntime() !== null;
}

/* ------------------------------------------------------------------ *
 * 主题
 * ------------------------------------------------------------------ */

/** 读取持久化的主题。后端不可用或取值非法时，分别给出明确结果。 */
export async function getTheme(): Promise<Result<Theme>> {
  const call = method('GetTheme');
  if (!call) {
    return { ok: false, message: '桌面端未连接' };
  }
  try {
    const value = await call();
    if (!isTheme(value)) {
      return { ok: false, message: '桌面端返回了未知的主题值' };
    }
    return { ok: true, value };
  } catch (error) {
    return failure(error);
  }
}

/** 持久化主题；失败必须让调用方知道（B-804 的持久值要与界面一致）。 */
export async function setTheme(theme: Theme): Promise<Result<void>> {
  const call = method('SetTheme');
  if (!call) {
    return { ok: false, message: '桌面端未连接，主题未保存' };
  }
  try {
    await call(theme);
    return { ok: true, value: undefined };
  } catch (error) {
    return failure(error);
  }
}

/* ------------------------------------------------------------------ *
 * 计划（[04 §3.2]）
 * ------------------------------------------------------------------ */

function notConnected(what: string): Result<never> {
  return { ok: false, message: `桌面端未提供${what}接口` };
}

/** 校验一个来自后端的计划视图：只认关键判别字段，认不出就明确报错（不返回半成品）。 */
function parsePlan(value: unknown): Result<PlanView> {
  if (typeof value !== 'object' || value === null) {
    return { ok: false, message: '桌面端返回了无法识别的任务记录' };
  }
  const plan = value as Partial<PlanView>;
  if (typeof plan.id !== 'string' || plan.id === '' || !isPlanStatus(plan.status)) {
    return { ok: false, message: '桌面端返回了无法识别的任务记录' };
  }
  return { ok: true, value: plan as PlanView };
}

function isPlanStatus(value: unknown): value is PlanStatus {
  return (
    value === 'queued' ||
    value === 'running' ||
    value === 'completed' ||
    value === 'failed' ||
    value === 'canceled'
  );
}

/** 计划列表（[04 §3.2]）。B-310 不分页，但入参必须显式给出 `offset`/`limit`（[16 §3.1] B3）。 */
export async function plansList(query: PlansListQuery): Promise<Result<Paged<PlanView>>> {
  const call = method('PlansList');
  if (!call) {
    return notConnected('任务列表');
  }
  try {
    const raw = await call(query);
    const items = (raw as Partial<Paged<unknown>> | null)?.items;
    if (!Array.isArray(items)) {
      return { ok: false, message: '桌面端返回了无法识别的任务列表' };
    }
    return { ok: true, value: { ...(raw as Paged<PlanView>), items: items as PlanView[] } };
  } catch (error) {
    return failure(error);
  }
}

/** 单个计划（[04 §3.2]）。 */
export async function planGet(id: string): Promise<Result<PlanView>> {
  const call = method('PlanGet');
  if (!call) {
    return notConnected('任务详情');
  }
  try {
    return parsePlan(await call(id));
  } catch (error) {
    return failure(error);
  }
}

/** 创建下载计划（[04 §3.2]）。重复提交由调用方禁止连点（[04 §3.2] 幂等性一节）。 */
export async function planCreate(request: CreatePlanRequest): Promise<Result<PlanView>> {
  const call = method('PlanCreate');
  if (!call) {
    return notConnected('创建任务');
  }
  try {
    return parsePlan(await call(request));
  } catch (error) {
    return failure(error);
  }
}

/** 停止（[05 §10]：非终态 → `canceled`）。 */
export async function planStop(id: string): Promise<Result<PlanView>> {
  const call = method('PlanStop');
  if (!call) {
    return notConnected('停止任务');
  }
  try {
    return parsePlan(await call(id));
  } catch (error) {
    return failure(error);
  }
}

/** 重试（[05 §2.2]：只有 `failed` 能手动重试，且取决于错误码与地址能否重建）。 */
export async function planRetry(id: string): Promise<Result<PlanView>> {
  const call = method('PlanRetry');
  if (!call) {
    return notConnected('重试任务');
  }
  try {
    return parsePlan(await call(id));
  } catch (error) {
    return failure(error);
  }
}

/** 删除记录（[04 §3.2]）。只删数据库记录，文件归属判定在 Go 侧（B-402/B-403）。 */
export async function planRemove(id: string): Promise<Result<void>> {
  const call = method('PlanRemove');
  if (!call) {
    return notConnected('删除任务记录');
  }
  try {
    await call(id);
    return { ok: true, value: undefined };
  } catch (error) {
    return failure(error);
  }
}

/**
 * 打开计划的输出所在目录（B-309 要求的入口）。
 *
 * ⚠ **后端尚未落地该方法**：[04 §3.2] 的 `PlanOpenOutput` 已在方法清单里，但
 * `internal/ui/plans.go` 目前只到 `PlanRemove`。落地之前点击会得到
 * "桌面端未提供打开所在文件夹接口"——界面如实显示，不假装成功。
 */
export async function planOpenOutput(id: string): Promise<Result<void>> {
  const call = method('PlanOpenOutput');
  if (!call) {
    return notConnected('打开所在文件夹');
  }
  try {
    await call(id);
    return { ok: true, value: undefined };
  } catch (error) {
    return failure(error);
  }
}

/* ------------------------------------------------------------------ *
 * 计划事件（[04 §4]）
 *
 * 每个事件一个订阅函数，**返回取消函数**（[16 §3.1]；卸载必须调用，见 §4.1 `P-109`）。
 * 载荷非法时忽略这一次，不让一个坏载荷打断订阅——事件不是数据库，权威在 Go 侧。
 * ------------------------------------------------------------------ */

function subscribe<T>(
  eventName: string,
  parse: (payload: unknown) => T | null,
  handler: (value: T) => void,
): () => void {
  const runtime = wailsRuntime();
  if (!runtime) {
    return () => {};
  }
  const off = runtime.EventsOn(eventName, (payload) => {
    const parsed = parse(payload);
    if (parsed !== null) {
      handler(parsed);
    }
  });
  // 取消函数必须真的可调用：调用方在卸载时会无差别地调用它（[16 §4.1] `P-109`）。
  return typeof off === 'function' ? off : () => {};
}

function parsePlanId(payload: unknown): string | null {
  if (typeof payload !== 'object' || payload === null) {
    return null;
  }
  const id = (payload as { id?: unknown }).id;
  return typeof id === 'string' && id !== '' ? id : null;
}

/** `planChanged`（载荷 `{id}`）：前端重新 `PlanGet(id)`（[04 §4.1]）。 */
export function subscribePlanChanged(handler: (id: string) => void): () => void {
  return subscribe(EventPlanChanged, parsePlanId, handler);
}

/** `plansChanged`（载荷 `{}`）：前端重新 `PlansList()`（[04 §4.1]）。 */
export function subscribePlansChanged(handler: () => void): () => void {
  return subscribe(EventPlansChanged, () => true, handler);
}

/** `planProgress`（载荷为**完整** `PlanView`）：前端直接覆盖该条（[04 §4.2]）。 */
export function subscribePlanProgress(handler: (view: PlanView) => void): () => void {
  return subscribe(
    EventPlanProgress,
    (payload) => {
      const parsed = parsePlan(payload);
      return parsed.ok ? parsed.value : null;
    },
    handler,
  );
}
