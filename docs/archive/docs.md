> ⚠️ **已归档（2026-08-28）**：重新设计讨论完整实录（Snapshot/SyncCommit/对象存储概念的起源）。核心概念已沉淀进 [架构文档](../architecture/)，仅作历史参考，不再维护。

# PackGradle 重新设计讨论记录

## 1. 重新定义 PackGradle 的真实目的

### 用户

我想重新设计我的packgradle，因为我觉得他跟我真实目的比较背离，我实际上需要他的原因是，packwiz管理的实例项目很干净健康，方便直接上传至github方便多人开发，但是由于mod目录结构不同，以及为了保持GitHub项目在本地环境的目录健康（统一管理project），我无法使用minecraft启动器直接读取packwiz实例项目，我需要一个方案将两边的目录/文件可选同步，同时在启动器环境下没有对应的实例时，可以快速的根据packwiz实例新建，并且后续在mod变动时能否双方得知差异并同步差异一切的主旨都是为了本地实例和真正协作的实例能够隔离管理的同时保持同步，同时避免来回复制粘贴行为占用硬盘空间和挤占效率

### 助手

你的这个重新定义非常重要。

你现在描述的 PackGradle，本质上已经不是一个“整合包开发辅助工具”，而更接近：

> 一个连接「可协作的 Packwiz Project」与「本地 Minecraft Runtime」的双向同步与状态管理工具。

核心问题不是“怎么管理项目”，而是：

```text
        GitHub / Git
             │
             ▼
      Packwiz Project
             │
             │ 同步
             ▼
     PackGradle Sync Layer
             │
             ▼
     Minecraft Runtime
             │
             ▼
      Prism Launcher
```

两边应该保持：

```
项目环境
    ↓
适合 Git / 多人协作 / 干净
```

以及：

```
运行环境
    ↓
适合 Minecraft Launcher / 实际运行
```

彼此独立，但 PackGradle 负责建立两者之间的关系。

### 关键设计

#### Project

真正参与 Git 协作的 Packwiz 项目：

```
project/
├── pack.toml
├── index.toml
├── mods/
│   ├── sodium.pw.toml
│   ├── jei.pw.toml
│   └── ...
├── config/
├── kubejs/
└── ...
```

#### Runtime

Prism Launcher 实际运行的实例：

```
instance/
├── .minecraft/
│   ├── mods/
│   │   ├── sodium-xxx.jar
│   │   └── ...
│   ├── config/
│   ├── kubejs/
│   └── ...
```

二者不再要求目录结构完全一致。

而是建立：

```
Logical File
      │
      ├── Project Path
      │
      └── Runtime Path
```

例如：

```
mod:sodium
    │
    ├── project: mods/sodium.pw.toml
    └── runtime: .minecraft/mods/sodium-0.6.5.jar
```

这样以后 Mod 更新的时候，不需要简单粗暴地复制整个目录，而是：

```
Project State
      │
      ▼
    Diff
      │
      ▼
Runtime State
```

反过来也一样：

```
Runtime State
      │
      ▼
    Diff
      │
      ▼
Project State
```

------

# 2. 同步机制应该如何实现

### 用户

那么同步部分需要怎么实现，因为对于整合包而言，并不像其他的编程项目中存在编译，整合包本身的运行环境是直接包含项目源代码的

### 助手

这里最重要的概念是：

> Minecraft 整合包虽然没有传统意义上的“编译”，但 PackGradle 仍然可以拥有类似编译系统的“状态解析 → 差异计算 → 应用变更”流程。

不要把它理解为：

```
Project → Compile → Runtime
```

而应该理解为：

```
Project State
      │
      ▼
  Normalize
      │
      ▼
 Logical State
      │
      ▼
    Diff
      │
      ▼
 Reconciliation
      │
      ▼
Runtime State
```

------

## Project 与 Runtime 不应该直接互相覆盖

例如：

```
Project:

mods/create.pw.toml
```

Runtime：

```
.minecraft/mods/create-6.0.0.jar
```

这不是简单的：

```
A → B
```

而是：

