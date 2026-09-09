package secrets

import (
	"os"
	"strings"
)

// CFEnvVar 覆盖 curseforge/api-key 槽位（grilling Q8-a）：开发、脚本与
// headless 流程的无桌面通道，存在且非空时优先于轮子。只此一个——GitHub
// token 的环境变量等消费方落地时随其用途定名。
const CFEnvVar = "PACKGRADLE_CF_API_KEY"

// ResolveCFAPIKey 返回生效的 CurseForge 钥匙：环境变量优先，其次槽位。
// 两者皆缺返回 ErrNotFound；存储不可用返回其结构化错误——调用方（CF 面）
// 统一归一为 err.cf.api_key_missing，设置页的 SecretsService.Status 会把
// 真实原因透出来。
func ResolveCFAPIKey(store Store) (string, error) {
	if v := strings.TrimSpace(os.Getenv(CFEnvVar)); v != "" {
		return v, nil
	}
	v, err := store.Get(SlotCurseForgeAPIKey)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}
