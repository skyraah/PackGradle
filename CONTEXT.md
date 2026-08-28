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

**修订号（Revision）**:
一条 Relation 的策略代次；创建时即第 1 代，仅随 MappingPolicy 修改递增，不在 UI 展示（内部一致性字段）。见 ADR-0002。
_Avoid_: 版本（指关系时）、策略集版本

**策略集版本（Policy Set Version）**:
MappingPolicy 模板自身的版本（如 default-v1 的 1），随模板演进变化；与关系修订号无关。
_Avoid_: 修订号（指模板时）

**能力（Feature）**:
当前版本/平台实际实现的功能开关；`feature=false` 的动作不注册，前端不渲染入口。见 `docs/contract/03-p1-contract.md` §2.1。
_Avoid_: 功能（指单动作时）

**可用性（Availability）**:
单动作在当前工作区状态下的可执行性，由后端推导并携带原因码，前端不得自行推断。见 `docs/contract/03-p1-contract.md` §2.1。
_Avoid_: 权限、是否可点

**重绑（Rebind）**:
把关系一端的根路径替换为新端点（Prepare 预检 + Apply 执行）；P1 下 Apply 后不继承基线、重走初始化，等价证明留 Phase 2。见 `docs/contract/03-p1-contract.md` §2.4。
_Avoid_: 重新绑定（指 UI 文案时）
