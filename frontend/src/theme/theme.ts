/**
 * 主题的读写原语。
 *
 * 主题**只在根元素的 `data-theme` 上表达**，切换即替换 CSS 自定义属性，
 * 不触发组件树重建（[16 §4.7]），也不残留旧主题（B-805）。
 *
 * 首帧的值由 `index.html` 的同步内联脚本从 Go 侧注入的值确定（B-803）——
 * 这里只负责读取与后续切换。
 */
export type Theme = 'light' | 'dark';

/** 根元素上的主题属性名。用 `setAttribute`/`getAttribute` 而不是 `dataset`：
 *  `dataset` 的键是**去掉 `data-` 前缀**的名字，写成 `dataset['data-theme']` 会得到
 *  `data-data-theme`——测试抓到过这个错误。 */
const THEME_ATTR = 'data-theme';

export function isTheme(value: unknown): value is Theme {
  return value === 'light' || value === 'dark';
}

/** 读取当前生效的主题（首帧已确定，不会返回未定义）。 */
export function currentTheme(): Theme {
  const value = document.documentElement.getAttribute(THEME_ATTR);
  return isTheme(value) ? value : 'light';
}

/** 应用主题：只改一个属性，整棵树的令牌随之更新。 */
export function applyTheme(theme: Theme): void {
  document.documentElement.setAttribute(THEME_ATTR, theme);
}

/** 另一个主题。文案与切换都基于它，避免两处各写一套三元表达式。 */
export function opposite(theme: Theme): Theme {
  return theme === 'dark' ? 'light' : 'dark';
}
