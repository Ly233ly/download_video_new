import type { ButtonHTMLAttributes } from 'react';

/**
 * 按钮——对应设计稿 `.btn`（[09 §4.7] 的三种变体）。
 *
 * | 变体 | 规定 |
 * | --- | --- |
 * | `plain` | 主表面 + 1px 描边；hover 变次级表面且描边转三级文字色 |
 * | `primary` | 强调色填充 + 白字 + 600；hover 提亮 6% |
 * | `danger` | 红字 + 30% 透明红描边（**不用红色填充**，避免误触） |
 *
 * 禁用态透明度 `.45`、光标 `not-allowed`；键盘焦点可见（B-807）。
 */
export type ButtonVariant = 'plain' | 'primary' | 'danger';

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  plain: 'border-line bg-surface text-content hover:bg-surface-2 hover:border-content-3',
  primary: 'border-accent bg-accent text-accent-ink font-semibold hover:brightness-[1.06]',
  danger: 'border-err/30 bg-surface text-err hover:bg-surface-2',
};

export function Button({
  variant = 'plain',
  className = '',
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  return (
    <button
      type="button"
      className={[
        'rounded-ctl border px-3 py-[6px] text-[12.5px] transition-colors duration-100',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
        'disabled:cursor-not-allowed disabled:opacity-45',
        VARIANT_CLASS[variant],
        className,
      ].join(' ')}
      {...rest}
    />
  );
}
