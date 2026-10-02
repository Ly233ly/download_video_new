/**
 * **唯一**调用 Wails 的地方（[16 §3.1] 的 S1）。
 *
 * 组件与 store **不得**直接访问 `window.go` 或 Wails 运行时——否则绑定层一变，
 * 全仓都要改。所有方法在这里做三件事：
 *   1. 类型化包装；
 *   2. 把后端的失败转成 `Result`（**不得**把失败静默当成功，[16 §3.1]）；
 *   3. 后端缺席（例如纯浏览器里跑测试）时给出明确结果，而不是抛未捕获异常。
 */
import { isTheme, type Theme } from '../theme/theme';

export type Result<T> = { ok: true; value: T } | { ok: false; message: string };

/** Go 侧 `internal/ui` 暴露的方法（Wails 绑定路径：window.go.ui.UI.*）。 */
interface UiBinding {
  GetTheme(): Promise<string>;
  SetTheme(theme: string): Promise<void>;
}

function backend(): UiBinding | null {
  const wails = (globalThis as { go?: { ui?: { UI?: UiBinding } } }).go;
  return wails?.ui?.UI ?? null;
}

/** 后端是否可用（纯前端调试与测试时为 false）。 */
export function hasBackend(): boolean {
  return backend() !== null;
}

function failure(error: unknown): Result<never> {
  const message = error instanceof Error ? error.message : '后端调用失败';
  return { ok: false, message };
}

/** 读取持久化的主题。后端不可用或取值非法时，分别给出明确结果。 */
export async function getTheme(): Promise<Result<Theme>> {
  const api = backend();
  if (!api) {
    return { ok: false, message: '桌面端未连接' };
  }
  try {
    const value = await api.GetTheme();
    if (!isTheme(value)) {
      return { ok: false, message: '桌面端返回了未知的主题值' };
    }
    return { ok: true, value };
  } catch (error) {
    return failure(error);
  }
}

/** 持久化主题；失败必须让调用方知道（B-804 的持久值要与界面一致）。 */
export async function setTheme(theme: Theme): Promise<Result<void>> {
  const api = backend();
  if (!api) {
    return { ok: false, message: '桌面端未连接，主题未保存' };
  }
  try {
    await api.SetTheme(theme);
    return { ok: true, value: undefined };
  } catch (error) {
    return failure(error);
  }
}
