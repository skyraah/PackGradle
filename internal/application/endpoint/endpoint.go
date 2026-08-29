// Package endpoint 收敛 Project/Runtime 两个端点用例包共享的错误码与健康评估
// （契约 03 §2.5/§3）。用例本身仍按架构 §4.2 落在 application/project 与
// application/runtime；本包只承载两侧完全一致的纯函数与常量，避免复制漂移。
package endpoint

import (
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
)

// 错误码（契约 03 §3；文案由前端 zh-CN locale 提供）。
const (
	// CodeNotFound 端点不存在，args {0}=endpoint_id。
	CodeNotFound = "err.endpoint.not_found"
	// CodeInvalidPath 路径无法解析/非目录（登记输入），args {0}=path。
	CodeInvalidPath = "err.endpoint.invalid_path"
	// CodeMissing 健康检查：路径不存在，args {0}=path。
	CodeMissing = "err.endpoint.missing"
	// CodeIdentityMismatch 健康检查：绑定指纹不匹配（提示重绑）；也用于
	// 登记时同名实例目录已被登记为不同路径的身份冲突，args {0}=endpoint_id。
	CodeIdentityMismatch = "err.endpoint.identity_mismatch"
	// CodeDiscoveryFailed 项目源发现失败，args {0}=parent_dir。
	CodeDiscoveryFailed = "err.endpoint.discovery_failed"
	// CodeInstancesDirNotFound Prism 实例目录不可定位，args {0}=path。
	CodeInstancesDirNotFound = "err.endpoint.instances_dir_not_found"
)

// 健康状态枚举（EndpointHealthView.Status 取值，契约 03 §2.5）。
const (
	StatusOK              = "ok"
	StatusMissing         = "missing"
	StatusIdentityMismatch = "identity_mismatch"
)

// HealthDeps 是健康评估的最小依赖。
type HealthDeps struct {
	Paths         ports.EndpointNormalizer
	Fingerprinter ports.BindingFingerprinter
	Now           func() time.Time
}

// Evaluate 从存储的绑定指纹与当前路径实况推导健康投影（只读，不改状态；
// relation 侧健康由 relation_health 承担，二者互补）。
//
// 存储路径规范化失败 → missing；指纹采集失败（路径存在但身份无法证明，
// 如权限问题）按最保守处理为 identity_mismatch，提示用户重绑。
func Evaluate(deps HealthDeps, endpointID, storedRoot, storedFingerprint string) view.EndpointHealthView {
	v := view.EndpointHealthView{EndpointID: endpointID, CheckedAt: deps.Now().UTC().Format(time.RFC3339)}
	real, err := deps.Paths.NormalizeEndpointPath(storedRoot)
	if err != nil {
		v.Status = StatusMissing
		return v
	}
	v.PathExists = true
	current, err := deps.Fingerprinter.Fingerprint(real)
	if err != nil {
		v.Status = StatusIdentityMismatch
		return v
	}
	v.FingerprintMatches = current != "" && current == storedFingerprint
	if v.FingerprintMatches {
		v.Status = StatusOK
	} else {
		v.Status = StatusIdentityMismatch
	}
	return v
}
