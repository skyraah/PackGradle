// Package download 实现 CF 免钥匙直链下载物化引擎（ADR-0008 全量落地）。
//
// 职责单一：产「已过声明 hash 校验的字节」——`.part` 下载完成即验声明格式
// hash，这是两层校验的第一层「取对了」；第二层 sha256 复核是 StageContent
// 既有机器（P2 零新增），本包不碰。DB/计划/写路径零依赖：接线（把产出喂
// StageContent）归执行票，所有权证明/原子写/journal 全在既有管线复用。
//
// 失败分桶（ADR-0008 §5）：
//   - 重试面 = 网络错误/408/429/5xx，重试耗尽 → err.download.network
//     （其中最后失败为 429/503 → err.download.rate_limited）；
//   - 403/404 一律不重试 → err.download.unavailable（CDN 403 = 路径不可达，
//     不移植 API 面的 403 体嗅探）；
//   - hash 不符 → 清 `.part` 全量重取一次，仍败 → err.download.hash_mismatch。
//
// 韧性（ADR-0008 §4）：并发默认 6、用户可配 [download] concurrency（1–16，
// 加载层与构造层共用 NormalizeConcurrency 校验）；单文件重试 4 次指数退避
// 1s→30s + jitter + 尊重 Retry-After；连接超时 30s；读停顿 120s 无字节推进即断；
// `.part` + Range 续传（run 内，调用方管生命周期）；UA = PackGradle/<version>；
// 下载本身不做 HEAD 预检。
//
// 直链构造（ADR-0008 §2）见 DirectURL：整数除法不补零（实测钉死），单函数 +
// 黄金向量单测；fileID ≥ 10^7 记日志不换口径。
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"packgradle/internal/errs"
)

// Options 是引擎构造参数；零值字段取默认。
type Options struct {
	// Concurrency 并发下载度；0 取默认 6，越界（<1 或 >16）New 报错
	//（与全局 config [download] concurrency 加载层校验同口径）
	Concurrency int
	// BaseURL 覆盖 CDN 直链前缀（单测注入假 CDN；空 = 生产 mediafilez 前缀）
	BaseURL string
	// Client 覆盖 HTTP 客户端（测试注入；nil = 默认客户端，连接超时 30s）
	Client *http.Client
	// Backoff 覆盖重试退避函数（测试注入固定小值；nil = 指数退避 1s→30s + jitter）
	Backoff func(attempt int) time.Duration
	// Sleep 覆盖重试等待（测试注入记录器；nil = context 感知的真实等待）
	Sleep func(ctx context.Context, d time.Duration) error
	// StallTimeout 覆盖读停顿阈值（测试缩短；0 = 120s）
	StallTimeout time.Duration
	// Log 日志出口（nil = 标准库默认）
	Log *log.Logger
}

// Engine 是下载物化引擎。零共享可变状态，并发安全。
type Engine struct {
	concurrency  int
	baseURL      string
	client       *http.Client
	backoff      func(attempt int) time.Duration
	sleep        func(ctx context.Context, d time.Duration) error
	stallTimeout time.Duration
	log          *log.Logger
}

// New 构造引擎；并发度越界报错（err.config.download_concurrency_invalid）。
func New(opts Options) (*Engine, error) {
	n, err := NormalizeConcurrency(opts.Concurrency)
	if err != nil {
		return nil, err
	}
	base := opts.BaseURL
	if base == "" {
		base = cfFileBase
	}
	client := opts.Client
	if client == nil {
		client = newDefaultClient(n)
	}
	backoff := opts.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	stall := opts.StallTimeout
	if stall <= 0 {
		stall = stallTimeout
	}
	logger := opts.Log
	if logger == nil {
		logger = log.Default()
	}
	return &Engine{
		concurrency:  n,
		baseURL:      base,
		client:       client,
		backoff:      backoff,
		sleep:        opts.Sleep,
		stallTimeout: stall,
		log:          logger,
	}, nil
}

// newDefaultClient 构造生产 HTTP 客户端：连接（拨号 + TLS 握手）超时 30s，
// 连接池按并发度放宽（默认 MaxIdleConnsPerHost=2 会让并发反复建连）。
func newDefaultClient(concurrency int) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   connectTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   connectTimeout,
			MaxIdleConnsPerHost:   concurrency,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// Request 描述一次取数。输入只有 packwiz metafile 携带的三样：文件名、
// update.curseforge.file-id 与声明的 hash（ADR-0008 §2/§3）。
type Request struct {
	FileID     int64  // update.curseforge.file-id
	Filename   string // metafile filename（也是落盘成品文件名）
	HashFormat string // 声明 hash 格式（md5/sha1/sha256/sha512；murmur2/未知 → 不支持信号）
	Hash       string // 声明 hex 摘要
}

// Result 是成功取数的产出：已过声明 hash 校验的本地文件。
type Result struct {
	Path string // 成品文件路径（dir 下，以 Filename 命名）
	Size int64  // 字节数
}

