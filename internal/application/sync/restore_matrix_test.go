package sync

// 四标记判定矩阵全格单测（ADR-0006 §2；契约 06 §3.1 行为语义 2；票 #59 AC 1）。
// judgeRestoreMarker 是 prepare 时点确定函数的纯判定核：本表覆盖 rec × 重取信息
// × hash 可验性 × CAS 实存的全格，含三个票面负例——CAS miss → user_object_
// required、手放 mod（rec=redownload 但无重取数据）→ user_object_required、
// unrecoverable 默认阻止 exact。

import (
	"testing"

	"packgradle/internal/core/model"
	"packgradle/internal/download"
)

func TestJudgeRestoreMarkerFullGrid(t *testing.T) {
	full := restoreMarkerInput{Rec: model.RecoverabilityCAS, HasRedownloadInfo: true, HashSupported: true, CASReady: true}
	cases := []struct {
		name       string
		mutate     func(*restoreMarkerInput)
		wantMarker model.RestoreMarker
		wantReason string
	}{
		{
			// rec=cas 且 CAS 对象实存：零网络零用户介入写回（ADR-0006 §2 定义）。
			name:       "rec=cas ∧ CAS 实存 → restorable_from_cas",
			wantMarker: model.MarkerRestorableFromCAS,
		},
		{
			// 负例（票面）：restorable_from_cas 要求 CAS 对象实存，缺失 →
			// user_object_required（凭目标 digest 验收用户提供字节）。
			name:       "rec=cas ∧ CAS 缺失 → user_object_required + no_redownload_info（CAS miss 负例）",
			mutate:     func(in *restoreMarkerInput) { in.CASReady = false },
			wantMarker: model.MarkerUserObjectRequired,
			wantReason: model.MarkerReasonNoRedownloadInfo,
		},
		{
			// 未记录策略（none/空）沿「宁可多存」缺省走 CAS 途径，实存判定兜底。
			name:       "rec=none ∧ CAS 实存 → restorable_from_cas",
			mutate:     func(in *restoreMarkerInput) { in.Rec = model.RecoverabilityNone },
			wantMarker: model.MarkerRestorableFromCAS,
		},
		{
			name:       "rec=none ∧ CAS 缺失 → user_object_required + no_redownload_info",
			mutate:     func(in *restoreMarkerInput) { in.Rec = model.RecoverabilityNone; in.CASReady = false },
			wantMarker: model.MarkerUserObjectRequired,
			wantReason: model.MarkerReasonNoRedownloadInfo,
		},
		{
			name:       "rec 空串（历史数据缺列）∧ CAS 实存 → restorable_from_cas",
			mutate:     func(in *restoreMarkerInput) { in.Rec = "" },
			wantMarker: model.MarkerRestorableFromCAS,
		},
		{
			// rec=user_object 本身不作为判定输入格（apply 侧策略词汇）；判定核按
			// CAS 缺省途径兜底，实存即写回。
			name:       "rec=user_object ∧ CAS 实存 → restorable_from_cas",
			mutate:     func(in *restoreMarkerInput) { in.Rec = model.RecoverabilityUserObject },
			wantMarker: model.MarkerRestorableFromCAS,
		},
		{
			// 负例（票面「重取性看数据不看出身」）：kind=mod（rec=redownload 出身）
			// 不自动等于可重取——重取信息缺失即 user_object_required（手放 mod）。
			name: "rec=redownload ∧ 无重取信息 → user_object_required + no_redownload_info（手放 mod 负例）",
			mutate: func(in *restoreMarkerInput) {
				in.Rec = model.RecoverabilityRedownload
				in.HasRedownloadInfo = false
				in.HashSupported = false
				in.CASReady = false
			},
			wantMarker: model.MarkerUserObjectRequired,
			wantReason: model.MarkerReasonNoRedownloadInfo,
		},
		{
			// murmur2 等不可验声明格式：不验不装（ADR-0008），不能安全重取 →
			// user_object_required + hash_format_unsupported（与引擎信号同字面）。
			name: "rec=redownload ∧ 重取信息实存 ∧ hash 不可验 → user_object_required + hash_format_unsupported",
			mutate: func(in *restoreMarkerInput) {
				in.Rec = model.RecoverabilityRedownload
				in.HashSupported = false
				in.CASReady = false
			},
			wantMarker: model.MarkerUserObjectRequired,
			wantReason: model.MarkerReasonHashFormatUnsupported,
		},
		{
			// 乐观标记：重取信息实存且可验即 redownload_required（探测可降标，
			// ADR-0006 §7）；CAS 有无内容不牵制重取途径（两途径独立）。
			name:       "rec=redownload ∧ 重取信息实存 ∧ hash 可验 → redownload_required",
			mutate:     func(in *restoreMarkerInput) { in.Rec = model.RecoverabilityRedownload },
			wantMarker: model.MarkerRedownloadRequired,
		},
		{
			name:       "rec=redownload ∧ hash 可验 ∧ CAS 缺失 → redownload_required（重取不依赖 CAS）",
			mutate:     func(in *restoreMarkerInput) { in.Rec = model.RecoverabilityRedownload; in.CASReady = false },
			wantMarker: model.MarkerRedownloadRequired,
		},
		{
			// 负例（票面）：unrecoverable 恒 unrecoverable，重取信息与 CAS 实存
			// 都不翻案；默认阻止 exact（流程面由 restoreExactReady default 分支拒绝）。
			name:       "rec=unrecoverable → unrecoverable（阻止 exact 负例）",
			mutate:     func(in *restoreMarkerInput) { in.Rec = model.RecoverabilityUnrecoverable },
			wantMarker: model.MarkerUnrecoverable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := full
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			gotMarker, gotReason := judgeRestoreMarker(in)
			if gotMarker != tc.wantMarker || gotReason != tc.wantReason {
				t.Fatalf("judgeRestoreMarker(%+v) = (%s, %q)，期望 (%s, %q)",
					in, gotMarker, gotReason, tc.wantMarker, tc.wantReason)
			}
		})
	}
}

// TestRestoreHashGateSharesEngineSet 验证判定核的 hash 可验格与下载引擎取数
// gate 共用同一份格式清单（SupportsHashFormat 即 newHasher 的导出视图）——
// 「计划期说可重取、执行期引擎拒验」的口径漂移被结构性排除。
func TestRestoreHashGateSharesEngineSet(t *testing.T) {
	for _, format := range []string{"md5", "sha1", "sha256", "sha512", "SHA256"} {
		if !download.SupportsHashFormat(format) {
			t.Errorf("SupportsHashFormat(%q) = false，期望 true（引擎可验集）", format)
		}
	}
	for _, format := range []string{"murmur2", "", "xxhash", "crc32"} {
		if download.SupportsHashFormat(format) {
			t.Errorf("SupportsHashFormat(%q) = true，期望 false（不可验格式）", format)
		}
	}
}
