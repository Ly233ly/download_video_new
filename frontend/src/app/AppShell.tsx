import { useState } from 'react';

import { DEFAULT_PAGE, type PageKey } from '../navigation';
import { MediaPage } from '../pages/MediaPage';
import { PlaceholderPage } from '../pages/PlaceholderPage';
import { Sidebar } from './Sidebar';
import { TopBar } from './TopBar';

/** 各页面所属阶段（[13 §2]），用于占位文案。 */
const PAGE_STAGE: Record<Exclude<PageKey, 'media'>, string> = {
  wechat: '阶段 5',
  idm: '阶段 4',
  settings: '阶段 6',
  diagnostics: '阶段 6',
};

/**
 * 应用外壳——对应设计稿 `.win` 的结构：顶栏在上，下面是「侧栏 + 主区」。
 *
 * **外壳与页面同级替换，外壳自身不重建**——[16 §4.7] 要求切页不得重建整棵应用树，
 * 顶栏与侧栏必须是同一组件实例。
 *
 * TODO(阶段 6)：按 [09 §2.3] 为每个页面键保留滚动位置与筛选条件。
 */
export function AppShell() {
  const [page, setPage] = useState<PageKey>(DEFAULT_PAGE);

  return (
    <div className="flex h-full flex-col">
      <TopBar />
      <div className="flex min-h-0 flex-1">
        <Sidebar current={page} onSelect={setPage} />
        <main className="main-scroll min-w-0 flex-1 overflow-y-auto px-[26px] pt-[22px] pb-[30px]">
          {page === 'media' ? (
            <MediaPage />
          ) : (
            <PlaceholderPage page={page} stage={PAGE_STAGE[page]} />
          )}
        </main>
      </div>
    </div>
  );
}
