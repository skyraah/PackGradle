---
status: accepted
date: 2026-09-10
---

# 0015 · 凭据存储：系统保险柜 + 信封加密（槽位制 · 明文不出后端）

凭据轮子（grilling 2026-09-10，两轮九问决议）的决策产物。此前 CurseForge API Key 以明文存于 `%APPDATA%\PackGradle\config.toml` 的 `curseforge_api_key` 字段（`internal/appconfig/config.go`，已删），且 `EnvService.GetApiKey` 整串明文回前端（前端仅判空）——盘上与前后端通道两处明文暴露。钥匙唯一真消费点是 CF API 请求头 `x-api-key`（`internal/curseforge/client.go:50`，ADR-0011 §9 审计在案），其余全链路含 pgheadless 走免钥匙直链（ADR-0008）。

锚点：ADR-0011 R3（凭据永不进日志/诊断/错误 detail——本 ADR 把 R3 的载体从「config 值」迁到「槽位值」，静置密文断言由 `internal/secrets` 测试承接）、ADR-0008（免钥匙直链——headless 验收链不需钥匙，环境变量覆盖是唯一无桌面通道）。

## 0. 决议清单

- **Q1-b** 加密分层：系统凭据管理器（Keyring；Windows 凭据管理器）承载 AES-256-GCM 密文信封，信封钥匙（KEK，32 字节随机）经 DPAPI（用户域）包裹落 `credentials.kek`。防不了「已能以当前用户身份运行代码」的对手（其可调 DPAPI），买到的是密文信封可迁移与导出面收窄；主口令档（1Password 式）否决。
- **Q2-a** 槽位制、只收短秘密：固定枚举 `curseforge/api-key`、`github/token`、`github/ssh-passphrase`，上限 1KiB；SSH 私钥文件本体不是凭据（路径属普通配置）——Windows 凭据管理器单条目 2560 字节硬上限（`CRED_MAX_CREDENTIAL_BLOB_SIZE`）自然规避。
- **Q3-a** 明文不出后端：绑定方法面只有 Set/Delete/Status（掩码 + 末四位 + 设置时间），无任何返回明文的通路。
- **Q4** 无迁移：项目尚处开发期、无生产用户无存量（长期记忆 packgradle-dev-stage），`curseforge_api_key` 字段直接删除；r3 金丝雀测试改为文件层断言。
- **Q5-a** 缺钥匙报既有 `err.cf.api_key_missing`；环境变量 `PACKGRADLE_CF_API_KEY` 优先于槽位（dev/headless 通道）；明文文件回退永久否决——初始化失败退 `BrokenStore`（四操作恒报 `err.secrets.store_unavailable`），应用照常启动。
- **Q6-a** 落点：`internal/secrets` 包 + `internal/service/secretsservice.go`（Wails 出口）；`EnvService.GetApiKey/SetApiKey` 薄委托（Get 已设置回非空掩码串——main 分支 Vue 判空用法零改动；两方法随 Vue 退场一并删除）。
- **Q8-a** 环境变量只开 CF 一个；GitHub token 的变量名随其消费方落地时定。

## 1. 形态

```
槽位值（明文，仅内存）
  → payload{secret, updated_at}（内层 JSON）
  → AES-256-GCM（随机 12B nonce，密文自带认证标签）
  → envelope{v:1, n, c}（外层 JSON，自描述）
  → 系统凭据管理器条目 PackGradle:<slot>
KEK：32B 随机，一次生成；DPAPI 包裹（crypt32 纯 syscall，x/sys 自带）存 <root>/credentials.kek（0600）
```

- 值与元数据同密：盘上与凭据管理器条目里没有任何明文或可读元数据；Status 解密后投影。
- 每次写入全新 nonce（同值两次封装密文不同）；篡改、异钥、版本不符一律 `err.secrets.store_corrupt`（不区分原因——条目已不可信）。
- KEK 文件损坏不静默重建（重建会让既有条目变成永远解不开的乱码），报 `err.secrets.store_unavailable` 让用户重填。

## 2. 库选型（研究代理 2026-09-10 回报定案）

- `github.com/zalando/go-keyring` v0.2.8（活跃维护；Wails 官方安全指南点名、Wails3 内部自用同款；`MockInit` 内存测试钩子；注意其依赖是 `danieljoos/wincred` 双 o）。此前已是 wails 间接依赖，本次转直接引用。
- 99designs/keyring 否决：2022-12 后停更约四年，不引停更库进凭据链路。
- DPAPI 走 `golang.org/x/sys/windows` 自带 `CryptProtectData`（零新依赖，go.mod 原有直接依赖）。

## 3. 边界与非目标

- 只防「盘上与导出面」，不防同用户代码执行（明说了：那种对手连 DPAPI 也能解）。
- 非 Windows 无 DPAPI：KEK 以 0600 裸形态落盘（`kek_other.go`）——密文本体仍在系统凭据服务内，让步仅影响第二道锁；Windows 主平台不受影响。
- 不做凭据导出/多机迁移功能（信封格式已为其留好空间，v1 不做）。
- GitHub token / SSH 口令槽位先注册占位，消费方落地前无读者。

## 4. 错误码（locale 一致性测试把关，五键已入 zh-CN.json）

`err.secrets.slot_unknown`（{0}=槽位名）、`err.secrets.value_empty`、`err.secrets.value_too_large`（{0}=上限字节）、`err.secrets.store_unavailable`、`err.secrets.store_corrupt`。
