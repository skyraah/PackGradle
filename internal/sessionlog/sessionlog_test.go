// 会话日志保留策略 fake clock 单测族（ADR-0011 §1、P4 验收规格 §5.1）：
// 三轴全格——① 份数窗口（保最近 N 会话，超窗从最旧删）、② 明文/压缩分层
// （最近 KeepPlaintext 份明文 .log、更早原地压缩 .log.gz）、③ 总量硬顶优先
// 于份数（造超顶体积 → 从最旧会话删至限内且允许低于 20 份）。另覆盖会话
// 文件必产与 JSON 形态、同秒目录唯一序号、外来条目不误删、当次会话永不自删。
package sessionlog

import (
	"bytes"
	"compress/gzip"
	crand "crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stepClock 返回每次调用前进 step 的假时钟（起点即首次返回值）。
func stepClock(start time.Time, step time.Duration) func() time.Time {
	current := start.Add(-step)
	return func() time.Time {
		current = current.Add(step)
		return current
	}
}

// openAt 以假时钟逐次推进 1 分钟连开 n 份会话（每份落一条日志后关闭）。
func openAt(t *testing.T, logsDir string, policy Policy, n int, start time.Time) {
	t.Helper()
	clock := stepClock(start, time.Minute)
	for i := 0; i < n; i++ {
		s, err := Open(logsDir, Options{Now: clock, Policy: policy})
		if err != nil {
			t.Fatalf("第 %d 次启动 Open 失败: %v", i+1, err)
		}
		s.Logger.Info("会话自检", "seq", i+1)
		if err := s.Close(); err != nil {
			t.Fatalf("第 %d 次启动 Close 失败: %v", i+1, err)
		}
	}
}

// sessionDirs 列出 logsDir 下的会话目录名（升序）。
func sessionDirs(t *testing.T, logsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatalf("读日志根目录失败: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && sessionDirRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// readSessionFile 读会话目录内的明文 session.log（要求存在）。
func readSessionFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name, sessionFile))
	if err != nil {
		t.Fatalf("会话 %s 应有明文 session.log: %v", name, err)
	}
	return data
}

// readSessionGz 读会话目录内的 session.log.gz 并解压（要求存在）。
func readSessionGz(t *testing.T, dir, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, name, sessionGzFile))
	if err != nil {
		t.Fatalf("会话 %s 应有压缩 session.log.gz: %v", name, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("会话 %s 的 .gz 应为合法 gzip: %v", name, err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("会话 %s 的 .gz 解压失败: %v", name, err)
	}
	return data
}

// writeIncompressible 向 path 写 n KB 不可压缩随机字节（硬顶轴造体积用：
// 若用规律文本，压缩分层会把体积缩没，硬顶永远不触发）。
func writeIncompressible(t *testing.T, path string, kb int) {
	t.Helper()
	buf := make([]byte, kb*1024)
	if _, err := crand.Read(buf); err != nil {
		t.Fatalf("随机字节生成失败: %v", err)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("造体积文件 %s 失败: %v", path, err)
	}
}

// TestOpenProducesSessionFileJSON：会话文件必产 + 结构化 JSON 形态
// （时间戳目录、time/level/msg 字段、附加属性结构化落盘）。
func TestOpenProducesSessionFileJSON(t *testing.T) {
	logsDir := t.TempDir()
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)
	s, err := Open(logsDir, Options{Now: func() time.Time { return start }})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	wantDir := start.Format(sessionDirLayout)
	if got := filepath.Base(s.Dir); got != wantDir {
		t.Fatalf("会话目录应为启动时间戳 %s，got %s", wantDir, got)
	}
	s.Logger.Info("启动自检", "root", strings.Repeat("x", 4))
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(readSessionFile(t, logsDir, wantDir))), "\n")
	if len(lines) != 1 {
		t.Fatalf("应落 1 条 JSON 日志，实际 %d 行", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("日志行应为合法 JSON: %v\n%s", err, lines[0])
	}
	if rec["msg"] != "启动自检" || rec["level"] != "INFO" || rec["root"] != "xxxx" {
		t.Fatalf("JSON 字段不符: %v", rec)
	}
	if _, ok := rec["time"]; !ok {
		t.Fatalf("JSON 应含 slog time 字段: %v", rec)
	}
}

