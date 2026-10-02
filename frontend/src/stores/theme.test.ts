import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../bindings', () => ({
  setTheme: vi.fn(),
  getTheme: vi.fn(),
  hasBackend: vi.fn(() => true),
}));

import * as bindings from '../bindings';
import { applyTheme } from '../theme/theme';
import { useThemeStore } from './theme';

describe('主题 store', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    applyTheme('light');
    // store 是模块级单例，测试之间必须显式复位（[12 §5.3]：不得共享可变全局状态）。
    useThemeStore.setState({ theme: 'light', pending: false, error: null });
  });

  it('切换成功时同时更新根元素与持久值', async () => {
    vi.mocked(bindings.setTheme).mockResolvedValue({ ok: true, value: undefined });

    await useThemeStore.getState().toggle();

    expect(useThemeStore.getState().theme).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(bindings.setTheme).toHaveBeenCalledWith('dark');
    expect(useThemeStore.getState().error).toBeNull();
  });

  it('持久化失败时回滚界面——界面不得与权威值（settings.theme）不一致（B-804）', async () => {
    vi.mocked(bindings.setTheme).mockResolvedValue({ ok: false, message: '桌面端未连接' });

    await useThemeStore.getState().toggle();

    expect(useThemeStore.getState().theme).toBe('light');
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(useThemeStore.getState().error).toBe('桌面端未连接');
  });

  it('切到当前主题是空操作，不产生多余的持久化调用', async () => {
    vi.mocked(bindings.setTheme).mockResolvedValue({ ok: true, value: undefined });

    await useThemeStore.getState().setTheme('light');

    expect(bindings.setTheme).not.toHaveBeenCalled();
  });
});
