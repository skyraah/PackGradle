package main

// pgheadless -download（P3 票 #66；验收规格 §5.1）：假 CDN 注入的五场景
// 断言链（L0，零真网）。假 CDN 进程（pgfixture -serve）由本链自动拉起
//（-cdn 为空时；非空则附着外部假 CDN），脚本故障经控制面在场景间热切换。
// 场景造数法沿 #63 apply_download_test.go 头注释：项目端 metafile 携带
// [update.curseforge] file-id + [download] sha1，运行端缺 jar——计划期推导
// write_runtime(download)；假 CDN 路径与引擎同口径（directURL 整数除法不补零，
// cdnproc.FilePath 零漂移重算）。
//
//	场景①成功链：initialize 下载落盘（c1）→ 升 v2 sync（c2）→ PrepareRestore(c1)
//	      含 redownload 行（probe ok）→ exact restore committed → 两层校验
//	      （声明 sha1「取对了」+ sha256 复核「写对了」= 落盘字节逐字节一致）
//	      + 历史不改写；
//	场景②探测降标：假 CDN 404 → prepare 时点 marker=user_object_required +
//	      marker_reason=cf_unavailable、exact_infeasible（契约 06 §5）；
//	场景③failed 终局可重入：假 CDN 429×5（重试耗尽）→ confirm 后 run=failed +
//	      task=failed + Problem=err.download.rate_limited、关系健康不动（不进
//	      recovery）→ 假 CDN 恢复 → 同 plan 重 Confirm 新运行 committed；
//	场景④sync 剔除语义：两 download 行挂其一 → 剔出本场照常原子提交
//	      （commit=partial + 跳过清单带 err.download.unavailable）；再全败 →
//	      failed 终局（零提交、staging_cleared、健康不动）→ 同 plan 重 Confirm
//	      committed（ADR-0008 §7）；
//	场景⑤续传：半截断流 → .part Range 续传 → 成功；假 CDN 请求记录含
//	      Range 头（续传证据）。
//
// 断言失败即 panic 短路（dnlAbort），由 Scenario 的 recover 收为该场景失败并
// 继续后续场景（记录透明化）；链末任一场景未过即非零退出。

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/cdnproc"
	"packgradle/internal/core/model"
	"packgradle/internal/download"
)

// dnlAbort 是场景断言失败的控制流信号（Scenario recover 捕获）。
type dnlAbort struct{ msg string }

func (a dnlAbort) Error() string { return a.msg }

// dnlMod 是链内 mod 规格：filename 恒定（同名覆盖落盘），版本经 file-id 演进。
type dnlMod struct {
	name     string // metafile display name（mod 资源身份基）
	filename string // 运行端 jar 文件名（版本间不变）
	projID   int64
}

// dnlVersion 是一个版本：file-id 与 CDN 字节（声明 sha1 由字节现算）。
type dnlVersion struct {
	tag    string
	fileID int64
	bytes  []byte
}

var (
	dnlModA = dnlMod{name: "dl-mod-a", filename: "dl-mod-a-1.0.jar", projID: 900101}
	dnlModB = dnlMod{name: "dl-mod-b", filename: "dl-mod-b-1.0.jar", projID: 900102}
)

// dnlVersions 是全部版本的确定性字节（b4 放大供续传截断面）。
func dnlVersions() map[string]dnlVersion {
	mk := func(tag string, fileID int64, seed string, size int) dnlVersion {
		b := make([]byte, size)
		for i := range b {
			b[i] = seed[i%len(seed)]
		}
		return dnlVersion{tag: tag, fileID: fileID, bytes: b}
	}
	return map[string]dnlVersion{
		"a1": mk("a1", 7270801, "dl-mod-a v1 payload;", 8<<10),
		"a2": mk("a2", 7270803, "dl-mod-a v2 payload;", 8<<10),
		"a5": mk("a5", 7270805, "dl-mod-a v5 payload;", 8<<10),
		"b1": mk("b1", 7270802, "dl-mod-b v1 payload;", 8<<10),
		"b2": mk("b2", 7270806, "dl-mod-b v2 payload;", 8<<10),
		"b3": mk("b3", 7270807, "dl-mod-b v3 payload;", 8<<10),
		"b4": mk("b4", 7270808, "dl-mod-b v4 payload for resume;", 96<<10),
	}
}

// dnlMetafile 渲染 mods/<name>.pw.toml（#63 造数法：sha1 + update.curseforge）。
func dnlMetafile(m dnlMod, v dnlVersion) string {
	sum := sha1.Sum(v.bytes)
	return fmt.Sprintf("name = %q\nfilename = %q\nside = \"both\"\nversion = %q\n\n"+
		"[download]\nurl = \"https://media.example/%s\"\nhash-format = \"sha1\"\nhash = \"%s\"\n\n"+
		"[update.curseforge]\nproject-id = %d\nfile-id = %d\n",
		m.name, m.filename, v.tag, m.filename, hex.EncodeToString(sum[:]), m.projID, v.fileID)
}

