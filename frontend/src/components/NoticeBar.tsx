import { Info, TriangleAlert } from 'lucide-react';
import type { ReactNode } from 'react';

/**
 * 提示条——对应设计稿 `.note`（[09 §4.7]：主表面 + 1px 描边 + 左侧 3px 色条，
 * **不得**用整块彩色底）。
 *
 * `tone='error'` 时色条与图标取危险色（[09 §4.4] 的语义色），仍不使用彩色底。
 */
export function NoticeBar({
  tone = 'info',
  children,
}: {
  tone?: 'info' | 'error';
  children: ReactNode;
}) {
  const error = tone === 'error';
  const color = error ? 'var(--liudi-err)' : 'var(--liudi-accent)';
  const Icon = error ? TriangleAlert : Info;

  return (
    <div
      role={error ? 'alert' : 'status'}
      className="mb-4 flex items-start gap-[11px] rounded-card border border-line bg-surface py-3 pr-[15px] pl-[15px] text-[12.5px] leading-[1.6] shadow-card"
      style={{ borderLeftWidth: 3, borderLeftColor: color }}
    >
      <Icon size={16} strokeWidth={1.8} className="mt-[2px] flex-none" style={{ color }} />
      <div className="min-w-0">{children}</div>
    </div>
  );
}