// 轴① 份数窗口：保最近 KeepSessions 份，超窗最旧会话整目录删除。
func TestOpenCountWindowTrimsOldest(t *testing.T) {
	logsDir := t.TempDir()
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)
	policy := Policy{KeepSessions: 4, KeepPlaintext: 2, MaxTotalBytes: 1 << 30}
	openAt(t, logsDir, policy, 6, start)

	dirs := sessionDirs(t, logsDir)
	if len(dirs) != 4 {
		t.Fatalf("应保最近 4 份会话，实际 %v", dirs)
	}
	// 最旧两份（第 1、2 分钟）应被删，保留第 3–6 分钟
	want := []string{
		start.Add(2 * time.Minute).Format(sessionDirLayout),
		start.Add(3 * time.Minute).Format(sessionDirLayout),
		start.Add(4 * time.Minute).Format(sessionDirLayout),
		start.Add(5 * time.Minute).Format(sessionDirLayout),
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Fatalf("保留窗不符：got %v, want %v", dirs, want)
		}
	}
}

// 轴② 明文/压缩分层：最近 KeepPlaintext 份明文，更早原地压缩 .log.gz。
func TestOpenLayeringPlaintextAndGzip(t *testing.T) {
	logsDir := t.TempDir()
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)
	policy := Policy{KeepSessions: 5, KeepPlaintext: 2, MaxTotalBytes: 1 << 30}
	openAt(t, logsDir, policy, 5, start)

	dirs := sessionDirs(t, logsDir)
	if len(dirs) != 5 {
		t.Fatalf("应保 5 份会话，实际 %v", dirs)
	}
	for i, name := range dirs {
		logPath := filepath.Join(logsDir, name, sessionFile)
		gzPath := filepath.Join(logsDir, name, sessionGzFile)
		if i < len(dirs)-2 {
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("较旧会话 %s 不应保留明文 session.log", name)
			}
			gz := readSessionGz(t, logsDir, name)
			if !bytes.Contains(gz, []byte("会话自检")) {
				t.Fatalf("会话 %s 的 .gz 应还原出日志内容", name)
			}
			// 原地压缩：同一会话目录内改名产物，不产生旁路文件
			entries, err := os.ReadDir(filepath.Join(logsDir, name))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != sessionGzFile {
				t.Fatalf("会话 %s 目录应只剩 session.log.gz，实际 %v", name, entries)
			}
		} else {
			if _, err := os.Stat(logPath); err != nil {
				t.Fatalf("最近会话 %s 应保持明文 session.log: %v", name, err)
			}
			if _, err := os.Stat(gzPath); !os.IsNotExist(err) {
				t.Fatalf("最近会话 %s 不应产生 .gz", name)
			}
		}
	}

	// 再启动一轮：分层幂等——已压缩会话不再有明文，新会话进明文窗。
	clock := stepClock(start.Add(5*time.Minute), time.Minute)
	s, err := Open(logsDir, Options{Now: clock, Policy: policy})
	if err != nil {
		t.Fatalf("再次启动 Open 失败: %v", err)
	}
	s.Close()
	dirs = sessionDirs(t, logsDir)
	if len(dirs) != 5 {
		t.Fatalf("第二轮后仍应保 5 份，实际 %v", dirs)
	}
	for i, name := range dirs {
		_, statLogErr := os.Stat(filepath.Join(logsDir, name, sessionFile))
		_, statGzErr := os.Stat(filepath.Join(logsDir, name, sessionGzFile))
		isPlaintext := statLogErr == nil
		isGz := statGzErr == nil
		if i < len(dirs)-2 && (isPlaintext || !isGz) {
			t.Fatalf("会话 %s 应保持压缩态（幂等）", name)
		}
		if i >= len(dirs)-2 && (!isPlaintext || isGz) {
			t.Fatalf("会话 %s 应为明文态", name)
		}
	}
}

