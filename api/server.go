package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"asset-mgr/config"
	"asset-mgr/integration/yunzhijia"
	"asset-mgr/model"
	"asset-mgr/store"
	"asset-mgr/syncer"
)

type Server struct {
	st        *store.Store
	cfg       *config.Config
	syncSvc   *syncer.Service
	syncSched *syncer.Scheduler
	yzj       *yunzhijia.Client
}

func NewServer(st *store.Store, cfg *config.Config) *Server {
	return &Server{st: st, cfg: cfg}
}

func (s *Server) SetSyncService(svc *syncer.Service) {
	s.syncSvc = svc
}

// SetSyncScheduler 仅在定时同步启用时调用；未启用时状态接口返回 enabled=false。
func (s *Server) SetSyncScheduler(sched *syncer.Scheduler) {
	s.syncSched = sched
}

// SetYunzhijiaClient 仅在云之家配置齐备时调用；未调用时 SSO 登录接口返回「未配置」。
func (s *Server) SetYunzhijiaClient(c *yunzhijia.Client) {
	s.yzj = c
}

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/sso/login", s.handleSSOLogin)
	mux.HandleFunc("GET /api/me", s.handleMe)

	mux.HandleFunc("GET /api/assets", s.handleListCards)
	mux.HandleFunc("POST /api/assets", s.requirePerm(model.PermAssetManage, s.handleCreateCard))
	mux.HandleFunc("POST /api/assets/status", s.requirePerm(model.PermAssetManage, s.handleBatchStatus))
	mux.HandleFunc("GET /api/assets/{id}", s.handleGetCard)
	mux.HandleFunc("PUT /api/assets/{id}", s.requirePerm(model.PermAssetManage, s.handleUpdateCard))
	mux.HandleFunc("DELETE /api/assets/{id}", s.requirePerm(model.PermAssetManage, s.handleDeleteCard))
	mux.HandleFunc("GET /api/assets/{id}/history", s.handleHistory)
	// 扫码/标签。by-code 必须走查询串：/api/assets/by-code/{code} 会和上面这条
	// 以及下面的 {id}/attachments 构成 ServeMux 模式冲突，注册时 panic。
	mux.HandleFunc("GET /api/assets/by-code", s.handleGetCardByCode)
	mux.HandleFunc("GET /api/assets/labels", s.handleListCardsByIDs)
	mux.HandleFunc("GET /api/assets/{id}/count-items", s.handleCardCountItems)

	mux.HandleFunc("GET /api/code-rule/next", s.handleNextCode)

	mux.HandleFunc("GET /api/enums", s.handleEnums)
	mux.HandleFunc("GET /api/categories", s.handleListCategories)
	mux.HandleFunc("POST /api/categories", s.requirePerm(model.PermMasterManage, s.handleSaveCategory))
	mux.HandleFunc("DELETE /api/categories/{id}", s.requirePerm(model.PermMasterManage, s.handleDeleteCategory))
	mux.HandleFunc("GET /api/areas", s.handleListAreas)
	mux.HandleFunc("POST /api/areas", s.requirePerm(model.PermMasterManage, s.handleSaveArea))
	mux.HandleFunc("DELETE /api/areas/{id}", s.requirePerm(model.PermMasterManage, s.handleDeleteArea))
	mux.HandleFunc("GET /api/companies", s.handleListCompanies)
	mux.HandleFunc("POST /api/companies", s.requirePerm(model.PermMasterManage, s.handleSaveCompany))
	mux.HandleFunc("DELETE /api/companies/{id}", s.requirePerm(model.PermMasterManage, s.handleDeleteCompany))
	mux.HandleFunc("GET /api/departments", s.handleListDepartments)
	mux.HandleFunc("POST /api/departments", s.requirePerm(model.PermMasterManage, s.handleSaveDepartment))
	mux.HandleFunc("DELETE /api/departments/{id}", s.requirePerm(model.PermMasterManage, s.handleDeleteDepartment))
	mux.HandleFunc("GET /api/employees", s.handleListEmployees)
	mux.HandleFunc("POST /api/employees", s.requirePerm(model.PermMasterManage, s.handleSaveEmployee))
	mux.HandleFunc("DELETE /api/employees/{id}", s.requirePerm(model.PermMasterManage, s.handleDeleteEmployee))
	mux.HandleFunc("GET /api/vendors", s.handleListVendors)
	mux.HandleFunc("POST /api/vendors", s.requirePerm(model.PermMasterManage, s.handleSaveVendor))
	mux.HandleFunc("DELETE /api/vendors/{id}", s.requirePerm(model.PermMasterManage, s.handleDeleteVendor))
	mux.HandleFunc("GET /api/tags", s.handleListTags)
	mux.HandleFunc("POST /api/tags", s.requirePerm(model.PermMasterManage, s.handleSaveTag))
	mux.HandleFunc("DELETE /api/tags/{id}", s.requirePerm(model.PermMasterManage, s.handleDeleteTag))

	mux.HandleFunc("GET /api/assets/export", s.handleExport)
	mux.HandleFunc("GET /api/assets/import-template", s.handleImportTemplate)
	mux.HandleFunc("POST /api/assets/import", s.requirePerm(model.PermAssetManage, s.handleImport))
	// 批量更新财务信息：与新增导入分开的入口。编码必须已存在，只改财务列。
	mux.HandleFunc("POST /api/assets/import-fin", s.requirePerm(model.PermAssetManage, s.handleImportFinance))

	mux.HandleFunc("POST /api/upload", s.requirePerm(model.PermAssetManage, s.handleUpload))
	mux.HandleFunc("GET /api/assets/{id}/attachments", s.handleListAttachments)
	mux.HandleFunc("DELETE /api/attachments/{id}", s.requirePerm(model.PermAssetManage, s.handleDeleteAttachment))

	mux.HandleFunc("GET /api/sync/status", s.handleSyncStatus)
	mux.HandleFunc("POST /api/sync/test-connect", s.requirePerm(model.PermSyncManage, s.handleSyncTestConnect))
	mux.HandleFunc("POST /api/sync/run", s.requirePerm(model.PermSyncManage, s.handleSyncRun))
	mux.HandleFunc("GET /api/sync/runs", s.handleSyncRuns)
	mux.HandleFunc("GET /api/sync/runs/{id}/errors", s.handleSyncRunErrors)

	mux.HandleFunc("GET /api/users", s.requirePerm(model.PermUserManage, s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.requirePerm(model.PermUserManage, s.handleCreateUser))
	mux.HandleFunc("PUT /api/users/{id}", s.requirePerm(model.PermUserManage, s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.requirePerm(model.PermUserManage, s.handleDeleteUser))

	mux.HandleFunc("GET /api/count/plans", s.handleListPlans)
	mux.HandleFunc("POST /api/count/plans", s.requirePerm(model.PermCountManage, s.handleCreatePlan))
	mux.HandleFunc("GET /api/count/plans/{id}", s.handleGetPlan)
	mux.HandleFunc("PUT /api/count/plans/{id}", s.requirePerm(model.PermCountManage, s.handleUpdatePlan))
	mux.HandleFunc("DELETE /api/count/plans/{id}", s.requirePerm(model.PermCountManage, s.handleDeletePlan))
	mux.HandleFunc("POST /api/count/plans/{id}/generate", s.requirePerm(model.PermCountManage, s.handleGeneratePlan))
	mux.HandleFunc("POST /api/count/plans/{id}/assign", s.requirePerm(model.PermCountManage, s.handleAssignItems))
	mux.HandleFunc("POST /api/count/plans/{id}/submit", s.requirePerm(model.PermCountEnter, s.handleSubmitResults))
	mux.HandleFunc("POST /api/count/plans/{id}/finish", s.requirePerm(model.PermCountManage, s.handleFinishPlan))
	mux.HandleFunc("POST /api/count/plans/{id}/cancel", s.requirePerm(model.PermCountManage, s.handleCancelPlan))
	mux.HandleFunc("GET /api/count/plans/{id}/items", s.handleListCountItems)
	mux.HandleFunc("GET /api/count/plans/{id}/report", s.handleCountReport)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			log.Printf("write json: %v", err)
		}
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("请求体解析失败：" + err.Error())
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// requirePerm 按固定角色的权限表放行，取代此前的 requireAdmin。
func (s *Server) requirePerm(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !model.Can(userFrom(r).Role, perm) {
			writeErr(w, http.StatusForbidden, "当前角色没有此操作权限")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleEnums(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"statuses":       model.AssetStatuses,
		"sources":        model.AssetSources,
		"fin_asset_type": model.FinAssetTypes,
		"fin_status":     model.FinStatuses,
		"roles":          model.Roles,
		"count_results":  model.CountResults,
	})
}

func trimAll(vals ...*string) {
	for _, v := range vals {
		*v = strings.TrimSpace(*v)
	}
}