```
Logical Mod: create
       │
       ├── Project representation
       │     mods/create.pw.toml
       │
       └── Runtime representation
             mods/create-6.0.0.jar
```

因此 PackGradle 应该首先把两边都转换成统一的逻辑状态：

```
type LogicalFile struct {
    ID          string
    Kind        string
    ProjectPath string
    RuntimePath string
    Hash        string
}
```

然后：

```
Project Scanner
       │
       ▼
Project State

Runtime Scanner
       │
       ▼
Runtime State

Project State
       │
       ├──────────────┐
       │              │
       ▼              ▼
       Diff Engine
              │
              ▼
        Sync Plan
              │
              ▼
         Sync Engine
```

------

# 3. 后端是否适合继续使用 Go

### 用户

这样的后端系统还适合用go语言吗？还是说用其他的框架更好一点

### 助手

非常适合，而且我反而建议你继续用 Go。

你现在重新定义后的 PackGradle，本质上不是一个“Minecraft 启动器”，而是一个：

> 跨目录、文件系统监控、Hash/Manifest、差异计算、同步执行、进程/Launcher 集成的本地开发工具。

这正好是 Go 很擅长的领域。

如果准备继续使用 Wails：

```
┌──────────────────────────────┐
│          Vue / Wails         │
│       UI / 状态 / 交互        │
└──────────────┬───────────────┘
               │
            Go API
               │
┌──────────────▼───────────────┐
│        PackGradle Core       │
│                              │
│  Project Scanner             │
│  Runtime Scanner             │
│  Packwiz Parser              │
│  Mapping Engine              │
│  Snapshot / State            │
│  Diff Engine                 │
│  Sync Engine                 │
│  File Watcher                │
│  Launcher Adapter            │
└──────────────┬───────────────┘
               │
       Windows / Linux / macOS
```

Go + Wails 非常匹配。

------

## Go 适合的原因

你的核心工作大量是：

```
文件系统
目录扫描
文件 Hash
文件复制/移动
符号链接/Junction
文件监听
进程启动
HTTP 下载
JSON/TOML
并发任务
跨平台路径处理
```

这些都是 Go 的强项。

例如：

```
func (e *Engine) Sync(ctx context.Context) error
```

里面可以自然地：

```
Scanner
 ↓
goroutine
 ↓
Hash
 ↓
Diff
 ↓
Worker Pool
 ↓
Copy/Download
```

------

## 不建议为了性能换 Rust

你的真正瓶颈主要是：

```
磁盘 IO
网络下载
Modrinth / CurseForge API
Minecraft 文件数量
用户硬盘速度
```

而不是语言性能。

因此 Rust 带来的收益有限，反而会增加：

```
开发复杂度
Windows API 处理复杂度
GUI 集成复杂度
异步代码复杂度
```

------

## C# 是唯一值得考虑的竞争者

如果只做 Windows：

```
C# + .NET
```

也非常适合。

但如果目标是：

```
Windows
Linux
macOS
```

Go 更合适。

最终建议：

> 保留 Go + Wails + Vue + TypeScript，不要为了重构业务模型而推翻技术栈。

------

# 4. Go 后端应该如何分层

建议分成两层。

## Core

完全不依赖 Wails：

```
internal/core/

project/
runtime/
mapping/
snapshot/
diff/
sync/
```

这里不允许：

```
import Wails
知道 Vue
直接处理 UI
```

例如：

```
result := engine.Compare(
    projectState,
    runtimeState,
    baseState,
)
```

## Application

负责：

```
Wails
事件
任务
进度
日志
用户操作
```

例如：

```
app/
├── project_service.go
├── sync_service.go
├── runtime_service.go
└── event_service.go
```

Vue 应该看到：

```
SyncStarted
SyncProgress
SyncCompleted
ConflictDetected
```

而不是直接操作：

```
os.Copy(...)
```

------

# 5. Base / Temp 存储是否需要数据库

### 用户

对于base的temp存储，是否需要用到数据库以应对可能出现的数据管理？比如sqlite，还是给出一些比较简单的解决方案，json/toml/纯文本。。。

### 助手

第一版不要上 SQLite。

