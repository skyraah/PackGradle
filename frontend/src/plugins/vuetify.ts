// Vuetify 实例：主题与组件默认值集中在此。
// 深色主题为默认（石板底 + 祖母绿主色，契合整合包开发工具气质），
// 亮色主题同步提供，为后续主题切换留好扩展位。
import 'vuetify/styles'
import { createVuetify } from 'vuetify'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'

const dark = {
    background: '#0D1017',
    surface: '#151A23',
    'surface-bright': '#1B212C',
    'surface-variant': '#1E2633',
    'on-surface-variant': '#93A0B4',
    'on-background': '#E7ECF4',
    'on-surface': '#E7ECF4',
    primary: '#4ADE80',
    'on-primary': '#0A1B10',
    secondary: '#818CF8',
    'on-secondary': '#12132B',
    error: '#F87171',
    'on-error': '#2C0B0B',
    warning: '#FBBF24',
    'on-warning': '#241A02',
    info: '#60A5FA',
    'on-info': '#08182E',
    success: '#34D399',
    'on-success': '#052015',
}

const light = {
    background: '#F5F7FA',
    surface: '#FFFFFF',
    'surface-bright': '#FFFFFF',
    'surface-variant': '#E8EDF4',
    'on-surface-variant': '#5A6A7E',
    'on-background': '#161B22',
    'on-surface': '#161B22',
    primary: '#16A34A',
    'on-primary': '#FFFFFF',
    secondary: '#4F46E5',
    'on-secondary': '#FFFFFF',
    error: '#DC2626',
    'on-error': '#FFFFFF',
    warning: '#D97706',
    'on-warning': '#FFFFFF',
    info: '#2563EB',
    'on-info': '#FFFFFF',
    success: '#059669',
    'on-success': '#FFFFFF',
}

export default createVuetify({
    components,
    directives,
    theme: {
        defaultTheme: 'dark',
        themes: {
            dark: { dark: true, colors: dark },
            light: { dark: false, colors: light },
        },
    },
    defaults: {
        // 无阴影 + 描边的圆角卡片：扁平现代的观感；对话框内卡片显式传 elevation 以保持浮层感
        VCard: { rounded: 'lg', elevation: 0, border: 'sm' },
        VTextField: { variant: 'outlined' },
    },
})