// dnlChainEnv 是 -download 链的运行环境。
type dnlChainEnv struct {
	app          syncapp.Application
	projectRoot  string // 项目端绝对路径（链内先造夹具再登记关系）
	instanceDir  string // 实例目录绝对路径
	cdnFlag      string // -cdn 值（main 已保证非空：外部给定或自动拉起注入）
	managed      *cdnproc.Serve // 自动拉起的假 CDN 进程句柄（Close 归 main）
	pgfixtureBin string
	work         string // 工作目录（夹具）
	recordPath   string // p3-download 记录输出（空 = 默认路径；"-" 不落盘）
}

// runDownloadChain 执行五场景链。返回 error 即链失败（记录仍写盘）。
func runDownloadChain(env dnlChainEnv) error {
	ctx := context.Background()
	ver := dnlVersions()
	rec := &dnlRecord{Schema: "p3-download/1", Ticket: "skyraah/PackGradle#66",
		Spec:    "docs/acceptance/p3-acceptance-spec.md §5.1 五场景注入链（零真网）",
		Date:    time.Now().Format("2006-01-02"),
		Machine: newMachineInfo()}

	// ---- 假 CDN：附着 main 装配前确定的进程面（自动拉起句柄或 -cdn 外部）----
	cdn := env.managed
	external := false
	if cdn == nil {
		cdn = cdnproc.Attach(env.cdnFlag)
		external = env.cdnFlag != ""
	}
	fmt.Printf("== -download == 假 CDN：%s（%s）\n", cdn.URL(),
		map[bool]string{true: "external 附着", false: "managed 进程"}[external])
	rec.CDN.Mode = map[bool]string{true: "external", false: "managed"}[external]
	rec.CDN.BaseURL = cdn.URL()

	// 注册全部版本字节（下载面底座；场景故障由脚本步覆盖——script 优先于
	// set-file，404/429/截断脚本清除后即回落此处的正常内容）。
	for key, m := range map[string]dnlMod{"a1": dnlModA, "b1": dnlModB} {
		for _, vk := range []string{"a1", "a2", "a5", "b1", "b2", "b3", "b4"} {
			if vk[0] != key[0] {
				continue // 只注册该 mod 自己的版本
			}
			v := ver[vk]
			if err := cdn.SetFile(cdnproc.FilePath(v.fileID, m.filename), v.bytes); err != nil {
				return fmt.Errorf("假 CDN 注册 %s: %w", vk, err)
			}
		}
	}

	// ---- 夹具：项目端双 metafile、运行端缺 jar（#63 造数法）----
	// 生成先于关系登记（端点登记要求端点可读）。
	fixtureDir := filepath.Join(env.work, "fixture")
	if err := os.RemoveAll(fixtureDir); err != nil {
		return err
	}
	if err := dnlWriteFixture(fixtureDir, map[string]dnlVersion{"a1": ver["a1"], "b1": ver["b1"]}); err != nil {
		return err
	}
	rel := ensureRelation(ctx, env.app, env.projectRoot, env.instanceDir)
	d := &dnlChain{
		app:      env.app,
		rel:      rel,
		cdn:      cdn,
		ver:      ver,
		modsDir:  filepath.Join(fixtureDir, "project", "mods"),
		gameMods: filepath.Join(fixtureDir, "instance", "minecraft", "mods"),
	}

	// ---- 场景①成功链（升版回滚 restore redownload 两层校验）----
	// 造数法说明（执行面缺口的发现与回票记录见报告偏差节）：mod 的 metafile
	// 是事实源，restore 判定面对 mod 资源的 project 侧写回无内容源（基线只存
	// 语义摘要，write_project 的目标引用为空 → content_unavailable）。本链
	// 「回滚 v2→v1」走 packwiz 降版口径：升版 sync 后把 metafile 手改回 v1
	//（packwiz 是唯一事实源写者），restore 计划只剩 runtime 侧 jar 写回
	//（redownload 行）——升版回滚的用户语义不变，绕开缺口组合。
	rec.Scenario("成功链（升版回滚 restore redownload 两层校验 + 历史不改写）", "§5.1 场景1", func(s *dnlScenario) {
		// c1：initialize——mod init_choice 走 initialize_from_project →
		// write_runtime(download)，v1 字节落盘。
		c1 := d.applyRound(ctx, s, dnlInitFromProject, "initialize apply（2 download 行）")
		d.bytesEq(s, d.jar(dnlModA), ver["a1"].bytes, "c1 后 dl-mod-a = v1 字节（两层校验都过）")
		d.bytesEq(s, d.jar(dnlModB), ver["b1"].bytes, "c1 后 dl-mod-b = v1 字节")

		// c2：项目侧 mod-a 升 v2 → sync modify（download）落盘。
		d.writeMeta(s, dnlModA, ver["a2"])
		c2 := d.applyRound(ctx, s, nil, "sync v2 apply")
		d.bytesEq(s, d.jar(dnlModA), ver["a2"].bytes, "c2 后 dl-mod-a = v2 字节")

		// packwiz 降版（事实源声明写回 v1）→ restore c1：mod-a 行只剩
		// runtime 侧 jar 漂移（A2→A1；jar 不进 CAS → redownload_required；
		// probe 200 → availability=ok）。
		d.writeMeta(s, dnlModA, ver["a1"])
		draft := d.prepareRestore(ctx, s, c1, "PrepareRestore(c1)")
		it := d.restoreItem(s, draft, dnlModA)
		d.want(s, string(it.Marker), string(model.MarkerRedownloadRequired), "mod-a 判定 redownload_required（CAS miss + CF 声明齐备）")
		d.want(s, it.Availability, model.RestoreAvailabilityOK, "probe ok（假 CDN 200）")
		if it.Redownload != nil {
			d.want(s, fmt.Sprint(it.Redownload.FileID), fmt.Sprint(ver["a1"].fileID), "redownload file-id = v1")
		}
		cR := d.restoreExact(ctx, s, draft.PlanID, "restore exact")
		d.bytesEq(s, d.jar(dnlModA), ver["a1"].bytes, "restore 后 dl-mod-a = v1 字节（声明 sha1 引擎校验 + staging sha256 复核）")
		d.assertHistory(ctx, s, cR, c2, c1, "历史追加不改写（新头 kind=restore，c2/c1 原样）")
		s.Evidence = map[string]string{"c1": c1, "c2": c2, "restore": cR}
	})

	// ---- 场景②探测降标 ----
	rec.Scenario("探测降标（假 CDN 404 → user_object_required + cf_unavailable）", "§5.1 场景2", func(s *dnlScenario) {
		a1Path := cdnproc.FilePath(ver["a1"].fileID, dnlModA.filename)
		// 删运行端 jar A → restore head 的 create 行（redownload 候选）。
		d.must(os.Remove(d.jar(dnlModA)), "删运行端 jar A")
		d.script(s, a1Path, "装 404 脚本", cdnproc.Step{Status: 404})
		defer func() { _ = cdn.ClearScript(a1Path) }()
		draft := d.prepareRestore(ctx, s, d.headCommit(ctx, s), "PrepareRestore(head)")
		it := d.restoreItem(s, draft, dnlModA)
		d.want(s, string(it.Marker), string(model.MarkerUserObjectRequired), "404 降标 user_object_required")
		d.want(s, it.MarkerReason, model.MarkerReasonCFUnavailable, "marker_reason=cf_unavailable")
		d.want(s, fmt.Sprint(draft.ExactFeasible), "false", "exact 不可行")
		d.want(s, fmt.Sprint(len(draft.BlockedBy) > 0), "true", "blocked_by 携带阻塞行")
	})

	// ---- 场景③failed 终局可重入 ----
	rec.Scenario("failed 终局可重入（429 → failed 不进 recovery → 恢复 → 同 plan 重 Confirm）", "§5.1 场景3", func(s *dnlScenario) {
		a1Path := cdnproc.FilePath(ver["a1"].fileID, dnlModA.filename)
		// jar A 场景②已删且只 prepare 未执行（状态延续）；429×5：引擎首次 +
		// 4 次重试全 429 → 耗尽 → rate_limited 桶。
		if _, err := os.Stat(d.jar(dnlModA)); err == nil {
			d.must(os.Remove(d.jar(dnlModA)), "删运行端 jar A")
		}
		d.script(s, a1Path, "装 429×5 脚本", cdnproc.Step{Status: 429}, cdnproc.Step{Status: 429},
			cdnproc.Step{Status: 429}, cdnproc.Step{Status: 429}, cdnproc.Step{Status: 429})
		defer func() { _ = cdn.ClearScript(a1Path) }()
		draft := d.prepareRestore(ctx, s, d.headCommit(ctx, s), "PrepareRestore(head)（429 面新 draft）")
		it := d.restoreItem(s, draft, dnlModA)
		d.want(s, string(it.Marker), string(model.MarkerRedownloadRequired), "429=unknown 不降标（探测是辅助非承诺）")
		before := d.headCommit(ctx, s)
		planID := d.resolveRestore(ctx, s, draft.PlanID, "ResolveRestorePlan(exact)（429=unknown 乐观就绪面）")
		tv := d.confirmRestore(ctx, s, planID, "ConfirmRestorePlan（429 面）")
		final := d.waitTask(ctx, s, tv.TaskID)
		d.want(s, final.Status, model.TaskStatusFailed, "restore 任务 failed 终局")
		code := ""
		if final.Problem != nil {
			code = final.Problem.Code
		}
		d.want(s, code, download.CodeRateLimited, "Problem=err.download.rate_limited（429 重试耗尽分桶）")
		run := d.applyRun(s)
		d.want(s, run.State, model.ApplyRunFailed, "run=failed（网络失败 ≠ 恢复面）")
		ws := d.workspace(s)
		d.want(s, ws.Relation.Health, "healthy", "关系健康不动（不进 recovery_required）")
		d.want(s, d.headCommit(ctx, s), before, "failed 终局零提交（head 不变）")

		// 假 CDN 恢复（清脚本回落 200）→ 同 plan 重 Confirm 新运行 committed。
		d.clearScript(s, a1Path, "假 CDN 恢复")
		tv2 := d.confirmRestore(ctx, s, planID, "同 plan 重 Confirm（failed 可重入）")
		final2 := d.waitTask(ctx, s, tv2.TaskID)
		d.want(s, final2.Status, model.TaskStatusSucceeded, "重试运行 committed")
		d.want(s, fmt.Sprint(tv2.TaskID != tv.TaskID), "true", "新任务新运行（同 plan 重确认语义）")
		d.bytesEq(s, d.jar(dnlModA), ver["a1"].bytes, "重试后 dl-mod-a = v1 字节")
		s.Evidence = map[string]string{"failed_task": tv.TaskID, "retry_task": tv2.TaskID, "problem": download.CodeRateLimited}
	})

	// ---- 场景④sync 剔除语义（挂其一 → partial；全败 → failed）----
	rec.Scenario("sync 剔除语义与全败 failed", "§5.1 场景4", func(s *dnlScenario) {
		// 双 download 行，假 CDN 挂 mod-b → 剔出本场照常原子提交。
		d.writeMeta(s, dnlModA, ver["a5"])
		d.writeMeta(s, dnlModB, ver["b2"])
		b2Path := cdnproc.FilePath(ver["b2"].fileID, dnlModB.filename)
		d.script(s, b2Path, "挂 mod-b v2（404）", cdnproc.Step{Status: 404})
		d.applyRound(ctx, s, nil, "双行 apply（mod-b 被剔）")
		head := d.headDetail(ctx, s)
		d.want(s, head.Summary.Completeness, model.TaskOutcomePartial, "单行失败剔出本场照常提交（partial）")
		found := false
		for _, sk := range head.Skipped {
			if sk.ReasonCode == download.CodeUnavailable {
				found = true
			}
		}
		d.want(s, fmt.Sprint(found), "true", "跳过清单承载 err.download.unavailable")
		d.bytesEq(s, d.jar(dnlModA), ver["a5"].bytes, "健康行 mod-a 照常落盘 = v5 字节")
		d.bytesEq(s, d.jar(dnlModB), ver["b1"].bytes, "被剔行 mod-b 保持 v1 现状")

		// 全败：mod-b v3 单 download 行 + 404 → failed 终局。
		d.clearScript(s, b2Path, "清 mod-b v2 脚本")
		d.writeMeta(s, dnlModB, ver["b3"])
		b3Path := cdnproc.FilePath(ver["b3"].fileID, dnlModB.filename)
		d.script(s, b3Path, "挂 mod-b v3（404）", cdnproc.Step{Status: 404})
		plan := d.prepareSync(ctx, s, "全败轮 PrepareSync")
		planID := d.resolvePlan(ctx, s, plan, nil, "全败轮 ResolvePlan")
		tv := d.confirmPlan(ctx, s, planID, "全败轮 ConfirmPlan")
		final := d.waitTask(ctx, s, tv.TaskID)
		before := d.headCommit(ctx, s)
		d.want(s, final.Status, model.TaskStatusFailed, "全败 → failed 终局（零提交不进 recovery）")
		run := d.applyRun(s)
		d.want(s, run.State, model.ApplyRunFailed, "run=failed")
		d.want(s, fmt.Sprint(run.StagingCleared), "true", "failed 终局清理运行暂存（.part 不跨运行复用）")
		ws := d.workspace(s)
		d.want(s, ws.Relation.Health, "healthy", "关系健康不动")
		d.want(s, d.headCommit(ctx, s), before, "全败零提交")

		// 重试 = 同 plan 重新确认（#63 failed 可重入语义）→ committed。
		d.clearScript(s, b3Path, "假 CDN 恢复")
		tv2 := d.confirmPlan(ctx, s, planID, "全败轮同 plan 重 Confirm")
		d.waitTask(ctx, s, tv2.TaskID)
		d.want(s, d.lastTaskStatus, model.TaskStatusSucceeded, "重试 committed")
		d.bytesEq(s, d.jar(dnlModB), ver["b3"].bytes, "重试后 mod-b = v3 字节")
	})

	// ---- 场景⑤续传 ----
	rec.Scenario("续传（半截断流 → .part Range 续传 → 成功）", "§5.1 场景5", func(s *dnlScenario) {
		d.writeMeta(s, dnlModB, ver["b4"])
		b4Path := cdnproc.FilePath(ver["b4"].fileID, dnlModB.filename)
		cut := int64(len(ver["b4"].bytes) / 3)
		d.script(s, b4Path, "装半截断流脚本",
			cdnproc.Step{Body: ver["b4"].bytes, TruncateAt: int(cut)}, // 首发声明全长只发 1/3 后断流
			cdnproc.Step206(ver["b4"].bytes, cut)) // 续传请求给 206 余下部分
		d.applyRound(ctx, s, nil, "续传轮 apply")
		d.want(s, d.lastTaskStatus, model.TaskStatusSucceeded, "续传轮收口")
		d.want(s, d.lastTaskOutcome, model.TaskOutcomeExact, "续传后 exact 收口")
		d.bytesEq(s, d.jar(dnlModB), ver["b4"].bytes, "mod-b = v4 字节（.part 拼接 + sha1 验收）")
		reqs := d.requests(s)
		withRange := 0
		for _, r := range reqs {
			if r.Path == b4Path && r.Range != "" {
				withRange++
			}
		}
		d.want(s, fmt.Sprint(withRange >= 1), "true", "假 CDN 请求记录含 Range 头（续传证据）")
		s.Evidence = map[string]any{"b4_requests": len(reqs), "range_requests": withRange, "truncate_at": cut}
	})

	rec.finish()
	if err := rec.write(env.recordPath); err != nil {
		fmt.Fprintf(os.Stderr, "记录写盘失败: %v\n", err)
	}
	if !rec.Verdict.AllPass {
		return fmt.Errorf("download 链 %d 场景未全过（详见记录）", len(rec.Scenarios))
	}
	fmt.Println("== -download 链路完成 == 五场景断言全过（零真网）")
	return nil
}

