import { DropdownMenuContent } from "../ui/dropdown-menu";
import { motion } from "motion/react";
import { fadeUp } from "@/lib/motion";

export function MotionDropdownMenu() {
    const MotionDropdownMenuContent = motion.create(DropdownMenuContent)
    return (
        <MotionDropdownMenuContent
            variants={fadeUp}
            initial="initial"
            animate="animate"
            exit="exit"
        />
    )
}