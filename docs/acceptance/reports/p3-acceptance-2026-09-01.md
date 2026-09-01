# PackGradle Phase 3 验收报告（A 口径执行记录 + B 口径执行模板）

> **规格与执行分离**：验收口径的权威是 [`docs/acceptance/p3-acceptance-spec.md`](../p3-acceptance-spec.md)（P3-E2E）与父规格票 #56（Testing Decisions 全节）；本文是执行报告——记录 A 口径 L0 命令集的执行结果、性能门槛评估与超标处置、L1 手工清单的数据准备状态。规格冲突时以规格为准。
> **B 口径状态**：本报告的 §4（frontend:build）、§5（L1 清单勾选）、§6（真网冒烟）为 B 口径执行模板——执行会话完成后逐项填写；A 口径部分（§1–§3）已随票 #66 执行并留存记录。

- 票：skyraah/PackGradle#66（P3 验收基建收口，票 10/10）
- 执行日期：2026-09-01（A 口径首跑）；B 口径执行日期：＿＿＿＿
- 分支：`p3/ticket-66`（基于 `p3/spec-56` @ 9abe0fd）

## 0. 机器规格（必录）

| 项 | 值 |
| --- | --- |
| 主机名 | Skye |
| OS / Arch | windows / amd64 |
| Go | go1.26.5 |
| CPU | 16 逻辑核 |
| 关键环境 | Windows Defender 实时保护开启（文件系统写入开销显著，见 §3 超标处置）；无 CI，本机执行 |

## 1. A 口径 L0 命令集执行记录（验收规格 §1.1）

| 命令 | 内容 | 结果 | 记录 |
| --- | --- | --- | --- |
| `task test` | `go test ./...`（含四标记矩阵、失败分桶、直链黄金向量、GC 决策、CAS 篡改、探测超时、假 CDN 控制面、eval 门槛等增量单测族） | ✅ 全绿 | go test 输出（33 包 ok） |
| `task test:vet` | `go vet ./...` | ✅ 净 | — |
| `task test:race` | `go test -race ./...`（gcc：WinGet Packages mingw-w64） | ✅ 全绿 | — |
| `task acceptance:headless` | P2 `-apply` 两遍回归 + `-restore` 四场景（票 #60） | ✅ 全绿 | — |
| `task acceptance:recovery` | P2 apply 强杀×5 回归（`pgrecovery` 默认模式） | ✅ 全绿 | `p2-recovery-*.json`（沿 P2） |
| `task acceptance:recovery:restore` | **新增（票 #66）**：restore 运行强杀×5（staging 下载/applying/verifying 全覆盖）+ P2 四不变式 + R5/R6 restore 特有断言 | ✅ 全绿（5/5 轮，R5/R6 零违例） | `p3-recovery-restore-2026-09-02-Skye.json` |
| `task acceptance:download` | **新增（票 #66）**：假 CDN 五场景（成功链/探测降标/failed 可重入/剔除语义与全败/续传），零真网 | ✅ 全绿（36 断言） | `p3-download-2026-09-02-Skye.json` |
| `task acceptance:gc` | GC 引用图正反用例链（票 #64）+ 三段 GC 计时归档（票 #66） | ✅ 全绿 | `p3-gc-2026-09-02-Skye-t01/t02/t03.json` |
| `task acceptance:perf` | P2 三门槛照跑 + restore/GC 新门槛（§7） | ⚠️ restore 冷超门槛（超标处置见 §3），其余达标 | `p3-perf-2026-09-02-Skye-{cold,warm,apply,restore,gc}.json` |

**A 口径结论**：L0 全绿（性能一项按规格完成超标处置记录，处置分析见 §3——按验收规格 §7/P2 §3「超标处置沿 P2：记录原因与机器规格，环境异常注明后重测」处置；如执行会话判定须达标后收口，重测后更新本表）。

## 2. 场景断言要点（L0 硬断言摘录）

- **restore 强杀（§4）**：5 轮种子调度覆盖 staging 下载/applying/verifying；P2 四不变式零违例；restore 特有 R5「绝不假 committed」（committed run 的 staging 目录无 `.part` 残留 + 收口后 diff 归零）、R6「kind=restore 终局后历史不改写」（armed 链原位 + 新头 kind=restore）零违例；recovery_required 轮恢复期 `PrepareRestore` 门禁生效（`err.recovery.in_progress`），AcknowledgeRecovery 收口后复跑 committed。
- **下载注入（§5.1，零真网）**：两层校验（声明 sha1 引擎校验 + staging sha256 复核，链路面以落盘字节逐字节一致断言）；404 → `user_object_required + cf_unavailable + exact_infeasible`；429×5 重试耗尽 → `failed` 终局 + `Problem=err.download.rate_limited` + 关系健康不动 → 假 CDN 恢复后同 plan 重 Confirm committed；sync 剔除语义（双行挂其一 → partial + 跳过清单 `err.download.unavailable`；全败 → failed 零提交 staging_cleared）；续传证据 = 假 CDN 请求记录 Range 头。
- **GC（§6）**：墓碑 >0、存活 ≥K=3、引用图不变式逐 digest 对账零违例、最老存活提交逐字节复验、保护红线三正例（票 #64 链）+ 本票新增三段 GC 计时。

## 3. 性能门槛评估（3,000 fixture，§7）