对于现在的 PackGradle，Base Snapshot 本质上不是业务数据库，而是：

> 某一个 Project ↔ Runtime 关系的“上次同步快照”。

数据量通常很小，而且结构高度固定。

用 JSON 就足够。

------

## Snapshot 示例

例如：

```
Collapse/
├── pack.toml
├── index.toml
├── mods/
├── config/
├── kubejs/
│
└── .packgradle/
    └── state.json
```

`state.json`：

```
{
  "version": 1,
  "project": {
    "path": "C:\\Projects\\Collapse"
  },
  "runtime": {
    "type": "prism",
    "path": "C:\\PrismLauncher\\instances\\Collapse"
  },
  "snapshot": {
    "created_at": "2026-08-22T06:20:00+08:00"
  },
  "files": {
    "config/foo.toml": {
      "project_hash": "sha256:...",
      "runtime_hash": "sha256:..."
    },
    "kubejs/server.js": {
      "project_hash": "sha256:...",
      "runtime_hash": "sha256:..."
    }
  }
}
```

更进一步，如果 Base 的定义就是：

> 上次同步完成时双方应该达到的状态

那么甚至可以只保存：

```
{
  "files": {
    "config/foo.toml": {
      "hash": "sha256:..."
    }
  }
}
```

------

# 6. Snapshot 最好不要叫 Base

建议内部使用：

```
Snapshot
```

而不是 Base。

例如：

```
type Snapshot struct {
    Version   int                      `json:"version"`
    CreatedAt time.Time                `json:"created_at"`
    Files     map[string]FileSnapshot  `json:"files"`
}

type FileSnapshot struct {
    Hash string `json:"hash"`
}
```

概念会更清晰：

```
Project State
Runtime State
Last Snapshot
```

------

# 7. JSON、TOML、纯文本、SQLite 的职责划分

### TOML

适合：

```
人类编辑的配置
```

例如：

```
name = "Collapse"
path = "..."
```

### JSON

适合：

```
程序生成
程序读取
结构化状态
```

所以：

```
pack.toml       → TOML
PackGradle 配置 → TOML
Snapshot        → JSON
```

### 纯文本

不推荐。

很快会遇到：

```
版本升级
字段增加
Mapping
Mod
删除状态
Runtime path
Sync metadata
```

然后不得不自己设计一套格式。

### SQLite

等以后出现以下需求再考虑：

```
最近 100 次同步
同步历史
大量项目
多个 Runtime
复杂查询
版本记录
```

------

# 8. Snapshot 应该与 Project 配置分离

建议：

```
%AppData%\PackGradle\
├── config.toml
└── projects.json
```

项目：

```
C:\Projects\Collapse\
├── pack.toml
├── index.toml
├── mods/
├── config/
└── .packgradle/
    └── snapshot.json
```

`.packgradle` 应该加入：

```
.packgradle/
```

因为：

```
pack.toml
index.toml
mods/*.pw.toml
```

属于协作状态。

而：

```
.packgradle/snapshot.json
```

属于本机状态。

不同开发者不能共享同一个 Snapshot。

------

# 9. Snapshot 应该原子写入

不要直接：

```
os.WriteFile("snapshot.json", data, ...)
```

应该：

```
snapshot.json.tmp
        ↓
写完整
        ↓
fsync
        ↓
rename
        ↓
snapshot.json
```

防止程序崩溃或断电导致 Snapshot 损坏。

同时可以保存：

```
{
  "version": 1
}
```

方便未来进行 Snapshot 数据结构迁移。

------

# 10. 大型整合包下 JSON 是否会因为太长而影响性能

### 用户

对于单个实例，300条pwtoml，＜300条config，加上很多零散文件，kubejs的脚本在符合编辑规范的情况下还是挺多的，如果实例再多几个，Snapshot还方便用json吗，超长json会不会有点性能问题？

### 助手

按照你描述的大型整合包：

```
mods/*.pw.toml：约 300
config：约 300
kubejs：几十到几百
其他零散文件：若干
```

假设：

```
约 1100 个逻辑文件
```

对于 JSON 来说依然很小。

------

