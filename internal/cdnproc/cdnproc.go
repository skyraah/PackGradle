// Package cdnproc 是假 CDN 进程形态的管理客户端（票 #66）：拉起 pgfixture
// -serve 子进程（stdout LISTEN 行协议取端口）或附着到外部假 CDN，经控制面
//（/__control/{set-file,script,requests}）做运行中脚本热更新与请求证据读取。
// pgheadless -download 链与 pgrecovery -mode restore harness 共用。
package cdnproc

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Step 是脚本步的客户端形态（服务端 fakeStepJSON 的 wire 契约；Body 由本包
// 编码为 body_base64）。字段语义与 download.FakeStep 一一对应。
type Step struct {
	Status            int    // 0 = 200
	Body              []byte // 响应体
	RangeFrom         int64  // ≥0 且 RangeTotal>0 → 206 部分内容
	RangeTotal        int64
	RetryAfterSeconds int
	TruncateAt        int // 半截断流：声明全长只发前缀
	HangMS            int
	DelayMS           int
	Abort             bool // 收到请求即断连
}

// Step206 生成 206 部分内容步（download.Step206 的客户端等价）。
func Step206(content []byte, from int64) Step {
	return Step{
		Status:     206,
		Body:       content[from:],
		RangeFrom:  from,
		RangeTotal: int64(len(content)),
	}
}

// Request 是控制面读回的请求记录（Range 头即续传证据）。
type Request struct {
	Path      string `json:"Path"`
	Method    string `json:"Method"`
	Range     string `json:"Range"`
	UserAgent string `json:"UserAgent"`
}

// Serve 是假 CDN 进程句柄（Own=true 时 Close 会终止子进程）。
type Serve struct {
	cmd    *exec.Cmd // nil = 外部附着
	base   string    // 下载面 BaseURL（http://<addr>/files，directURL 前缀口径）
	client *http.Client
}

// StartServe 拉起 pgfixture -serve 子进程，读取 LISTEN 行取得监听地址，
// 返回句柄（下载面 BaseURL = http://<addr>/files）。
func StartServe(bin, addr string) (*Serve, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	cmd := exec.Command(bin, "-serve", addr)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cdnproc: 启动 %s: %w", bin, err)
	}
	// LISTEN 行协议：读第一行（带超时——进程起不来时不悬挂）。
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			lineCh <- sc.Text()
			return
		}
		errCh <- fmt.Errorf("stdout 提前关闭")
	}()
	var listenLine string
	select {
	case listenLine = <-lineCh:
	case err := <-errCh:
		cmd.Process.Kill()  //nolint:errcheck // 已失败面
		return nil, fmt.Errorf("cdnproc: %s 未打印 LISTEN 行: %v（stderr: %s）", bin, err, strings.TrimSpace(stderr.String()))
	case <-time.After(15 * time.Second):
		cmd.Process.Kill() //nolint:errcheck // 已失败面
		return nil, fmt.Errorf("cdnproc: 等待 %s 的 LISTEN 行超时", bin)
	}
	const prefix = "LISTEN "
	if !strings.HasPrefix(listenLine, prefix) {
		cmd.Process.Kill() //nolint:errcheck // 已失败面
		return nil, fmt.Errorf("cdnproc: 首行 %q 不是 LISTEN 行", listenLine)
	}
	hostPort := strings.TrimSpace(strings.TrimPrefix(listenLine, prefix))
	return &Serve{
		cmd:    cmd,
		base:   "http://" + hostPort + "/files",
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Attach 附着到外部假 CDN（不拥有进程；Close 不终止）。baseURL 是下载面
// BaseURL（与 -cdn 参数同值，如 http://127.0.0.1:PORT/files）。
func Attach(baseURL string) *Serve {
	return &Serve{base: baseURL, client: &http.Client{Timeout: 10 * time.Second}}
}

// URL 返回下载面 BaseURL（-cdn 参数值 / directURL 前缀口径）。
func (s *Serve) URL() string { return s.base }

// Close 终止自有子进程（外部附着为 no-op）。
func (s *Serve) Close() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill() //nolint:errcheck // 验收进程清理
		s.cmd.Wait()         //nolint:errcheck // 回收僵尸
	}
}

// controlURL 由下载面 BaseURL 推控制面 URL（同 host:port，/__control/ 前缀）。
func (s *Serve) controlURL(endpoint string) string {
	// base = http://host:port/files → 去掉末段路径
	root := s.base
	if i := strings.LastIndex(root, "/"); i >= 0 {
		root = root[:i]
	}
	return root + "/__control/" + endpoint
}

// postControl 控制面 POST JSON。
func (s *Serve) postControl(endpoint string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := s.client.Post(s.controlURL(endpoint), "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("cdnproc: 控制面 %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck // 错误面
		return fmt.Errorf("cdnproc: 控制面 %s 状态 %d", endpoint, resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // 清空响应体复用连接
	return nil
}

// SetFile 登记正常可下载文件（path 为 URL 路径，如 /files/7270/801/x.jar）。
func (s *Serve) SetFile(path string, content []byte) error {
	return s.postControl("set-file", map[string]any{"path": path, "content_bytes": content})
}

// Script 为路径安装故障脚本（空 steps 清除回落 set-file）。
func (s *Serve) Script(path string, steps ...Step) error {
	wire := make([]map[string]any, 0, len(steps))
	for _, st := range steps {
		m := map[string]any{
			"status": st.Status, "range_from": st.RangeFrom, "range_total": st.RangeTotal,
			"retry_after_seconds": st.RetryAfterSeconds, "truncate_at": st.TruncateAt,
			"hang_ms": st.HangMS, "delay_ms": st.DelayMS, "abort": st.Abort,
		}
		if len(st.Body) > 0 {
			m["body_base64"] = base64.StdEncoding.EncodeToString(st.Body)
		}
		wire = append(wire, m)
	}
	return s.postControl("script", map[string]any{"path": path, "steps": wire})
}

// ClearScript 清除路径脚本（回落 set-file 内容——「假 CDN 恢复」）。
func (s *Serve) ClearScript(path string) error { return s.Script(path) }

// Requests 读回全部请求记录（Range 头即续传证据）。
func (s *Serve) Requests() ([]Request, error) {
	resp, err := s.client.Get(s.controlURL("requests"))
	if err != nil {
		return nil, fmt.Errorf("cdnproc: 控制面 requests: %w", err)
	}
	defer resp.Body.Close()
	var out []Request
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// FilePath 按 directURL 同口径构造 URL 路径（/files/{id/1000}/{id%1000}/{name}，
// 整数除法不补零——internal/download 黄金向量钉死的公式，客户端零漂移重算）。
func FilePath(fileID int64, filename string) string {
	return fmt.Sprintf("/files/%d/%d/%s", fileID/1000, fileID%1000, filename)
}