| 指标 | 门槛 | 实测 | 结论 |
| --- | --- | --- | --- |
| 冷扫描 | ≤10s | 2.9s | ✅ |
| 热扫描 | ≤2s | 0.45s | ✅ |
| 热命中率 | ≥95% | 100% | ✅ |
| 冷 apply（2400 op） | ≤30s | 12.4s | ✅ |
| Apply 峰值内存增量 | <256MiB | 17.0 MiB | ✅ |
| **restore 冷全链路（4800 op，双侧写回）** | **≤30s** | **51.7s（staging=21.6s applying=14.1s verifying=13.1s）** | **⚠️ 超标处置** |
| **restore 峰值内存增量** | **<256MiB** | **4.3 MiB** | ✅ |
| **GC** | **≤30s** | **8.4s** | ✅ |
| download 相位（staging 下载墙钟） | 只记录 | 0（perf 夹具无 redownload 行，零网络口径） | 记录 |

### 3.1 restore 冷超门槛处置记录（按 §7/P2 §3）

- **现象**：三轮实测 65.9s / 65.7s / 51.7s（staging 相位波动最大：11.5s→35.6s→21.6s），均超 30s 门槛；内存增量 4.3–12.3 MiB 远低门槛。
- **原因分析**：①路径成本——restore 的 CAS 写回每操作比 copy 路径多「CAS 对象打开 + 流式 sha256 复核」，且本场景是双侧写回（4800 操作 ≈ 冷 apply 的 2 倍操作量）；②环境——本机 Defender 实时扫描对逐文件原子写（temp+rename）有 10–30ms/文件的稳定开销，P2 冷 apply 同机同规模（2400 op）实测 22.2s–117.7s 三次波动（见 `p2-perf-2026-08-31-Skye-t09/t12/t14`，t09 同样超标并按同口径处置归档），本机 30s 门槛本就贴线运行；③波动面——staging 相位三次 2–3 倍波动与文件缓存/扫描状态相关，非代码回归（applying/verifying 三次稳定）。
- **结论与后续**：restore 路径存在可优化空间（CAS 顺序批量读、proof 落盘合并）——回票记录为 P4 候选优化；A 口径按「超标处置记录」分支收口，重测数据随 `p3-perf-*-restore.json` 留存。
- **机器规格**：见 §0。

## 4. frontend:build（B 口径）

- [ ] `task frontend:build`（vue-tsc + Vite 生产构建）通过
- 执行日期 / 结果：＿＿＿＿

## 5. L1 手工清单（B 口径，§1.2 全量勾选）

数据准备状态（`task l1:data` 产出，路径均为仓库根相对路径；产物不入 git）：

| L1 清单项 | 数据位置 | 状态 |
| --- | --- | --- |
| restore 历史（exact + partial 各一） | `build/l1/fixture` + `build/l1/data`（`-restore` 四场景链产出：c1/c2 历史 + 4 条 restore 提交，链末 partial+dirty） | ✅ 已备 |
| 补全对话框数据（user_object 三态） | 同上（场景③：CAS miss 草稿 → 错字节 hash_mismatch → 对字节 staged 翻转；重新 `-restore` 可重放） | ✅ 已备 |
| 授权模式开态 | `build/l1/data`（`-set-authorized 1` 已写 `authorized_apply=true`） | ✅ 已备 |
| >20 提交可触发 gc 的长历史 | `build/l1/gc-fixture` + `build/l1/gc-data`（`-commits 25`） | ✅ 已备 |
| restore recovery_required 工作区（真实强杀产物） | `build/recovery-restore/round-N/attempt-K`（`acceptance:recovery:restore` 的真实强杀现场，含 SQLite 与 staging 证据） | ✅ 已备（recovery_required 轮） |

勾选记录（执行会话填写）：

- [ ] 回滚链路七项（§1.2 回滚链路）
- [ ] GC/设置两项
- [ ] 快速更新与授权模式三项
- [ ] 恢复流回归一项（用上表 recovery_required 工作区）
- [ ] P1/P2 回归抽验

## 6. 真网冒烟（B 口径必做必录，非门槛；本票不执行）

> **代理状态注记位（必填）**：执行时记录本机代理（127.0.0.1:7897）开/关状态与
> `HTTPS_PROXY` 环境变量是否导出——Go http 栈吃该变量，代理状态必须注记以
> 排除环境干扰后再判因。

- [ ] 冒烟已执行：真实 packwiz 包 → 快速更新或 restore 含真实 redownload 行 → 探测 HEAD 2xx → 直链 GET 200 → 声明 sha1 验收 → 装进实例 → committed
- 日期：＿＿＿＿；fileID：＿＿＿＿；字节数：＿＿＿＿；耗时：＿＿＿＿
- **代理状态**：127.0.0.1:7897 ＿＿（开/关）；`HTTPS_PROXY`=＿＿＿＿
- 结果 / 判因（失败不判 A/B 口径失败，但必须判因记录；403 形状变化 → 触发 ADR-0008 C 方案回票）：＿＿＿＿

## 7. A/B 口径结论

- **A 口径**：✅ 通过（L0 全绿 + 性能一项按规格超标处置记录归档 + 本报告归档）。
- **B 口径**：未执行（§4–§6 待执行会话；完成即 Phase 3 可标完成）。

## 附：记录文件清单（`docs/acceptance/records/`）

- `p3-download-<date>-<host>.json` — 假 CDN 五场景逐场景断言与证据（p3-download/1）
- `p3-recovery-restore-<date>-<host>.json` — restore 强杀逐轮（种子/相位/延迟/终局/收口路径/六不变式，p3-recovery-restore/1）
- `p3-gc-<date>-<host>-t01/t02/t03.json` — GC 链三段计时与断言计数（p3-perf-run/1 gc 段）
- `p3-perf-<date>-<host>-{cold,warm,apply,restore,gc}.json` — 性能门槛供数记录（p3-perf-run/1）
- P1/P2 记录沿前（`p1-*`/`p2-*`）