## 粗略估算

一个文件状态可能是：

```
{
  "id": "file:config/example.toml",
  "kind": "file",
  "hash": "sha256:xxxxxxxx",
  "project": "config/example.toml",
  "runtime": ".minecraft/config/example.toml"
}
```

即使按照 500～1000 字节计算：

```
1000 × 1000 byte
≈ 1 MB
```

即使：

```
3000 files × 1 KB
≈ 3 MB
```

对于现代机器也完全不是问题。

真正的性能瓶颈是磁盘 IO，而不是 JSON 解析。

------

# 11. Snapshot 千万不要保存文件内容

错误设计：

```
{
  "files": [
    {
      "content": "整个文件内容"
    }
  ]
}
```

尤其不能把大量：

```
kubejs
config
```

源码直接塞进 Snapshot。

Snapshot 应该保存：

```
path
hash
size
mtime
metadata
```

例如：

```
{
  "path": "kubejs/server_scripts/main.js",
  "hash": "sha256:xxxx",
  "size": 12345,
  "modified": "2026-08-22T06:00:00"
}
```

也就是说：

> Snapshot 是文件状态，不是文件备份。

------

# 12. Mod 不应该被当作普通文件处理

例如：

```
mods/sodium.pw.toml
```

和：

```
.minecraft/mods/sodium-0.6.5.jar
```

不是同一个文件实体。

应该抽象成：

```
Logical Mod
```

例如：

```
{
  "id": "mod:sodium",
  "version": "0.6.5",
  "hash": "...",
  "side": "client",
  "runtime_file": "minecraft/mods/sodium-0.6.5.jar"
}
```

因此 Snapshot 最好分区：

```
{
  "mods": {},
  "files": {},
  "directories": {}
}
```

例如：

```
{
  "mods": {
    "sodium": {
      "version": "0.6.5",
      "hash": "..."
    }
  },

  "files": {
    "config/foo.toml": {
      "hash": "..."
    }
  }
}
```

------

# 13. 多实例情况下 JSON 依然足够

例如：

```
Projects/
├── Collapse
│   └── .packgradle/snapshot.json
│
├── SkyFactory
│   └── .packgradle/snapshot.json
│
└── TechPack
    └── .packgradle/snapshot.json
```

即使每个 Snapshot：

```
1～5 MB
```

总量也只是：

```
10～20 MB
```

完全可以接受。

真正开始不适合 JSON 的情况是：

```
1000 个整合包
×
10000 文件
×
大量历史数据
```

这时才应该考虑 SQLite。

------

# 14. 可以采用 JSON + 分片

如果不希望单文件过大，可以：

```
.packgradle/
│
├── snapshot.json
│
└── cache/
    ├── mods.json
    ├── files.json
    └── dirs.json
```

`Snapshot` 只保存引用：

```
{
  "version": 1,
  "mods": "mods.json",
  "files": "files.json"
}
```

这样修改 config 时不需要重写整个 Snapshot。

不过对于第一版而言，单个 JSON 仍然足够。

------

# 15. Hash 缓存可以显著减少扫描成本

不要每次启动都重新 SHA256 所有文件。

可以记录：

```
{
  "path": "config/foo.toml",
  "size": 1234,
  "mtime": 123456789,
  "hash": "xxxx"
}
```

下一次扫描：

```
size 相同
+
mtime 相同
```

即可认为文件未变化，无需重新计算 Hash。

只有变化时才重新 Hash。

这对大型整合包非常有意义。

------

# 16. 第一版建议的状态目录

建议：

```
.packgradle/
│
├── snapshot.json
├── cache.json
└── mappings.json
```

其中：

### snapshot.json

保存：

```
上次同步状态
```

### cache.json

保存：

```
文件 Hash 缓存
```

### mappings.json

保存：

```
逻辑文件 ↔ 实际路径
```

------

# 17. 关于回滚：Snapshot 不够了

### 用户

在多人协作的开发环境下，可能会遇到run time和源码部分都修改的情况，本地开发修改Run time，Git hub同步源码。如果涉及到修改历史回滚的情况呢？我这一次同步了，但是我想回到上次同步的状态

