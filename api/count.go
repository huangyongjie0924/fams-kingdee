package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"asset-mgr/model"
	"asset-mgr/store"
)

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	plans, total, err := s.st.ListPlans(queryInt(r, "page", 1), queryInt(r, "page_size", 20))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": plans, "total": total})
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	plan, err := s.st.GetPlan(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if plan == nil {
		writeErr(w, http.StatusNotFound, "盘点计划不存在")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

type planReq struct {
	Name   string           `json:"name"`
	Scope  model.CountScope `json:"scope"`
	Remark string           `json:"remark"`
}

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	var req planReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.Name, &req.Remark)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "计划名称不能为空")
		return
	}
	id, err := s.st.CreatePlan(req.Name, req.Scope, req.Remark, operatorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	var req planReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.Name, &req.Remark)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "计划名称不能为空")
		return
	}
	if err := s.st.UpdatePlan(id, req.Name, req.Scope, req.Remark); err != nil {
		writeStoreErr(w, err, "盘点计划不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	if err := s.st.DeletePlan(id); err != nil {
		writeStoreErr(w, err, "盘点计划不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGeneratePlan 按计划范围重新生成盘点表。已录入的结果会被清掉，前端需二次确认。
func (s *Server) handleGeneratePlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	plan, err := s.st.GetPlan(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if plan == nil {
		writeErr(w, http.StatusNotFound, "盘点计划不存在")
		return
	}
	if plan.Status == model.CountPlanDone || plan.Status == model.CountPlanCancelled {
		writeErr(w, http.StatusBadRequest, "已完成的计划不能重新生成盘点表")
		return
	}
	n, err := s.st.GenerateItems(id, plan.Scope)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
}

type assignReq struct {
	ItemIDs    []int64 `json:"item_ids"`
	AssigneeID int64   `json:"assignee_id"`
}

func (s *Server) handleAssignItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	var req assignReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.ItemIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "没有选中任何资产")
		return
	}
	name := ""
	if req.AssigneeID > 0 {
		name, err = s.st.EmployeeName(req.AssigneeID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if name == "" {
			writeErr(w, http.StatusBadRequest, "指定的盘点人不存在")
			return
		}
	}
	if err := s.st.AssignItems(id, req.ItemIDs, req.AssigneeID, name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type submitReq struct {
	Items []store.CountResultInput `json:"items"`
}

func (s *Server) handleSubmitResults(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	var req submitReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "没有需要提交的盘点结果")
		return
	}

	me := userFrom(r)
	for _, it := range req.Items {
		if err := validCountResult(it.Result); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// 盘点员只能录自己名下的行；管理员/资产管理员可代录任意行
	restrict := int64(0)
	if me.Role == model.RoleCounter {
		if me.EmployeeID == 0 {
			writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法盘点")
			return
		}
		restrict = me.EmployeeID
	}

	n, err := s.st.SubmitResults(id, restrict, operatorOf(r), req.Items)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, http.StatusForbidden, "没有可提交的盘点记录（可能未指派给你）")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": n})
}

func validCountResult(v string) error {
	if v == "" {
		return nil
	}
	for _, r := range model.CountResults {
		if r == v {
			return nil
		}
	}
	return errors.New("盘点结果无效：" + v)
}

func (s *Server) handleFinishPlan(w http.ResponseWriter, r *http.Request) {
	s.planAction(w, r, s.st.FinishPlan)
}

func (s *Server) handleCancelPlan(w http.ResponseWriter, r *http.Request) {
	s.planAction(w, r, s.st.CancelPlan)
}

func (s *Server) planAction(w http.ResponseWriter, r *http.Request, fn func(int64) error) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	if err := fn(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleListCountItems 返回盘点明细。盘点员强制只看自己名下的行；其他人可用 mine=1 收窄。
func (s *Server) handleListCountItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	me := userFrom(r)
	q := store.CountItemQuery{
		Result:  r.URL.Query().Get("result"),
		Keyword: r.URL.Query().Get("keyword"),
	}
	if r.URL.Query().Get("mine") == "1" || me.Role == model.RoleCounter {
		if me.EmployeeID == 0 {
			writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法盘点")
			return
		}
		q.AssigneeID = me.EmployeeID
	}
	items, err := s.st.ListItems(id, q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCountReport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	report, err := s.st.Report(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if report == nil {
		writeErr(w, http.StatusNotFound, "盘点计划不存在")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// writeStoreErr 把 store 层的 sql.ErrNoRows 翻成 404，其余按 400 返回业务错误。
func writeStoreErr(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, notFoundMsg)
		return
	}
	writeErr(w, http.StatusBadRequest, err.Error())
}
