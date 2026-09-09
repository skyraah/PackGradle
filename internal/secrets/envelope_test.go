package secrets

import (
	"strings"
	"testing"
	"time"

	"packgradle/internal/errs"
)

// 信封往返：seal 后 open 还原值与时间戳。
func TestEnvelopeRoundtrip(t *testing.T) {
	kek := testKEK(t)
	in := payload{Secret: "CFKEY-roundtrip", UpdatedAt: time.Now().UTC().Truncate(0)}
	stored, err := sealEnvelope(kek, in)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	out, err := openEnvelope(kek, stored)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if out.Secret != in.Secret {
		t.Errorf("值不还原: %q != %q", out.Secret, in.Secret)
	}
	if !out.UpdatedAt.Equal(in.UpdatedAt) {
		t.Errorf("时间戳不还原: %v != %v", out.UpdatedAt, in.UpdatedAt)
	}
}

// 静置形态（R3 口径）：信封文本里不得出现明文值，且是自描述 JSON。
func TestEnvelopeCiphertextAtRest(t *testing.T) {
	kek := testKEK(t)
	stored, err := sealEnvelope(kek, payload{Secret: "CFKEY-canary-at-rest"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(stored, "CFKEY-canary-at-rest") {
		t.Fatalf("信封含明文值（R3 违规）: %s", stored)
	}
	for _, want := range []string{`"v":1`, `"n"`, `"c"`} {
		if !strings.Contains(stored, want) {
			t.Errorf("信封应为含 %s 的 JSON: %s", want, stored)
		}
	}
}

// 篡改任一字节都解不开（GCM 认证），且报 err.secrets.store_corrupt。
func TestEnvelopeTamperDetected(t *testing.T) {
	kek := testKEK(t)
	stored, err := sealEnvelope(kek, payload{Secret: "CFKEY-tamper"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	runes := []byte(stored)
	// 翻转密文段中间一个字符（base64 字母数字区），避开结构性 JSON 位置
	for i := len(runes) / 2; i < len(runes); i++ {
		if runes[i] >= 'A' && runes[i] <= 'z' {
			runes[i] ^= 0x01
			break
		}
	}
	if _, err := openEnvelope(kek, string(runes)); errs.CodeOf(err) != CodeStoreCorrupt {
		t.Errorf("篡改信封应报 %s，实际 %v", CodeStoreCorrupt, err)
	}
}

// 换钥匙解不开既有信封（KEK 丢失/重建场景 → 同样归 store_corrupt）。
func TestEnvelopeWrongKey(t *testing.T) {
	stored, err := sealEnvelope(testKEK(t), payload{Secret: "CFKEY-wrongkey"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := openEnvelope(testKEK(t), stored); errs.CodeOf(err) != CodeStoreCorrupt {
		t.Errorf("异钥解封应报 %s，实际 %v", CodeStoreCorrupt, err)
	}
}

// 版本不符拒绝（未来 v2 信封出现前的防御）。
func TestEnvelopeUnknownVersion(t *testing.T) {
	kek := testKEK(t)
	stored, err := sealEnvelope(kek, payload{Secret: "CFKEY-version"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	stored = strings.Replace(stored, `"v":1`, `"v":99`, 1)
	if _, err := openEnvelope(kek, stored); errs.CodeOf(err) != CodeStoreCorrupt {
		t.Errorf("未知版本应报 %s，实际 %v", CodeStoreCorrupt, err)
	}
}

// 同一 payload 两次封装产生不同密文（随机 nonce，不重用）。
func TestEnvelopeNonceFreshness(t *testing.T) {
	kek := testKEK(t)
	p := payload{Secret: "CFKEY-nonce", UpdatedAt: time.Now()}
	a, err := sealEnvelope(kek, p)
	if err != nil {
		t.Fatalf("seal a: %v", err)
	}
	b, err := sealEnvelope(kek, p)
	if err != nil {
		t.Fatalf("seal b: %v", err)
	}
	if a == b {
		t.Errorf("两次封装密文相同（nonce 重用）")
	}
}

// 掩码投影：空值空串；非空值 = 掩码点 + 末四位，不含前缀明文。
func TestMask(t *testing.T) {
	if got := Mask(""); got != "" {
		t.Errorf("空值掩码应为空串，实际 %q", got)
	}
	got := Mask("abcd-efgh-ijkl")
	if !strings.HasPrefix(got, "••••") || !strings.HasSuffix(got, "ijkl") {
		t.Errorf("掩码形态不符: %q", got)
	}
	if strings.Contains(got, "abcd") {
		t.Errorf("掩码泄露前段明文: %q", got)
	}
}
