package download

import (
	"io"
	"strings"
	"testing"
	"time"
)

// 假 CDN 自身行为（票 10 以进程形态复用同一 Handler，这里钉死脚本语义）

func TestFakeCDNServesRegisteredFiles(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	cdn.SetFile("/files/7270/446/a.jar", []byte("hello"))

	resp, err := srv.Client().Get(cdnURL(srv) + "/files/7270/446/a.jar")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("应 200/hello，实际 %d/%q", resp.StatusCode, body)
	}

	// 未登记路径默认 404
	resp2, err := srv.Client().Get(cdnURL(srv) + "/files/0/1/missing.jar")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("未登记路径应 404，实际 %d", resp2.StatusCode)
	}
}

// 206 步携带正确 Content-Range（Range 续传第二程的响应形状）
func TestFakeCDNStep206Headers(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	full := "0123456789"
	cdn.Script("/files/0/1/p.bin", Step206([]byte(full), 4))

	resp, err := srv.Client().Get(cdnURL(srv) + "/files/0/1/p.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 206 {
		t.Fatalf("应 206，实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 4-9/10" {
		t.Fatalf("Content-Range 不符: %q", got)
	}
	if string(body) != "456789" {
		t.Fatalf("206 体应为余下部分，实际 %q", body)
	}
}

// 脚本按序消费、越界重复最后一步
func TestFakeCDNScriptConsumption(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	cdn.Script("/files/0/1/s.bin",
		FakeStep{Status: 503},
		FakeStep{Status: 200, Body: []byte("ok")},
	)

	want := []int{503, 200, 200, 200} // 第三次起重复最后一步
	for i, code := range want {
		resp, err := srv.Client().Get(cdnURL(srv) + "/files/0/1/s.bin")
		if err != nil {
			t.Fatalf("第 %d 次 Get: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != code {
			t.Fatalf("第 %d 次应 %d，实际 %d", i, code, resp.StatusCode)
		}
	}

	// 请求记录含 UA 与 Range
	reqs := cdn.Requests()
	if len(reqs) != len(want) {
		t.Fatalf("记录数 %d != %d", len(reqs), len(want))
	}
}

// 半截断流：声明全长 Content-Length、只发前缀后断流
func TestFakeCDNTruncation(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	full := strings.Repeat("x", 100)
	cdn.Script("/files/0/1/t.bin", FakeStep{Status: 200, Body: []byte(full), TruncateAt: 10})

	resp, err := srv.Client().Get(cdnURL(srv) + "/files/0/1/t.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if cl := resp.Header.Get("Content-Length"); cl != "100" {
		t.Fatalf("应声明全长 Content-Length 100，实际 %q", cl)
	}
	n, rerr := io.Copy(io.Discard, resp.Body)
	if rerr == nil {
		t.Fatal("半截断流应以读错误收场")
	}
	if n != 10 {
		t.Fatalf("应只收到 10 字节，实际 %d", n)
	}
}

// Retry-After 头透出
func TestFakeCDNRetryAfterHeader(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	cdn.Script("/files/0/1/r.bin", FakeStep{Status: 429, RetryAfterSeconds: 9})

	resp, err := srv.Client().Get(cdnURL(srv) + "/files/0/1/r.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if got := resp.Header.Get("Retry-After"); got != "9" {
		t.Fatalf("Retry-After 不符: %q", got)
	}
}

// Hang 步骤：客户端断开后服务端解除挂起（不泄漏 goroutine/连接）
func TestFakeCDNHangReleases(t *testing.T) {
	cdn := NewFakeCDN()
	srv := newTestServer(t, cdn)
	cdn.Script("/files/0/1/h.bin", FakeStep{Hang: 30 * time.Second})

	client := srv.Client()
	client.Timeout = 150 * time.Millisecond
	start := time.Now()
	var err error
	resp, err := client.Get(cdnURL(srv) + "/files/0/1/h.bin")
	if err == nil {
		// 响应头即刻返回，body 挂住：读 body 才会撞上客户端超时
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("挂住响应应使客户端超时")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("客户端超时后应尽快返回，实际 %v", elapsed)
	}
}