// 轴③ 总量硬顶优先于份数：造超顶体积（不可压缩字节）→ 从最旧会话删至
// 限内，允许低于 KeepSessions（20 份不保）。夹具口径：每轮启动后向该会话
// 灌 10KB 不可压缩 .flood，最后一轮只启动不灌——末轮清理后的实态即断言态
// （清理时机=启动时，启动后新写入不回扫）。硬顶留 5KB 余量吸收空日志压缩
// 产物字节数，使「删至 10 份恰好限内」的边界确定。
func TestOpenHardCapDeletesOldestBelowCountWindow(t *testing.T) {
	logsDir := t.TempDir()
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)
	// 30 份 × 10KB 不可压缩 = 300KB；份数窗 100 不绑定，硬顶 105KB 绑定：
	// 压缩分层对随机字节无效，删至 10 份（≈100KB ≤ 限）止——10 < 20 即
	// 「允许低于份数窗口」。
	policy := Policy{KeepSessions: 100, KeepPlaintext: 3, MaxTotalBytes: 105 * 1024}
	for i := 0; i < 30; i++ {
		clock := stepClock(start.Add(time.Duration(i)*time.Minute), time.Minute)
		s, err := Open(logsDir, Options{Now: clock, Policy: policy})
		if err != nil {
			t.Fatalf("第 %d 次启动失败: %v", i+1, err)
		}
		s.Close()
		name := start.Add(time.Duration(i) * time.Minute).Format(sessionDirLayout)
		writeIncompressible(t, filepath.Join(logsDir, name, sessionFile+".flood"), 10)
	}
	// 末轮启动：触发对 30 份超顶体积的启动时清理（本会话计入但为空）
	clock := stepClock(start.Add(30*time.Minute), time.Minute)
	s, err := Open(logsDir, Options{Now: clock, Policy: policy})
	if err != nil {
		t.Fatalf("末轮启动失败: %v", err)
	}
	s.Close()

	dirs := sessionDirs(t, logsDir)
	// 保留 = 最新 10 份超顶会话（各 10KB，≈100KB 限内）+ 当次空会话
	if len(dirs) != 11 {
		t.Fatalf("硬顶应从最旧删至 10 份超顶会话（+当次），实际 %d 份: %v", len(dirs), dirs)
	}
	// 保留的应是最旧 20 份被删后的最新 10 份
	wantOldest := start.Add(20 * time.Minute).Format(sessionDirLayout)
	if dirs[0] != wantOldest {
		t.Fatalf("应从最旧删起，最旧保留应为 %s，实际 %s", wantOldest, dirs[0])
	}
	var total int64
	for _, name := range dirs {
		total += dirSize(filepath.Join(logsDir, name))
	}
	if total > policy.MaxTotalBytes {
		t.Fatalf("清理后总量 %d 应 ≤ 硬顶 %d", total, policy.MaxTotalBytes)
	}
}

// 轴③ 补格：压缩分层后的 .log.gz 体积计入硬顶（.gz 大文件同样触发从最旧删）。
func TestOpenHardCapCountsCompressedFiles(t *testing.T) {
	logsDir := t.TempDir()
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)
	policy := Policy{KeepSessions: 100, KeepPlaintext: 1, MaxTotalBytes: 50 * 1024}
	// 8 份会话各灌 10KB 不可压缩 .gz 体积（预先放好，绕过本轮压缩）：
	// 总量 80KB > 50KB → 删最旧 3 份至 50KB 限内（5×10KB）。
	for i := 0; i < 8; i++ {
		name := start.Add(time.Duration(i) * time.Minute).Format(sessionDirLayout)
		if err := os.MkdirAll(filepath.Join(logsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		writeIncompressible(t, filepath.Join(logsDir, name, sessionGzFile), 10)
	}
	clock := stepClock(start.Add(8*time.Minute), time.Minute)
	s, err := Open(logsDir, Options{Now: clock, Policy: policy})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	s.Close()

	dirs := sessionDirs(t, logsDir)
	if len(dirs) != 6 {
		t.Fatalf(".gz 体积应计入硬顶：9 份（含当次）删至 6 份（6×10KB=60KB>50KB 应再删 1），实际 %v", dirs)
	}
	var total int64
	for _, name := range dirs {
		total += dirSize(filepath.Join(logsDir, name))
	}
	if total > policy.MaxTotalBytes {
		t.Fatalf("清理后总量 %d 应 ≤ 硬顶 %d", total, policy.MaxTotalBytes)
	}
	if dirs[0] != start.Add(3*time.Minute).Format(sessionDirLayout) {
		t.Fatalf("应从最旧 .gz 会话删起，实际最旧 %s", dirs[0])
	}
}

// 同秒重复启动：目录追加 -2 序号，两份会话并存互不覆盖。
func TestOpenSameSecondCollisionSuffix(t *testing.T) {
	logsDir := t.TempDir()
	fixed := time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)
	policy := Policy{KeepSessions: 10, KeepPlaintext: 3, MaxTotalBytes: 1 << 30}
	for i := 0; i < 2; i++ {
		s, err := Open(logsDir, Options{Now: func() time.Time { return fixed }, Policy: policy})
		if err != nil {
			t.Fatalf("第 %d 次同秒启动失败: %v", i+1, err)
		}
		s.Close()
	}
	base := fixed.Format(sessionDirLayout)
	dirs := sessionDirs(t, logsDir)
	if len(dirs) != 2 || dirs[0] != base || dirs[1] != base+"-2" {
		t.Fatalf("同秒两次启动应得 %s 与 %s-2，实际 %v", base, base, dirs)
	}
	// 序号后缀保序：-2 排在基础名之后（字典序=时间序不被破坏）
	if dirs[0] >= dirs[1] {
		t.Fatalf("序号后缀应保字典序，实际 %v", dirs)
	}
}