// Open 打开成品文件供读取（调用方负责 Close）。接线层以此喂既有
// Run.StageContent(targetRel string, content io.Reader, wantDigest string)——
// 流式传递，大文件不整载内存。
func (r *Result) Open() (io.ReadCloser, error) {
	f, err := os.Open(r.Path)
	if err != nil {
		return nil, errs.NewDetail("err.file.read", err.Error(), r.Path)
	}
	return f, nil
}

// transientError 标记一次可重试的失败（网络错误/408/429/5xx/读停顿/半截断流）。
type transientError struct {
	err         error
	retryAfter  time.Duration // 服务器 Retry-After 指示（0 = 无，用默认退避）
	rateLimited bool          // 最后失败为 429/503（重试耗尽 → rate_limited 桶）
}

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// Fetch 取回单个文件，产出已过声明 hash 校验的字节（成品文件）。
//
// dir 是本次运行的下载暂存目录（`.part` 与成品都落在这里）；生命周期归调用方：
// run 内续传（同 dir 同文件名的 `.part` 续 Range）、跨 run 不复用——failed 重试
// 算新运行，调用方清空 dir 后重下（ADR-0008 §6）。崩溃后 dir 随运行暂存目录
// 按 ADR-0004 恢复矩阵处置。
func (e *Engine) Fetch(ctx context.Context, dir string, req Request) (*Result, error) {
	// 不验不装：声明 murmur2/未识别格式在下载之前就拒绝——绝不发起无校验下载
	//（ADR-0008 §3；该资源由计划阶段降标 user_object_required）
	if newHasher(req.HashFormat) == nil {
		return nil, errHashFormatUnsupported(req.HashFormat)
	}
	if req.Filename == "" || req.FileID <= 0 {
		// 编程错误：接线层从 metafile 构造，模式推导已保证非空（不设错误码）
		return nil, fmt.Errorf("download: 非法请求（fileID=%d filename=%q）", req.FileID, req.Filename)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, errs.NewDetail("err.file.mkdir", err.Error(), dir)
	}

	partPath := filepath.Join(dir, req.Filename+".part")
	finalPath := filepath.Join(dir, req.Filename)
	url := directURL(e.baseURL, req.FileID, req.Filename)

	// hash 轮次：hash 不符 → 清 `.part` 全量重取一次，仍败 → hash_mismatch
	//（round 1 起禁用续传，保证全量重取语义）
	for round := 0; ; round++ {
		err := e.fetchWithRetry(ctx, url, partPath, finalPath, req, round == 0)
		if err == nil {
			fi, statErr := os.Stat(finalPath)
			if statErr != nil {
				return nil, errs.NewDetail("err.file.read", statErr.Error(), finalPath)
			}
			return &Result{Path: finalPath, Size: fi.Size()}, nil
		}
		if errs.CodeOf(err) == CodeHashMismatch {
			if round > 0 {
				return nil, err // 全量重取一次仍败
			}
			os.Remove(partPath)
			continue
		}
		return nil, err
	}
}

// fetchWithRetry 执行一轮完整下载（含网络类重试）。允许续传由 allowResume 控制
//（hash 重取轮传 false）。网络类耗尽后按最后失败分桶：429/503 → rate_limited，
// 其余 → network；403/404 与磁盘错误是终局，原样返回。
func (e *Engine) fetchWithRetry(ctx context.Context, url, partPath, finalPath string, req Request, allowResume bool) error {
	for attempt := 0; ; attempt++ {
		err := e.fetchOnce(ctx, url, partPath, finalPath, req, allowResume)
		if err == nil {
			return nil
		}
		var te *transientError
		if !errors.As(err, &te) {
			return err // 终局：unavailable / hash_mismatch / 磁盘错误 / ctx 取消
		}
		if attempt >= maxRetries {
			if te.rateLimited {
				return errs.New(CodeRateLimited)
			}
			return errs.NewDetail(CodeNetwork, te.err.Error())
		}
		// 指数退避 + jitter，尊重 Retry-After（取两者较大值）
		if waitErr := e.wait(ctx, max(e.backoff(attempt), te.retryAfter)); waitErr != nil {
			return waitErr // ctx 取消
		}
	}
}

