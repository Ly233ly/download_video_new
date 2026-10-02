import type { PlanView } from '../bindings';

/**
 * 测试用的计划视图。
 *
 * 字段与 Go 侧 `internal/service/plans.go` 的 `PlanView` 逐条对齐；
 * `canStop` / `canRetry` / `canOpen` 在这里**按 Go 侧的派生规则**从状态推出来，
 * 只为让测试用例读起来连贯（真实值一律由后端给，界面不自行推断，见 B-313）。
 * 需要"后端说不允许"的场景，直接覆盖对应字段即可。
 *
 * 只 import 类型，因此**不会**把绑定层的运行时模块拉进测试图（[16 §3.1]）。
 */
export function makePlan(over: Partial<PlanView> & { id: string }): PlanView {
  const status = over.status ?? 'running';
  const finalPath = over.finalPath;
  return {
    sourceUrl: 'https://example.com/video',
    sourceTitle: '来源标题',
    mediaKind: 'direct',
    // 默认与提示一致（没发生降级）；要测降级的场景直接覆盖这个字段。
    resolvedKind: 'direct',
    outputName: '输出名.mp4',
    outputContainer: 'mp4',
    mergeMode: 'single',
    qualityLabel: '1080P',
    status,
    phase: 'downloading',
    progress: 10,
    downloadedBytes: 1024,
    totalBytes: 2048,
    phaseDetail: '',
    finalPath,
    attemptCount: 0,
    importToEagle: false,
    deleteAfterImport: false,
    createdAt: 1_758_921_234,
    updatedAt: 1_758_921_234,
    canStop: status === 'queued' || status === 'running',
    canRetry: status === 'failed',
    // 与 Go 一致：completed 且最终路径非空（B-309）
    canOpen: status === 'completed' && finalPath !== undefined && finalPath !== '',
    canImport: false,
    ...over,
  };
}
