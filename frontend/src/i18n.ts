// 全局 i18n：唯一文案来源为 src/locales/zh-CN.json。
// 服务端只返回错误码（err.*），由这里渲染为用户可读文本
import { createI18n } from 'vue-i18n'
import zhCN from './locales/zh-CN.json'

const i18n = createI18n({
    legacy: false,
    locale: 'zh-CN',
    fallbackLocale: 'zh-CN',
    messages: { 'zh-CN': zhCN },
})

// 供非组件模块（如 utils/errors.ts）在 setup 之外使用
export const t = i18n.global.t

export default i18n
