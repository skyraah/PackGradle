import type {
    Transition,
    Variants,} from "motion/react"

export const duration = {
    instant: 0.08,
    fast: 0.12,
    normal: 0.18,
    moderate: 0.24,
    slow: 0.32,
} as const


export const ease = {
    smooth: [0.22, 1, 0.36, 1],
    enter: [0.16, 1, 0.3, 1],
    exit: [0.7, 0, 0.84, 0],
} as const


export const spring = {
    snappy: {
        type: "spring",
        stiffness: 500,
        damping: 35,
        mass: 0.8,
    },

    smooth: {
        type: "spring",
        stiffness: 400,
        damping: 32,
        mass: 1,
    },

    gentle: {
        type: "spring",
        stiffness: 280,
        damping: 30,
        mass: 1,
    },

    // 侧栏折叠弹簧（壳层专用，勿并入 smooth 档）：全树共用一拍——收敛时长与幅度无关，
    // 宽度主时钟（useSidebarCollapse）与各元素几何（SidebarSpring 族）天然同拍。
    // damping=40 为轻微过阻尼（ζ≈1.07，零过冲）；32 档欠阻尼（ζ≈0.86）会让宽度冲过
    // 目标 ~0.7px 再弹回，高 DPI 下是 1-2 物理像素的尾段抖动（2026-09-08 实测）。
    sidebar: {
        type: "spring",
        stiffness: 350,
        damping: 40,
    },
} as const satisfies Record<
    string,
    Transition>


export const fade: Variants = {
    initial: {
        opacity: 0,
    },

    animate: {
        opacity: 1,

        transition: {
        duration: duration.normal,
        ease: ease.enter,
        },
    },

    exit: {
        opacity: 0,

        transition: {
        duration: duration.fast,
        ease: ease.exit,
        },
    },
}


export const fadeUp: Variants = {
    initial: {
        opacity: 0,
        y: 8,
    },

    animate: {
        opacity: 1,
        y: 0,

        transition: {
        duration: duration.normal,
        ease: ease.enter,
        },
    },

    exit: {
        opacity: 0,
        y: 4,

        transition: {
        duration: duration.fast,
        ease: ease.exit,
        },
    },
}


export const scaleFade: Variants = {
    initial: {
        opacity: 0,
        scale: 0.96,
    },

    animate: {
        opacity: 1,
        scale: 1,

        transition: {
        duration: duration.normal,
        ease: ease.enter,
        },
    },

    exit: {
        opacity: 0,
        scale: 0.98,

        transition: {
        duration: duration.fast,
        ease: ease.exit,
        },
},
}


export const popover: Variants = {
    initial: {
        opacity: 0,
        scale: 0.98,
        y: -4,
    },

    animate: {
        opacity: 1,
        scale: 1,
        y: 0,

        transition: {
        duration: duration.fast,
        ease: ease.enter,
        },
    },

    exit: {
        opacity: 0,
        scale: 0.98,

        transition: {
        duration: duration.instant,
        ease: ease.exit,
        },
    },
}


export const listItem: Variants = {
    initial: {
        opacity: 0,
        y: 6,
    },

    animate: {
        opacity: 1,
        y: 0,

        transition: {
        duration: duration.normal,
        ease: ease.enter,
        },
    },

    exit: {
        opacity: 0,
        y: -4,

        transition: {
        duration: duration.fast,
        ease: ease.exit,
        },
    },
}


export const staggerContainer: Variants = {
    animate: {
        transition: {
        staggerChildren: 0.03,
        },
    },
}