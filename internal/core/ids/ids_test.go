package ids

import (
	"regexp"
	"sync"
	"testing"
)

func TestNewFormat(t *testing.T) {
	re := regexp.MustCompile(`^rel_[0-9A-HJKMNP-TV-Z]{26}$`)
	for i := 0; i < 100; i++ {
		id := New("rel_")
		if !re.MatchString(id) {
			t.Fatalf("格式不符: %q", id)
		}
		if err := Validate("rel_", id); err != nil {
			t.Fatalf("Validate 拒绝了合法 id %q: %v", id, err)
		}
	}
	if err := Validate("prj_", New("rel_")); err == nil {
		t.Fatal("前缀错误应当被拒绝")
	}
}

func TestNewUniqueness(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := New("snap_")
		if _, dup := seen[id]; dup {
			t.Fatalf("重复 id: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewConcurrent(t *testing.T) {
	const workers = 8
	perWorker := 500
	var mu sync.Mutex
	seen := make(map[string]struct{}, workers*perWorker)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, 0, perWorker)
			for i := 0; i < perWorker; i++ {
				local = append(local, New("task_"))
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if _, dup := seen[id]; dup {
					t.Errorf("并发重复 id: %s", id)
					return
				}
				seen[id] = struct{}{}
			}
		}()
	}
	wg.Wait()
}

func TestNewMonotonicWithinProcess(t *testing.T) {
	// 进程内时间不减：后生成的 id（同前缀）字典序 >= 先生成的
	prev := New("evt_")
	for i := 0; i < 1000; i++ {
		next := New("evt_")
		if next < prev {
			t.Fatalf("id 字典序回退: %s < %s", next, prev)
		}
		prev = next
	}
}
