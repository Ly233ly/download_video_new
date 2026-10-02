import { Inbox } from 'lucide-react';

import { PageHeading } from '../components/PageHeading';

/**
 * 下载任务页（默认页，[09 §2.1]）。
 *
 * 阶段 1 只做**空列表页**：列表本身、统计卡与虚拟滚动属阶段 2/6
 * （行高与虚拟化参数见 [16 §4.5]，`estimateSize` 必须是常量）。
 */
export function MediaPage() {
  return (
    <>
      <PageHeading title="下载任务" sub="连续滚动，无分页；进度由后端推送实时更新" />

      {/* 卡片：设计稿 .card（主表面 + 1px 描边 + 圆角 12 + 极轻阴影） */}
      <div className="rounded-card border border-line bg-surface shadow-card">
        <div className="flex items-center justify-center px-[17px] py-16">
          <div className="text-center">
            <Inbox className="mx-auto text-content-3" size={28} strokeWidth={1.6} />
            <p className="mt-3 text-[14px] font-semibold">还没有下载任务</p>
            <p className="mt-1 max-w-[380px] text-[12.5px] text-content-2">
              在浏览器里点扩展图标，选好画质与保存位置，任务就会出现在这里。
            </p>
          </div>
        </div>
      </div>
    </>
  );
}
