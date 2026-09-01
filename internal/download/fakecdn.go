package download

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// FakeCDN 是可脚本化的假 CurseForge CDN（测试缝，验收规格 §Testing 决议 3：
// 「最高点」）。确定性故障逻辑（分桶/重试/续传/hash/并发）只有注入能精确复现，
// 全部下载单测经本实现喂 httptest；票 10 将以进程形态（`pgfixture -serve`）
// 复用同一 Handler 喂 pgheadless/pgrecovery 的 `-cdn` 参数——因此本文件是
// 非测试源码，放在可被进程化 import 的位置。
//
// 用法：
//
//	cdn := NewFakeCDN()
//	cdn.SetFile("/files/7270/446/jei.jar", goodBytes)          // 正常文件
//	cdn.Script("/files/8778/11/a.jar", FakeStep{Status: 503})  // 故障脚本
//	srv := httptest.NewServer(cdn.Handler())
//	defer srv.Close()
//	// 引擎注入：Options.BaseURL = srv.URL + "/files"（经 directURL 同口径构造）
//
// 脚本语义：对同一路径的请求按序消费 FakeStep，越界后重复最后一步；
// 未脚本化的路径回落到 SetFile 的内容（缺失则 404）。
type FakeCDN struct {
	mu          sync.Mutex
	files       map[string][]byte
	scripts     map[string][]FakeStep
	counters    map[string]int
	requests    []FakeRequest
	inFlight    int
	maxInFlight int
}

// FakeStep 是脚本化单步响应。
type FakeStep struct {
	// Status 是 HTTP 状态码；0 表示 200（配合 SetFile 语义）。
	Status int
	// Body 是响应体。
	Body []byte
	// RangeFrom >= 0 时按 206 部分内容语义响应：发 Content-Range
	//（bytes RangeFrom-(RangeTotal-1)/RangeTotal），Body 即余下部分。
	// 用 Step206 helper 构造，勿手填。
	RangeFrom  int64
	RangeTotal int64
	// RetryAfterSeconds > 0 时附加 Retry-After 响应头（秒）。
	RetryAfterSeconds int
	// TruncateAt > 0 时按 Body 全长发 Content-Length 但只写前 TruncateAt 字节
	// 后断流（半截断流：客户端收到 unexpected EOF）。
	TruncateAt int
	// Hang > 0 时发完响应头即挂住至客户端断开（读停顿场景：客户端看门狗先超时）。
	Hang time.Duration
}

// FakeRequest 是一次被假 CDN 记录的请求（Range 头记录即续传证据）。
type FakeRequest struct {
	Path      string
	Method    string
	Range     string // Range 请求头原文（无则为空）
	UserAgent string
}

// NewFakeCDN 构造假 CDN。
func NewFakeCDN() *FakeCDN {
	return &FakeCDN{
		files:    map[string][]byte{},
		scripts:  map[string][]FakeStep{},
		counters: map[string]int{},
	}
}

// SetFile 登记一个正常可下载文件（path 为 URL 路径，如 /files/7270/446/jei.jar）。
func (c *FakeCDN) SetFile(path string, content []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[path] = content
}

// Script 为路径安装请求脚本：每次请求按序消费一步，越界后重复最后一步；
// 传空脚本清除该路径脚本回落 SetFile。
func (c *FakeCDN) Script(path string, steps ...FakeStep) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(steps) == 0 {
		delete(c.scripts, path)
		return
	}
	c.scripts[path] = steps
}

// Step206 生成 206 部分内容步：content 为全量内容，从 from 字节起返回余下部分，
// 自动携带正确的 Content-Range（Range 续传第二程）。
func Step206(content []byte, from int64) FakeStep {
	return FakeStep{
		Status:     http.StatusPartialContent,
		Body:       content[from:],
		RangeFrom:  from,
		RangeTotal: int64(len(content)),
	}
}

// Handler 返回假 CDN 的 HTTP 处理器（单测挂 httptest，票 10 挂进程形态 server）。
func (c *FakeCDN) Handler() http.Handler {
	return http.HandlerFunc(c.serveHTTP)
}

// Requests 返回迄今为止全部请求记录快照。
func (c *FakeCDN) Requests() []FakeRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]FakeRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

// CountRequests 返回命中 path 的请求次数。
func (c *FakeCDN) CountRequests(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.requests {
		if r.Path == path {
			n++
		}
	}
	return n
}

// MaxInFlight 返回同时在处理的请求峰值（并发度上限验证用）。
func (c *FakeCDN) MaxInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInFlight
}

// serveHTTP 按脚本消费逻辑响应单次请求。
func (c *FakeCDN) serveHTTP(w http.ResponseWriter, r *http.Request) {
	c.record(r)
	c.enter()
	defer c.leave()
	step := c.stepFor(r.URL.Path)

	// 头统一在 WriteHeader 前设置
	if step.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(step.RetryAfterSeconds))
	}

	switch {
	case step.Hang > 0:
		// 发完头即挂住：Content-Length 给非零值诱导客户端等待，直到客户端断开
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	case step.TruncateAt > 0:
		// 半截断流：声明全长 Content-Length，只发前缀后断流
		w.Header().Set("Content-Length", strconv.Itoa(len(step.Body)))
		w.WriteHeader(statusOr(step.Status, http.StatusOK))
		w.Write(step.Body[:step.TruncateAt]) //nolint:errcheck // 测试缝
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // 先把前缀推到网络，再断连——客户端才收得到部分字节
		}
		panic(http.ErrAbortHandler) // 断连，客户端得 unexpected EOF
	case step.RangeFrom >= 0 && step.RangeTotal > 0:
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", step.RangeFrom, step.RangeTotal-1, step.RangeTotal))
		w.Header().Set("Content-Length", strconv.Itoa(len(step.Body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(step.Body) //nolint:errcheck // 测试缝
	default:
		w.Header().Set("Content-Length", strconv.Itoa(len(step.Body)))
		w.WriteHeader(statusOr(step.Status, http.StatusOK))
		w.Write(step.Body) //nolint:errcheck // 测试缝
	}
}

// record 记录一次请求（Range 头即续传证据）。
func (c *FakeCDN) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, FakeRequest{
		Path:      r.URL.Path,
		Method:    r.Method,
		Range:     r.Header.Get("Range"),
		UserAgent: r.Header.Get("User-Agent"),
	})
}

// enter / leave 维护在处理峰值计数（并发度上限验证用）。
func (c *FakeCDN) enter() {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.maxInFlight {
		c.maxInFlight = c.inFlight
	}
	c.mu.Unlock()
}

func (c *FakeCDN) leave() {
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
}

// stepFor 取该路径下一步脚本（越界重复最后一步）；无脚本回落文件内容，缺失则 404。
func (c *FakeCDN) stepFor(path string) FakeStep {
	c.mu.Lock()
	defer c.mu.Unlock()
	if steps, ok := c.scripts[path]; ok && len(steps) > 0 {
		i := c.counters[path]
		if i >= len(steps) {
			i = len(steps) - 1
		}
		c.counters[path]++
		return steps[i]
	}
	if body, ok := c.files[path]; ok {
		return FakeStep{Status: http.StatusOK, Body: body}
	}
	return FakeStep{Status: http.StatusNotFound}
}

func statusOr(status, fallback int) int {
	if status == 0 {
		return fallback
	}
	return status
}

// RefusingAddr 起一个监听后立即关闭，返回必然拒绝连接的地址
//（连接拒绝场景注入：传给 Options.BaseURL 的 host 部分）。
func RefusingAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	l.Close()
	return addr, nil
}
