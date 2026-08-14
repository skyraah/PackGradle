import { createApp } from 'vue'

import '@mdi/font/css/materialdesignicons.css'
import './assets/main.css'

import App from './App.vue'
import i18n from './i18n'
import router from './router'
import vuetify from './plugins/vuetify'

createApp(App).use(i18n).use(vuetify).use(router).mount('#app')
