package download

import (
	"os"
	"strings"
	"testing"

	"packgradle/internal/errs"
)

// stdlib 四格式校验向量（"abc" 的标准摘要）
func TestVerifyDeclaredHashKnown(t *testing.T) {
	cases := []struct {
		format string // 故意混合大小写，钉死归一口径
		want   string
	}{
		{"md5", "900150983cd24fb0d6963f7d28e17f72"},
		{"sha1", "a9993e364706816aba3e25717850c26c9cd0d89d"},
		{"sha256", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"sha512", "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a" +
			"2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			if err := VerifyDeclaredHash(tc.format, tc.want, []byte("abc")); err != nil {
				t.Fatalf("正确字节应通过: %v", err)
			}
			// 大写格式 + 大写声明摘要也应通过（hex 与格式名大小写无关）
			if err := VerifyDeclaredHash(uppercase(tc.format), uppercase(tc.want), []byte("abc")); err != nil {
				t.Fatalf("大写格式+大写摘要应通过: %v", err)
			}
			err := VerifyDeclaredHash(tc.format, tc.want, []byte("abcd"))
			if err == nil {
				t.Fatal("错误字节不应通过")
			}
			if errs.CodeOf(err) != CodeHashMismatch {
				t.Fatalf("字节不符应归 hash_mismatch，实际 %v", err)
			}
		})
	}
}

// murmur2 / 未识别格式 → hash_format_unsupported 信号（不验不装）
func TestVerifyDeclaredHashUnsupported(t *testing.T) {
	for _, format := range []string{"murmur2", "MURMUR2", "xxhash64", "", "sha224"} {
		err := VerifyDeclaredHash(format, "00", []byte("abc"))
		if err == nil {
			t.Fatalf("格式 %q 应返回不支持信号", format)
		}
		if !IsHashFormatUnsupported(err) {
			t.Fatalf("格式 %q 的错误应为 hash_format_unsupported 信号，实际 %v", format, err)
		}
		if errs.CodeOf(err) != "hash_format_unsupported" {
			t.Fatalf("信号码应与 marker_reason 枚举值同字面，实际 %q", errs.CodeOf(err))
		}
	}
}

// 声明值非法 hex → hash_mismatch（hash 兜底：宁可拒绝不可装错）
func TestVerifyDeclaredHashBadHex(t *testing.T) {
	err := VerifyDeclaredHash("sha256", "not-hex!", []byte("abc"))
	if err == nil || errs.CodeOf(err) != CodeHashMismatch {
		t.Fatalf("非法 hex 声明应归 hash_mismatch，实际 %v", err)
	}
}

// 空输入（零字节文件）的校验边界
func TestVerifyDeclaredHashEmptyInput(t *testing.T) {
	// sha256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	if err := VerifyDeclaredHash("sha256",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil); err != nil {
		t.Fatalf("空输入应通过: %v", err)
	}
}

// 引擎在下载前 gate：不支持格式零请求零落盘（不验不装，绝不无校验下载）
func TestFetchUnsupportedFormatNoRequest(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	path := "/files/8778/11/old.jar"

	e := mustEngine(t, Options{BaseURL: srv.URL + "/files"})
	dir := t.TempDir()
	_, err := e.Fetch(t.Context(), dir, Request{
		FileID: 8778011, Filename: "old.jar", HashFormat: "murmur2", Hash: "12345678",
	})
	if !IsHashFormatUnsupported(err) {
		t.Fatalf("murmur2 声明应返回 hash_format_unsupported 信号，实际 %v", err)
	}
	if cdn.CountRequests(path) != 0 {
		t.Fatal("不支持格式绝不发起下载（零请求）")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("不支持格式不得落盘，实际 %v（err=%v）", entries, readErr)
	}
	if !strings.HasPrefix(cdnURL(srv), "http://") {
		t.Fatal("假 CDN 应为 http 测试服务")
	}
}

func uppercase(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'a' && c <= 'z' {
			out[i] = c - 32
		}
	}
	return string(out)
}
