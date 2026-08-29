package policy

import "testing"

func TestMergeSuggestions(t *testing.T) {
	base := DefaultV1()

	// 空勾选：原样返回
	merged, err := MergeSuggestions(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rules) != 1 {
		t.Fatalf("空勾选不应增规则: %d", len(merged.Rules))
	}

	// 勾选建议：追加对应规则，模板原值不被修改
	merged, err = MergeSuggestions(base, []string{"config", "scripts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Rules) != 1 {
		t.Fatalf("MergeSuggestions 不得修改模板入参: %d", len(base.Rules))
	}
	if len(merged.Rules) != 3 {
		t.Fatalf("勾选 2 条建议应得 3 规则: %d", len(merged.Rules))
	}
	got := map[string]bool{}
	for _, r := range merged.Rules {
		got[r.ID] = true
	}
	for _, want := range []string{"mods", "config", "scripts"} {
		if !got[want] {
			t.Errorf("缺少规则 %s", want)
		}
	}

	// 未知 ID 拒绝
	if _, err := MergeSuggestions(base, []string{"config", "nope"}); err == nil {
		t.Fatal("未知建议 ID 应返回错误")
	}
}
