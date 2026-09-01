package main

// pgheadless -restore-target（P3 票 #66；验收规格 §4）：pgrecovery -mode
// restore 的强杀目标子进程。本进程对 harness 生成的空夹具骨架（双侧一致的
// 120 个受管文本 + 6 个 CF metafile）自注册假 CDN 字节（-cdn 控制面——字节
// 生成与 metafile 声明同源，消除 harness/子进程两端漂移），并负责：
//
//	①历史夹具（提交链为空时）：c1 = initialize（文本双侧一致入基线；mod 行
//	  skip——离线面不物化，基线随复扫收录 runtime absent 现实）→ rtDrift 落
//	  漂移（40 文本 runtime 侧 v2 + 3 个 mod metafile 升 v2）→ c2 apply
//	  （文本 copy 传播，project 侧 v1 经 before 保全进 CAS——restore 写回的
//	  内容源）；
//	②scan → PrepareRestore(最老提交 c1) → 40 文本双侧 CAS 写回 + 3 mod
//	  create 行 redownload（probe 假 CDN ok）→ resolve exact →
//	  打印 `== restore-chain ==`（提交链，harness R5b 历史不改写断言的数据面）
//	  与 `== ConfirmRestorePlan ==`（killwindow 击杀窗口的 armed 信号）→ 确认；
//	③轮询打印 phase=staging/applying/verifying（killwindow 相位观察面）→
//	  终态 committed 断言（diff 归零 + 关系复位 healthy）→ 退出码 0。
//
// 复跑（AcknowledgeRecovery 收口后）：同一命令——提交链非空即跳过①，直接
// ②③建新运行（恢复收口后 restore 可重入的出口）。armed 之前的相位标记属于
// 夹具建立期 apply，killwindow 忽略。

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/cdnproc"
	"packgradle/internal/core/model"
)

// restore 夹具参数（相位窗口与 P2 recovery 同规模：120 文本展宽 staging；
// mod 字节 1MB 加宽下载相位）。
const (
	rtTextFiles   = 120 // 受管文本数（双侧一致入基线）
	rtDriftFiles  = 40  // 漂移改写文本数（restore 写回工作量）
	rtMods        = 6   // CF 声明 mod 数（3 个升版作 redownload 行）
	rtBytesSize   = 1 << 20
	rtFirstFileID = 7270901 // v1 fileID 起点；v2 = +10（直链分段同公式）
)

// rtModBytes 确定性 mod 字节（tag 区分版本）。
func rtModBytes(tag string, i int) []byte {
	b := make([]byte, rtBytesSize)
	seed := fmt.Sprintf("restore-target %s mod-%02d;", tag, i)
	for k := range b {
		b[k] = seed[k%len(seed)]
	}
	return b
}

