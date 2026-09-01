package main

// pgfixture -serve（票 #66；验收规格 §5.1 假 CDN 进程形态）：复用票 #58 的
// internal/download.FakeCDN.Handler()（#58 专门把该实现放在非测试源码位置），
// 以独立进程供 pgheadless/pgrecovery 的 `-cdn <url>` 参数接入——单测走
// httptest、E2E 走同一实现的进程形态（#56 Testing Decisions 缝 3「最高点」）。
//
//	pgfixture -serve [-addr 127.0.0.1:0]
//
// 启动后向 stdout 打印一行 `LISTEN <实际地址>` 并 flush——拉起方（pgheadless
// -download 链 / pgrecovery restore harness）逐行读取该行取得监听地址后拼
// `http://<addr>/files` 作为 -cdn 值。
//
// 控制面（同端口 /__control/ 前缀；脚本化故障在链路中途切换的入口，下载链
// 场景「failed 终局可重入」「探测降标」依赖它做运行中脚本热更新）：
//
//	POST /__control/set-file  {"path":"/files/a/b/x.jar","content_base64":"..."}
//	POST /__control/script    {"path":"...","steps":[<fakeStepJSON>...]}
//	                          （steps 空 = 清除脚本，回落 set-file 内容）
//	GET  /__control/requests  → FakeRequest JSON 数组（Range 头即续传证据）
//
// 故障脚本步（fakeStepJSON）覆盖验收规格 §5.1 七种：404/403/429/503（status）、
// 连接拒绝（单测 RefusingAddr；进程形态以 abort 断连等价注入——HTTP 层无法
// 拒绝已建立的连接）、半截断流（truncate_at）、错误字节（body 与声明 hash
// 不符的普通 200）。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"packgradle/internal/download"
)

// serveOptions 是 -serve 模式的参数。
type serveOptions struct {
	addr string // 监听地址（默认 127.0.0.1:0 自动分配）
}

// fakeStepJSON 是脚本步的 wire 形态（控制面 JSON ↔ download.FakeStep）。
type fakeStepJSON struct {
	Status            int    `json:"status"`               // 0 = 200
	BodyBase64        string `json:"body_base64"`          // 响应体
	RangeFrom         int64  `json:"range_from"`           // ≥0 且 range_total>0 → 206（Step206 口径）
	RangeTotal        int64  `json:"range_total"`
	RetryAfterSeconds int    `json:"retry_after_seconds"` // Retry-After 头（秒）
	TruncateAt        int    `json:"truncate_at"`         // 半截断流：声明全长只发前缀
	HangMS            int    `json:"hang_ms"`             // 发完头即挂住
	DelayMS           int    `json:"delay_ms"`            // 头之前延迟
	Abort             bool   `json:"abort"`               // 收到请求即断连
}

func (s fakeStepJSON) toStep() download.FakeStep {
	return download.FakeStep{
		Status:            s.Status,
		Body:              mustB64(s.BodyBase64),
		RangeFrom:         s.RangeFrom,
		RangeTotal:        s.RangeTotal,
		RetryAfterSeconds: s.RetryAfterSeconds,
		TruncateAt:        s.TruncateAt,
		Hang:              time.Duration(s.HangMS) * time.Millisecond,
		Delay:             time.Duration(s.DelayMS) * time.Millisecond,
		Abort:             s.Abort,
	}
}

func mustB64(s string) []byte {
	if s == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic("pgfixture: body_base64 解码失败: " + err.Error())
	}
	return b
}

// runServe 启动假 CDN 进程（不返回，除非监听失败）。
func runServe(opts serveOptions) {
	addr := opts.addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "假 CDN 监听失败:", err)
		os.Exit(1)
	}
	// 拉起方协议：打印监听行（随 stdout 缓冲必须立即 flush——http.Serve 之前）。
	fmt.Printf("LISTEN %s\n", l.Addr())
	os.Stdout.Sync()
	if err := http.Serve(l, newServeMux(download.NewFakeCDN())); err != nil {
		fmt.Fprintln(os.Stderr, "假 CDN 退出:", err)
		os.Exit(1)
	}
}

// newServeMux 组装下载面（FakeCDN Handler）与控制面（/__control/ 前缀）。
// 独立成函数供控制面单测挂 httptest（同一 mux 形态，进程与测试零漂移）。
func newServeMux(cdn *download.FakeCDN) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", cdn.Handler())
	mux.HandleFunc("/__control/set-file", controlSetFile(cdn))
	mux.HandleFunc("/__control/script", controlScript(cdn))
	mux.HandleFunc("/__control/requests", controlRequests(cdn))
	return mux
}

// writeJSON 统一控制面响应（H2 之外也让客户端可断言 Content-Type）。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// controlSetFile 处理 POST /__control/set-file。
func controlSetFile(cdn *download.FakeCDN) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var req struct {
			Path         string `json:"path"`
			ContentB64   string `json:"content_base64"`
			ContentBytes []byte `json:"content_bytes"` // 测试/本机链路直传字节（免 base64 往返）
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path/content 必填"})
			return
		}
		content := req.ContentBytes
		if len(content) == 0 && req.ContentB64 != "" {
			content = mustB64(req.ContentB64)
		}
		cdn.SetFile(req.Path, content)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": req.Path, "bytes": len(content)})
	}
}

// controlScript 处理 POST /__control/script（空 steps = 清脚本回落 set-file）。
func controlScript(cdn *download.FakeCDN) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var req struct {
			Path  string         `json:"path"`
			Steps []fakeStepJSON `json:"steps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path 必填"})
			return
		}
		steps := make([]download.FakeStep, 0, len(req.Steps))
		for _, s := range req.Steps {
			steps = append(steps, s.toStep())
		}
		cdn.Script(req.Path, steps...) // 空 steps 即清除（FakeCDN 契约）
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": req.Path, "steps": len(steps)})
	}
}

// controlRequests 处理 GET /__control/requests（Range 头记录 = 续传证据）。
func controlRequests(cdn *download.FakeCDN) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
			return
		}
		writeJSON(w, http.StatusOK, cdn.Requests())
	}
}
