// Vuetify 实例：主题与组件默认值集中在此。
// 色板与控件形状贴近 Windows 11 Fluent，同时保留 Vuetify 的成熟交互行为。
import 'vuetify/styles'
import { createVuetify } from 'vuetify'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'

const dark = {
    background: '#202020',
    surface: '#2B2B2B',
    'surface-bright': '#323232',
    'surface-variant': '#393939',
    'on-surface-variant': '#C8C8C8',
    'on-background': '#FFFFFF',
    'on-surface': '#FFFFFF',
    primary: '#4CC2FF',
    'on-primary': '#00364E',
    secondary: '#C3A7FF',
    'on-secondary': '#27164A',
    error: '#FF99A4',
    'on-error': '#5A0010',
    warning: '#FCE100',
    'on-warning': '#433800',
    info: '#60CDFF',
    'on-info': '#00364E',
    success: '#6CCB5F',
    'on-success': '#0C3B08',
}

const light = {
    background: '#F3F3F3',
    surface: '#FBFBFB',
    'surface-bright': '#FFFFFF',
    'surface-variant': '#EDEDED',
    'on-surface-variant': '#5D5D5D',
    'on-background': '#1B1B1B',
    'on-surface': '#1B1B1B',
    primary: '#0067C0',
    'on-primary': '#FFFFFF',
    secondary: '#8764B8',
    'on-secondary': '#FFFFFF',
    error: '#C42B1C',
    'on-error': '#FFFFFF',
    warning: '#9D5D00',
    'on-warning': '#FFFFFF',
    info: '#0067C0',
    'on-info': '#FFFFFF',
    success: '#0F7B0F',
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
        VBtn: { rounded: 'md' },
        VCard: { rounded: 'md', elevation: 0, border: 'sm' },
        VChip: { rounded: 'sm' },
        VListItem: { rounded: 'md' },
        VMenu: { offset: 6 },
        VTextField: { variant: 'outlined', rounded: 'md' },
    },
})
