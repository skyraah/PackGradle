/// <reference types="vite/client" />

// __DEV__：vite define 注入的全局静态门（见 vite.config.ts；ADR-0001 §5 mock 裁剪）。
// 生产构建被常量折叠为 false，mock 相关分支整体裁剪出产物。
declare const __DEV__: boolean
