import { describe, expect, it } from 'vitest';

import type { PlanView } from '../bindings';
import { makePlan } from '../test/planFixture';
import * as planViewModule from './planView';
import {
  byteRange,
  canRemove,
  failureReason,
  formatBytes,
  isTerminal,
  mediaKindLabel,
  phaseLabel,
  progressCaption,
  progressPercent,
  routeNotice,
  rowMeta,
  showsProgressBar,
  sourceHost,
  statusLabel,
  statusTone,
} from './planView';

describe('计划视图映射', () => {
  describe('进度百分比（B-312 / B-313）', () => {
    it('只有 completed 显示 100%', () => {
      expect(progressPercent(makePlan({ id: 'a', status: 'completed', progress: 100 }))).toBe(100);
      expect(progressPercent(makePlan({ id: 'a', status: 'running', progress: 62 }))).toBe(62);
      expect(progressPercent(makePlan({ id: 'a', status: 'canceled', progress: 62 }))).toBe(62);
    });

    it('非 completed 即使后端给了 100 也不显示 100%', () => {
      for (const status of ['queued', 'running', 'failed', 'canceled'] as const) {
        expect(progressPercent(makePlan({ id: 'a', status, progress: 100 }))).toBe(99);
      }
    });

    it('四舍五入不得把 99.6 变成 100%', () => {
      expect(progressPercent(makePlan({ id: 'a', status: 'running', progress: 99.6 }))).toBe(99);
    });

    it('异常取值夹在 0–99，不显示 NaN 或负数', () => {
      expect(progressPercent(makePlan({ id: 'a', status: 'running', progress: Number.NaN }))).toBe(
        0,
      );
      expect(progressPercent(makePlan({ id: 'a', status: 'running', progress: -5 }))).toBe(0);
      expect(progressPercent(makePlan({ id: 'a', status: 'running', progress: 1e9 }))).toBe(99);
    });
  });

  describe('状态与阶段文案', () => {
    it('五种状态都有中文标签与语义色', () => {
      expect(statusLabel('queued')).toBe('排队中');
      expect(statusLabel('running')).toBe('下载中');
      expect(statusLabel('completed')).toBe('已完成');
      expect(statusLabel('failed')).toBe('失败');
      expect(statusLabel('canceled')).toBe('已取消');
      expect(statusTone('failed')).toBe('err');
      expect(statusTone('completed')).toBe('ok');
      expect(statusTone('running')).toBe('run');
    });

    it('阶段说明只在 running 内有值（[05 §2.1]）', () => {
      expect(phaseLabel('downloading')).toBe('下载中');
      expect(phaseLabel('merging')).toBe('合并中');
      expect(phaseLabel('validating')).toBe('校验中');
      expect(phaseLabel('')).toBe('');
    });
  });

  describe('行信息字段映射', () => {
    it('副标题为 来源主机 · 质量标签 · 容器（设计稿 .meta）', () => {
      const row = makePlan({
        id: 'a',
        sourceUrl: 'https://www.bilibili.com/video/BV1?x=1',
        qualityLabel: '1080P',
        outputContainer: 'mp4',
      });
      expect(rowMeta(row)).toBe('www.bilibili.com · 1080P · mp4');
    });

    it('地址不可解析或缺字段时不留空档', () => {
      expect(
        rowMeta(makePlan({ id: 'a', sourceUrl: '', qualityLabel: '', outputContainer: 'mkv' })),
      ).toBe('mkv');
    });

    it('主机名解析失败返回空串，不抛异常', () => {
      expect(sourceHost('not a url')).toBe('');
      expect(sourceHost('')).toBe('');
      expect(sourceHost('https://example.com:8443/a')).toBe('example.com');
    });

    it('路径中文名不认识的取值原样返回', () => {
      expect(mediaKindLabel('direct')).toBe('直链');
      expect(mediaKindLabel('page')).toBe('页面解析');
      expect(mediaKindLabel('wechat2')).toBe('wechat2');
    });

    it('没改道就没有提示（B-316 只在真的改道时才出现）', () => {
      // 实际路径与提示一致
      expect(routeNotice(makePlan({ id: 'a', mediaKind: 'hls', resolvedKind: 'hls' }))).toBe('');
      // 还没开始执行：resolvedKind 是 null，不是改道（[03 §2.1]）
      expect(routeNotice(makePlan({ id: 'a', status: 'queued', resolvedKind: null }))).toBe('');
    });

    it('改道后提示实际路径与原判，不静默发生（B-316）', () => {
      // [05 §4.0] 第二步：提示直链、实际是清单
      expect(routeNotice(makePlan({ id: 'a', mediaKind: 'direct', resolvedKind: 'hls' }))).toBe(
        '已自动改走HLS 清单（原判直链）',
      );
      // 提示直链、实际走了页面解析
      expect(routeNotice(makePlan({ id: 'a', mediaKind: 'direct', resolvedKind: 'page' }))).toBe(
        '已自动改走页面解析（原判直链）',
      );
    });

    it('进度说明含阶段、百分比与字节数', () => {
      const caption = progressCaption(
        makePlan({
          id: 'a',
          status: 'running',
          phase: 'downloading',
          progress: 62,
          downloadedBytes: 13_002_342,
          totalBytes: 21_076_787,
        }),
      );
      expect(caption).toContain('下载中');
      expect(caption).toContain('62%');
      expect(caption).toContain('12.4 MB');
      expect(caption).toContain('20.1 MB');
    });

    it('排队中显示尝试次数与下次重试时刻（用绝对时刻，不引入跳动定时器）', () => {
      const next = new Date(2026, 9, 2, 14, 32, 5).getTime() / 1000;
      const caption = progressCaption(
        makePlan({ id: 'a', status: 'queued', attemptCount: 2, nextAttemptAt: next }),
      );
      expect(caption).toContain('排队中');
      expect(caption).toContain('第 2 次尝试');
      expect(caption).toContain('下次重试 14:32');
    });

    it('失败行不画进度条，说明走失败原因行', () => {
      const row = makePlan({ id: 'a', status: 'failed', progress: 62 });
      expect(showsProgressBar(row.status)).toBe(false);
      expect(progressCaption(row)).toBe('');
      expect(showsProgressBar('running')).toBe(true);
      expect(showsProgressBar('completed')).toBe(true);
    });

    it('字节格式化：0 与非法值都好处理', () => {
      expect(formatBytes(0)).toBe('0 B');
      expect(formatBytes(1024)).toBe('1.0 KB');
      expect(formatBytes(13_002_342)).toBe('12.4 MB');
      expect(formatBytes(null)).toBe('');
      expect(formatBytes(Number.NaN)).toBe('');
      expect(byteRange(1024, 2048)).toBe('1.0 KB / 2.0 KB');
      expect(byteRange(0, 2048)).toBe('0 B / 2.0 KB');
      expect(byteRange(2048, null)).toBe('2.0 KB');
      expect(byteRange(0, null)).toBe('0 B');
    });
  });

  describe('失败原因（[09 §3.1]、[05 §7.4]）', () => {
    it('优先用后端给的中文消息，并带上重试次数', () => {
      const reason = failureReason(
        makePlan({
          id: 'a',
          status: 'failed',
          errorCode: 'download_failed',
          errorMessage: '下载未完成，请稍后重试',
          attemptCount: 5,
        }),
      );
      expect(reason).toBe('失败原因：下载未完成，请稍后重试 · 已重试 5 次');
    });

    it('后端没给消息时给通用兜底文案，不显示空行', () => {
      const reason = failureReason(
        makePlan({ id: 'a', status: 'failed', errorCode: 'download_failed', errorMessage: '' }),
      );
      expect(reason).toBe('失败原因：下载未完成，请稍后重试');
    });

    it('非 failed 行没有失败原因', () => {
      expect(failureReason(makePlan({ id: 'a', status: 'running' }))).toBeNull();
      expect(failureReason(makePlan({ id: 'a', status: 'completed' }))).toBeNull();
    });
  });

  describe('删除记录的可用性（其余动作由后端派生字段决定）', () => {
    it('删除记录只对终态开放，非终态由停止承接', () => {
      expect(canRemove('completed')).toBe(true);
      expect(canRemove('failed')).toBe(true);
      expect(canRemove('canceled')).toBe(true);
      expect(canRemove('running')).toBe(false);
      expect(canRemove('queued')).toBe(false);
      expect(isTerminal('queued')).toBe(false);
    });

    it('停止/重试/打开入口**不在这里判断**：Go 侧已派生 canStop/canRetry/canOpen（B-313）', () => {
      // 界面直接用后端给的布尔值；这里只钉住"我们确实没有前端推断"这件事：
      // lib 不再导出任何按 status 推 stop/retry/open 的函数。
      expect(Object.keys(planViewModule)).not.toContain('canStop');
      expect(Object.keys(planViewModule)).not.toContain('canRetry');
      expect(Object.keys(planViewModule)).not.toContain('canOpenFolder');
    });
  });
});

// 类型层面的兜底：fixture 必须与 PlanView 完全对齐（少字段或多字段都会在这里报错）。
const _typeCheck: PlanView = makePlan({ id: 'type-check' });
void _typeCheck;
