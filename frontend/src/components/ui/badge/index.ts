import type { VariantProps } from "class-variance-authority"
import { cva } from "class-variance-authority"

export { default as Badge } from "./Badge.vue"

// st-* 状态徽章公共形态（UX 原型 workspace-ux-prototype.html §3.2 .st）：
// 高 22px、圆角 4px、字重 600、11.5px、5px 间距、6px 前置圆点（::before 用
// currentColor 着色，plain 场景由 Badge 的 plain prop 隐藏）。配色逐项对齐
// 原型 .st-ok/.st-run/.st-warn/.st-err/.st-mut/.st-info（tint 底 + 同系色文字，
// 深浅主题随 assets/main.css 的语义/tint 令牌切换）。
const st = "h-[22px] gap-[5px] rounded-[4px] border-transparent px-[9px] text-[11.5px] font-semibold before:content-[''] before:h-1.5 before:w-1.5 before:rounded-full before:bg-current before:flex-none"

export const badgeVariants = cva(
  "inline-flex items-center justify-center rounded-full border px-2 py-0.5 text-xs font-medium w-fit whitespace-nowrap shrink-0 [&>svg]:size-3 gap-1 [&>svg]:pointer-events-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive transition-[color,box-shadow] overflow-hidden",
  {
    variants: {
      variant: {
        default:
          "border-transparent bg-primary text-primary-foreground [a&]:hover:bg-primary/90",
        secondary:
          "border-transparent bg-secondary text-secondary-foreground [a&]:hover:bg-secondary/90",
        destructive:
         "border-transparent bg-destructive text-white [a&]:hover:bg-destructive/90 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/60",
        outline:
          "text-foreground [a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        "st-ok": `${st} bg-tint-success text-success`,
        "st-run": `${st} bg-tint-primary text-primary`,
        "st-warn": `${st} bg-tint-warning text-warning`,
        "st-err": `${st} bg-tint-error text-error`,
        "st-mut": `${st} bg-surface-2 text-muted-foreground`,
        "st-info": `${st} bg-tint-primary text-info`,
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
)
export type BadgeVariants = VariantProps<typeof badgeVariants>
