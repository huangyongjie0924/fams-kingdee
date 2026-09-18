package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/kingdee"
	"asset-mgr/model"
	"asset-mgr/store"
	"asset-mgr/syncer"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	modeFlag := flag.String("mode", "full", "同步模式：full 或 incremental")
	dryRun := flag.Bool("dry-run", true, "只比对不写库")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	cfg.Sync.DryRun = *dryRun

	mode := model.SyncModeFull
	switch *modeFlag {
	case "full":
	case "incremental":
		mode = model.SyncModeIncremental
	default:
		log.Fatalf("未知同步模式 %q", *modeFlag)
	}

	st, err := store.New(cfg.DSN())
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	client, err := kingdee.NewClient(cfg.KingdeeClientConfig())
	if err != nil {
		log.Fatalf("创建金蝶客户端失败: %v", err)
	}

	svc := syncer.NewService(st, client, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Println("测试金蝶连通性...")
	if err := svc.TestConnect(ctx); err != nil {
		log.Fatalf("连通性测试失败: %v", err)
	}
	log.Println("连通性 OK")

	trigger := "manual"
	if *dryRun {
		trigger = "dryrun"
	}
	log.Printf("执行 %s 同步（dry_run=%v）...", mode, *dryRun)

	run, err := svc.Run(ctx, mode, trigger)
	if err != nil {
		log.Fatalf("同步失败: %v", err)
	}
	fmt.Printf("同步结果: id=%d status=%s total=%d created=%d updated=%d skipped=%d deleted=%d failed=%d\n",
		run.ID, run.Status, run.TotalCount, run.CreatedCount, run.UpdatedCount,
		run.SkippedCount, run.DeletedCount, run.FailedCount)
	if run.ErrorSummary != "" {
		fmt.Printf("摘要: %s\n", run.ErrorSummary)
	}
}
