import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../bindings', () => ({
  setTheme: vi.fn(async () => ({ ok: true, value: undefined })),
  getTheme: vi.fn(),
  hasBackend: vi.fn(() => true),
}));

import { useThemeStore } from '../stores/theme';
import { applyTheme } from '../theme/theme';
import { AppShell } from './AppShell';

describe('应用外壳', () => {
  beforeEach(() => {
    applyTheme('light');
    useThemeStore.setState({ theme: 'light', pending: false, error: null });
  });

  // 顺序与文案来自 [09 §2.1]，任何增删都是规范变更。
  it('按 [09 §2.1] 渲染五项主导航', () => {
    render(<AppShell />);

    for (const label of ['下载任务', '视频号', 'IDM 导入', '设置', '导出诊断信息']) {
      expect(screen.getByRole('button', { name: label })).toBeInTheDocument();
    }
  });

  // 设计稿把品牌放在应用顶栏（.titlebar），不是侧栏。
  it('应用顶栏含品牌与版本号（对应设计稿 .titlebar）', () => {
    render(<AppShell />);

    const banner = screen.getByRole('banner');
    expect(banner).toHaveTextContent('留底下载器');
    expect(banner).toHaveTextContent(__APP_VERSION__);
  });

  it('默认页是下载任务，并显示空列表状态', () => {
    render(<AppShell />);

    expect(screen.getByRole('heading', { name: '下载任务' })).toBeInTheDocument();
    expect(screen.getByText('还没有下载任务')).toBeInTheDocument();
  });

  it('切页只换内容，外壳不重建（[09 §2.3]、[16 §4.7]）', () => {
    render(<AppShell />);
    const navBefore = screen.getByRole('navigation', { name: '主导航' });

    fireEvent.click(screen.getByRole('button', { name: '设置' }));

    expect(screen.getByRole('heading', { name: '设置' })).toBeInTheDocument();
    expect(screen.queryByText('还没有下载任务')).not.toBeInTheDocument();
    // 同一个 DOM 节点 = 外壳没有被重建
    expect(screen.getByRole('navigation', { name: '主导航' })).toBe(navBefore);
  });

  it('侧边栏底部的主题切换文案随主题变化，并作用于根元素（[09 §2.2]）', async () => {
    render(<AppShell />);

    fireEvent.click(screen.getByRole('button', { name: '切换到深色主题' }));

    expect(await screen.findByRole('button', { name: '切换到浅色主题' })).toBeInTheDocument();
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
  });

  it('顶部状态如实说明 Eagle 可用性，不假装可用（B-809）', () => {
    render(<AppShell />);

    expect(screen.getByText('Eagle 未连接 · 下载可用')).toBeInTheDocument();
  });
});