// ---- 链编排辅助（断言失败 panic 短路，Scenario recover 收为场景失败） ----

// dnlChain 聚合链路依赖。
type dnlChain struct {
	app      syncapp.Application
	rel      view.RelationView
	cdn      *cdnproc.Serve
	ver      map[string]dnlVersion
	modsDir  string
	gameMods string

	// 最近一次 waitTask 的终态投影（断言消费）。
	lastTaskStatus  string
	lastTaskOutcome string
}

func (d *dnlChain) abort(format string, args ...any) {
	panic(dnlAbort{msg: fmt.Sprintf(format, args...)})
}

// want 断言字符串相等。
func (d *dnlChain) want(s *dnlScenario, got, want, label string) {
	if got != want {
		d.abort("%s：got=%q want=%q", label, got, want)
	}
	s.Assertions = append(s.Assertions, label+" ✓")
}

// bytesEq 断言文件字节一致（两层校验第二层的链路面：写对了 = 落盘字节一致）。
func (d *dnlChain) bytesEq(s *dnlScenario, path string, want []byte, label string) {
	got, err := os.ReadFile(path)
	if err != nil {
		d.abort("%s：读 %s: %v", label, path, err)
	}
	if string(got) != string(want) {
		d.abort("%s：字节不符（got %d B sha256=%s… want %d B）", label, len(got), dsha256(got), len(want))
	}
	s.Assertions = append(s.Assertions, label+" ✓")
}

