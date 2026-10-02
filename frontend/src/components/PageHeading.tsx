/** 页面标题 + 副标题——对应设计稿 `h1`（含 28×3 强调色短横线）与 `.sub`。 */
export function PageHeading({ title, sub }: { title: string; sub: string }) {
  return (
    <>
      <h1 className="text-[19px] font-[650] tracking-[0.2px]">
        {title}
        <span className="mt-[9px] block h-[3px] w-7 rounded-[2px] bg-accent" />
      </h1>
      <p className="mt-[9px] mb-[18px] text-[12.5px] text-content-3">{sub}</p>
    </>
  );
}
