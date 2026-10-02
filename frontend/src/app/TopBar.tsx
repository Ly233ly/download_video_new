/**
 * 应用顶栏——对应设计稿 `design/mockup.html` 的 `.titlebar`。
 *
 * 注意术语（[09 §4.1]）：这是**应用顶栏**，不是系统标题栏。系统标题栏与窗口控制键
 * 由 Windows 提供，本程序**不自绘**。
 *
 * 结构照设计稿：logo · 品牌 · 版本号 · 撑开 · 连接状态 pill。
 * B-809 要求顶部状态说明 Eagle 可用性——阶段 1 未接入 Eagle（阶段 4），如实显示"未连接"。
 */
export function TopBar() {
  return (
    <header
      className="flex h-12 flex-none items-center gap-2.5 border-b border-line px-4"
      style={{ backgroundImage: 'var(--liudi-titlebar-bg)' }}
    >
      <span
        className="h-5 w-5 flex-none rounded-md shadow-[0_2px_6px_rgba(47,109,246,.32)]"
        style={{ backgroundImage: 'linear-gradient(135deg, var(--liudi-accent), #7aa7ff)' }}
        aria-hidden
      />
      <span className="font-[650] tracking-[0.2px]">留底下载器</span>
      <span className="text-[12px] text-content-3">{__APP_VERSION__}</span>

      <span className="flex-1" />

      {/* TODO(阶段 4)：改为真实 Eagle 健康状态，并按 B-502/B-503 发布可用性 */}
      <span className="inline-flex items-center gap-1.5 rounded-pill border border-line bg-surface px-2.5 py-1 text-[12px] text-content-2 shadow-[0_1px_2px_rgba(16,24,40,.04)]">
        <span className="h-[7px] w-[7px] flex-none rounded-full bg-warn" aria-hidden />
        Eagle 未连接 · 下载可用
      </span>
    </header>
  );
}
