package download

import (
	"math/rand"
	"strconv"
	"time"

	"packgradle/internal/errs"
)

// 韧性编译期常量（ADR-0008 §4；除并发度外不暴露设置，其余一律编译期定值）
const (
	// maxRetries 单文件重试 4 次（首次请求之外；go-retryablehttp RetryMax 口径）
	maxRetries = 4
	// retryBaseDelay 指数退避起点 1s；retryMaxDelay 封顶 30s
	retryBaseDelay = 1 * time.Second
	retryMaxDelay  = 30 * time.Second
	// connectTimeout 连接超时（拨号 + TLS 握手）
	connectTimeout = 30 * time.Second
	// stallTimeout 读停顿阈值：120s 无字节推进即断（非绝对读超时，
	// 慢而不死的连接不误杀、半开连接必出错）
	stallTimeout = 120 * time.Second
// 并发默认 6（Prism 锚点），用户可配 [download] concurrency，合法 1–16
defaultConcurrency = 6
minConcurrency     = 1
maxConcurrency     = 16
// MinConcurrency / MaxConcurrency 是并发度合法域边界（加载层显式值校验用）
MinConcurrency = minConcurrency
MaxConcurrency = maxConcurrency
	// userAgentVersion 与 build/config.yml 的 productVersion 同步（改版本两处一起改）
	userAgentVersion = "0.1.0"
)

// UserAgent 是下载请求的 UA。实测 CDN 对 UA 无关，取可识别值（ADR-0008 §4）。
const UserAgent = "PackGradle/" + userAgentVersion

// defaultBackoff 计算第 attempt 次（0 基）重试的退避：1s 起指数倍增、30s 封顶，
// 加 [0, d/2] 抖动（go-retryablehttp 口径）。
func defaultBackoff(attempt int) time.Duration {
	d := retryBaseDelay << attempt
	if d <= 0 || d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d + time.Duration(rand.Int63n(int64(d/2)+1))
}

// parseRetryAfter 解析 Retry-After 头（仅秒数形态；HTTP-date 形态或非法值
// 返回 0，退回默认退避）。
func parseRetryAfter(h string) time.Duration {
	n, err := strconv.Atoi(h)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// NormalizeConcurrency 归一引擎构造参数中的并发度：0（未配置）取默认 6；
// 其余越界（<1 或 >16）报错。全局 config 加载层（internal/appconfig）对
// 「显式配置值」直接按 MinConcurrency–MaxConcurrency 判界（显式 0 必须拒绝，
// 与「未配置」区分），两端口径单点见本文件常量（ADR-0008 §4）。
func NormalizeConcurrency(n int) (int, error) {
	if n == 0 {
		return defaultConcurrency, nil
	}
	if n < minConcurrency || n > maxConcurrency {
		return 0, errs.New(CodeConcurrencyInvalid, n)
	}
	return n, nil
}