func rtSha1(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

func rtSha256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// rtModMeta 渲染 CF 声明 metafile（#63 造数法同源：sha1 + file-id）。
func rtModMeta(i int, fileID int64, bytes []byte) string {
	return fmt.Sprintf("name = \"rt-mod-%02d\"\nfilename = \"rt-mod-%02d-1.0.jar\"\nside = \"both\"\n\n"+
		"[download]\nurl = \"https://media.example/rt-%02d.jar\"\nhash-format = \"sha1\"\nhash = \"%s\"\n\n"+
		"[update.curseforge]\nproject-id = %d\nfile-id = %d\n",
		i, i, i, rtSha1(bytes), 910000+i, fileID)
}

// rtTextContent 确定性文本内容（版本号进内容，漂移/写回逐字节可推导）。
func rtTextContent(i int, v int) string {
	line := fmt.Sprintf("restore-target kill fixture file %03d version %d;", i, v)
	return strings.Repeat(line+"\n", (1<<10)/len(line)+1)[:1<<10]
}

// rtWriteFixture 造夹具骨架：双侧一致的 120 文本 + 6 个 CF metafile（v1）+
// index + pack.toml/instance.cfg。返回项目根与实例目录。
func rtWriteFixture(dir string) (projectRoot, instanceDir string, err error) {
	projectRoot = filepath.Join(dir, "project")
	instanceDir = filepath.Join(dir, "instance")
	gameDir := filepath.Join(instanceDir, "minecraft")

	write := func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
	if err := write(filepath.Join(projectRoot, "pack.toml"),
		"name = \"RT\"\nauthor = \"pgrecovery\"\nversion = \"1.0.0\"\n"); err != nil {
		return "", "", err
	}
	if err := write(filepath.Join(instanceDir, "instance.cfg"),
		"[General]\nname=\"RT\"\niconKey=default\n"); err != nil {
		return "", "", err
	}
	if err := write(filepath.Join(gameDir, ".keep"), ""); err != nil {
		return "", "", err
	}
	for i := 0; i < rtTextFiles; i++ {
		rel := filepath.Join("config", "kill", fmt.Sprintf("kill-%03d.toml", i))
		content := rtTextContent(i, 1)
		if err := write(filepath.Join(projectRoot, rel), content); err != nil {
			return "", "", err
		}
		if err := write(filepath.Join(gameDir, rel), content); err != nil {
			return "", "", err
		}
	}
	var index strings.Builder
	index.WriteString("hash-format = \"sha256\"\n")
	for i := 0; i < rtMods; i++ {
		meta := rtModMeta(i, rtFirstFileID+int64(i), rtModBytes("v1", i))
		if err := write(filepath.Join(projectRoot, "mods", fmt.Sprintf("rt-mod-%02d.pw.toml", i)), meta); err != nil {
			return "", "", err
		}
		fmt.Fprintf(&index, "\n[[files]]\nfile = \"mods/rt-mod-%02d.pw.toml\"\nhash = \"%d\"\nmetafile = true\n\n", i, i+1)
	}
	for i := 0; i < rtTextFiles; i++ {
		fmt.Fprintf(&index, "\n[[files]]\nfile = \"config/kill/kill-%03d.toml\"\nhash = \"%d\"\n\n", i, 100+i)
	}
	if err := write(filepath.Join(projectRoot, "index.toml"), index.String()); err != nil {
		return "", "", err
	}
	return projectRoot, instanceDir, nil
}

// rtDriftTexts 落文本漂移（c2 前奏）：40 文本 runtime 侧改写（v2）。
func rtDriftTexts(instanceDir string) error {
	gameDir := filepath.Join(instanceDir, "minecraft")
	for i := 0; i < rtDriftFiles; i++ {
		rel := filepath.Join("config", "kill", fmt.Sprintf("kill-%03d.toml", i))
		if err := os.MkdirAll(filepath.Dir(filepath.Join(gameDir, rel)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(gameDir, rel), []byte(rtTextContent(i, 2)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// rtDriftJars 删 3 个运行端 jar（restore 前的外部漂移；prepare 快照观察缺失
// → mod 行 create(redownload)——staging 下载相位的真下载量来源）。容忍缺失
// （复跑时 jar 可能已被上次运行删除/恢复补偿未重装——缺失即漂移已就位）。
func rtDriftJars(instanceDir string) error {
	gameDir := filepath.Join(instanceDir, "minecraft")
	for i := 0; i < 3; i++ {
		if err := os.Remove(filepath.Join(gameDir, "mods", fmt.Sprintf("rt-mod-%02d-1.0.jar", i))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// rtRegisterCDN 经控制面注册 mod 字节（v1 全 6 个 + v2 前 3 个）。子进程内
// 调用——字节与 metafile 声明同源，harness 零漂移。
func rtRegisterCDN(cdn *cdnproc.Serve) error {
	for i := 0; i < rtMods; i++ {
		v1 := rtModBytes("v1", i)
		if err := cdn.SetFile(cdnproc.FilePath(rtFirstFileID+int64(i), fmt.Sprintf("rt-mod-%02d-1.0.jar", i)), v1); err != nil {
			return err
		}
	}
	return nil
}

// runRestoreTarget 执行 -restore-target 链：夹具骨架生成（首次）→ 关系登记 →
// 历史夹具 → restore 运行 → committed 断言。cdn 为装配面已确定的假 CDN（自动
// 拉起句柄或 -cdn 附着；nil = 无下载面——redownload 行将 failed，仅供离线调试）。
// 任一断言不符即 error（main 非零退出）。
func runRestoreTarget(ctx context.Context, app syncapp.Application, projectAbs, instanceAbs string, cdn *cdnproc.Serve) error {
	// 夹具骨架先于关系登记（端点登记要求端点可读）；-project 指向 <fixture>/project。
	if err := os.MkdirAll(filepath.Dir(projectAbs), 0o755); err != nil {
		return err
	}
	if _, _, err := rtWriteFixture(filepath.Dir(projectAbs)); err != nil {
		return fmt.Errorf("生成夹具骨架: %w", err)
	}
	rel := ensureRelation(ctx, app, projectAbs, instanceAbs)
	return runRestoreTargetRel(ctx, app, rel, projectAbs, instanceAbs, cdn)
}

// runRestoreTargetRel 是已登记关系下的链主体（见包注释 ①②③）。
func runRestoreTargetRel(ctx context.Context, app syncapp.Application, rel view.RelationView,
	projectRoot, instanceDir string, cdn *cdnproc.Serve) error {

	// ---- ①历史夹具（提交链为空时）：c1 initialize + 漂移 + c2 ----
	commits, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 50})
	if err != nil {
		return err
	}
	if len(commits.Items) == 0 {
		if cdn != nil {
			if err := rtRegisterCDN(cdn); err != nil {
				return fmt.Errorf("注册假 CDN 字节: %w", err)
			}
		}
		fmt.Println("== restore-target == 建历史夹具（c1 initialize → 漂移 → c2）")
		// c1：mod 行 initialize_from_project——jar 经假 CDN 下载落盘入基线
		//（restore 的 redownload/create 行由此有 runtime 侧写回目标）。
		if _, err := rtApplyRound(ctx, app, rel, dnlInitFromProject, "c1 initialize"); err != nil {
			return err
		}
		if err := rtDriftTexts(instanceDir); err != nil {
			return fmt.Errorf("落漂移: %w", err)
		}
		// c2：文本漂移轮（copy 传播；mod 无漂移不进计划——jar/metafile 任何
		// 一侧变动都会触发 sync 侧 mod 边界面，验收 harness 不依赖它）。
		if _, err := rtApplyRound(ctx, app, rel, applyResolutions, "c2 漂移 apply"); err != nil {
			return err
		}
		commits, err = app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 50})
		if err != nil {
			return err
		}
	}

	// restore 前的最后一击：删 3 个运行端 jar（外部漂移，不经 sync——
	// prepare 快照直接观察缺失 → mod 行 create(write_runtime redownload)，
	// restore 的 staging 下载相位由此有真下载量；metafile 恒 v1 无 project 侧
	// 写回）。
	if err := rtDriftJars(instanceDir); err != nil {
		return fmt.Errorf("删运行端 jar: %w", err)
	}

	// ---- ②restore 到最老提交（c1）----
	target := commits.Items[len(commits.Items)-1].CommitID
	chain := make([]string, 0, len(commits.Items))
	for i := len(commits.Items) - 1; i >= 0; i-- {
		chain = append(chain, commits.Items[i].CommitID)
	}
	fmt.Printf("== restore-target == 目标=%s 链=[%s]\n", target, strings.Join(chain, ","))

	// 重扫刷新双端快照（外部漂移——删 jar——经扫描进入 prepare 输入）。
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return fmt.Errorf("restore 前重扫: %w", err)
	}
	draft, err := app.PrepareRestore(ctx, view.PrepareRestoreInput{RelationID: rel.RelationID, CommitID: target})
	if err != nil {
		return fmt.Errorf("PrepareRestore(%s): %w", target, err)
	}
	redownloadRows := 0
	for i := range draft.Items {
		if draft.Items[i].Marker == model.MarkerRedownloadRequired {
			redownloadRows++
		}
	}
	fmt.Printf("== restore-target == 计划=%s 行=%d redownload=%d exact_feasible=%v\n",
		draft.PlanID, len(draft.Items), redownloadRows, draft.ExactFeasible)
	if len(draft.Items) == 0 || redownloadRows == 0 {
		return fmt.Errorf("restore 计划缺写回/redownload 行（行=%d redownload=%d）——夹具漂移缺失", len(draft.Items), redownloadRows)
	}
	resolved, err := app.ResolveRestorePlan(ctx, view.ResolveRestorePlanInput{PlanID: draft.PlanID, RequestedExactness: "exact"})
	if err != nil {
		return fmt.Errorf("ResolveRestorePlan: %w", err)
	}
	// armed 行：提交链（R5b 历史不改写断言的数据面）与确认标记（killwindow
	// 的 armed 信号——此行之前的相位标记属夹具建立期 apply，观察面忽略）。
	fmt.Printf("== restore-chain == %s\n", strings.Join(chain, ","))
	fmt.Println("== ConfirmRestorePlan ==")
	tv, err := app.ConfirmRestorePlan(ctx, view.ConfirmRestorePlanInput{PlanID: resolved.PlanID})
	if err != nil {
		return fmt.Errorf("ConfirmRestorePlan: %w", err)
	}

	// ---- ③轮询至终态（相位标记 = killwindow 观察面；内存采样随轮询）----
	mem := beginMemPeakSample()
	final, err := waitApplyTask(ctx, app, tv.TaskID, mem, applyPollBaseTimeout+2*time.Minute)
	if err != nil {
		return err
	}
	mem.sample()
	if final.Status != model.TaskStatusSucceeded || final.Outcome != model.TaskOutcomeExact {
		dumpApplyFailure(ctx, app, rel.RelationID, final)
		return fmt.Errorf("restore 任务终态 %s/%s（期望 succeeded/exact）problem=%s",
			final.Status, final.Outcome, problemText(final.Problem))
	}

	// ---- 终态断言：diff 归零 + 关系复位 healthy ----
	changes, err := app.GetChanges(ctx, view.GetChangesInput{RelationID: rel.RelationID})
	if err != nil {
		return err
	}
	if s := changes.Summary; s.CreateCount != 0 || s.ModifyCount != 0 || s.DeleteCount != 0 ||
		s.ConflictCount != 0 || s.InitChoiceCount != 0 {
		return fmt.Errorf("restore 收口后 diff 未归零: %+v", s)
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return err
	}
	if ws.Relation.Health != "healthy" {
		return fmt.Errorf("restore 后关系健康=%s，期望 healthy", ws.Relation.Health)
	}
	timing := lastApplyTiming(app)
	fmt.Printf("== restore-target == committed %s exact（staging=%dms applying=%dms verifying=%dms total=%dms）\n",
		final.CommitID, timing.StagingMs, timing.ApplyingMs, timing.VerifyingMs, timing.TotalMs)
	return nil
}

// rtApplyRound 跑一轮完整同步链（scan → plan → resolver 决议 → confirm →
// 终态）。c1 传 dnlInitFromProject（mod 下载落盘入基线），c2 传
// applyResolutions（mod skip——删 jar 漂移不重装，基线收录 absent）。
func rtApplyRound(ctx context.Context, app syncapp.Application, rel view.RelationView,
	resolver func([]model.Conflict) []model.Resolution, label string) (string, error) {
	if err := rstScan(ctx, app, rel.RelationID); err != nil {
		return "", fmt.Errorf("%s（scan）: %w", label, err)
	}
	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	if err != nil {
		return "", fmt.Errorf("%s（GetWorkspace）: %w", label, err)
	}
	draft, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       ws.State.RelationRevision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	if err != nil {
		return "", fmt.Errorf("%s（PrepareSync）: %w", label, err)
	}
	resolved, err := app.ResolvePlan(ctx, view.ResolvePlanInput{PlanID: draft.PlanID, Resolutions: resolver(draft.Conflicts)})
	if err != nil {
		return "", fmt.Errorf("%s（ResolvePlan）: %w", label, err)
	}
	tv, err := app.ConfirmPlan(ctx, view.ConfirmPlanInput{PlanID: resolved.PlanID})
	if err != nil {
		return "", fmt.Errorf("%s（ConfirmPlan）: %w", label, err)
	}
	final, err := rstWaitTask(ctx, app, tv.TaskID)
	if err != nil {
		return "", err
	}
	if final.Status != model.TaskStatusSucceeded {
		return "", fmt.Errorf("%s：apply 终态 %s（problem=%+v）", label, final.Status, final.Problem)
	}
	head, err := app.ListCommits(ctx, rel.RelationID, ports.PageRequest{Limit: 1})
	if err != nil || len(head.Items) == 0 {
		return "", fmt.Errorf("%s（ListCommits）: %w", label, err)
	}
	return head.Items[0].CommitID, nil
}
