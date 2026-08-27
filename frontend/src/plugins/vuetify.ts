// Vuetify 实例：主题与组件默认值集中在此。
// PCL2 向视觉：高饱和强调色、圆润卡片、醒目的选中态与 chip 色块。
import 'vuetify/styles'
import { createVuetify } from 'vuetify'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'

const dark = {
    background: '#1E1F24',
    surface: '#26272E',
    'surface-bright': '#2F3038',
    'surface-variant': '#383943',
    'on-surface-variant': '#C9CAD1',
    'on-background': '#F2F3F5',
    'on-surface': '#F2F3F5',
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
    background: '#F2F3F5',
    surface: '#FFFFFF',
    'surface-bright': '#FFFFFF',
    'surface-variant': '#ECEEF2',
    'on-surface-variant': '#5D616B',
    'on-background': '#1B1D22',
    'on-surface': '#1B1D22',
    primary: '#0A84D6',
    'on-primary': '#FFFFFF',
    secondary: '#8764B8',
    'on-secondary': '#FFFFFF',
    error: '#D13438',
    'on-error': '#FFFFFF',
    warning: '#986F0B',
    'on-warning': '#FFFFFF',
    info: '#0A84D6',
    'on-info': '#FFFFFF',
    success: '#107C10',
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
        VBtn: { rounded: 'lg' },
        VCard: { rounded: 'xl', elevation: 0, border: 'sm' },
        VChip: { rounded: 'md' },
        VListItem: { rounded: 'lg' },
        VMenu: { offset: 6 },
        VTextField: { variant: 'outlined', rounded: 'lg' },
        VSelect: { variant: 'outlined', rounded: 'lg' },
    },
})