// fetchOnce 执行单次请求下载：`.part`（可续传）→ 流式喂 hasher → 完成即比对
// 声明摘要 → 验过 rename 成品。返回 transientError 表示可重试。
func (e *Engine) fetchOnce(ctx context.Context, url, partPath, finalPath string, req Request, allowResume bool) error {
	// 派生 ctx：读停顿看门狗只中断本次请求，不动调用方 ctx
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resumeSize := int64(0)
	if allowResume {
		if fi, err := os.Stat(partPath); err == nil {
			resumeSize = fi.Size()
		}
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return &transientError{err: err} // URL 由我们构造，不会坏；保守归网络面
	}
	httpReq.Header.Set("User-Agent", UserAgent)
	if resumeSize > 0 {
		httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeSize))
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // 调用方取消：透传，不重试
		}
		return &transientError{err: err} // 连接拒绝/超时/DNS 等传输错误
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent:
		// 继续下载
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// `.part` 越界失效（比服务器全量大）：清除后按全量重取（计入重试）
		os.Remove(partPath)
		return &transientError{err: fmt.Errorf("HTTP 416（.part 失效已清除，按全量重取）")}
	case resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= http.StatusInternalServerError:
		te := &transientError{err: fmt.Errorf("HTTP %d", resp.StatusCode)}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			te.rateLimited = true
		}
		te.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		return te
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		// 403/404 一律不重试（ADR-0008 §5）：CDN 403 = 路径不可达（实测错分段即 403），
		// 不做 API 面的「403+traffic 体嗅探 = 频控」
		os.Remove(partPath)
		return errs.New(CodeUnavailable, req.Filename)
	default:
		// 其余非 2xx（401 等杂项 4xx）不在重试面：保守归不可获取，不重试
		os.Remove(partPath)
		return errs.New(CodeUnavailable, req.Filename)
	}

	// 响应有效性：206 需要 `.part` 在场（有续传请求）；200 = 服务器无视 Range，
	// 从头来。若 206 但无 `.part`（异常服务器），按全量处理。
	truncate := true
	if resp.StatusCode == http.StatusPartialContent && resumeSize > 0 {
		truncate = false
	}
	if truncate {
		resumeSize = 0
	}

	h := newHasher(req.HashFormat) // Fetch 已 gate 格式，必非 nil
	var f *os.File
	if truncate {
		f, err = os.Create(partPath)
	} else {
		// 续传：已有字节先喂 hasher（保持全文件 digest 连续性），再 append
		f, err = os.OpenFile(partPath, os.O_RDWR, 0o644)
		if err == nil {
			if _, err = io.Copy(h, io.LimitReader(f, resumeSize)); err != nil {
				f.Close()
				return &transientError{err: fmt.Errorf("读取 .part 续传段失败: %w", err)}
			}
			if _, err = f.Seek(resumeSize, io.SeekStart); err != nil {
				f.Close()
				return &transientError{err: fmt.Errorf("定位 .part 续传点失败: %w", err)}
			}
		}
	}
	if err != nil {
		return errs.NewDetail("err.file.write", err.Error(), partPath)
	}

	// 下载循环：读停顿看门狗——stallTimeout 无字节推进即断（取消本次请求，
	// 底层连接随之关闭，阻塞中的 Read 立即出错），非绝对读超时
	buf := make([]byte, 64<<10)
	for {
		watchdog := time.AfterFunc(e.stallTimeout, cancel)
		n, rerr := resp.Body.Read(buf)
		watchdog.Stop()
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return errs.NewDetail("err.file.write", werr.Error(), partPath)
			}
			h.Write(buf[:n]) // hash.Hash.Write 恒不返回错误
		}
		if rerr == nil {
			continue
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		f.Close()
		if ctx.Err() != nil {
			return ctx.Err() // 调用方取消：透传，不重试
		}
		// 传输中断（含读停顿看门狗取消、服务器半截断流）：保留 `.part`
		// 作续传资本，网络类重试
		return &transientError{err: fmt.Errorf("读取响应中断: %w", rerr)}
	}
	if err := f.Close(); err != nil {
		return errs.NewDetail("err.file.write", err.Error(), partPath)
	}

	// `.part` 完成即验声明 hash = 两层校验第一层「取对了」（来源正确性）
	if !digestMatches(h, req.Hash) {
		os.Remove(partPath)
		return errs.New(CodeHashMismatch, req.Filename)
	}

	// 验过才落成品名（成品在场的文件必是已过校验的）
	if err := os.Rename(partPath, finalPath); err != nil {
		return errs.NewDetail("err.file.write", err.Error(), finalPath)
	}
	return nil
}

// FetchAll 并发批量取数（并发度 = Options.Concurrency）。
//
// onResult 对每个请求恰好回调一次（成功带 Result、失败带已分桶错误），在
// worker goroutine 上执行，调用方自行串行化。k 是该请求在 reqs 中的对位下标——
// 结果记账必须按 k 对位（outcomes[k]），不得以 req 值作 map 键归集：reqs 可含
// 字段全同的相等请求（同 FileID+Filename+Hash 两行），值语义键会塌缩，两行
// 结果互覆、一行丢回填。单文件失败不中断批次——sync 失败语义 = 剔出本场
// 照常提交（ADR-0008 §7），接线层据此归集跳过清单。ctx 取消时停止排程新任务、
// 进行中的任务随之中止，返回 ctx.Err()。
func (e *Engine) FetchAll(ctx context.Context, dir string, reqs []Request, onResult func(k int, req Request, res *Result, err error)) error {
	sem := make(chan struct{}, e.concurrency)
	var wg sync.WaitGroup
	cancelled := false
loop:
	for k, req := range reqs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			cancelled = true
			break loop
		}
		wg.Add(1)
		go func(k int, req Request) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := e.Fetch(ctx, dir, req)
			if onResult != nil {
				onResult(k, req, res, err)
			}
		}(k, req)
	}
	wg.Wait()
	if cancelled {
		return ctx.Err()
	}
	return nil
}

// wait 等待重试间隔（context 感知；测试注入 Sleep 记录器替代真实等待）。
func (e *Engine) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if e.sleep != nil {
		return e.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
