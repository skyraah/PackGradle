---
status: accepted
date: 2026-08-28
---

# 0001 · P1 前端切换与旧入口退场

新 `/workspaces` 前端达到验收后旧入口如何退场（决策图票 #3；背景见检视报告 P0-1/P1-7、roadmap Step 7/8、重写边界 §2）。本 fork 无发布、无外部用户，切换无需为任何受众设计共存期或迁移路径。决议如下：

1. **单发布切换**。通过 P1 验收（口径由「P1 验收口径与 E2E 方式」票决议）的那个发布直接启用新导航（工作区 / 项目源 / 运行实例 / 设置，默认首页 `/workspaces`）；不存在新旧并行可见的共存期，切换前的两套路由只存在于 dev 构建。
2. **旧页面整体退场，不做只读冻结**。Projects / ProjectDetail / Instances / Dev / Dashboard 连同其跨端写操作（Junction/硬链接创建、meta 推/拉、文件级同步）从产品面消失；不留「只读旧页面」形态。
3. **不设任何 legacy 工具区——推翻 roadmap Step 8.5 的工具保留要求**。旧配置导入预览与「解除 legacy link」工具一并取消；旧 Junction/TOML 由使用者手动处理，新关联经 `/sources`、`/runtimes` 重新登记。新栈仍按 roadmap Step 3.3/8.3 在扫描与重绑定预检中把旧 Junction/TOML **识别**为 legacy 输入且不自动覆盖——识别语义保留，工具形态不存在。
4. **旧路由静默重定向**。`/`、`/projects*`、`/instances`、`/dev` 及 catch-all 一律重定向 `/workspaces`，无提示。
5. **mock 仅存在于开发构建**（关闭 P1-7）。设置页 mock 区块与顶栏 MOCK 徽标仅 `import.meta.env.DEV` 渲染；生产构建 `isMockEnabled()` 恒为 false，mocks 模块经静态分支从产物裁剪（构建验收：dist 中无 mock 痕迹）。新 `api/` 适配器层的 mock 遵循同一策略。
6. **设置页只迁入新栈消费的项**。主题/语言（及 dev 构建的 mock 开关）入新设置页；packwiz CLI 路径、实例目录路径、CF API Key 不迁——API Key 待 Phase 3 重新下载真正消费时再入。
7. **shadcn 迁移指南（docs/frontend/06）§5 随本决议修订**：取消旧页面逐页 shadcn 迁移；切换发布即删除旧路由/页面/旧 store/旧 mocks，并在同一发布执行 Vuetify 收尾拆除；新页面一律 shadcn-vue。

## Considered Options

- 旧跨端写操作退场三态：完全隐藏 / 只读冻结 / 收敛为迁移工具区。选**页面级完全隐藏**：只读旧页面会误导用户以为旧同步模型仍被支持。
- 迁移工具区：完整保留（导入 + 解除链接）/ 缩减（仅解除链接）/ 完全取消。选**完全取消**：无受众可迁移，本人存量 Junction 手动清理的一次性成本，低于长期维护一条冻结工具线。

## Consequences

- 决策图票「legacy-import 适配器范围与解除链接工具边界」（#9）随本决议出图关闭；legacy-import 不再是产品任务，遗留的「识别不覆盖」检查属 adapter 执行面（roadmap 已指定，不建票）。
- roadmap Step 8.5 与本 ADR 冲突之处以本 ADR 为准。
