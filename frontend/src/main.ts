import { createApp } from 'vue'

import './assets/main.css'

import App from './App.vue'
import i18n from './i18n'
import router from './router'
import { subscribeCoreEvents } from './api/events'
import { subscribeNotificationNav } from './api/notificationNav'
import { bootstrapSyncCache, markTaskDirty, notifyWatchFailed, triggerRequery } from './stores/syncCache'
import { initTheme } from './stores/theme'

const app = createApp(App).use(i18n).use(router)

// 契约 04 §2.1 时序：订阅先于一切查询、先于 mount（此处的代码顺序即证明，
// 全前端仅 api/events 一处订阅核心事件）；bootstrap 与渲染并行发起，
// 查询到达后填缓存，不阻塞首帧。
subscribeCoreEvents({
    onRequery: triggerRequery,
    onTaskUpdated: markTaskDirty,
    onWatchFailed: notifyWatchFailed,
})
bootstrapSyncCache()

// 系统通知点击直达（票 #97）：同样 mount 前订阅（独立 topic，非核心事件流）
subscribeNotificationNav()

// 主题先于 mount 落 html.dark，避免首帧闪烁
initTheme()

app.mount('#app')
