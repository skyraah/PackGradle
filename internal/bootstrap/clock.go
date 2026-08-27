package bootstrap

import "time"

// defaultNow 是生产时钟；测试可经 Build 之外的装配路径注入假时钟。
func defaultNow() time.Time { return time.Now() }
