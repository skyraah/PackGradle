package secrets

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"packgradle/internal/errs"
)

// mockKeyring 把 zalando 全局实现换成内存替身（官方 MockInit；不碰真
// 凭据管理器）。
func mockKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
}

// 完整生命周期：Set → Get/Status 命中 → Delete → Get 报 ErrNotFound、
// Status 报未设置；Delete 幂等。
func TestKeyringStoreLifecycle(t *testing.T) {
	mockKeyring(t)
	store := &KeyringStore{kek: testKEK(t)}

	if _, err := store.Get(SlotCurseForgeAPIKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("未设置的槽位应报 ErrNotFound，实际 %v", err)
	}
	if st, err := store.Status(SlotCurseForgeAPIKey); err != nil || st.Set {
		t.Errorf("未设置的槽位 Status 应为 Set=false，实际 %+v err=%v", st, err)
	}

	if err := store.Set(SlotCurseForgeAPIKey, "  CFKEY-lifecycle  "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(SlotCurseForgeAPIKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "CFKEY-lifecycle" {
		t.Errorf("Get 应还原 trim 后的值: %q", got)
	}
	st, err := store.Status(SlotCurseForgeAPIKey)
	if err != nil || !st.Set {
		t.Fatalf("Status 应命中: %+v err=%v", st, err)
	}
	if st.Last4 != "ycle" || st.UpdatedAt.IsZero() {
		t.Errorf("Status 投影不符: %+v", st)
	}

	if err := store.Delete(SlotCurseForgeAPIKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(SlotCurseForgeAPIKey); err != nil {
		t.Errorf("Delete 应幂等，二次删除报 %v", err)
	}
	if _, err := store.Get(SlotCurseForgeAPIKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后 Get 应报 ErrNotFound，实际 %v", err)
	}
}

// 静置形态（R3 口径，新载体）：凭据管理器条目里只有密文信封——不含明文值，
// 是含 v/n/c 的 JSON；明文只在 Get 之后出现。
func TestKeyringStoreCiphertextAtRest(t *testing.T) {
	mockKeyring(t)
	store := &KeyringStore{kek: testKEK(t)}
	if err := store.Set(SlotGitHubToken, "ghp_canaryAtRest1234"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := keyring.Get(keyringService, string(SlotGitHubToken))
	if err != nil {
		t.Fatalf("读原始条目: %v", err)
	}
	if strings.Contains(raw, "ghp_canaryAtRest1234") {
		t.Fatalf("凭据管理器条目含明文值（R3 违规）: %s", raw)
	}
	if !strings.Contains(raw, `"v":1`) {
		t.Errorf("条目应为 v1 信封 JSON: %s", raw)
	}
}

// KEK 换代场景：另一把钥匙读不了既有条目（store_corrupt，不静默返回空）。
func TestKeyringStoreWrongKEK(t *testing.T) {
	mockKeyring(t)
	a := &KeyringStore{kek: testKEK(t)}
	if err := a.Set(SlotCurseForgeAPIKey, "CFKEY-kekA"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	b := &KeyringStore{kek: testKEK(t)}
	if _, err := b.Get(SlotCurseForgeAPIKey); errs.CodeOf(err) != CodeStoreCorrupt {
		t.Errorf("异钥读取应报 %s，实际 %v", CodeStoreCorrupt, err)
	}
}

// NewKeyringStore 走真实 KEK 路径（DPAPI 包裹落盘）：同目录两次构造得到
// 同一把钥匙，跨实例读写通；KEK 文件本体不是裸 32 字节明文。
func TestNewKeyringStoreKEKPersistence(t *testing.T) {
	mockKeyring(t)
	root := t.TempDir()

	a, err := NewKeyringStore(root)
	if err != nil {
		t.Fatalf("首次构造: %v", err)
	}
	if err := a.Set(SlotCurseForgeAPIKey, "CFKEY-persist"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	b, err := NewKeyringStore(root)
	if err != nil {
		t.Fatalf("二次构造: %v", err)
	}
	got, err := b.Get(SlotCurseForgeAPIKey)
	if err != nil || got != "CFKEY-persist" {
		t.Errorf("跨实例读取不符: %q err=%v", got, err)
	}
	raw := readKEKFile(t, filepath.Join(root, KEKFilename))
	if len(raw) == keySize {
		t.Errorf("KEK 文件疑似裸钥（长度恰为 %d）", keySize)
	}
}

// 写入校验：空值报 value_empty；超长报 value_too_large（不截断）。
func TestKeyringStoreValidation(t *testing.T) {
	mockKeyring(t)
	store := &KeyringStore{kek: testKEK(t)}
	if err := store.Set(SlotGitHubToken, "   "); errs.CodeOf(err) != CodeValueEmpty {
		t.Errorf("空白值应报 %s，实际 %v", CodeValueEmpty, err)
	}
	big := strings.Repeat("x", MaxValueBytes+1)
	if err := store.Set(SlotGitHubToken, big); errs.CodeOf(err) != CodeValueTooLarge {
		t.Errorf("超长值应报 %s，实际 %v", CodeValueTooLarge, err)
	}
}

// BrokenStore：初始化失败的退路，四操作恒报 store_unavailable（不降级）。
func TestBrokenStore(t *testing.T) {
	store := Broken(errors.New("boom"))
	if err := store.Set(SlotCurseForgeAPIKey, "x"); errs.CodeOf(err) != CodeStoreUnavailable {
		t.Errorf("Set 应报 %s，实际 %v", CodeStoreUnavailable, err)
	}
	if _, err := store.Get(SlotCurseForgeAPIKey); errs.CodeOf(err) != CodeStoreUnavailable {
		t.Errorf("Get 应报 %s，实际 %v", CodeStoreUnavailable, err)
	}
	if _, err := store.Status(SlotCurseForgeAPIKey); errs.CodeOf(err) != CodeStoreUnavailable {
		t.Errorf("Status 应报 %s，实际 %v", CodeStoreUnavailable, err)
	}
}

// ParseSlot：三个既注册槽位通过，任意命名拒绝。
func TestParseSlot(t *testing.T) {
	for _, ok := range []string{
		string(SlotCurseForgeAPIKey), string(SlotGitHubToken), string(SlotGitHubSSHPassphrase),
	} {
		if _, err := ParseSlot(ok); err != nil {
			t.Errorf("合法槽位 %q 被拒: %v", ok, err)
		}
	}
	if _, err := ParseSlot("evil/freeform"); errs.CodeOf(err) != "err.secrets.slot_unknown" {
		t.Errorf("未知槽位应报 err.secrets.slot_unknown，实际 %v", err)
	}
}

// MemoryStore 与 KeyringStore 行为契约对齐（同一套生命周期断言的最小回放）。
func TestMemoryStoreContract(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Get(SlotGitHubSSHPassphrase); !errors.Is(err, ErrNotFound) {
		t.Errorf("未设置应报 ErrNotFound，实际 %v", err)
	}
	if err := store.Set(SlotGitHubSSHPassphrase, "ssh-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, _ := store.Get(SlotGitHubSSHPassphrase); got != "ssh-secret" {
		t.Errorf("Get: %q", got)
	}
	if st, _ := store.Status(SlotGitHubSSHPassphrase); !st.Set || st.Last4 != "cret" {
		t.Errorf("Status: %+v", st)
	}
	if err := store.Delete(SlotGitHubSSHPassphrase); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(SlotGitHubSSHPassphrase); err != nil {
		t.Errorf("Delete 应幂等: %v", err)
	}
}
