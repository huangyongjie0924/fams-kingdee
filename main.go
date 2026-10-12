package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"asset-mgr/api"
	"asset-mgr/config"
	"asset-mgr/integration/kingdee"
	"asset-mgr/integration/yunzhijia"
	"asset-mgr/store"
	"asset-mgr/syncer"
)

//go:embed all:web/dist
var webFS embed.FS

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	st, err := store.New(cfg.DSN())
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	if err := st.Seed(cfg.CodeRule.Prefix, cfg.CodeRule.SeqWidth); err != nil {
		log.Fatalf("写入初始数据失败: %v", err)
	}
	if err := st.EnsureAdmin(cfg.Auth.InitAdminUser, cfg.Auth.InitAdminPassword); err != nil {
		log.Fatalf("创建初始管理员失败: %v", err)
	}

	srv := api.NewServer(st, cfg)

	var syncSvc *syncer.Service
	if cfg.Kingdee.ClientID != "" {
		kdClient, err := kingdee.NewClient(cfg.KingdeeClientConfig())
		if err != nil {
			log.Fatalf("创建金蝶客户端失败: %v", err)
		}
		syncSvc = syncer.NewService(st, kdClient, cfg)
		srv.SetSyncService(syncSvc)
		if cfg.Sync.Enable {
			if cfg.Sync.DryRun {
				log.Printf("提醒: 定时同步已启用但 dry_run=true，调度只会比对、不会写库")
			}
			hour, minute, err := config.ParseDailyAt(cfg.Sync.DailyAt)
			if err != nil {
				log.Fatalf("sync.daily_at 配置有误: %v", err)
			}
			sched := syncer.NewScheduler(syncSvc, hour, minute)
			srv.SetSyncScheduler(sched)
			log.Printf("定时同步已启用：每天 %02d:%02d 先同步部门与人员，再同步资产卡",
				hour, minute)
			go sched.Run(context.Background())
		}
	}

	var yzjClient *yunzhijia.Client
	if cfg.Yunzhijia.AppID != "" && cfg.Yunzhijia.Secret != "" {
		yzjClient = yunzhijia.NewClient(cfg.Yunzhijia.BaseURL, cfg.Yunzhijia.AppID, cfg.Yunzhijia.Secret, 10*time.Second)
		srv.SetYunzhijiaClient(yzjClient)
	}

	// 维修待办通知服务：无论是否启用都构造（未启用时全部 no-op），
	// 让挂载点代码保持简单、默认行为与接入前逐字节一致（todo_enabled 默认 false）。
	notifySvc := api.NewNotifyService(st, cfg, yzjClient)
	srv.SetNotifyService(notifySvc)

	mux := http.NewServeMux()
	srv.Routes(mux)

	if err := os.MkdirAll(cfg.Server.UploadDir, 0o750); err != nil {
		log.Fatalf("创建上传目录失败: %v", err)
	}
	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/",
		http.FileServer(http.Dir(cfg.Server.UploadDir))))
	mux.Handle("GET /", spaHandler())

	handler := srv.Authenticate(mux)

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("固定资产台账系统启动，监听 %s，数据库 %s/%s",
		cfg.Server.Addr, cfg.MySQL.Host, cfg.MySQL.Database)

	// 云之家免登跳转需走 HTTPS，避免浏览器 HTTPS→HTTP 降级拦截；与 HTTP 并存。
	var httpsSrv *http.Server
	if cfg.Server.TLSAddr != "" && cfg.Server.CertFile != "" && cfg.Server.KeyFile != "" {
		httpsSrv = &http.Server{
			Addr:              cfg.Server.TLSAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			log.Printf("HTTPS 监听 %s", cfg.Server.TLSAddr)
			if err := httpsSrv.ListenAndServeTLS(cfg.Server.CertFile, cfg.Server.KeyFile); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTPS 服务退出: %v", err)
			}
		}()
	}

	// 优雅关闭（§7.4）：SIGTERM / SIGINT → 停止接收新请求 → 等在途 HTTP 完成 →
	// 停止接收新通知任务并等在途投递完成，再退出。取代此前的 ListenAndServe 裸调用。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Printf("收到退出信号，开始优雅关闭…")
	case err := <-errCh:
		log.Printf("HTTP 服务异常退出: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP 优雅关闭超时: %v", err)
	}
	if httpsSrv != nil {
		if err := httpsSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTPS 优雅关闭超时: %v", err)
		}
	}
	// 停止通知投递：不再接收新任务，等在途投递完成（带超时上限）。
	notifySvc.Stop(5 * time.Second)
	log.Printf("已退出")
}

func spaHandler() http.Handler {
	dist, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		log.Fatalf("读取前端资源失败: %v", err)
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(dist, p); err != nil {
			if filepath.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