func (d *dnlChain) must(err error, label string) {
	if err != nil {
		d.abort("%s: %v", label, err)
	}
}

// jar 运行端 jar 路径。
func (d *dnlChain) jar(m dnlMod) string { return filepath.Join(d.gameMods, m.filename) }

// headCommit 取最新提交 id。
func (d *dnlChain) headCommit(ctx context.Context, s *dnlScenario) string {
	return d.commitAt(ctx, s, 0)
}

// commitAt 取倒数第 n（0=最新）提交 id。
func (d *dnlChain) commitAt(ctx context.Context, s *dnlScenario, n int) string {
	page, err := d.app.ListCommits(ctx, d.rel.RelationID, ports.PageRequest{Limit: 20})
	if err != nil || len(page.Items) <= n {
		d.abort("ListCommits（取倒数第 %d）: %v（%d 行）", n, err, len(page.Items))
	}
	return page.Items[n].CommitID
}

// workspace 取工作区投影。
func (d *dnlChain) workspace(s *dnlScenario) view.WorkspaceView {
	ws, err := d.app.GetWorkspace(context.Background(), d.rel.RelationID)
	if err != nil {
		d.abort("GetWorkspace: %v", err)
	}
	return ws
}

// applyRun 取运行头。
func (d *dnlChain) applyRun(s *dnlScenario) view.ApplyRunView {
	run, err := d.app.GetApplyRun(context.Background(), d.rel.RelationID)
	if err != nil {
		d.abort("GetApplyRun: %v", err)
	}
	return run
}

