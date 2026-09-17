package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"asset-mgr/model"
)

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	state, err := s.st.GetSyncState("kingdee", "asset_card")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	latest, err := s.st.ListSyncRuns(1, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 调度器只有真的启用时才存在；未启用时给一个显式的 enabled=false，前端不必区分 null
	sched := any(map[string]any{"enabled": false})
	if s.syncSched != nil {
		sched = s.syncSched.Status()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": s.syncSvc != nil,
		"enabled":    s.cfg.Sync.Enable,
		"dry_run":    s.cfg.Sync.DryRun,
		"allow_full": s.cfg.Sync.AllowFullSync,
		"state":      state,
		"latest_run": latest,
		"scheduler":  sched,
	})
}

func (s *Server) handleSyncTestConnect(w http.ResponseWriter, r *http.Request) {
	if s.syncSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "金蝶同步未配置")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.syncSvc.TestConnect(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, "连接失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSyncRun(w http.ResponseWriter, r *http.Request) {
	if s.syncSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "金蝶同步未配置")
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = model.SyncModeIncremental
	}
	if mode != model.SyncModeIncremental && mode != model.SyncModeFull {
		writeErr(w, http.StatusBadRequest, "mode 必须是 incremental 或 full")
		return
	}
	if mode == model.SyncModeFull && !s.cfg.Sync.AllowFullSync {
		writeErr(w, http.StatusForbidden, "未开启全量同步权限")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	run, err := s.syncSvc.Run(ctx, mode, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleSyncRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	runs, err := s.st.ListSyncRuns(limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleSyncRunErrors(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	errs, err := s.st.ListSyncRunErrors(id, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, errs)
}