### 助手

这个问题触及 PackGradle 架构里最容易被低估的部分：

> Snapshot 只能解决“现在双方哪里不同”，不能解决“我已经同步了，但我想回到同步之前”。

这已经属于：

```
Versioned State
```

或者：

```
Runtime History
```

------

# 18. Git 不能直接解决 Runtime 回滚

虽然源码由 Git 管理，但 Runtime 通常包含：

```
.minecraft/
├── mods/
├── config/
├── kubejs/
├── shaderpacks/
├── saves/
└── ...
```

其中可能存在大量：

- 二进制 JAR
- 日志
- 缓存
- 世界存档

这些都不适合直接通过 Git 管理。

因此：

```
Git
    ↓
源码协作历史

PackGradle
    ↓
Runtime 生命周期与同步历史
```

两者职责不同。

------

# 19. 需要区分三个历史层

可以理解为：

```
              Git History
                   │
                   ▼
          Packwiz Project History
                   │
                   │ Sync
                   ▼
          PackGradle Sync History
                   │
                   ▼
            Runtime Snapshot
```

### Git History

负责：

```
pack.toml
index.toml
*.pw.toml
config
kubejs
```

也就是团队协作历史。

### PackGradle Sync History

记录某一次同步：

```
Sync #23

时间：
2026-08-22 06:30

来源：
Project → Runtime

Changes:
+ sodium 0.6.5
+ jei 19.5
modified:
config/create.toml
deleted:
oldmod.jar
```

### Runtime Snapshot

记录实际恢复所需的状态：

```
minecraft/mods/sodium.jar
hash: abc123

minecraft/config/create.toml
hash: def456
```

------

# 20. 一次同步应该成为一个“版本节点”

例如：

```
State A
   |
  sync
   |
State B
   |
  sync
   |
State C
```

现在 Runtime 是：

```
State C
```

如果想回到上一次：

```
State C
   |
reverse diff
   |
State B
```

这就是：

```
Rollback
```

------

# 21. 不需要保存完整 Runtime，可以保存 Snapshot + Diff

例如：

第一次：

```
Snapshot 001

config/foo.toml = A
```

第二次：

```
A → B
```

保存：

```
{
  "from": 1,
  "to": 2,
  "changes": [
    {
      "path": "config/foo.toml",
      "oldHash": "aaa",
      "newHash": "bbb"
    }
  ]
}
```

第三次：

```
B → C
```

同理。

这样就形成：

```
S1
 |
S2
 |
S3
```

------

# 22. 但 Hash 本身不能用于恢复

如果只保存：

```
oldHash
newHash
```

你只能知道发生了变化。

不能恢复文件。

因此需要：

```
Content Cache / Object Store
```

例如：

```
.packgradle/

objects/
    sha256-abcd
    sha256-defg
```

某个文件内容：

```
hash = abc123
```

对象存储：

```
objects/
    abc123
```

回滚时：

```
找到旧 hash
      ↓
恢复旧内容
```

这就是类似 Git Object Store 的思路。

------

# 23. Mod 回滚不要保存 JAR 历史

例如：

```
Create 6.0
    ↓
Create 6.1
```

不要缓存：

```
create-old.jar
create-new.jar
```

而是记录：

```
mod:create
version=6.0
hash=xxxx
```

恢复时重新下载对应版本。

因此：

### 配置文件

保存内容。

### KubeJS

保存文本内容。

### Mod

保存：

```
ID
Version
Hash
Download information
```

恢复时重新下载。

### 大文件

根据需求：

```
只保存 hash
或
可选缓存
```

------

# 24. 多人协作下的冲突

例如：

```
开发者 A：
Runtime 修改 config

开发者 B：
Git 修改同一个 config
```

此时不能简单覆盖。

应该：

```
Project State
Runtime State
Base Snapshot
       │
       ▼
Three-way Diff
       │
       ▼
Conflict
```

即：

```
Base
  │
  ├── Project changed
  │
  └── Runtime changed
```

如果双方修改的是不同内容：

```
自动合并
```

如果修改冲突：

```
Conflict
```