// headDetail 取最新提交详情。
func (d *dnlChain) headDetail(ctx context.Context, s *dnlScenario) view.CommitView {
	id := d.headCommit(ctx, s)
	cv, err := d.app.GetCommit(ctx, d.rel.RelationID, id)
	if err != nil {
		d.abort("GetCommit(%s): %v", id, err)
	}
	return cv
}

// requests 读假 CDN 请求记录。
func (d *dnlChain) requests(s *dnlScenario) []cdnproc.Request {
	reqs, err := d.cdn.Requests()
	if err != nil {
		d.abort("读请求记录: %v", err)
	}
	return reqs
}

// script 控制面装脚本（失败即断言失败）；steps 变参置于 label 之后。
func (d *dnlChain) script(s *dnlScenario, path, label string, steps ...cdnproc.Step) {
	d.must(d.cdn.Script(path, steps...), label)
}

func (d *dnlChain) clearScript(s *dnlScenario, path, label string) {
	d.must(d.cdn.ClearScript(path), label)
}

// waitTask 轮询任务至终态（记录终态供断言）。
func (d *dnlChain) waitTask(ctx context.Context, s *dnlScenario, taskID string) view.TaskView {
	tv, err := rstWaitTask(ctx, d.app, taskID)
	if err != nil {
		d.abort("等待任务 %s: %v", taskID, err)
	}
	d.lastTaskStatus = tv.Status
	d.lastTaskOutcome = tv.Outcome
	return tv
}

