# PackGradle

Minecraft 模组包项目源（Packwiz）与运行实例（Prism）之间受管同步的桌面工具。本文件是全项目唯一术语表。

## Language

**工作区（Workspace）**:
一个项目源与一个运行实例之间受管同步关系的唯一产品投影，对应一条 Relation。
_Avoid_: 项目联动、链接（旧 dir link 语境）

**项目源（Project）**:
以 Packwiz `pack.toml` 为根的模组包项目端点，同步的一方。
_Avoid_: 项目（指端点时）、pack

**运行实例（Runtime）**:
以 Prism 实例（`<实例>/minecraft`）为根的运行时端点，同步的另一方。
_Avoid_: 实例、Prism 联动

**切换（Cutover）**:
新前端成为唯一产品入口的那一次发布；旧页面同发布退场。
_Avoid_: 上线、灰度、迁移（指前端切换时）

**退场（Retirement）**:
旧页面或旧操作从产品面永久消失，不留只读或冻结形态。
_Avoid_: 下线（易被理解为可逆）、冻结

**legacy 识别（Legacy Recognition）**:
新栈在扫描与重绑定预检中把旧 Junction/TOML 关联识别为 legacy 输入且不自动覆盖的探测语义；只有识别，没有对应 UI 工具。
_Avoid_: legacy 迁移、legacy 工具（已取消，见 ADR-0001）
