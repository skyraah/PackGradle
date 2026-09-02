package main

// 假 CDN 进程形态控制面单测（票 #66）：三个控制端点对 FakeCDN 的脚本化
// 写入/清除/读取行为。进程监听面（LISTEN 行协议）由 acceptance:download 链
// 端到端消费；此处只测控制语义——set-file 登记内容、script 安装/清除脚本、
// requests 记录含 Range 头（续传证据）、abort 步断连。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"packgradle/internal/download"
)

func newServeTest(t *testing.T) (*httptest.Server, *download.FakeCDN) {
	t.Helper()
	cdn := download.NewFakeCDN()
	srv := httptest.NewServer(newServeMux(cdn))
	t.Cleanup(srv.Close)
	return srv, cdn
}

// postControl 控制面 POST JSON 并断言 200。
func postControl(t *testing.T, srv *httptest.Server, path string, body any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST %s 状态 %d", path, resp.StatusCode)
	}
}

func TestControlSetFileAndScript(t *testing.T) {
	srv, _ := newServeTest(t)
	content := []byte("good jar bytes v1")
	postControl(t, srv, "/__control/set-file", map[string]string{
		"path": "/files/7270/801/dl-mod-a-1.0.jar", "content_base64": base64.StdEncoding.EncodeToString(content),
	})

	// 正常文件可取（内容逐字节一致）。
	resp, err := srv.Client().Get(srv.URL + "/files/7270/801/dl-mod-a-1.0.jar")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != string(content) {
		t.Fatalf("set-file 后 GET 状态=%d 内容=%q，期望 200/%q", resp.StatusCode, got, content)
	}

	// 脚本 404：探测降标证据面（acceptance:download 场景②消费）。
	postControl(t, srv, "/__control/script", map[string]any{
		"path": "/files/7270/801/dl-mod-a-1.0.jar", "steps": []fakeStepJSON{{Status: 404}},
	})
	resp2, err := srv.Client().Get(srv.URL + "/files/7270/801/dl-mod-a-1.0.jar")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("脚本后 GET 状态 %d，期望 404", resp2.StatusCode)
	}

	// 清脚本（空 steps）回落 set-file 内容（场景③「假 CDN 恢复」入口）。
	postControl(t, srv, "/__control/script", map[string]any{
		"path": "/files/7270/801/dl-mod-a-1.0.jar", "steps": []fakeStepJSON{},
	})
	resp3, err := srv.Client().Get(srv.URL + "/files/7270/801/dl-mod-a-1.0.jar")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("清脚本后 GET 状态 %d，期望 200", resp3.StatusCode)
	}
}

func TestControlRequestsRecordsRange(t *testing.T) {
	srv, _ := newServeTest(t)
	content := []byte("0123456789abcdef")
	postControl(t, srv, "/__control/set-file", map[string]string{
		"path": "/files/7270/808/dl-mod-b-4.0.jar", "content_base64": base64.StdEncoding.EncodeToString(content),
	})
	// 半截断流 + 206 余下部分（场景⑤续传脚本形状）。
	postControl(t, srv, "/__control/script", map[string]any{
		"path": "/files/7270/808/dl-mod-b-4.0.jar",
		"steps": []fakeStepJSON{
			{TruncateAt: 4},
			{Status: 206, RangeFrom: 4, RangeTotal: int64(len(content)),
				BodyBase64: base64.StdEncoding.EncodeToString(content[4:])},
		},
	})

	// 首请求（无 Range）→ 截断断流（客户端得传输中断 EOF，容错）；
	// 续请求（带 Range）→ 206。
	c := srv.Client()
	r1, err := c.Get(srv.URL + "/files/7270/808/dl-mod-b-4.0.jar")
	if err == nil {
		io.Copy(io.Discard, r1.Body) //nolint:errcheck // 截断流读中断属预期
		r1.Body.Close()
	}
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/files/7270/808/dl-mod-b-4.0.jar", nil)
	req2.Header.Set("Range", "bytes=4-")
	r2, err := c.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 206 || string(got2) != string(content[4:]) {
		t.Fatalf("Range 续传状态=%d 内容=%q，期望 206/%q", r2.StatusCode, got2, content[4:])
	}

	// 控制面读回请求记录：Range 头即续传证据。
	resp, err := c.Get(srv.URL + "/__control/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reqs []download.FakeRequest
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		t.Fatal(err)
	}
	withRange := 0
	for _, r := range reqs {
		if r.Range != "" {
			withRange++
		}
	}
	if withRange != 1 {
		t.Fatalf("Range 请求记录 %d 条，期望 1: %+v", withRange, reqs)
	}
}

func TestControlSetFileBytesDirect(t *testing.T) {
	srv, _ := newServeTest(t)
	// content_bytes 直传字节（链路本机形态免 base64 往返）。
	postControl(t, srv, "/__control/set-file", map[string]any{
		"path": "/files/7/1/x.jar", "content_bytes": []byte("direct"),
	})
	resp, err := srv.Client().Get(srv.URL + "/files/7/1/x.jar")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "direct" {
		t.Fatalf("content_bytes 直传内容=%q", got)
	}
}

func TestFakeStepAbort(t *testing.T) {
	srv, _ := newServeTest(t)
	// abort 步：收到请求即断连——客户端得传输中断（刷新响应错误），不得 200。
	postControl(t, srv, "/__control/script", map[string]any{
		"path": "/files/7/2/abort.jar", "steps": []fakeStepJSON{{Abort: true}},
	})
	_, err := srv.Client().Get(srv.URL + "/files/7/2/abort.jar")
	if err == nil {
		t.Fatal("abort 步应产生传输错误")
	}
	if !strings.Contains(fmt.Sprint(err), "EOF") {
		t.Logf("abort 传输错误形态（记录性断言）: %v", err)
	}
}