// prepareSync 扫描 + PrepareSync（sync 计划）。
func (d *dnlChain) prepareSync(ctx context.Context, s *dnlScenario, label string) view.SyncPlanView {
	d.must(rstScan(ctx, d.app, d.rel.RelationID), label+"（StartScan）")
	ws, err := d.app.GetWorkspace(ctx, d.rel.RelationID)
	d.must(err, label+"（GetWorkspace）")
	plan, err := d.app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             d.rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	d.must(err, label)
	return plan
}

// prepareRestore 包装 PrepareRestore。
func (d *dnlChain) prepareRestore(ctx context.Context, s *dnlScenario, commitID, label string) view.RestorePlanView {
	d.must(rstScan(ctx, d.app, d.rel.RelationID), label+"（StartScan 预扫）")
	draft, err := d.app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: d.rel.RelationID, CommitID: commitID})
	d.must(err, label)
	return draft
}

// resolvePlan 用默认决议固化 sync 计划。
func (d *dnlChain) resolvePlan(ctx context.Context, s *dnlScenario, plan view.SyncPlanView,
	resolver func([]model.Conflict) []model.Resolution, label string) string {
	res := resolver
	if res == nil {
		res = defaultResolutions
	}
	resolved, err := d.app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: plan.PlanID, Resolutions: res(plan.Conflicts)})
	d.must(err, label)
	return resolved.PlanID
}

// applyRound 完整同步轮：扫描 → 计划 → 决议 → 确认 → 终态，返回新提交 id。
// resolver 为 nil 时用 defaultResolutions（sync 轮无冲突即空决议）。
func (d *dnlChain) applyRound(ctx context.Context, s *dnlScenario,
	resolver func([]model.Conflict) []model.Resolution, label string) string {
	plan := d.prepareSync(ctx, s, label)
	planID := d.resolvePlan(ctx, s, plan, resolver, label+"（ResolvePlan）")
	tv := d.confirmPlan(ctx, s, planID, label+"（ConfirmPlan）")
	final := d.waitTask(ctx, s, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded {
		d.abort("%s：apply 终态 %s（problem=%+v）", label, final.Status, final.Problem)
	}
	head, err := d.app.ListCommits(ctx, d.rel.RelationID, ports.PageRequest{Limit: 1})
	if err != nil || len(head.Items) == 0 {
		d.abort("%s：ListCommits: %v", label, err)
	}
	return head.Items[0].CommitID
}

// confirmPlan 包装 ConfirmPlan。
func (d *dnlChain) confirmPlan(ctx context.Context, s *dnlScenario, planID, label string) view.TaskView {
	tv, err := d.app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: planID})
	d.must(err, label)
	return tv
}

