import { createApp } from 'vue'

import '@mdi/font/css/materialdesignicons.css'
import './assets/main.css'

import App from './App.vue'
import i18n from './i18n'
import router from './router'
import vuetify from './plugins/vuetify'
import { subscribeCoreEvents } from './api/events'
import { bootstrapSyncCache, markTaskDirty, notifyWatchFailed, triggerRequery } from './stores/syncCache'

const app = createApp(App).use(i18n).use(vuetify).use(router)

// 契约 04 §2.1 时序：订阅先于一切查询、先于 mount（此处的代码顺序即证明，
// 全前端仅 api/events 一处订阅核心事件）；bootstrap 与渲染并行发起，
// 查询到达后填缓存，不阻塞首帧。
subscribeCoreEvents({
    onRequery: triggerRequery,
    onTaskUpdated: markTaskDirty,
    onWatchFailed: notifyWatchFailed,
})
bootstrapSyncCache()

app.mount('#app')
