// Package ids 生成带前缀的 ULID 风格标识符：prefix + 26 位 Crockford base32
// （48bit 毫秒时间戳 + 80bit 随机熵，进程内同毫秒单调递增）。
// 不引入外部依赖；仅使用 crypto/rand 与标准库。
package ids

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// crockford 是 Crockford base32 字母表（排除 I L U O）。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	mu          sync.Mutex
	lastMilli   int64
	lastEntropy [10]byte
)

// New 返回 prefix + 26 位标识符，例如 New("rel_") => "rel_01J9JZ7B3MBJY9V1X2H6DXJQ9F"。
func New(prefix string) string {
	mu.Lock()
	defer mu.Unlock()

	ms := time.Now().UnixMilli()
	var entropy [10]byte
	if ms == lastMilli {
		increment(&lastEntropy)
		entropy = lastEntropy
	} else {
		if _, err := rand.Read(entropy[:]); err != nil {
			// crypto/rand 失败极罕见：退化为纳秒时钟熵，保证可用性与唯一性概率
			ns := time.Now().UnixNano()
			for i := 0; i < 10; i++ {
				entropy[i] = byte(ns >> (uint(i%8) * 8))
			}
		}
		lastMilli = ms
		lastEntropy = entropy
	}

	// 128bit 值 = 48bit 毫秒时间戳（大端）+ 80bit 熵
	var value [16]byte
	value[0] = byte(ms >> 40)
	value[1] = byte(ms >> 32)
	value[2] = byte(ms >> 24)
	value[3] = byte(ms >> 16)
	value[4] = byte(ms >> 8)
	value[5] = byte(ms)
	copy(value[6:], entropy[:])

	return prefix + encode(value)
}

func encode(v [16]byte) string {
	// ULID 标准编码：把 128bit 左移进 130bit 空间（最高 2bit 为零填充），
	// 再切成 26 个 5bit 组；组 0 对应最高位，保证时间戳单调时字典序单调。
	out := make([]byte, 26)
	for i := 0; i < 26; i++ {
		var chunk byte
		for b := 0; b < 5; b++ {
			r := i*5 + b - 2 // 虚拟 130bit 空间中的绝对 bit 位减去 2bit 填充
			if r < 0 {
				continue // 填充位恒为 0
			}
			byteIdx := r / 8
			bitInByte := 7 - uint(r%8)
			if v[byteIdx]&(1<<bitInByte) != 0 {
				chunk |= 1 << (4 - uint(b))
			}
		}
		out[i] = crockford[chunk]
	}
	return string(out)
}

func increment(e *[10]byte) {
	for i := len(e) - 1; i >= 0; i-- {
		e[i]++
		if e[i] != 0 {
			return
		}
	}
	// 全溢出：极小概率；翻转高位避免同毫秒重复
	e[0] ^= 0xFF
}

// Validate 检查 id 是否为合法前缀 + 26 位 Crockford base32。
func Validate(prefix, id string) error {
	if len(id) != len(prefix)+26 {
		return fmt.Errorf("id %q: 长度不符合 %s 前缀 + 26", id, prefix)
	}
	if id[:len(prefix)] != prefix {
		return fmt.Errorf("id %q: 前缀不符合 %s", id, prefix)
	}
	for _, c := range id[len(prefix):] {
		if !validChar(byte(c)) {
			return fmt.Errorf("id %q: 非法字符 %q", id, c)
		}
	}
	return nil
}

func validChar(c byte) bool {
	for i := 0; i < len(crockford); i++ {
		if crockford[i] == c {
			return true
		}
	}
	return false
}
