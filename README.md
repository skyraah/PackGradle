# Pack Gradle

一个用于在 packwiz 项目与 Prism Launcher 实例之间进行同步的桌面工具/CLI。

旨在为您提供更健康的modpack项目管理模式。

## 概览

- 基于**packwiz**架构的 远端协作modpack项目 与 本地开发时实例 分体管理，便于您统一管理您的packwiz项目。
- 接管**prism launcher**基于`.index`与`*.pw.toml`的管理系统，由packwiz作为统一交付入口。

## 特性

- 工作区管理：登记 packwiz 项目与 Prism 实例并建立同步关系
- 双端差异察觉与同步：项目源与运行实例两侧的变更差异可 `push` / `pull`
- 历史与回滚：每次同步更改计入本地历史，可回滚
- mod管理（开发中）：基于 [packwiz cli](https://github.com/packwiz/packwiz) 开发的mod管理，更新mod版本并下载推送至运行实例
- 尽请期待……
