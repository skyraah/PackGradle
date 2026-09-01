package download

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// CF 探测（契约 06 §5；ADR-0006 §7「尽力探测」）：PrepareRestore 对
// redownload 候选行的可用性辅助，非承诺。三个预算全部编译期常量、用户不可配：
// 单请求 5s、行间并发 4、总预算 10s。探测只区分三态——2xx → ok；
// 404/403 → 降标证据（接线层降 user_object_required + cf_unavailable）；
// 其余（超时/预算耗尽/网络错误/杂项状态码）→ unknown，乐观标记不阻塞 prepare。
// 探测证据（状态码/Content-Length/耗时）为内部诊断，不透出 DTO。

// 探测预算（编译期常量；契约 06 §5）。
const (
	// ProbeRequestTimeout 是单行 HEAD 请求超时。
	ProbeRequestTimeout = 5 * time.Second
	// ProbeConcurrency 是行间探测并发度。
	ProbeConcurrency = 4
	// ProbeBudget 是整轮探测总预算（超预算后未完成行按 unknown 回调）。
	ProbeBudget = 10 * time.Second
)

// ProbeRequest 是一行探测输入：只有直链构造所需的两个字段（与 Request 的
// 取数字段同源——metafile 的 file-id 与 filename；探测不取字节）。
type ProbeRequest struct {
	FileID   int64
	Filename string
}

// ProbeResult 是一行探测结果。
type ProbeResult struct {
	// OK 报告 2xx（可获取）。
	OK bool
	// Demote 报告 404/403（资源不可获取，prepare 时点降标证据）。
	Demote bool
	// StatusCode 是原始状态码；0 表示传输错误/超时/预算耗尽（unknown）。
	StatusCode int
	// ContentLength 是响应声明长度（证据，不透出 DTO）。
	ContentLength int64
	// Err 是传输错误/超时/预算耗尽（unknown 证据；nil 表示拿到了状态码）。
	Err error
}

// ProbeHead 对候选行并发 HEAD 探测（复用引擎的直链构造与 HTTP 通道）：
//
//   - 总预算 ProbeBudget：派生 ctx 超时兜底；调用方 ctx 更早到期（更短 deadline
//     或取消）时以先到者为准，未完成行回调 Err=ctx.Err()（unknown）；
//   - 单请求 ProbeRequestTimeout 超时；
//   - 行间并发 ProbeConcurrency；每行恰好回调一次。
//
// 无返回值：探测尽力而为，任何失败都转化为行内 unknown，不阻塞调用方。
func (e *Engine) ProbeHead(ctx context.Context, reqs []ProbeRequest, onResult func(ProbeRequest, ProbeResult)) {
	if len(reqs) == 0 || onResult == nil {
		return
	}
	// 总预算：与调用方 deadline 取先到（预算常量兜底无 deadline 的生产路径；
	// 测试注入更短 deadline 即可验证「超预算 unknown」而无需等满常量）。
	budgetCtx, cancel := context.WithTimeout(ctx, ProbeBudget)
	defer cancel()

	sem := make(chan struct{}, ProbeConcurrency)
	var wg sync.WaitGroup
	for _, req := range reqs {
		select {
		case sem <- struct{}{}:
		case <-budgetCtx.Done():
			// 预算耗尽：停止排程（未持有槽位，勿释放）；未排程行按 unknown
			// 回调（预算耗尽证据）。
			onResult(req, ProbeResult{Err: fmt.Errorf("探测预算耗尽: %w", budgetCtx.Err())})
			continue
		}
		wg.Add(1)
		go func(req ProbeRequest) {
			defer wg.Done()
			defer func() { <-sem }()
			onResult(req, e.probeOne(budgetCtx, req))
		}(req)
	}
	wg.Wait()
}

// probeOne 执行单行 HEAD 探测（单请求超时 ProbeRequestTimeout）。
func (e *Engine) probeOne(ctx context.Context, req ProbeRequest) ProbeResult {
	reqCtx, cancel := context.WithTimeout(ctx, ProbeRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodHead, directURL(e.baseURL, req.FileID, req.Filename), nil)
	if err != nil {
		return ProbeResult{Err: err} // URL 由我们构造，不会坏；保守 unknown
	}
	httpReq.Header.Set("User-Agent", UserAgent)

	resp, err := e.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ProbeResult{Err: fmt.Errorf("探测预算耗尽: %w", ctx.Err())}
		}
		return ProbeResult{Err: err} // 超时/连接失败：unknown
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return ProbeResult{OK: true, StatusCode: resp.StatusCode, ContentLength: resp.ContentLength}
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		// 404/403：资源不可获取（下架/路径不可达），降标证据（与取数分桶同口径）
		return ProbeResult{Demote: true, StatusCode: resp.StatusCode}
	default:
		// 杂项状态码（429 频控等）：不据此降标，按 unknown 保持乐观标记
		return ProbeResult{StatusCode: resp.StatusCode}
	}
}
