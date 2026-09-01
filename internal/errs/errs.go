package errs

import (
	"encoding/json"
	"errors"
	"fmt"
)

// AppError 是服务返回的结构化错误：Code 为前端翻译键（如 err.proj.not_found），
// Args 为插值参数（项目名、路径、HTTP 状态码等），Detail 为透传的底层错误文本
// （如网络错误、packwiz 原生输出）。
//
// 按 client/server 分离原则，Go 端不产生任何用户可见文案：错误只携带错误码与
// 数据，由前端语言文件渲染为可读文本。序列化通过 Wails 的 MarshalError 完成，
// 前端从 err.cause 读取。
type AppError struct {
	Code   string   `json:"code"`
	Args   []string `json:"args,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

// New 构造错误码错误（无底层透传文本）
func New(code string, args ...any) error {
	return &AppError{Code: code, Args: strArgs(args)}
}

// NewDetail 构造错误码错误，携带透传的底层错误文本
func NewDetail(code, detail string, args ...any) error {
	return &AppError{Code: code, Args: strArgs(args), Detail: detail}
}

// CodeOf 返回错误的错误码；非 AppError（如透传错误）返回空串
func CodeOf(err error) string {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ""
}

// ArgsOf 返回错误的插值参数；非 AppError 返回 nil。供非抛出通道（如剔除
// 语义的跳过清单，票 #63）携带与调用级错误一致的 code+args 投影。
func ArgsOf(err error) []string {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Args
	}
	return nil
}

// Error 实现 error 接口。返回与 MarshalError 一致的结构化 JSON，
// 供前端统一解析渲染（无论错误经 err.cause 传递还是文本落入数据字段，
// 如 RefreshResult.Output / PackProject.Error）；日志场景下也完整可读。
func (e *AppError) Error() string {
	b, err := json.Marshal(e)
	if err != nil {
		return e.Code
	}
	return string(b)
}

func strArgs(args []any) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = fmt.Sprint(a)
	}
	return out
}
