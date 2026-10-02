import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';

// [16 §5.1] 的 C3：必须配 `react-hooks` 的 `recommended-latest`——
// 编译器规则会指出哪些组件**无法被优化**，这是"渲染纪律"的可执行检查。
//
// 为什么手写 plugins/rules 而不用 `reactHooks.configs['recommended-latest']`：
// 该预设仍是 eslintrc 形态（`plugins` 是字符串数组），ESLint 9 的 flat config
// 会直接报错。这里只把它**规则集**搬过来，语义与预设完全一致。
const reactHooksLatest = reactHooks.configs['recommended-latest'];

export default tseslint.config(
  { ignores: ['dist/**', 'wailsjs/**', 'node_modules/**'] },
  ...tseslint.configs.recommended,
  {
    plugins: { 'react-hooks': reactHooks },
    rules: { ...reactHooksLatest.rules },
  },
);
