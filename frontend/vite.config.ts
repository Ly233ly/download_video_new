import { readFileSync } from 'node:fs';

import { defineConfig } from 'vitest/config';
import react, { reactCompilerPreset } from '@vitejs/plugin-react';
import babel from '@rolldown/plugin-babel';
import tailwindcss from '@tailwindcss/vite';

// 版本号只有一个来源：package.json（[12 §8.1] 要求各清单一致，前端以它为准）。
const pkg = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8')) as {
  version: string;
};

// 接入顺序按 [16 §5.1]：react() 之后接编译器，最后是 Tailwind。
// 编译器接入方式（reactCompilerPreset + @rolldown/plugin-babel）是 plugin-react 6 起的官方路径。
export default defineConfig({
  define: { __APP_VERSION__: JSON.stringify(pkg.version) },
  plugins: [react(), babel({ presets: [reactCompilerPreset()] }), tailwindcss()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
  },
});
