import { PageHeading } from '../components/PageHeading';
import { labelOf, type PageKey } from '../navigation';

/** 尚未实现的页面占位。每个页面属于哪个阶段见 [13 §2]。 */
export function PlaceholderPage({ page, stage }: { page: PageKey; stage: string }) {
  return (
    <>
      <PageHeading title={labelOf(page)} sub={`该页面属于${stage}的交付物`} />
      <div className="rounded-card border border-line bg-surface p-[17px] shadow-card">
        <p className="text-[13px] text-content-2">功能尚未实现。</p>
      </div>
    </>
  );
}
