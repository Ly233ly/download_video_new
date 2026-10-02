/**
 * 主导航定义：**固定顺序**来自 [09 §2.1]，不要在这里增删或重排。
 *
 * 分隔线用 `dividerBefore` 表达（诊断项之前有一条分隔），
 * 与页面键一起放在同一处，避免侧边栏组件里再写一遍顺序。
 */
import { Activity, Download, Package, Settings, Video } from 'lucide-react';
import type { ComponentType } from 'react';

export type PageKey = 'media' | 'wechat' | 'idm' | 'settings' | 'diagnostics';

export interface NavItem {
  key: PageKey;
  /** 侧边栏显示名（[09 §2.1]） */
  label: string;
  /** 图标一律内联 SVG、`currentColor` 描边（[09 §4.7]） */
  icon: ComponentType<{ size?: number; strokeWidth?: number; className?: string }>;
  dividerBefore?: boolean;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { key: 'media', label: '下载任务', icon: Download },
  { key: 'wechat', label: '视频号', icon: Video },
  { key: 'idm', label: 'IDM 导入', icon: Package },
  { key: 'settings', label: '设置', icon: Settings },
  { key: 'diagnostics', label: '导出诊断信息', icon: Activity, dividerBefore: true },
] as const;

export const DEFAULT_PAGE: PageKey = 'media';

export function labelOf(key: PageKey): string {
  return NAV_ITEMS.find((item) => item.key === key)?.label ?? key;
}
