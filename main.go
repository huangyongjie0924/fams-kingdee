package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
		kdClient, err := kingdee.NewClient(kingdee.Config{
			BaseURL:        cfg.Kingdee.BaseURL,
			ClientID:       cfg.Kingdee.ClientID,
			ClientSecret:   cfg.Kingdee.ClientSecret,
			Username:       cfg.Kingdee.Username,
			AccountID:      cfg.Kingdee.AccountID,
			RequestTimeout: cfg.Kingdee.RequestTimeout,
			PageSize:       cfg.Kingdee.PageSize,
			MaxRetries:     cfg.Kingdee.MaxRetries,
			QueryPath:      cfg.Kingdee.QueryPath,
		})
		if err != nil {
			log.Fatalf("创建金蝶客户端失败: %v", err)
		}
		syncSvc = syncer.NewService(st, kdClient, cfg)
		srv.SetSyncService(syncSvc)
		if cfg.Sync.Enable {
			if cfg.Sync.DryRun {
				log.Printf("提醒: 定时同步已启用但 dry_run=true，调度只会比对、不会写库")
			}
			sched := syncer.NewScheduler(syncSvc, cfg.Sync.IntervalMinutes)
			srv.SetSyncScheduler(sched)
			go sched.Run(context.Background())
		}
	}

	if cfg.Yunzhijia.AppID != "" && cfg.Yunzhijia.Secret != "" {
		srv.SetYunzhijiaClient(yunzhijia.NewClient(cfg.Yunzhijia.BaseURL, cfg.Yunzhijia.AppID, cfg.Yunzhijia.Secret, 10*time.Second))
	}

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
	if cfg.Server.TLSAddr != "" && cfg.Server.CertFile != "" && cfg.Server.KeyFile != "" {
		go func() {
			httpsSrv := &http.Server{
				Addr:              cfg.Server.TLSAddr,
				Handler:           handler,
				ReadHeaderTimeout: 10 * time.Second,
			}
			log.Printf("HTTPS 监听 %s", cfg.Server.TLSAddr)
			if err := httpsSrv.ListenAndServeTLS(cfg.Server.CertFile, cfg.Server.KeyFile); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTPS 服务退出: %v", err)
			}
		}()
	}

	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
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