// confirmRestore 包装 ConfirmRestorePlan。
func (d *dnlChain) confirmRestore(ctx context.Context, s *dnlScenario, planID, label string) view.TaskView {
	tv, err := d.app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: planID})
	d.must(err, label)
	return tv
}

// dnlInitFromProject 是 initialize 轮决议：mod 行 initialize_from_project
// （产生 download 物化写操作；区别于 P2 离线划线的 skip）。
func dnlInitFromProject(conflicts []model.Conflict) []model.Resolution {
	out := make([]model.Resolution, 0, len(conflicts))
	for _, c := range conflicts {
		choice := model.ChoiceInitializeFromRuntime
		if c.Project != nil {
			choice = model.ChoiceInitializeFromProject
		}
		out = append(out, model.Resolution{ResourceID: c.ResourceID, Choice: choice})
	}
	return out
}

// restoreItem 查判定行（缺失即断言失败）。CF 声明 mod 的资源身份 =
// mod:curseforge:<project-id>（packwiz modIdentity：modrinth > curseforge > path）。
func (d *dnlChain) restoreItem(s *dnlScenario, p view.RestorePlanView, m dnlMod) view.RestorePlanItemView {
	id := fmt.Sprintf("mod:curseforge:%d", m.projID)
	for _, it := range p.Items {
		if string(it.ResourceID) == id {
			return it
		}
	}
	d.abort("计划缺 %s 判定行（共 %d 行）", id, len(p.Items))
	return view.RestorePlanItemView{}
}

// resolveRestore 仅决议（不确认）——failed 重入场景需要同 plan 多次确认。
func (d *dnlChain) resolveRestore(ctx context.Context, s *dnlScenario, planID, label string) string {
	resolved, err := d.app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: planID, RequestedExactness: "exact"})
	d.must(err, label)
	return resolved.PlanID
}

// restoreExact 决议 exact + 确认 + 轮询 committed，返回新提交 id。
func (d *dnlChain) restoreExact(ctx context.Context, s *dnlScenario, planID, label string) string {
	resolved, err := d.app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: planID, RequestedExactness: "exact"})
	d.must(err, label+"（ResolveRestorePlan）")
	tv := d.confirmRestore(ctx, s, resolved.PlanID, label+"（ConfirmRestorePlan）")
	final := d.waitTask(ctx, s, tv.TaskID)
	if final.Status != model.TaskStatusSucceeded || final.Outcome != model.TaskOutcomeExact {
		d.abort("%s：restore 终态 %s/%s（problem=%+v）", label, final.Status, final.Outcome, final.Problem)
	}
	return final.CommitID
}

// assertHistory 断言历史追加不改写（新头 kind=restore，原两行原样）。
func (d *dnlChain) assertHistory(ctx context.Context, s *dnlScenario, newID, first, second, label string) {
	page, err := d.app.ListCommits(ctx, d.rel.RelationID, ports.PageRequest{Limit: 20})
	d.must(err, label)
	if len(page.Items) < 3 {
		d.abort("%s：历史 %d 行 <3", label, len(page.Items))
	}
	if page.Items[0].CommitID != newID || page.Items[0].Kind != string(model.PlanRestore) {
		d.abort("%s：新头行应 kind=restore 的 %s（got %s/%s）", label, newID, page.Items[0].CommitID, page.Items[0].Kind)
	}
	if page.Items[1].CommitID != first || page.Items[2].CommitID != second {
		d.abort("%s：原历史被改写（%s/%s）", label, page.Items[1].CommitID, page.Items[2].CommitID)
	}
	s.Assertions = append(s.Assertions, label+" ✓")
}

// writeMeta 写 mod metafile（指定版本）。
func (d *dnlChain) writeMeta(s *dnlScenario, m dnlMod, v dnlVersion) {
	d.must(os.WriteFile(filepath.Join(d.modsDir, m.name+".pw.toml"), []byte(dnlMetafile(m, v)), 0o644),
		"写 "+m.name+" metafile（"+v.tag+"）")
}

