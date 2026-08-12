package errs

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNew(t *testing.T) {
	err := New("err.proj.not_found", "Collapse")
	appErr, ok := err.(*AppError)
	if !ok {
		t.Fatalf("应返回 *AppError，实际 %T", err)
	}
	if appErr.Code != "err.proj.not_found" || len(appErr.Args) != 1 || appErr.Args[0] != "Collapse" {
		t.Errorf("Code/Args 不正确: %+v", appErr)
	}
	if appErr.Detail != "" {
		t.Errorf("New 不应携带 Detail: %+v", appErr)
	}
}

func TestNewDetail(t *testing.T) {
	err := NewDetail("err.toml.parse", "unexpected EOF", "pack.toml")
	appErr, ok := err.(*AppError)
	if !ok {
		t.Fatalf("应返回 *AppError，实际 %T", err)
	}
	if appErr.Code != "err.toml.parse" || appErr.Detail != "unexpected EOF" ||
		len(appErr.Args) != 1 || appErr.Args[0] != "pack.toml" {
		t.Errorf("Code/Args/Detail 不正确: %+v", appErr)
	}
}

func TestCodeOf(t *testing.T) {
	if got := CodeOf(New("err.cf.api_key_missing")); got != "err.cf.api_key_missing" {
		t.Errorf("CodeOf(AppError) = %q", got)
	}
	if got := CodeOf(errors.New("plain error")); got != "" {
		t.Errorf("CodeOf(普通错误) 应为空串，实际 %q", got)
	}
	if got := CodeOf(nil); got != "" {
		t.Errorf("CodeOf(nil) 应为空串，实际 %q", got)
	}
}

// 被 fmt.Errorf 包装后 CodeOf 仍能解包到错误码
func TestCodeOfWrapped(t *testing.T) {
	wrapped := errors.New("outer: " + New("err.file.write", "x").Error())
	if got := CodeOf(wrapped); got != "" {
		t.Errorf("普通包装错误不应解出错误码，实际 %q", got)
	}
}

func TestAppErrorError(t *testing.T) {
	// 无参数：仅错误码 JSON
	got := New("err.tool.packwiz_not_found").Error()
	if got != `{"code":"err.tool.packwiz_not_found"}` {
		t.Errorf("Error() = %s", got)
	}
	// 带参数与 Detail：完整 JSON，与 MarshalError 输出一致
	got = NewDetail("err.toml.parse", "boom", "pack.toml").Error()
	want := `{"code":"err.toml.parse","args":["pack.toml"],"detail":"boom"}`
	if got != want {
		t.Errorf("Error() = %s, 期望 %s", got, want)
	}
}

// 数据字段中的错误码文本（如 RefreshResult.Output）应能被 JSON 反解析
func TestAppErrorRoundTrip(t *testing.T) {
	err := New("err.proj.not_found", `C:\packs\Collapse`)
	text := err.Error()
	var decoded AppError
	if jerr := json.Unmarshal([]byte(text), &decoded); jerr != nil {
		t.Fatalf("Error() 应可反序列化: %v", jerr)
	}
	if decoded.Code != "err.proj.not_found" || len(decoded.Args) != 1 || decoded.Args[0] != `C:\packs\Collapse` {
		t.Errorf("反序列化不正确: %+v", decoded)
	}
}
