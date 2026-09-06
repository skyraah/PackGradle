// React 入口（ADR-0014 脚手架）。
//
// 契约 04 §2.1 时序：核心事件订阅必须先于一切查询、先于首帧渲染。Vue 版的证明方式
// 是在 mount 之前调用 subscribeCoreEvents()/bootstrapSyncCache()（main:frontend/src/main.ts，
// 全前端仅 api/events 一处订阅核心事件）。移植 api 层时，把订阅调用放到本文件
// createRoot(...).render(...) 之前——代码顺序即证明。
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './assets/main.css'
import './lib/i18n'
import { RouterProvider } from 'react-router'
import { router } from './router'

createRoot(document.getElementById('app')!).render(
    <StrictMode>
        <RouterProvider router={router} />
    </StrictMode>,
)