// 外来条目不误删：logs/ 下非会话目录名的目录与文件原样保留。
func TestSweepLeavesForeignEntries(t *testing.T) {
	logsDir := t.TempDir()
	foreignDir := filepath.Join(logsDir, "not-a-session")
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "README.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := Policy{KeepSessions: 1, KeepPlaintext: 1, MaxTotalBytes: 1 << 30}
	openAt(t, logsDir, policy, 3, time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local))

	if _, err := os.Stat(foreignDir); err != nil {
		t.Fatalf("非会话目录不应被清理: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(logsDir, "README.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("非会话文件不应被清理: %v %s", err, data)
	}
}

// 当次会话永不自删：硬顶极小（仅剩当次会话可删的场景）时清理停手，
// 会话文件仍在、logger 仍可用。
func TestOpenCurrentSessionNeverDeleted(t *testing.T) {
	logsDir := t.TempDir()
	// 预置一个 10KB 超顶旧会话；硬顶 1KB → 旧会话删光后仅剩当次会话。
	old := filepath.Join(logsDir, time.Date(2026, 9, 3, 9, 0, 0, 0, time.Local).Format(sessionDirLayout))
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIncompressible(t, filepath.Join(old, sessionFile), 10)

	policy := Policy{KeepSessions: 10, KeepPlaintext: 3, MaxTotalBytes: 1024}
	clock := stepClock(time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local), time.Minute)
	s, err := Open(logsDir, Options{Now: clock, Policy: policy})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("当次会话文件不得被硬顶自删: %v", err)
	}
	s.Logger.Warn("硬顶自删保护自检")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("超顶旧会话应被删除: %v", err)
	}
}

// 零值 Options 归一到编译期常量策略（生产入口零参即 ADR 数值）。
func TestZeroOptionsUseDefaultPolicy(t *testing.T) {
	got := normalizePolicy(Policy{})
	want := DefaultPolicy()
	if got != want {
		t.Fatalf("零值 Options 应归一到 DefaultPolicy，got %+v want %+v", got, want)
	}
	if want.KeepSessions != 20 || want.KeepPlaintext != 3 || want.MaxTotalBytes != 100<<20 {
		t.Fatalf("编译期常量应沿 ADR-0011 §1（20/3/100MB），实际 %+v", want)
	}
	// 单轴覆写：仅硬顶覆写时份数轴仍取常量
	p := normalizePolicy(Policy{MaxTotalBytes: 4096})
	if p.KeepSessions != 20 || p.KeepPlaintext != 3 || p.MaxTotalBytes != 4096 {
		t.Fatalf("单轴覆写失效: %+v", p)
	}
}

// 明文超大也先压后删：可压缩明文经分层压缩后体积大幅回落，硬顶零删除
// （顺序理由：压缩本身服务磁盘保护，先省后删；若先删，6 份大会话会被误删 5+ 份）。
func TestSweepCompressBeforeCapAvoidsDeletion(t *testing.T) {
	logsDir := t.TempDir()
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.Local)
	// 造 6 份可压缩明文（重复字节）各 ≈25KB ≈ 150KB；硬顶 30KB、明文窗 1：
	// 分层把 6 份全部原地压缩（≈每份 <1KB），压缩后远低于限 → 零删除。
	policy := Policy{KeepSessions: 100, KeepPlaintext: 1, MaxTotalBytes: 30 * 1024}
	for i := 0; i < 6; i++ {
		name := start.Add(time.Duration(i) * time.Minute).Format(sessionDirLayout)
		dir := filepath.Join(logsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sessionFile), []byte(strings.Repeat("PackGradle 会话日志填充\n", 850)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clock := stepClock(start.Add(6*time.Minute), time.Minute)
	s, err := Open(logsDir, Options{Now: clock, Policy: policy})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	s.Close()

	dirs := sessionDirs(t, logsDir)
	if len(dirs) != 7 {
		t.Fatalf("可压缩明文应先压后删零删除（6 份 + 当次全保），实际 %v", dirs)
	}
	var total int64
	for _, name := range dirs {
		total += dirSize(filepath.Join(logsDir, name))
	}
	if total > policy.MaxTotalBytes {
		t.Fatalf("压缩后总量 %d 应 ≤ 硬顶 %d", total, policy.MaxTotalBytes)
	}
	// 6 份旧会话应全部原地压缩（超出明文窗），当次会话保持明文
	for i, name := range dirs {
		_, logErr := os.Stat(filepath.Join(logsDir, name, sessionFile))
		_, gzErr := os.Stat(filepath.Join(logsDir, name, sessionGzFile))
		if i < 6 && (logErr == nil || gzErr != nil) {
			t.Fatalf("会话 %s 应已原地压缩", name)
		}
		if i == 6 && logErr != nil {
			t.Fatalf("当次会话应保持明文: %v", logErr)
		}
	}
}
