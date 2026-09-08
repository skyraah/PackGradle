import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "cn"
import { Slot } from "radix-ui"

// st-* 状态徽章公共形态（自 main:frontend/src/components/ui/badge/index.ts 并入；
// UX 原型 workspace-ux-prototype.html §3.2 .st）：高 22px、圆角 4px、字重 600、
// 11.5px、5px 间距、6px 前置圆点（before 用 currentColor 着色）。配色 = tint 底 +
// 同系色文字（--tint-*/--success 等 token 浅深主题随 main.css 切换）。
const st = "h-[22px] gap-[5px] rounded-[4px] px-[9px] text-[11.5px] font-semibold before:content-[''] before:h-1.5 before:w-1.5 before:rounded-full before:bg-current before:flex-none"

const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center justify-center gap-1 overflow-hidden rounded-full border border-transparent px-2 py-0.5 text-xs font-medium whitespace-nowrap transition-[color,box-shadow] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&>svg]:pointer-events-none [&>svg]:size-3",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground [a&]:hover:bg-primary/90",
        secondary:
          "bg-secondary text-secondary-foreground [a&]:hover:bg-secondary/90",
        destructive:
          "bg-destructive text-white focus-visible:ring-destructive/20 dark:bg-destructive/60 dark:focus-visible:ring-destructive/40 [a&]:hover:bg-destructive/90",
        outline:
          "border-border text-foreground [a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        ghost: "[a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        link: "text-primary underline-offset-4 [a&]:hover:underline",
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
  }
)

function Badge({
  className,
  variant = "default",
  asChild = false,
  ...props
}: React.ComponentProps<"span"> &
  VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot.Root : "span"

  return (
    <Comp
      data-slot="badge"
      data-variant={variant}
      className={cn(badgeVariants({ variant }), className)}
      {...props}
    />
  )
}

export { Badge, badgeVariants }
