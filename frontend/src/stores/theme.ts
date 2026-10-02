/**
 * 主题状态（zustand，按领域切分，[16 §7] 的 F2 已定）。
 *
 * 本 store 是主题在**前端**的唯一归属：组件只读它、只调它，
 * 不直接改 `data-theme`，也不直接碰绑定层。
 */
import { create } from 'zustand';

import * as bindings from '../bindings';
import { applyTheme, currentTheme, opposite, type Theme } from '../theme/theme';

interface ThemeState {
  theme: Theme;
  /** 正在持久化。用于禁用按钮，避免连点造成乱序写入。 */
  pending: boolean;
  /** 持久化失败的提示；界面据此说明"主题没保存"，而不是假装成功。 */
  error: string | null;
  setTheme(theme: Theme): Promise<void>;
  toggle(): Promise<void>;
}

export const useThemeStore = create<ThemeState>((set, get) => ({
  // 首帧已由 index.html 的内联脚本确定，这里只读取它（B-803：不得先渲染默认色）。
  theme: currentTheme(),
  pending: false,
  error: null,

  async setTheme(theme) {
    const previous = get().theme;
    if (theme === previous && !get().error) {
      return;
    }

    // 先本地生效，保证切换手感即时；持久化失败再回滚。
    applyTheme(theme);
    set({ theme, pending: true, error: null });

    const result = await bindings.setTheme(theme);
    if (!result.ok) {
      // 回滚：界面与权威值（settings.theme）不能不一致（B-804）。
      applyTheme(previous);
      set({ theme: previous, pending: false, error: result.message });
      return;
    }
    set({ pending: false });
  },

  async toggle() {
    await get().setTheme(opposite(get().theme));
  },
}));
