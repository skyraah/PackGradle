package download

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"strings"

	"packgradle/internal/errs"
)

// 失败分桶错误码（契约 06 §10）。触发条件按 ADR-0008 §5 修订执行：
// rate_limited = 429/503（契约旧文案「403 体嗅探」已撤销，不做体嗅探）；
// 403/404 一律不重试 → unavailable。
const (
	CodeNetwork      = "err.download.network"
	CodeRateLimited  = "err.download.rate_limited"
	CodeUnavailable  = "err.download.unavailable"
	CodeHashMismatch = "err.download.hash_mismatch"
)

// CodeConcurrencyInvalid 是下载并发度越界错误码（config 加载层与引擎构造层
// 共用，两处发射同一码）。
const CodeConcurrencyInvalid = "err.config.download_concurrency_invalid"

// codeHashFormatUnsupported 是「声明 hash 格式不受支持」信号码，与契约 06
// marker_reason 新枚举值 hash_format_unsupported 同字面：接线层（票 4/7）
// 捕获后取 errs.CodeOf 直接作为 marker_reason，把该资源降标 user_object_required。
// 注意这不是 err.download.* 四码之一——它发生在下载之前（不验不装，绝不无校验下载）。
const codeHashFormatUnsupported = "hash_format_unsupported"

// IsHashFormatUnsupported 判定错误是否为「声明 hash 格式不受支持」信号
//（murmur2 / 未识别格式）。
func IsHashFormatUnsupported(err error) bool {
	return errs.CodeOf(err) == codeHashFormatUnsupported
}

// errHashFormatUnsupported 构造不支持格式信号（args = 声明格式名）。
func errHashFormatUnsupported(format string) error {
	return errs.New(codeHashFormatUnsupported, format)
}

// normalizeFormat 归一声明格式名（metafile 写法大小写不一，扫描器同口径转小写）。
func normalizeFormat(format string) string {
	return strings.ToLower(strings.TrimSpace(format))
}

// newHasher 返回声明格式的 stdlib 哈希器；不受支持（murmur2 / 未知 / 空）返回 nil。
// v1 校验集 = md5/sha1/sha256/sha512（ADR-0008 §3；murmur2 不实现，撞上真实
// murmur2 pack 再补，届时加 case 即可）。
func newHasher(format string) hash.Hash {
	switch normalizeFormat(format) {
	case "md5":
		return md5.New()
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	default:
		return nil
	}
}

// digestMatches 以常时比较判定哈希器累计结果与声明 hex 摘要是否一致；
// 声明值非法 hex 一律 false（hash 兜底：宁可拒绝不可装错）。
func digestMatches(h hash.Hash, declared string) bool {
	want, err := hex.DecodeString(strings.TrimSpace(declared))
	if err != nil {
		return false
	}
	return hmac.Equal(h.Sum(nil), want)
}

// VerifyDeclaredHash 校验 data 是否符合声明 hash（便捷一次性入口；引擎内部走
// 流式 hasher，大文件不整载内存）。murmur2/未识别格式返回
// hash_format_unsupported 信号；字节不符返回 err.download.hash_mismatch。
func VerifyDeclaredHash(format, declared string, data []byte) error {
	h := newHasher(format)
	if h == nil {
		return errHashFormatUnsupported(format)
	}
	h.Write(data)
	if !digestMatches(h, declared) {
		return errs.New(CodeHashMismatch)
	}
	return nil
}

// SupportsHashFormat 报告声明格式是否在引擎可验集内（md5/sha1/sha256/sha512；
// murmur2/未知格式 false）。回滚四标记判定（票 #59）据此判「重取信息可验」，
// 与引擎取数 gate 同一份清单，两处口径不漂移。
func SupportsHashFormat(format string) bool {
	return newHasher(format) != nil
}
