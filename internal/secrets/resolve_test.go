package secrets

import (
	"errors"
	"testing"
)

// 环境变量优先于槽位；未设环境变量时走槽位；两者皆缺报 ErrNotFound。
func TestResolveCFAPIKey(t *testing.T) {
	store := NewMemoryStore()

	t.Setenv(CFEnvVar, "")
	if _, err := ResolveCFAPIKey(store); !errors.Is(err, ErrNotFound) {
		t.Errorf("双缺应报 ErrNotFound，实际 %v", err)
	}

	if err := store.Set(SlotCurseForgeAPIKey, "CFKEY-in-slot"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := ResolveCFAPIKey(store); err != nil || got != "CFKEY-in-slot" {
		t.Errorf("槽位值应生效: %q err=%v", got, err)
	}

	t.Setenv(CFEnvVar, "CFKEY-from-env")
	if got, err := ResolveCFAPIKey(store); err != nil || got != "CFKEY-from-env" {
		t.Errorf("环境变量应优先: %q err=%v", got, err)
	}
}

// 环境变量仅空白视为未设（与槽位值同口径 trim）。
func TestResolveCFAPIKeyBlankEnv(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Set(SlotCurseForgeAPIKey, "CFKEY-in-slot"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	t.Setenv(CFEnvVar, "   ")
	if got, err := ResolveCFAPIKey(store); err != nil || got != "CFKEY-in-slot" {
		t.Errorf("空白环境变量应忽略: %q err=%v", got, err)
	}
}
