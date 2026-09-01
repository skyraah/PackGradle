package gc

import "testing"

// TestAudit 表驱动覆盖引用图不变式断言器四向（验收规格 §6 之③）：
// 一致态零违例；超集违例（账目/盘上不可达未隔离）；红线违例（可达缺文件）；
// ghost 悬账；隔离区豁免。
func TestAudit(t *testing.T) {
	live := []string{"aa01", "aa02", "aa03"} // 可达闭包
	cases := []struct {
		name    string
		in      AuditInput
		want    map[string]map[string]bool // kind -> digest -> 存在
		wantNil bool
	}{
		{
			name: "一致态零违例",
			in: AuditInput{
				Reachable:   live,
				Quarantined: []string{"bb01"},
				ReadyRows:   []string{"aa01", "aa02", "aa03"},
				OnDisk:      []string{"aa01", "aa02", "aa03", "bb01"}, // bb01 是 trash 副本（隔离区豁免）
			},
			wantNil: true,
		},
		{
			name: "超集违例：undead 行与盘上文件",
			in: AuditInput{
				Reachable: live,
				ReadyRows: []string{"aa01", "ee01"},
				OnDisk:    []string{"aa01", "ee02"},
			},
			// ee01 ready 无文件伴随 ghost；可达集只验了 aa01，aa02/aa03
			// 的缺文件红线在本 case 一并产出（输入构造使然，一并断言）。
			want: map[string]map[string]bool{
				FindingUndeadRow:   {"ee01": true},
				FindingUndeadFile:  {"ee02": true},
				FindingGhostRow:    {"ee01": true},
				FindingMissingFile: {"aa02": true, "aa03": true},
			},
		},
		{
			name: "红线违例：可达 digest 盘上缺文件",
			in: AuditInput{
				Reachable: []string{"aa01", "ff01"},
				ReadyRows: []string{"aa01"},
				OnDisk:    []string{"aa01"},
			},
			want: map[string]map[string]bool{
				FindingMissingFile: {"ff01": true},
			},
		},
		{
			name: "ghost 悬账：ready 行无文件",
			in: AuditInput{
				Reachable: []string{"aa01"},
				ReadyRows: []string{"aa01", "gg01"},
				OnDisk:    []string{"aa01"},
			},
			// ghost 悬账（不可达 ready 行无文件）天然伴随 undead_row：
			// 同一 digest 同时是超集违例与悬账。
			want: map[string]map[string]bool{
				FindingGhostRow:  {"gg01": true},
				FindingUndeadRow: {"gg01": true},
			},
		},
		{
			name: "大小写归一后一致",
			in: AuditInput{
				Reachable: []string{"AA01"},
				ReadyRows: []string{"aa01"},
				OnDisk:    []string{"Aa01"},
			},
			wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := Audit(tc.in)
			if tc.wantNil {
				if len(findings) != 0 {
					t.Fatalf("期望零违例，got %v", findings)
				}
				return
			}
			got := map[string]map[string]bool{}
			for _, f := range findings {
				if got[f.Kind] == nil {
					got[f.Kind] = map[string]bool{}
				}
				got[f.Kind][f.Digest] = true
			}
			for kind, digests := range tc.want {
				for d := range digests {
					if !got[kind][d] {
						t.Fatalf("缺违例 %s/%s，got %v", kind, d, findings)
					}
				}
			}
			if len(got) > len(tc.want) {
				t.Fatalf("多余违例类别：want %v got %v", tc.want, got)
			}
		})
	}
}