------

# 25. Snapshot 的概念应该升级

原来的：

```
Snapshot = 当前状态
```

升级为：

```
Snapshot = 状态节点
```

结构类似：

```
type Snapshot struct {
    ID
    Parent
    Timestamp

    ProjectState
    RuntimeState

    Changes
}
```

形成：

```
S1
 |
S2
 |
S3
```

这已经非常接近 Git Commit 的概念。

------

# 26. PackGradle 最终架构可以理解为

```
                GitHub
                  |
                  |
            Packwiz Project
                  |
                  |
             Sync Engine
                  |
      -------------------------
      |                       |
 Snapshot Manager       Runtime Manager
      |                       |
 History                Prism Instance
      |
 Object Cache
```

PackGradle 并不是取代 Git。

而是：

> Git 管源码，PackGradle 管 Runtime 与源码之间的状态关系以及 Runtime 生命周期。

------

# 27. 这种功能有没有现成库

### 用户

在这一方面有现成的库让我们使用吗？

### 助手

有，但是需要区分你想复用的是哪一部分。

PackGradle 现在混合了：

1. 文件版本存储
2. Snapshot 管理
3. Diff
4. 三方合并
5. 文件同步
6. Minecraft 特殊资源映射（`pw.toml → jar`）

目前没有一个现成库能完整覆盖，因为你的场景比普通文件同步特殊：

> Git 管源码，PackGradle 管 Runtime，两者之间存在语义映射。

但是可以组合现有组件。

------

# 28. 可以借鉴 Git 模型

Git 的核心模型：

```
Blob
 ↓
Tree
 ↓
Commit
 ↓
Reference
```

PackGradle 可以对应：

```
File Content
 ↓
Logical File
 ↓
Sync Commit
 ↓
Runtime State
```

概念几乎一致。

------

# 29. Go 中可以参考 go-git

go-git 可以处理：

- Git repository
- Commit
- Object
- Diff

但不建议直接拿 Git 仓库管理 Runtime。

因为 Git 假设：

```
文件 = 源码
文件名 = 唯一路径
版本 = 提交
```

而 PackGradle 中：

```
mods/sodium.pw.toml
        |
        ↓
sodium-0.6.5.jar
```

不是同一个文件。

所以：

> go-git 可以参考 Git 模型，但不应该成为 PackGradle 的核心。

------

# 30. 可以参考内容寻址 Snapshot / Backup 系统

例如：

```
Git object
Docker layer
restic snapshot
```

共同思路：

```
snapshot
   |
   ├── tree
   |
   └── blobs
```

一个 Snapshot：

```
config/foo.toml
    ↓
hash abc
```

另一个：

```
config/foo.toml
    ↓
hash def
```

两个版本只保存变化。

这非常接近 PackGradle 的需求。

------

# 31. 可以参考 FVS / snapdir 一类库

例如内容寻址 Snapshot 系统可以提供：

```
snapshot
deduplication
content-addressed storage
history
restore
diff
```

但这些库更偏：

```
备份整个目录
```

而 PackGradle 的目标是：

```
Project ↔ Runtime 双向同步
```

因此不建议直接把这类库作为核心。

更适合：

> 借鉴其数据模型，而不是完整依赖。

------

# 32. 更适合 PackGradle 的方案：SQLite + Object Store

可以设计成：

```
.packgradle/

history.db

objects/
    sha256/
        abcd
        efgh
```

SQLite 保存：

```
commits
------------
id
parent
time

files
------------
commit_id
logical_id
path
hash

changes
------------
commit_id
type
old_hash
new_hash
```

Object Store 保存：

```
hash
 ↓
真实文件内容
```

整体结构：

```
SQLite
    |
    +-- Snapshot graph
    +-- Commit
    +-- Changes
    +-- Metadata

Object Store
    |
    +-- 文件内容
```

这实际上就是一个针对 Minecraft Runtime 的“小型 Git”。

------

# 33. 但不要保存所有文件

特别是：

```
mods/*.jar
```

不要每次同步都保存整个 JAR。

应该：

```
Mod:
    ID
    Version
    URL
    Hash
```

