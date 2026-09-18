package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"asset-mgr/model"
	"asset-mgr/store"
	"asset-mgr/syncer"
)

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	state, err := s.st.GetSyncState("kingdee", "asset_card")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 状态卡片的「最近一次运行」只关心资产卡：增量游标、定时调度都是围绕资产卡的。
	// 若把组织同步也混进来，跑完一次部门同步会让这里显示一条与游标无关的记录。
	//
	// 字段名是单数，就返回单条或 null。此前这里直接塞了一个长度为 1 的数组，
	// 前端拿到的是 []，`|| null` 兜不住空数组，会渲染出一行空白。
	latestRuns, err := s.st.ListSyncRuns(model.ResourceAssetCard, 1, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var latest any
	if len(latestRuns) > 0 {
		latest = latestRuns[0]
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

// handleSyncOrg 触发一次组织主数据（公司 / 部门 / 员工）同步。
//
// 与 handleSyncRun 分开而不是加个 target 参数：组织同步**只做全量、不认 mode**，
// 而且返回体是「范围 / 计划 / 冲突」这套完全不同的结构。合在一起会得到一个
// 一半字段恒为空的响应，前端还得靠 mode 判断该读哪些字段。
func (s *Server) handleSyncOrg(w http.ResponseWriter, r *http.Request) {
	if s.syncSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "金蝶同步未配置")
		return
	}
	// 拉两个接口、每个几十页，超时给足；与资产卡同步同量级
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	res, err := s.syncSvc.SyncOrg(ctx, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := res.Plan

	byCompany := map[string]int{}
	for _, d := range p.Departments {
		byCompany[d.CompanyCode]++
	}
	// 一人多部门是 employee.dept_id 单值化必须交代清楚的口径
	multiDept := 0
	for _, e := range p.Employees {
		if len(e.DeptCodes) > 1 {
			multiDept++
		}
	}

	conflicts := res.Conflicts
	if conflicts == nil {
		conflicts = []store.OrgConflict{}
	}
	// ConflictErr 单独给：冲突检查没跑成 与 没有冲突，对使用者的含义完全相反，
	// 不能都表现为「conflicts 是空数组」。
	conflictErr := ""
	if res.ConflictErr != nil {
		conflictErr = res.ConflictErr.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run":          res.Run,
		"dry_run":      res.DryRun,
		"created":      res.Created,
		"updated":      res.Updated,
		"conflicts":    conflicts,
		"conflict_err": conflictErr,
		"source": map[string]any{
			"dept_rows":       p.DeptRows,
			"dept_filter":     p.DeptFilter,
			"dept_in_scope":   p.DeptInScope,
			"people_rows":     p.PeopleRows,
			"people_filter":   p.PeopleFilter,
			"people_in_scope": p.PeopleInScope,
		},
		"plan": map[string]any{
			"companies":   len(p.Companies),
			"departments": len(p.Departments),
			"employees":   len(p.Employees),
			"scope_roots": syncer.OrgScopeRoots,
			"by_company":  byCompany,
			"multi_dept":  multiDept,
		},
		// 诊断用：冲突检查依赖这张表，它是空的就什么都查不出来
		"resolved_names": res.ResolvedNames(),
	})
}

func (s *Server) handleSyncRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	// ?resource=org 只看组织同步；不传则资产卡与组织混排
	runs, err := s.st.ListSyncRuns(r.URL.Query().Get("resource"), limit, offset)
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
