package api

import (
	"encoding/json"
	"errors"
	"io"
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
	// 组织主数据（部门 / 人员）同步：范围固定在 1201、1202 两棵子树，只做全量。
	mux.HandleFunc("POST /api/sync/org", s.requirePerm(model.PermSyncManage, s.handleSyncOrg))
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

	// —— 维修流程（见 docs/维修流程模块架构建议.md §3.3）——
	// 读接口不门控，可见范围在数据行上收窄（api/scope.go 既有约定）。
	mux.HandleFunc("POST /api/repairs", s.requirePerm(model.PermRepairReport, s.handleCreateRepair))
	mux.HandleFunc("GET /api/repairs", s.handleListRepairs)
	mux.HandleFunc("GET /api/repairs/{id}", s.handleGetRepair)
	mux.HandleFunc("GET /api/repairs/{id}/logs", s.handleRepairLogs)
	mux.HandleFunc("POST /api/repairs/{id}/accept", s.requirePerm(model.PermRepairDispatch, s.handleAcceptRepair))
	mux.HandleFunc("POST /api/repairs/{id}/reject", s.requirePerm(model.PermRepairDispatch, s.handleRejectRepair))
	mux.HandleFunc("POST /api/repairs/{id}/dispatch", s.requirePerm(model.PermRepairDispatch, s.handleDispatchRepair))
	mux.HandleFunc("POST /api/repairs/{id}/take", s.requirePerm(model.PermRepairHandle, s.handleTakeRepair))
	mux.HandleFunc("POST /api/repairs/{id}/finish", s.requirePerm(model.PermRepairHandle, s.handleFinishRepair))
	mux.HandleFunc("POST /api/repairs/{id}/confirm", s.requirePerm(model.PermRepairReport, s.handleConfirmRepair))
	mux.HandleFunc("POST /api/repairs/{id}/return", s.requirePerm(model.PermRepairReport, s.handleReturnRepair))
	mux.HandleFunc("POST /api/repairs/{id}/cancel", s.requirePerm(model.PermRepairReport, s.handleCancelRepair))
	mux.HandleFunc("POST /api/repairs/{id}/cost", s.requirePerm(model.PermRepairManage, s.handleRepairCost)) // P1-4
	mux.HandleFunc("GET /api/repairs/{id}/attachments", s.handleRepairAttachments)
	// 资产卡的维修记录：按卡查历次维修，可见范围按资产卡收窄（与履历/附件一致）
	mux.HandleFunc("GET /api/assets/{id}/repairs", s.handleCardRepairs)
	// 维修附件上传：独立路由（不放宽 /api/upload，见 api/repair_upload.go）
	mux.HandleFunc("POST /api/repair-upload", s.requireAnyPerm(s.handleRepairUpload, model.PermRepairReport, model.PermRepairHandle))
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

// readJSONOptional 与 readJSON 相同，但允许空 body（视为「无参数」）。
// 用于那些参数可选的流转动作：前端点「接单 / 确认」时可能不带请求体。
func readJSONOptional(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
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

// requireAnyPerm 与 requirePerm 相同，但命中任意一个权限即放行。
// 用于「多个角色各凭不同权限做同一件事」的接口，如维修附件上传
// （报修人凭 repair.report、维修工凭 repair.handle）。风格与 requirePerm 一致。
func (s *Server) requireAnyPerm(next http.HandlerFunc, perms ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role := userFrom(r).Role
		for _, p := range perms {
			if model.Can(role, p) {
				next(w, r)
				return
			}
		}
		writeErr(w, http.StatusForbidden, "当前角色没有此操作权限")
	}
}

func (s *Server) handleEnums(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"statuses":         model.AssetStatuses,
		"sources":          model.AssetSources,
		"fin_asset_type":   model.FinAssetTypes,
		"fin_status":       model.FinStatuses,
		"roles":            model.Roles,
		"count_results":    model.CountResults,
		"repair_statuses":  model.RepairStatuses,
		"repair_urgencies": model.RepairUrgencies,
	})
}

func trimAll(vals ...*string) {
	for _, v := range vals {
		*v = strings.TrimSpace(*v)
	}
}
