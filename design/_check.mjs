// 设计稿自检：校验 design/mockup.html 的标签平衡、内联 JS 语法、CSS 变量闭合，
// 并列出样式块中的设计 token，便于与 docs/09-UI.md §4 逐项核对。
// 用法: node design/_check.mjs
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const file = join(here, 'mockup.html');
const src = readFileSync(file, 'utf8');

// 1) 剥离 style / script / 注释
let masked = src
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/<style>[\s\S]*?<\/style>/gi, '<style></style>')
  .replace(/<script>[\s\S]*?<\/script>/gi, '<script></script>');

const VOID = new Set(['area','base','br','col','embed','hr','img','input','link','meta',
  'param','source','track','wbr','title','path','rect','circle','line','polyline',
  'polygon','use','stop','ellipse']);

// 2) 逐个标签扫描
const tagRe = /<(\/?)([a-zA-Z][a-zA-Z0-9]*)((?:"[^"]*"|'[^']*'|[^>"'])*?)(\/?)>/g;
const stack = [];
const errors = [];
let m;
while ((m = tagRe.exec(masked)) !== null) {
  const closing = m[1] === '/';
  const name = m[2].toLowerCase();
  const selfClose = m[4] === '/';
  if (VOID.has(name) || selfClose) continue;
  if (!closing) {
    stack.push(name);
  } else {
    if (stack.length === 0) { errors.push(`多余的 </${name}> @${m.index}`); continue; }
    const top = stack[stack.length - 1];
    if (top === name) stack.pop();
    else { errors.push(`</${name}> 与 <${top}> 不匹配 @${m.index}`); stack.pop(); }
  }
}

// 3) 提取内联 script 做语法检查
const scripts = [...src.matchAll(/<script>([\s\S]*?)<\/script>/gi)].map(x => x[1]);
let jsError = null;
for (const code of scripts) {
  try { new Function(code); } catch (e) { jsError = e.message; }
}

// 4) CSS 变量引用闭合
const defined = new Set([...src.matchAll(/(--[a-z0-9-]+)\s*:/g)].map(x => x[1]));
const used = new Set([...src.matchAll(/var\((--[a-z0-9-]+)\)/g)].map(x => x[1]));
const missingVars = [...used].filter(v => !defined.has(v));

console.log('文件:', file);
console.log('字节:', Buffer.byteLength(src), '| 行:', src.split('\n').length);
console.log('未闭合栈深度:', stack.length, stack.length ? '-> ' + stack.slice(-8).join(', ') : '');
console.log('标签错误数:', errors.length);
errors.slice(0, 10).forEach(e => console.log('   ', e));
console.log('内联 JS 语法:', jsError ? '失败 -> ' + jsError : '通过（' + scripts.length + ' 块）');
console.log('CSS 变量: 定义', defined.size, '| 引用', used.size, '| 缺失', missingVars.length ? missingVars.join(', ') : '无');

// 5) 导出关键 token 的实际取值（供与 docs/09-UI.md §4 人工比对）
//    注意：锚点值只允许在 09-UI.md 定义一次；本脚本负责"如实报数"，不维护第二份期望值。
const KEYS = [
  ['--bg', '窗口外围'], ['--surface', '主表面'], ['--surface-2', '次级表面'],
  ['--sidebar-bg', '侧栏'], ['--border', '描边'],
  ['--text', '文字主'], ['--text-2', '文字次'], ['--text-3', '文字三级'],
  ['--accent', '强调色'], ['--accent-soft', '强调色浅底'],
  ['--ok', '成功'], ['--warn', '警告'], ['--err', '危险'], ['--track', '轨道'],
  ['--r-win', '窗口圆角'], ['--r-card', '卡片圆角'], ['--r-ctl', '控件圆角'],
  ['--sidebar-w', '侧栏宽度'], ['--titlebar-h', '标题栏高度'],
];
const readBlock = (re) => (src.match(re) || [''])[0];
const readVar = (block, name) =>
  (block.match(new RegExp(name.replace(/-/g, '\\-') + '\\s*:\\s*([^;]+);')) || [])[1];

const lightBlock = readBlock(/:root\{[\s\S]*?\n  \}/);
const darkBlock = readBlock(/\[data-theme="dark"\]\{[\s\S]*?\n  \}/);

console.log('\n设计稿 token（对照 docs/09-UI.md §4.4 ~ §4.6；规范是唯一权威）:');
console.log('  ' + 'token'.padEnd(15) + '浅色'.padEnd(34) + '深色');
for (const [name, label] of KEYS) {
  const l = (readVar(lightBlock, name) || '—').trim();
  const d = (readVar(darkBlock, name) || '—').trim();
  console.log('  ' + name.padEnd(15) + l.padEnd(34) + d + '   ' + label);
}

// 6) 主题一致性：深色块必须覆盖浅色块定义的全部颜色型 token
const colorVars = [...lightBlock.matchAll(/(--[a-z0-9-]+)\s*:\s*(#[0-9a-fA-F]{3,8}|rgba?\()/g)].map(m => m[1]);
const darkVars = new Set([...darkBlock.matchAll(/(--[a-z0-9-]+)\s*:/g)].map(m => m[1]));
const notOverridden = colorVars.filter(v => !darkVars.has(v));
console.log('\n颜色型 token 未被深色覆盖:', notOverridden.length ? notOverridden.join(', ') : '无');

process.exit(errors.length || stack.length || jsError || missingVars.length || notOverridden.length ? 1 : 0);

process.exit(errors.length || stack.length || jsError || missingVars.length || notOverridden.length ? 1 : 0);
