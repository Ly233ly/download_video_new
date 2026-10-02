import { Moon, Sun } from 'lucide-react';

import { NAV_ITEMS, type NavItem, type PageKey } from '../navigation';
import { useThemeStore } from '../stores/theme';

/** 导航按钮——对应设计稿 `.nav`（含选中态左侧 3px 竖条）。 */
function NavButton({
  item,
  active,
  onSelect,
}: {
  item: NavItem;
  active: boolean;
  onSelect(key: PageKey): void;
}) {
  const Icon = item.icon;
  return (
    <button
      type="button"
      onClick={() => onSelect(item.key)}
      aria-current={active ? 'page' : undefined}
      className={[
        // 设计稿 .nav：gap 10 / padding 9px 12px / 圆角 8 / 13.5px
        'relative flex w-full items-center gap-2.5 rounded-nav px-3 py-[9px] text-left text-[13.5px]',
        'transition-colors duration-100',
        active
          ? 'bg-accent-soft font-semibold text-accent'
          : 'text-content-2 hover:bg-surface hover:text-content hover:shadow-[0_1px_2px_rgba(16,24,40,.05)]',
      ].join(' ')}
    >
      {/* 设计稿 .nav.on::before：left:-10px（落在侧栏内边距里）、3×18、右侧圆角 */}
      {active && (
        <span className="absolute top-1/2 -left-2.5 h-[18px] w-[3px] -translate-y-1/2 rounded-r-[3px] bg-accent" />
      )}
      <Icon size={16} strokeWidth={1.9} className="flex-none opacity-90" />
      <span className="truncate">{item.label}</span>
    </button>
  );
}

/**
 * 侧边栏——对应设计稿 `.side`：**只放导航**（品牌在应用顶栏里）。
 * 底部放主题切换（[09 §2.2]：位置就是"侧边栏底部"）。
 */
export function Sidebar({ current, onSelect }: { current: PageKey; onSelect(key: PageKey): void }) {
  const theme = useThemeStore((state) => state.theme);
  const pending = useThemeStore((state) => state.pending);
  const error = useThemeStore((state) => state.error);
  const toggle = useThemeStore((state) => state.toggle);

  // 文案随当前主题变化（[09 §2.2]）
  const Icon = theme === 'dark' ? Sun : Moon;
  const label = theme === 'dark' ? '切换到浅色主题' : '切换到深色主题';

  const primary = NAV_ITEMS.filter((item) => !item.dividerBefore);
  const tail = NAV_ITEMS.filter((item) => item.dividerBefore);

  return (
    <nav
      className="flex w-[196px] flex-none flex-col gap-0.5 border-r border-line bg-sidebar px-2.5 py-3.5"
      aria-label="主导航"
    >
      {primary.map((item) => (
        <NavButton key={item.key} item={item} active={item.key === current} onSelect={onSelect} />
      ))}

      {/* .side .gap：把后面的项压到底部 */}
      <div className="flex-1" />
      <div className="mx-1 my-2 h-px bg-line" />

      {tail.map((item) => (
        <NavButton key={item.key} item={item} active={item.key === current} onSelect={onSelect} />
      ))}

      <button
        type="button"
        onClick={() => void toggle()}
        disabled={pending}
        className="flex w-full items-center gap-2.5 rounded-nav px-3 py-[9px] text-left text-[13.5px] text-content-2 transition-colors duration-100 hover:bg-surface hover:text-content disabled:cursor-not-allowed disabled:opacity-45"
      >
        <Icon size={16} strokeWidth={1.9} className="flex-none opacity-90" />
        <span className="truncate">{label}</span>
      </button>
      {error && (
        <p className="px-3 pt-1 text-[12px] text-err" role="status">
          {error}
        </p>
      )}
    </nav>
  );
}
