import { fileURLToPath, URL } from 'node:url'
import fs from 'node:fs'
import type { Plugin } from "vite";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import wails from "@wailsio/runtime/plugins/vite";

// stripDevMockLocale：生产构建从 zh-CN.json 剔除 mock.* 键（仅 dev 构建的 mock 卡片
// 引用它们），使产物中无任何 mock 痕迹（ADR-0001 §5 构建验收）。开发构建不受影响。
const stripDevMockLocale = (isDev: boolean): Plugin => ({
  name: "packgradle:strip-dev-mock-locale",
  enforce: "pre",
  load(id) {
    if (isDev || !id.replace(/\\/g, "/").endsWith("src/locales/zh-CN.json")) return null;
    const json = JSON.parse(fs.readFileSync(id.split("?")[0]!, "utf8")) as Record<string, string>;
    for (const k of Object.keys(json)) {
      // mock.*：mock 卡片专用；app.mockBadgeTip：顶栏 MOCK 徽标专用（组件均已 dev 门控）
      if (k.startsWith("mock.") || k === "app.mockBadgeTip") delete json[k];
    }
    return JSON.stringify(json);
  },
});

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => ({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  // __DEV__：开发/生产构建的静态门（与 import.meta.env.DEV 同语义）。
  // 生产构建中被替换为 false 字面量，mock 分支（mocks 动态导入、设置页 mock 卡片、
  // 顶栏 MOCK 徽标、目录选择 mock 路径）经常量折叠整体裁剪出产物（ADR-0001 §5）。
  // 模板表达式里不能写 import.meta，故统一走此全局常量。
  define: {
    __DEV__: mode === "development",
  },
  plugins: [vue(), tailwindcss(), wails("./bindings"), stripDevMockLocale(mode === "development")],
}));