// dnlWriteFixture 造夹具：project（pack.toml + index + metafiles）与 instance。
func dnlWriteFixture(dir string, versions map[string]dnlVersion) error {
	proj := filepath.Join(dir, "project")
	game := filepath.Join(dir, "instance", "minecraft")
	for path, content := range map[string]string{
		filepath.Join(proj, "pack.toml"):               "name = \"DL\"\nauthor = \"pgheadless\"\nversion = \"1.0.0\"\n",
		filepath.Join(dir, "instance", "instance.cfg"): "[General]\nname=\"DL\"\niconKey=default\n",
		filepath.Join(game, ".keep"):                   "", // 登记不变量：Prism 实例须含 minecraft/ 游戏目录
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	var index strings.Builder
	index.WriteString("hash-format = \"sha256\"\n")
	for i, key := range []string{"a1", "b1"} {
		m := dnlModA
		if key == "b1" {
			m = dnlModB
		}
		path := filepath.Join(proj, "mods", m.name+".pw.toml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(dnlMetafile(m, versions[key])), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(&index, "\n[[files]]\nfile = \"mods/%s.pw.toml\"\nhash = \"%d\"\nmetafile = true\n\n", m.name, i+1)
	}
	return os.WriteFile(filepath.Join(proj, "index.toml"), []byte(index.String()), 0o644)
}

// dsha256 字节摘要前缀（断言消息用）。
func dsha256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// ---- 记录形态（p3-download/1） ----

type dnlRecord struct {
	Schema  string      `json:"schema"`
	Ticket  string      `json:"ticket"`
	Spec    string      `json:"spec"`
	Date    string      `json:"date"`
	Machine machineInfo `json:"machine"`
	CDN     struct {
		Mode    string `json:"mode"` // managed（链自动拉起）| external（-cdn 附着）
		BaseURL string `json:"base_url"`
	} `json:"cdn"`
	Scenarios []dnlScenario `json:"scenarios"`
	Verdict   struct {
		AllPass    bool     `json:"all_pass"`
		Violations []string `json:"violations,omitempty"`
	} `json:"verdict"`
	Note string `json:"note"`
}

// dnlScenario 是单场景记录。
type dnlScenario struct {
	Name       string   `json:"name"`
	Spec       string   `json:"spec"`
	Passed     bool     `json:"passed"`
	Assertions []string `json:"assertions"`
	Evidence   any      `json:"evidence,omitempty"`
	FailedAt   string   `json:"failed_at,omitempty"`
}

// Scenario 运行单场景断言组（断言失败 panic 短路由 recover 收为场景失败，
// 不中断链——记录透明化，链末统一裁决）。
func (r *dnlRecord) Scenario(name, spec string, fn func(s *dnlScenario)) {
	s := &dnlScenario{Name: name, Spec: spec, Passed: true}
	func() {
		defer func() {
			if p := recover(); p != nil {
				if ab, ok := p.(dnlAbort); ok {
					s.Passed = false
					if s.FailedAt == "" {
						s.FailedAt = ab.msg
					}
					return
				}
				panic(p) // 非断言 panic 不吞
			}
		}()
		fn(s)
	}()
	r.Scenarios = append(r.Scenarios, *s)
	fmt.Printf("== -download 场景 == %s → passed=%v（断言 %d 条）\n", name, s.Passed, len(s.Assertions))
	for _, a := range s.Assertions {
		fmt.Println("   ✓", a)
	}
	if !s.Passed {
		fmt.Println("   ✗ FAIL:", s.FailedAt)
	}
}

func (r *dnlRecord) finish() {
	r.Verdict.AllPass = true
	for i, s := range r.Scenarios {
		if !s.Passed {
			r.Verdict.AllPass = false
			r.Verdict.Violations = append(r.Verdict.Violations,
				fmt.Sprintf("场景[%d] %s: %s", i+1, s.Name, s.FailedAt))
		}
	}
	r.Note = "五场景零真网（假 CDN 进程 pgfixture -serve 供给，脚本故障控制面热切换）；" +
		"两层校验口径 = 引擎声明 sha1（取对了）+ staging sha256 复核（写对了，链路面以落盘字节逐字节一致断言）；" +
		"续传证据 = 假 CDN 请求记录 Range 头"
}

func (r *dnlRecord) write(path string) error {
	if path == "-" {
		return nil
	}
	if path == "" {
		path = defaultDownloadRecordPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("== -download 记录 == %s\n", path)
	return nil
}

// defaultDownloadRecordPath 沿 records 先例自动命名：p3-download-<date>-<host>.json。
func defaultDownloadRecordPath() string {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return filepath.Join("docs", "acceptance", "records",
		fmt.Sprintf("p3-download-%s-%s.json", time.Now().Format("2006-01-02"), host))
}

// newMachineInfo 机器规格四元组（main.go machineInfo 复用）。
func newMachineInfo() machineInfo {
	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}
	return machineInfo{Host: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
		GoVersion: runtime.Version(), CPUs: runtime.NumCPU()}
}
