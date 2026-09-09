// Package secrets 提供凭据槽位（CONTEXT.md「凭据/凭据槽位」词条）的统一存取。
//
// 形态（ADR-0015）：值经 AES-256-GCM 加密为带版本号的密文信封后存入系统级
// 凭据管理器（Windows 凭据管理器 / macOS Keychain / Linux Secret Service，
// 经 zalando/go-keyring）；解开信封的钥匙（KEK，32 字节随机）经 DPAPI 包裹
// 落在应用数据目录。明文只存在于本包与消费点（CF API 请求头）的内存中；
// 前端只能写入、删除与查看脱敏状态，任何绑定方法不返回明文。
//
// 边界：只收短秘密（≤ MaxValueBytes）；SSH 私钥文件本体不是凭据，其路径
// 属普通配置。缺凭据不降级明文文件——环境变量覆盖（ResolveCFAPIKey）是
// 唯一无桌面通道。
package secrets

import (
	"errors"
	"strings"
	"time"

	"packgradle/internal/errs"
)

// Slot 是凭据槽位的具名标识。固定枚举，不开放任意命名——新增一种凭据即
// 在此追加常量，存储与加密机制不变（grilling Q2-a）。
type Slot string

const (
	// SlotCurseForgeAPIKey 是 CurseForge API 钥匙（CF API 元数据查询的唯一消费方）。
	SlotCurseForgeAPIKey Slot = "curseforge/api-key"
	// SlotGitHubToken 是 GitHub 个人访问令牌（消费方未落地，槽位先注册占位）。
	SlotGitHubToken Slot = "github/token"
	// SlotGitHubSSHPassphrase 是 GitHub SSH 私钥口令；私钥文件本体不进轮子。
	SlotGitHubSSHPassphrase Slot = "github/ssh-passphrase"
)

// ParseSlot 把外部（前端）传来的槽位名还原为枚举。
func ParseSlot(name string) (Slot, error) {
	switch Slot(name) {
	case SlotCurseForgeAPIKey, SlotGitHubToken, SlotGitHubSSHPassphrase:
		return Slot(name), nil
	}
	return "", errs.New("err.secrets.slot_unknown", name)
}

// Status 是槽位的脱敏状态投影。明文永不出现在这里（grilling Q3-a）。
type Status struct {
	Set       bool
	Last4     string
	UpdatedAt time.Time
}

// MaxValueBytes 是槽位值的长度上限（字节）。只收短秘密——远低于 Windows
// 凭据管理器单条目 2560 字节的硬上限，信封化（nonce/标签/base64/元数据）
// 后仍留有安全余量。
const MaxValueBytes = 1024

// 错误码（前端 locale 渲染；R3 口径——detail 只含底层错误文本，不含值本身）。
const (
	CodeValueEmpty       = "err.secrets.value_empty"
	CodeValueTooLarge    = "err.secrets.value_too_large"
	CodeStoreUnavailable = "err.secrets.store_unavailable"
	CodeStoreCorrupt     = "err.secrets.store_corrupt"
)

// ErrNotFound 表示槽位未设置（Get 的哨兵错误；Delete 幂等，不报此错）。
var ErrNotFound = errors.New("secrets: slot not set")

// Store 是凭据槽位的存取端口。生产实现 KeyringStore，测试替身 MemoryStore。
type Store interface {
	// Set 覆写槽位值（全新信封与随机 nonce），并刷新 UpdatedAt。
	Set(slot Slot, value string) error
	// Get 返回明文值；未设置时返回 ErrNotFound。
	Get(slot Slot) (string, error)
	// Delete 清除槽位；未设置时幂等成功。
	Delete(slot Slot) error
	// Status 返回脱敏状态投影（未设置时 Set=false）。
	Status(slot Slot) (Status, error)
}

// validate 归一并校验写入值：trim 后须非空且不超长。空值是调用方语义错误
//（清除走 Delete），超长直接拒绝而不是截断。
func validate(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", errs.New(CodeValueEmpty)
	}
	if len(v) > MaxValueBytes {
		return "", errs.New(CodeValueTooLarge, MaxValueBytes)
	}
	return v, nil
}

// last4 返回值的末四位（Status/EnvService 掩码投影的线索部分）。
func last4(value string) string {
	if len(value) > 4 {
		value = value[len(value)-4:]
	}
	return value
}

// Mask 返回脱敏掩码串：已设置时非空、未设置为空串。EnvService.GetApiKey
// 的既有消费方（Vue 判空用法）依赖这一非空语义（grilling Q6-a）。
func Mask(value string) string {
	if value == "" {
		return ""
	}
	return MaskLast4(last4(value))
}

// MaskLast4 返回掩码点拼末四位的展示形态（Status 投影的展示串）。
func MaskLast4(tail string) string {
	return "••••" + tail
}
