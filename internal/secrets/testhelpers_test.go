package secrets

import (
	"crypto/rand"
	"os"
	"testing"
)

// testKEK 生成一次性测试钥匙（信封单元测试不落盘，密文断言见 store_test）。
func testKEK(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成测试 KEK 失败: %v", err)
	}
	return key
}

// readKEKFile 读取 KEK 文件原始字节（DPAPI 包形态，非裸钥）。
func readKEKFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 KEK 文件失败: %v", err)
	}
	return raw
}