恢复时：

```
重新下载
```

而：

```
config
kubejs
scripts
```

这类文本内容才适合进入 Object Store。

------

# 34. Diff 可以使用现有库，但逻辑 Diff 需要自己实现

文件系统 Diff 可以参考：

```
fsdiff
```

用于：

```
Snapshot A
      |
     diff
      |
Snapshot B
```

输出：

```
create
modify
remove
rename
```

但是 Minecraft 中：

```
mod:create
```

这种逻辑对象不是普通文件，所以最终仍需要自己的：

```
Logical Diff Engine
```

------

# 35. 三方合并也不能完全依赖普通文本 Diff

例如：

```
config/*.toml
```

可以考虑：

```
TOML merge
```

而：

```
kubejs/*.js
```

可以进行文本三方合并。

但：

```
mods/*.pw.toml
```

应该使用 Packwiz 语义。

因此建议：

```
Merge Adapter
```

例如：

```
config/*.toml
       ↓
TOML Merge

kubejs/*.js
       ↓
Text Merge

mods/*.pw.toml
       ↓
Packwiz-aware Merge
```

------

# 36. Packwiz 本身应尽量复用

Packwiz 已经定义：

```
mod identity
version
hash
download
side
```

所以 Mod 历史应该使用 Packwiz 元数据，而不是自己重新实现 JAR 管理。

例如：

```
Before:
Create 6.0

After:
Create 6.1
```

PackGradle 记录：

```
mod:create
6.0 → 6.1
```

而不是保存两个 JAR。

------

# 37. 最终推荐的技术组合

```
PackGradle
│
├── Sync Engine
│     └── 自己实现
│
├── History Engine
│     └── SQLite
│
├── Object Store
│     └── SHA256 / BLAKE3 + 文件对象
│
├── Snapshot
│     └── JSON / SQLite
│
├── Diff Engine
│     └── 自己实现逻辑 Diff
│
├── Merge Engine
│     └── diff3 / TOML / Packwiz Adapter
│
├── File Watcher
│     └── fsnotify
│
├── Packwiz Adapter
│     └── 复用 Packwiz
│
└── Prism Adapter
      └── Prism Launcher
```

------

# 38. 最终方向

目前整个 PackGradle 的定位可以概括成：

> **Git 管理整合包的协作源码，PackGradle 管理本地 Minecraft Runtime，并通过 Snapshot、Diff、Sync、Conflict、Rollback 将两者连接起来。**

核心数据模型逐渐从：

```
Project
Instance
Mapping
```

升级为：

```
Project
Runtime
Logical Resource
Snapshot
Sync Commit
Diff
Object
Mapping
```

其中最重要的新概念是：

```
SyncCommit
```

一次同步就是一次 Commit：

```
SyncCommit
├── ID
├── Parent
├── Timestamp
├── Project State
├── Runtime State
├── Changes
└── Objects
```

这样：

```
同步
    ↓
产生 Commit

查看历史
    ↓
Commit History

撤销同步
    ↓
Rollback

发生冲突
    ↓
Three-way Merge

恢复历史状态
    ↓
Checkout / Restore
```

最终 PackGradle 可以理解成：

> **Git for Minecraft Runtime**

但它并不取代 Git，而是与 Git 分工：

```
Git
│
└── 协作源码
      │
      │
      ▼
Packwiz Project
      │
      │
      ▼
PackGradle
      │
      ├── Sync
      ├── Diff
      ├── Conflict
      ├── History
      └── Rollback
      │
      ▼
Prism Runtime
```

对于当前阶段，推荐：

```
Go + Wails + Vue
        +
JSON（当前状态）
        +
SQLite（历史）
        +
Content-addressed Object Store（可恢复文件）
        +
Packwiz Adapter
        +
Prism Adapter
```

不要一开始实现完整 Git。

优先把以下四个核心对象设计好：

```
LogicalResource
Snapshot
SyncCommit
Change
```

一旦这四个数据结构确定，后续的：

```
双向同步
差异检测
冲突检测
回滚
历史记录
Runtime 恢复
```

都会自然建立在同一个模型上。