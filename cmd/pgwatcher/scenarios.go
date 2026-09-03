package main

// 六场景编排（验收规格 §4.2；票 #96）：外部写真文件驱动 + 时间线不变式断言。
// 共享预跑面：perffixture 小型确定性夹具 → pgheadless -apply 全量收口（diff 归零）
// →（按场景）-set-authorized 开/关态 → pgheadless -watch 常驻子进程。
// 断言纪律：只断不变式与轮数上界，不卡毫秒时序（验收规格 §8.4）。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/cdnproc"
	"packgradle/internal/download"
	"packgradle/internal/errs"
	"packgradle/internal/perffixture"
)

// prepPaths 是单场景夹具与数据目录。
type prepPaths struct {
	project, instance, data string
}

// scenarioDir 建立并清空场景工作目录。
func (s *wScenario) scenarioDir(name string) string {
	dir := filepath.Join(s.env.work, name)
	if err := os.RemoveAll(dir); err != nil {
		s.abort("清理场景目录 %s: %v", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.abort("建场景目录 %s: %v", dir, err)
	}
	return dir
}

// genFixture 生成小型确定性夹具（mods 含 CF 声明变体；plainMods 供回滚面场景）。
func (s *wScenario) genFixture(out string, plainMods int) prepPaths {
	res, err := perffixture.Generate(context.Background(), perffixture.Options{
		OutDir: out, Seed: s.env.seed, Mods: s.env.mods, TextFiles: s.env.textFiles, PlainMods: plainMods,
	})
	if err != nil {
		s.abort("夹具生成 %s: %v", out, err)
	}
	return prepPaths{
		project:  res.ProjectRoot,
		instance: res.InstanceDir,
	}
}

// runApply 跑一遍 -apply 全量收口（第二遍起 noop；预跑后 diff=clean）。
func (s *wScenario) runApply(p prepPaths, data string, extra ...string) {
	args := append([]string{
		"-project", p.project, "-instance", p.instance, "-data", data, "-apply",
	}, extra...)
	s.runHeadless(5*time.Minute, "预跑 -apply 全量收口", args...)
}

// setAuthorized 经既有 -set-authorized 先例准备授权开/关态变体。
func (s *wScenario) setAuthorized(p prepPaths, data string, enabled bool) {
	v := "0"
	if enabled {
		v = "1"
	}
	s.runHeadless(2*time.Minute, "授权开关= "+v,
		"-data", data, "-project", p.project, "-instance", p.instance, "-set-authorized", v)
}

// startWatching 拉起常驻监听子进程（-record/-metrics/-哨兵/-控制面），并等待
// 挂载 active。返回挂载完成时刻的记录快照。
func (s *wScenario) startWatching(p prepPaths, dir, cdnURL string) *residentHandle {
	recordPath := filepath.Join(dir, "watch-record.json")
	metricsPath := filepath.Join(dir, "watch-metrics.json")
	sentinel := filepath.Join(dir, "exit.sentinel")
	control := filepath.Join(dir, "ctl")
	if err := os.MkdirAll(control, 0o755); err != nil {
		s.abort("建控制目录: %v", err)
	}
	args := []string{
		"-project", p.project, "-instance", p.instance, "-data", p.data,
		"-watch", "-record", recordPath, "-metrics", metricsPath,
		"-watch-exit-sentinel", sentinel,
		"-watch-control", control, "-watch-timeout", "8m",
	}
	if cdnURL != "" {
		args = append(args, "-cdn", cdnURL)
	}
	rh := s.startResident(args, recordPath, sentinel)
	rh.metricsPath = metricsPath
	s.waitActive(rh)
	return rh
}

// waitActive 等待 watch_status=active（挂载完成）。
func (s *wScenario) waitActive(rh *residentHandle) *watchRecMirror {
	return s.waitRecordCond(rh, 60*time.Second, "等待挂载 active",
		func(m *watchRecMirror) bool {
			st := lastState(m)
			return st != nil && st.WatchStatus == "active"
		})
}

// stopBySentinel 哨兵收敛常驻进程并断言退出原因、终态记录与 -metrics watcher 段。
func (s *wScenario) stopBySentinel(rh *residentHandle) {
	rh.stop(30 * time.Second)
	m, err := readWatchRecord(rh.recordPath)
	if err != nil {
		s.abort("终态记录不可读: %v", err)
	}
	s.want(m.EndReason, "sentinel", "常驻进程经哨兵收敛退出")
	if b, err := os.ReadFile(rh.metricsPath); err != nil || !strings.Contains(string(b), `"watcher"`) {
		s.abort("-metrics 未产出 watcher 段（%s）", rh.metricsPath)
	}
	s.assertions = append(s.assertions, "-metrics 产出 watcher 段（轮数/链墙钟/链相位） ✓")
	s.assertEventSet(m)
}

// writeFile 写真文件（外部写者的事实面：真 fsnotify 事件由此产生）。
func (s *wScenario) writeFile(path, content string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.abort("建目录 %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		s.abort("写 %s: %v", path, err)
	}
}

// editContaining 读文件做单处子串替换后写回（双侧冲突差异的构造面）。
func (s *wScenario) editContaining(path, old, new string) {
	b, err := os.ReadFile(path)
	if err != nil {
		s.abort("读 %s: %v", path, err)
	}
	if !strings.Contains(string(b), old) {
		s.abort("编辑 %s：锚点 %q 不在文件中", path, old)
	}
	s.writeFile(path, strings.Replace(string(b), old, new, 1))
}

// ---- 场景 1：触发与收敛（含红线④ .index 不触发）----

func scenarioTriggerConverge(s *wScenario) {
	dir := s.scenarioDir("s1")
	fx := s.genFixture(filepath.Join(dir, "fixture"), 0)
	p := prepPaths{project: fx.project, instance: fx.instance, data: filepath.Join(dir, "data")}
	s.runApply(p, p.data)
	s.setAuthorized(p, p.data, true)

	rh := s.startWatching(p, dir, "")
	base := lastState(s.waitActive(rh))
	commitsBefore := base.Commits

	// 外部写管辖目录文件 → 静默期后自动链触发 → 授权开态 committed。
	t0 := time.Now()
	s.writeFile(filepath.Join(p.project, "config", "acceptance-s1.toml"),
		"[acceptance]\nscenario = 1\nwritten_at = "+time.Now().Format(time.RFC3339)+"\n")
	m := s.waitRecordCond(rh, 90*time.Second, "等待自动链 apply_succeeded",
		func(m *watchRecMirror) bool {
			return anyChain(chainsAfter(m, t0, 2*time.Second), func(c chainMirror) bool {
				return c.Outcome == "apply_succeeded"
			})
		})
	applied := firstChain(chainsAfter(m, t0, 2*time.Second), func(c chainMirror) bool {
		return c.Outcome == "apply_succeeded"
	})
	s.wantTrue(lastState(m).Commits == commitsBefore+1, "授权开态自动链 committed（提交数 +1）")

	// 写盘自触发重扫 no_diff 收敛（applied 的 apply 写盘 → 标脏补轮 → 收敛扫描）。
	m = s.waitRecordCond(rh, 60*time.Second, "等待写盘自触发收敛链",
		func(m *watchRecMirror) bool {
			return anyChain(chainsAfter(m, t0, 2*time.Second), func(c chainMirror) bool {
				return c.Index > applied.Index && c.Outcome == "no_apply_task"
			})
		})
	conv := firstChain(chainsAfter(m, t0, 2*time.Second), func(c chainMirror) bool {
		return c.Index > applied.Index && c.Outcome == "no_apply_task"
	})
	s.wantTrue(conv.Outcome == "no_apply_task", "自触发重扫收敛（无 apply 任务的第二轮）")

	// 轮数有界：触发窗口内 ≤4 轮（期望 2：物化轮 + 收敛轮）。
	rounds := len(chainsAfter(m, t0, 2*time.Second))
	s.wantTrue(rounds <= 4, fmt.Sprintf("触发窗口扫描轮数有界：%d ≤ 4", rounds))

	// 收敛后静默：提交数稳定在 +1、无新增链、无待确认计划。
	s.settleSeconds(rh, 6)
	m = mustRead(s, rh)
	s.wantTrue(lastState(m).Commits == commitsBefore+1, "收敛后提交数稳定（无第三次自动执行）")
	s.wantTrue(lastState(m).PendingPlanID == "", "收敛后无待确认计划")
	s.wantTrue(countChain(chainsAfter(m, t0, 2*time.Second), func(c chainMirror) bool {
		return c.Index > conv.Index
	}) == 0, "收敛后静默（轮数有界的尾部证据）")

	// 红线④：写 mods/.index → 不触发（监听排除集；等待窗 > 上限 10s）。
	t1 := time.Now()
	roundsBefore := m.ScanRounds
	commitsStable := lastState(m).Commits
	s.writeFile(filepath.Join(p.project, "mods", ".index", "probe.toml"),
		"# Prism .index 噪声写入（只读不写面）\n")
	s.settleSeconds(rh, 13)
	m = mustRead(s, rh)
	s.wantTrue(m.ScanRounds == roundsBefore, "写 mods/.index 零自动链（红线④：监听排除）")
	s.wantTrue(lastState(m).Commits == commitsStable, "写 mods/.index 零物化（提交数不变）")
	s.wantTrue(eventsAfter(m, t1, "task_updated") == 0, "写 mods/.index 零链内任务事件")

	stopBySentinel(s, rh)
}

// ---- 场景 2：去抖上界 ----

func scenarioDebounceBound(s *wScenario) {
	dir := s.scenarioDir("s2")
	fx := s.genFixture(filepath.Join(dir, "fixture"), 0)
	p := prepPaths{project: fx.project, instance: fx.instance, data: filepath.Join(dir, "data")}
	s.runApply(p, p.data)
	s.setAuthorized(p, p.data, true)

	rh := s.startWatching(p, dir, "")
	s.waitActive(rh)

	// <1.5s 间隔（1.2s 节拍）持续写 ≥30s：每写一个新管辖文件（create 行无
	// overwrite 确认要求，授权开态可自动物化——修改既有文件的 overwrite 要求
	// 面 Docking 属另一语义，验收规格场景 2 只断轮数上界）。内容互异保证每次
	// 写都是真变化。
	t0 := time.Now()
	stormEnd := t0.Add(30 * time.Second)
	writes := 0
	for time.Now().Before(stormEnd) {
		writes++
		s.writeFile(filepath.Join(p.project, "config", fmt.Sprintf("storm-%03d.toml", writes)),
			fmt.Sprintf("[storm]\nwrite = %d\nat = %s\n", writes, time.Now().Format(watchTS)))
		time.Sleep(1200 * time.Millisecond)
	}

	// 风暴后收敛：等链停止增长（终态可能是收敛轮，也可能是风暴中的 apply 撞上
	// 「复扫与计划目标不一致」保护进 recovery_required——持续写入下的合法终态，
	// 场景只断轮数上界与不变式，不卡物化路径）。
	m := s.waitRecordCond(rh, 120*time.Second, "等待风暴后链停止增长",
		func(m *watchRecMirror) bool {
			ch := chainsAfter(m, t0, 2*time.Second)
			return len(ch) > 0 && time.Since(mustParse(ch[len(ch)-1].EndedAt)) > 6*time.Second
		})
	settled := len(m.Chains)
	s.settleSeconds(rh, 6)
	m = mustRead(s, rh)
	storm := chainsAfter(m, t0, 2*time.Second)
	applies := countChain(storm, func(c chainMirror) bool { return c.Outcome == "apply_succeeded" })

	s.wantTrue(len(storm) >= 2, fmt.Sprintf("风暴期链有触发且聚合（%d 轮）", len(storm)))
	s.wantTrue(len(storm) <= 6, fmt.Sprintf("风暴扫描轮数有上界：%d ≤ 6（30s / 10s 上限 + 收敛余量，不卡毫秒）", len(storm)))
	s.wantTrue(len(storm) < writes, fmt.Sprintf("轮数远小于写入次数：%d 轮 < %d 次写（静默期聚合实据）", len(storm), writes))
	s.wantTrue(len(m.Chains) == settled, fmt.Sprintf("风暴后链数稳定（%d，无失控重扫）", len(m.Chains)))
	s.wantTrue(applies >= 1, fmt.Sprintf("风暴期至少一轮物化收口（%d 轮 committed）", applies))

	stopBySentinel(s, rh)
}

// ---- 场景 3：停靠待确认（授权关态 + 冲突必停，红线①）----

func scenarioDockAwaiting(s *wScenario) {
	dir := s.scenarioDir("s3")
	fx := s.genFixture(filepath.Join(dir, "fixture"), 0)
	p := prepPaths{project: fx.project, instance: fx.instance, data: filepath.Join(dir, "data")}
	s.runApply(p, p.data) // 授权保持默认关闭

	rh := s.startWatching(p, dir, "")
	base := lastState(s.waitActive(rh))
	commitsBefore := base.Commits

	// Part A：授权关态 → 自动链停 awaiting_confirmation。
	tA := time.Now()
	s.writeFile(filepath.Join(p.project, "config", "dock-s3.toml"), "[dock]\nscenario = 3\n")
	m := s.waitRecordCond(rh, 90*time.Second, "等待授权关态停靠链",
		func(m *watchRecMirror) bool {
			st := lastState(m)
			return anyChain(chainsAfter(m, tA, 2*time.Second), func(c chainMirror) bool {
				return c.Outcome == "no_apply_task"
			}) && st != nil && st.PendingPlanID != ""
		})
	partAPlan := lastState(m).PendingPlanID
	s.wantTrue(partAPlan != "", "pending_plan_id 就绪（停靠待确认）")
	s.wantTrue(lastState(m).WatchStatus == "active", "停靠期监听保持 active")
	s.wantTrue(lastState(m).Commits == commitsBefore, "授权关态零自动物化（提交数不变）")
	inv := eventsAfter(m, tA.Add(-2*time.Second), "relation_invalidated")
	s.wantTrue(inv >= 2, fmt.Sprintf("收口点 relation_invalidated 发射（窗口 %d 条 ≥ 2：扫描提交发点 + 停靠新发点）", inv))

	// Part B：含冲突差异 → 必停（红线①）：手工样本双侧同段不同改。
	tB := time.Now()
	handmadeRel := filepath.Join("config", "handmade.toml")
	s.editContaining(filepath.Join(p.project, handmadeRel), "master_volume = 0.8", "master_volume = 0.42")
	s.editContaining(filepath.Join(p.instance, "minecraft", handmadeRel), "master_volume = 0.8", "master_volume = 0.11")
	m = s.waitRecordCond(rh, 90*time.Second, "等待冲突停靠链",
		func(m *watchRecMirror) bool {
			st := lastState(m)
			return anyChain(chainsAfter(m, tB, 2*time.Second), func(c chainMirror) bool {
				return c.Outcome == "no_apply_task"
			}) && st != nil && st.PendingPlanID != "" && st.PendingPlanID != partAPlan
		})
	partBPlan := lastState(m).PendingPlanID
	autoAfter := chainsAfter(m, tB, 2*time.Second)
	s.wantTrue(countChain(autoAfter, func(c chainMirror) bool {
		return c.Outcome != "no_apply_task"
	}) == 0, "含冲突差异零自动执行（链面）")
	s.wantTrue(lastState(m).Commits == commitsBefore, "含冲突差异零提交（红线①：冲突永不自动）")

	// 停靠计划面冲突证据（常驻收敛后进程内只读核验）。
	stopBySentinel(s, rh)
	stack, err := bootstrap.Build(p.data)
	if err != nil {
		s.abort("冲突核验装配: %v", err)
	}
	defer stack.Close()
	wsPage, err := stack.App.ListWorkspaces(context.Background(), ports.PageRequest{Limit: 10})
	if err != nil || len(wsPage.Items) != 1 {
		s.abort("冲突核验列工作区: %v（%d 项）", err, len(wsPage.Items))
	}
	plan, err := stack.App.GetPlan(context.Background(), partBPlan)
	if err != nil {
		s.abort("读停靠计划 %s: %v", partBPlan, err)
	}
	s.wantTrue(len(plan.Conflicts) > 0, fmt.Sprintf("停靠计划含冲突行（%d 个，红线①事实面）", len(plan.Conflicts)))
}

// ---- 场景 4：连败暂停与复位（假 CDN 全场 5xx 注入 ×2）----

// dlMod 是场景 4 夹具 mod 规格（沿 pgheadless -download 链造数法：CF file-id +
// 声明 sha1；运行端缺 jar → 计划推导 write_runtime(download)）。
type dlMod struct {
	name     string
	filename string
	fileID   int64
}

func (m dlMod) metafile(tag string, content []byte) string {
	sum := sha1.Sum(content)
	return fmt.Sprintf("name = %q\nfilename = %q\nside = \"both\"\nversion = %q\n\n"+
		"[download]\nurl = \"https://media.example/%s\"\nhash-format = \"sha1\"\nhash = \"%s\"\n\n"+
		"[update.curseforge]\nproject-id = %d\nfile-id = %d\n",
		m.name, m.filename, tag, m.filename, hex.EncodeToString(sum[:]), 900201, m.fileID)
}

func scenarioFailPauseReset(s *wScenario) {
	dir := s.scenarioDir("s4")
	cdn, err := cdnproc.StartServe(s.env.pgfixtureBin, "")
	if err != nil {
		s.abort("拉起假 CDN: %v", err)
	}
	defer cdn.Close()

	mod := dlMod{name: "watch-dl-mod", filename: "watch-dl-mod-1.0.jar", fileID: 7271001}
	v1 := seededBytes("watch scenario v1 payload;", 8<<10)
	v2 := seededBytes("watch scenario v2 payload;", 8<<10)
	if err := cdn.SetFile(cdnproc.FilePath(mod.fileID, mod.filename), v1); err != nil {
		s.abort("注册假 CDN v1: %v", err)
	}

	// 夹具：项目端 pack.toml + index + metafile v1；运行端缺 jar；config 目录预建
	//（暂停期/复位期写 config 面用）。
	fx := filepath.Join(dir, "fixture")
	p := prepPaths{project: filepath.Join(fx, "project"), instance: filepath.Join(fx, "instance"),
		data: filepath.Join(dir, "data")}
	s.writeFile(filepath.Join(p.project, "pack.toml"), "name = \"WatchDL\"\nauthor = \"pgwatcher\"\nversion = \"1.0.0\"\n")
	s.writeFile(filepath.Join(p.project, "index.toml"),
		"hash-format = \"sha256\"\n\n[[files]]\nfile = \"mods/"+mod.name+".pw.toml\"\nhash = \"1\"\nmetafile = true\n")
	s.writeFile(filepath.Join(p.project, "mods", mod.name+".pw.toml"), mod.metafile("1.0.0", v1))
	s.writeFile(filepath.Join(p.instance, "instance.cfg"), "[General]\nname=\"WatchDL\"\niconKey=default\n")
	s.writeFile(filepath.Join(p.instance, "minecraft", ".keep"), "")
	s.writeFile(filepath.Join(p.project, "config", ".keep"), "")

	// 预跑：initialize 下载 v1 committed（假 CDN 健康面），授权开。
	s.runApply(p, p.data, "-cdn", cdn.URL())
	s.setAuthorized(p, p.data, true)

	rh := s.startWatching(p, dir, cdn.URL())
	base := lastState(s.waitActive(rh))
	commitsBefore := base.Commits
	metaPath := filepath.Join(p.project, "mods", mod.name+".pw.toml")
	v2Path := cdnproc.FilePath(mod.fileID, mod.filename)

	// 第一败：全场 5xx 脚本 + metafile 升 v2 → 自动链 apply 终态 failed。
	t1 := time.Now()
	if err := cdn.Script(v2Path, cdnproc.Step{Status: 503}); err != nil {
		s.abort("装 503 脚本: %v", err)
	}
	if err := cdn.SetFile(cdnproc.FilePath(mod.fileID, mod.filename), v2); err != nil {
		s.abort("注册 v2 字节: %v", err)
	}
	s.writeFile(metaPath, mod.metafile("1.0.1", v2))
	m := s.waitRecordCond(rh, 120*time.Second, "等待第一次连败链",
		func(m *watchRecMirror) bool {
			return anyChain(chainsAfter(m, t1, 2*time.Second), func(c chainMirror) bool {
				return c.Outcome == "apply_failed"
			})
		})
	s.wantTrue(lastState(m).WatchStatus == "active", "第一次失败后仍是 active（连败计数 1/2）")
	s.wantTrue(lastState(m).Commits == commitsBefore, "失败轮零提交")

	// 第二败：无新文件事实（下载失败不写盘不触发）→ 重写 metafile 再触发。
	s.writeFile(metaPath, mod.metafile("1.0.1", v2))
	m = s.waitRecordCond(rh, 120*time.Second, "等待第二次连败链与暂停",
		func(m *watchRecMirror) bool {
			st := lastState(m)
			failed := countChain(chainsAfter(m, t1, 2*time.Second), func(c chainMirror) bool {
				return c.Outcome == "apply_failed"
			})
			return failed >= 2 && st != nil && st.WatchStatus == "paused"
		})
	s.wantTrue(countChain(chainsAfter(m, t1, 2*time.Second), func(c chainMirror) bool {
		return c.Outcome == "apply_failed"
	}) == 2, "恰两次自动执行失败（×2 注入）")
	s.want(lastState(m).WatchStatus, "paused", "连败 2 次 watch_status=paused")

	// 暂停期：文件变化仍标脏但无第三次自动执行（等待窗 > 上限 10s）；监听保持。
	rounds := m.ScanRounds
	commitsPaused := lastState(m).Commits
	s.writeFile(filepath.Join(p.project, "config", "pause-s4.toml"), "[pause]\nprobe = true\n")
	s.settleSeconds(rh, 14)
	m = mustRead(s, rh)
	s.wantTrue(m.ScanRounds == rounds, "暂停期无第三次自动执行（轮数不变）")
	s.wantTrue(lastState(m).Commits == commitsPaused, "暂停期零物化（提交数不变）")
	s.want(lastState(m).WatchStatus, "paused", "暂停态保持（非 unavailable=监听未死）")

	// 假 CDN 恢复 + 手动快速更新（控制面命令文件 → transport 服务，真实复位接线）。
	if err := cdn.ClearScript(v2Path); err != nil {
		s.abort("清脚本: %v", err)
	}
	s.writeFile(filepath.Join(dir, "ctl", "quickupdate"), "manual")
	m = s.waitRecordCond(rh, 120*time.Second, "等待手动快速更新成功复位",
		func(m *watchRecMirror) bool {
			st := lastState(m)
			hasManual := false
			for _, e := range m.Timeline {
				if e.Kind == "note" && strings.Contains(e.Note, "手动快速更新收口") {
					hasManual = true
				}
			}
			return hasManual && st != nil && st.WatchStatus == "active" && st.Commits > commitsPaused
		})
	s.want(lastState(m).WatchStatus, "active", "手动快速更新成功复位 active")
	s.wantTrue(lastState(m).Commits == commitsPaused+1, "手动快速更新 committed（v2 落盘）")
	manualIdx := 0
	for _, c := range m.Chains {
		if c.Manual {
			manualIdx = c.Index
		}
	}

	// 复位后监听照常触发（标脏事实随复位消费，新一轮照常物化）。等待面按链
	// 序号 > 手动链排除手动轮（时间 margin 在手动链紧邻 t4 时会误配）。
	t4 := time.Now()
	s.writeFile(filepath.Join(p.project, "config", "post-reset-s4.toml"), "[reset]\nprobe = true\n")
	m = s.waitRecordCond(rh, 90*time.Second, "等待复位后自动链",
		func(m *watchRecMirror) bool {
			return anyChain(chainsAfter(m, t4, 2*time.Second), func(c chainMirror) bool {
				return c.Index > manualIdx && c.Outcome == "apply_succeeded"
			})
		})
	s.wantTrue(lastState(m).Commits == commitsPaused+2, "复位后自动链照常物化")

	stopBySentinel(s, rh)
}

// ---- 场景 5：恢复期只标脏 ----

func scenarioRecoveryDirtyOnly(s *wScenario) {
	dir := s.scenarioDir("s5")
	fx := s.genFixture(filepath.Join(dir, "fixture"), 0)
	p := prepPaths{project: fx.project, instance: fx.instance, data: filepath.Join(dir, "data")}
	s.runApply(p, p.data)
	s.injectRecoveryRequired(p.data)

	rh := s.startWatching(p, dir, "")
	m := s.waitRecordCond(rh, 60*time.Second, "等待恢复期挂载态",
		func(m *watchRecMirror) bool {
			st := lastState(m)
			return st != nil && st.Health == "recovery_required" && st.WatchStatus == "active"
		})
	s.wantTrue(lastState(m).Health == "recovery_required", "恢复所需关系在场（造数手术）")
	s.wantTrue(lastState(m).WatchStatus == "active", "恢复期挂载保持（watch_status=active）")

	// 触发文件变化 → 无自动物化（chainGate 恢复期守卫；等待窗 > 上限 10s）。
	rounds := m.ScanRounds
	commits := lastState(m).Commits
	t0 := time.Now()
	s.writeFile(filepath.Join(p.project, "config", "recovery-s5.toml"), "[recovery]\nprobe = true\n")
	s.settleSeconds(rh, 14)
	m = mustRead(s, rh)
	s.wantTrue(m.ScanRounds == rounds, "恢复期触发不发射自动链（只标脏）")
	s.wantTrue(lastState(m).Commits == commits, "恢复期无自动物化（提交数不变）")
	s.wantTrue(eventsAfter(m, t0, "task_updated") == 0, "恢复期零链内任务事件")
	s.wantTrue(lastState(m).WatchStatus == "active", "等待窗内挂载未漂移")

	stopBySentinel(s, rh)
}

// injectRecoveryRequired 造数手术（GC probes 同款 SQL 注入，票 #64 先例）：
// recovery_required 终态 run（终态任务不走启动恢复裁决，健康面确定性）。
func (s *wScenario) injectRecoveryRequired(dataDir string) {
	stack, err := bootstrap.Build(dataDir)
	if err != nil {
		s.abort("恢复夹具装配: %v", err)
	}
	defer stack.Close()
	db := stack.DB
	var relID, planID, planDigest string
	if err := db.QueryRow("SELECT id FROM relations ORDER BY id LIMIT 1").Scan(&relID); err != nil {
		s.abort("恢复夹具读关系: %v", err)
	}
	if err := db.QueryRow(
		"SELECT id, plan_digest FROM sync_plans WHERE relation_id=? ORDER BY rowid DESC LIMIT 1",
		relID).Scan(&planID, &planDigest); err != nil {
		s.abort("恢复夹具读计划: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	taskID := fmt.Sprintf("task_watch_rec_%d", time.Now().UnixNano())
	if _, err := db.Exec(`
INSERT INTO tasks(id, relation_id, kind, status, phase, can_cancel, message_key, created_at, updated_at)
VALUES(?, ?, 'apply', 'recovery_required', 'probe', 0, 'msg.task.apply.recovery_required', ?, ?)`,
		taskID, relID, now, now); err != nil {
		s.abort("恢复夹具插任务: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO apply_runs(task_id, relation_id, plan_id, plan_digest, relation_revision, state,
	preconditions_json, recovery_refs_json, operation_count, created_at, updated_at)
VALUES(?, ?, ?, ?, 1, 'recovery_required', '[]', '[]', 1, ?, ?)`,
		taskID, relID, planID, planDigest, now, now); err != nil {
		s.abort("恢复夹具插运行: %v", err)
	}
	if _, err := db.Exec("UPDATE relations SET health='recovery_required' WHERE id=?", relID); err != nil {
		s.abort("恢复夹具置健康: %v", err)
	}
}

// ---- 场景 6：并发 join（进程内用例面 + 其他来源任务互斥）----

func scenarioConcurrentJoin(s *wScenario) {
	dir := s.scenarioDir("s6")
	fx := s.genFixture(filepath.Join(dir, "fixture"), 0)
	p := prepPaths{project: fx.project, instance: fx.instance, data: filepath.Join(dir, "data")}
	s.runApply(p, p.data)
	s.setAuthorized(p, p.data, true)

	stack, err := bootstrap.BuildWithDownloadOptions(p.data, nil, download.Options{})
	if err != nil {
		s.abort("join 面装配: %v", err)
	}
	defer stack.Close()
	app := stack.App
	ctx := context.Background()
	page, err := app.ListWorkspaces(ctx, ports.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		s.abort("join 面列工作区: %v（%d 项）", err, len(page.Items))
	}
	relID := page.Items[0].Relation.RelationID

	// 同 relation 并发双调 → 等待并返回同一结果。扫描任务计数用前/后差值
	//（created_at 秒级精度，预跑与 join 同秒时 floor 会误计）。
	var scansBefore, scansAfter int
	if err := stack.DB.QueryRow(
		"SELECT COUNT(*) FROM tasks WHERE relation_id=? AND kind='scan'", relID).Scan(&scansBefore); err != nil {
		s.abort("join 面扫描任务基数: %v", err)
	}
	s.writeFile(filepath.Join(p.project, "config", "join-s6.toml"), "[join]\nprobe = true\n")
	type quResult struct {
		outcome, planID, taskID string
		err                     error
	}
	barrier := make(chan struct{})
	results := make(chan quResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-barrier
			v, qerr := app.QuickUpdate(ctx, view.QuickUpdateInput{RelationID: relID})
			results <- quResult{outcome: v.Outcome, planID: v.PlanID, taskID: v.ApplyTaskID, err: qerr}
		}()
	}
	close(barrier)
	r1, r2 := <-results, <-results
	s.wantTrue(r1.err == nil && r2.err == nil, "并发双调零错误")
	s.want(r2.outcome, r1.outcome, "并发双调返回同一 outcome")
	s.wantTrue(r1.taskID != "" && r1.taskID == r2.taskID, "并发双调返回同一 apply 任务（join 同结果）")
	if err := stack.DB.QueryRow(
		"SELECT COUNT(*) FROM tasks WHERE relation_id=? AND kind='scan'", relID).Scan(&scansAfter); err != nil {
		s.abort("join 面扫描任务计数: %v", err)
	}
	s.wantTrue(scansAfter-scansBefore == 1, fmt.Sprintf("并发双调只建一个扫描任务（单飞实据：+%d）", scansAfter-scansBefore))
	waitQuiet(s, app, relID, "join 链收口")

	// 其他来源活跃任务照常互斥：StartScan（非快速更新来源）在场 →
	// QuickUpdate 透传 err.scan.already_running（扫描极快时重试，至多 5 次）。
	joined := false
	for attempt := 1; attempt <= 5 && !joined; attempt++ {
		if _, err := app.StartScan(ctx, relID); err != nil {
			s.abort("互斥面 StartScan: %v", err)
		}
		_, qerr := app.QuickUpdate(ctx, view.QuickUpdateInput{RelationID: relID})
		if qerr != nil {
			s.want(errs.CodeOf(qerr), "err.scan.already_running", "其他来源活跃任务互斥（错误码透传）")
			joined = true
		}
		waitQuiet(s, app, relID, "互斥面扫描收口")
	}
	s.wantTrue(joined, "互斥探针成立（扫描在场窗口内 QuickUpdate 被拒）")

	// 事件集（红线⑤，进程内面）：该数据目录全部事件类型 ⊆ 既有两型。
	rows, err := stack.DB.Query("SELECT DISTINCT event_type FROM task_events")
	if err != nil {
		s.abort("事件集查询: %v", err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var t string
		_ = rows.Scan(&t)
		types = append(types, t)
	}
	for _, t := range types {
		if t != "task_updated" && t != "relation_invalidated" {
			s.abort("红线⑤：事件类型 %q 超出 {task_updated, relation_invalidated}", t)
		}
	}
	s.assertions = append(s.assertions,
		fmt.Sprintf("全链事件集 ⊆ {task_updated, relation_invalidated}（实测 %v）✓", types))
}

// waitQuiet 轮询直到无活跃任务（事件不是事实源，以查询 API 为准）。
func waitQuiet(s *wScenario, app interface {
	ListTasks(ctx context.Context, relationID string, active bool, page ports.PageRequest) (view.TaskPage, error)
}, relationID, label string) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		page, err := app.ListTasks(context.Background(), relationID, true, ports.PageRequest{Limit: 5})
		if err == nil && len(page.Items) == 0 {
			return
		}
		if time.Now().After(deadline) {
			s.abort("%s：等待任务收口超时", label)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---- 小工具 ----

func anyChain(ch []chainMirror, pred func(chainMirror) bool) bool {
	return firstChain(ch, pred).ScanTaskID != ""
}

func firstChain(ch []chainMirror, pred func(chainMirror) bool) chainMirror {
	for _, c := range ch {
		if pred(c) {
			return c
		}
	}
	return chainMirror{}
}

func countChain(ch []chainMirror, pred func(chainMirror) bool) int {
	n := 0
	for _, c := range ch {
		if pred(c) {
			n++
		}
	}
	return n
}

func mustParse(ts string) time.Time {
	t, err := time.Parse(watchTS, ts)
	if err != nil {
		panic(wAbort{msg: "时间戳解析 " + ts + ": " + err.Error()})
	}
	return t
}

// mustRead 读取常驻记录（失败即场景失败）。
func mustRead(s *wScenario, rh *residentHandle) *watchRecMirror {
	m, err := readWatchRecord(rh.recordPath)
	if err != nil {
		s.abort("读常驻记录: %v", err)
	}
	return m
}

// stopBySentinel 顶层包装（scenario 函数尾部统一收敛）。
func stopBySentinel(s *wScenario, rh *residentHandle) {
	rh.stop(30 * time.Second)
	m, err := readWatchRecord(rh.recordPath)
	if err != nil {
		s.abort("终态记录不可读: %v", err)
	}
	s.want(m.EndReason, "sentinel", "常驻进程经哨兵收敛退出")
	s.assertEventSet(m)
}

// seededBytes 确定性假 CDN 字节（场景内版本演进用）。
func seededBytes(seed string, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = seed[i%len(seed)]
	}
	return b
}
