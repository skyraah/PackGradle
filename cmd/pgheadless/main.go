// pgheadless 是新架构 P1 只读核心的 headless 验证入口：
// 不启动 Wails，直接跑通 PrepareRelation → CreateRelation → StartScan → PrepareSync → GetPlan。
// 用法：pgheadless -project <packwiz项目根> -instance <Prism实例目录> [-data <用户数据目录>]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"packgradle/internal/application/ports"
	syncapp "packgradle/internal/application/sync"
	"packgradle/internal/application/view"
	"packgradle/internal/bootstrap"
	"packgradle/internal/core/model"
	"packgradle/internal/store"
)

func main() {
	projectRoot := flag.String("project", "", "Packwiz 项目根目录（含 pack.toml）")
	instanceDir := flag.String("instance", "", "Prism 实例目录（含 instance.cfg）")
	dataRoot := flag.String("data", "", "用户数据目录（默认系统用户数据目录下 PackGradle）")
	flag.Parse()
	if *projectRoot == "" || *instanceDir == "" {
		flag.Usage()
		os.Exit(2)
	}

	root := *dataRoot
	if root == "" {
		var err error
		root, err = store.DefaultRoot()
		if err != nil {
			log.Fatalf("定位用户数据目录失败: %v", err)
		}
	}
	stack, err := bootstrap.Build(root)
	if err != nil {
		log.Fatalf("装配失败: %v", err)
	}
	defer stack.Close()

	ctx := context.Background()
	app := stack.App

	prep, err := app.PrepareRelation(ctx, model.PrepareRelationInput{
		ProjectRoot: *projectRoot, RuntimeInstanceDir: *instanceDir,
	})
	fatalOn(err, "PrepareRelation")
	dump("PrepareRelation", prep)

	rel, err := app.CreateRelation(ctx, prep.PreparationID)
	fatalOn(err, "CreateRelation")
	dump("CreateRelation", rel)

	task, err := app.StartScan(ctx, rel.RelationID)
	fatalOn(err, "StartScan")
	fmt.Printf("StartScan -> task %s\n", task.TaskID)

	waitScan(ctx, app, rel.RelationID)

	ws, err := app.GetWorkspace(ctx, rel.RelationID)
	fatalOn(err, "GetWorkspace")
	dump("GetWorkspace", ws)

	plan, err := app.PrepareSync(ctx, view.PrepareSyncInput{
		RelationID:             rel.RelationID,
		RelationRevision:       rel.Revision,
		InputProjectSnapshotID: ws.LatestProjectSnapshot.SnapshotID,
		InputRuntimeSnapshotID: ws.LatestRuntimeSnapshot.SnapshotID,
		RequestedExactness:     "exact",
	})
	fatalOn(err, "PrepareSync")
	dump("PrepareSync", plan)

	got, err := app.GetPlan(ctx, plan.PlanID)
	fatalOn(err, "GetPlan")
	dump("GetPlan", got)
	fmt.Println("headless 全链路完成")
}

// waitScan 轮询直到无活动任务（事件不是事实源，以查询 API 为准）。
func waitScan(ctx context.Context, app syncapp.Application, relationID string) {
	for i := 0; i < 300; i++ {
		page, err := app.ListTasks(ctx, relationID, true, ports.PageRequest{Limit: 5})
		if err == nil && len(page.Items) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Fatalf("扫描任务超时未结束（relation=%s）", relationID)
}

func dump(stage string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("%s 序列化失败: %v", stage, err)
	}
	fmt.Printf("== %s ==\n%s\n", stage, b)
}

func fatalOn(err error, stage string) {
	if err != nil {
		log.Fatalf("%s 失败: %v", stage, err)
	}
}
