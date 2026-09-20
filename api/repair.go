package api

import (
	"fmt"
	"net/http"
	"strings"

	"asset-mgr/model"
	"asset-mgr/store"
)

// —— 对象级鉴权小工具 ——
//
// 路由上的 requirePerm 只回答「这个角色能不能做这类动作」；能不能动**这一张单**
// 还得看当事人（报修人 / 被指派维修工）。这一层堵住 IDOR。

func isRepairReporter(u model.User, o *model.RepairOrder) bool {
	return u.EmployeeID > 0 && u.EmployeeID == o.ReporterEmpID
}

func isRepairAssignee(u model.User, o *model.RepairOrder) bool {
	return u.EmployeeID > 0 && u.EmployeeID == o.AssigneeEmpID
}

// canManageRepair：admin 恒真；其余看 repair.dispatch（asset_manager）。
func canManageRepair(role string) bool {
	return model.Can(role, model.PermRepairDispatch)
}

// parseRepairQuery 解析维修单列表的查询串。
func parseRepairQuery(r *http.Request) model.RepairListQuery {
	v := r.URL.Query()
	q := model.RepairListQuery{
		UseDeptID: atoi64(v.Get("use_dept_id")),
		CardID:    atoi64(v.Get("card_id")),
		AssetCode: strings.TrimSpace(v.Get("asset_code")),
		Keyword:   strings.TrimSpace(v.Get("keyword")),
		Page:      int(atoi64(v.Get("page"))),
		PageSize:  int(atoi64(v.Get("page_size"))),
		From:      dayStart(v.Get("from")),
		To:        dayEnd(v.Get("to")),
	}
	if raw := v["status"]; len(raw) > 0 {
		for _, s := range raw {
			for _, one := range strings.Split(s, ",") {
				if one = strings.TrimSpace(one); one != "" {
					q.Status = append(q.Status, one)
				}
			}
		}
	}
	return q
}

// dayStart / dayEnd 把 "YYYY-MM-DD" 补成当天的起止时刻。
// 不补的话 "created_at <= 2026-01-02" 会把当天 00:00 之后的行全漏掉。
func dayStart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) == 10 {
		return s + " 00:00:00"
	}
	return s
}

func dayEnd(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) == 10 {
		return s + " 23:59:59"
	}
	return s
}

// handleCreateRepair 员工提交报修。报修人身份取自登录态（JWT eid），不问「你是谁」。
func (s *Server) handleCreateRepair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CardID        int64   `json:"card_id"`
		FaultDesc     string  `json:"fault_desc"`
		Urgency       string  `json:"urgency"`
		AttachmentIDs []int64 `json:"attachment_ids"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.FaultDesc, &req.Urgency)
	if req.CardID <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少资产")
		return
	}
	if req.FaultDesc == "" {
		writeErr(w, http.StatusBadRequest, "问题描述必填")
		return
	}
	if req.Urgency == "" {
		req.Urgency = model.RepairUrgencyNormal
	}
	if !model.IsValidRepairUrgency(req.Urgency) {
		writeErr(w, http.StatusBadRequest, "紧急程度取值非法："+req.Urgency)
		return
	}

	me := userFrom(r)
	if me.EmployeeID == 0 {
		writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法报修")
		return
	}
	card, err := s.st.GetCard(req.CardID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询资产失败")
		return
	}
	if card == nil {
		writeErr(w, http.StatusNotFound, "资产不存在")
		return
	}

	// —— 不可维修分类拦截（三层防线的底线）——
	// 只有设备类（分类被管理员打了「可维修」标签）的资产才能报修。判定用 cardSelect
	// 带出的派生字段 CategoryRepairable，不信前端传来的任何标记：前端隐藏按钮只是体验，
	// 后端独立拦住才是底线（前端可被绕过）。COALESCE 保证悬空分类 / category_id=0 时为 false。
	if !card.CategoryRepairable {
		// 分类名为空是独立且更常见的坏数据情形（category_id=0 的哨兵值，或指向已删除分类的
		// 悬空引用，LEFT JOIN 未匹配 → CategoryName 为空串）。此时若照常拼接会得到「」空引号，
		// 员工看不出问题在哪、也不知道该找谁，所以单独给一句说明真正原因与下一步的文案。
		if card.CategoryName == "" {
			writeErr(w, http.StatusBadRequest,
				"该资产未设置资产类别，不支持报修，请先联系资产管理员补全分类")
			return
		}
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("「%s」类资产不支持报修，如需维修请联系资产管理员", card.CategoryName))
		return
	}

	order, err := s.st.CreateRepair(card, store.RepairCreateInput{
		ReporterEmpID: me.EmployeeID,
		ReporterName:  reporterName(me),
		FaultDesc:     req.FaultDesc,
		Urgency:       req.Urgency,
		CreatedBy:     operatorOf(r),
		AttachmentIDs: req.AttachmentIDs,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "提交报修失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, order)
}

// reporterName 报修人显示名：优先姓名，其次用户名。
func reporterName(u model.User) string {
	if u.RealName != "" {
		return u.RealName
	}
	return u.Username
}

// handleListRepairs 维修单列表。读接口不门控，靠 repairScope 在数据行上收窄。
func (s *Server) handleListRepairs(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.repairScope(w, r)
	if !ok {
		return
	}
	// mine=1：强制只看「与我相关」。维修工 = 派给我的；其余 = 我提交的。
	if r.URL.Query().Get("mine") == "1" {
		me := userFrom(r)
		if me.EmployeeID == 0 {
			writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法查看我的单据")
			return
		}
		if me.Role == model.RoleRepairTech {
			sc = model.RepairScope{AssigneeEmpID: me.EmployeeID}
		} else {
			sc = model.RepairScope{ReporterEmpID: me.EmployeeID}
		}
	}
	res, err := s.st.ListRepairs(parseRepairQuery(r), sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询维修单失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// loadRepairInScope 取单张维修单并校验可见范围。校验失败时已写响应并返回 ok=false。
func (s *Server) loadRepairInScope(w http.ResponseWriter, r *http.Request) (*model.RepairOrder, bool) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return nil, false
	}
	o, err := s.st.GetRepair(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询维修单失败")
		return nil, false
	}
	if o == nil {
		writeErr(w, http.StatusNotFound, "维修单不存在")
		return nil, false
	}
	sc, ok := s.repairScope(w, r)
	if !ok {
		return nil, false
	}
	if denyRepairOutOfScope(w, o, sc) {
		return nil, false
	}
	return o, true
}

func (s *Server) handleGetRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairInScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleRepairLogs(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairInScope(w, r)
	if !ok {
		return
	}
	logs, err := s.st.RepairLogs(o.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询流转记录失败")
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (s *Server) handleRepairAttachments(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairInScope(w, r)
	if !ok {
		return
	}
	list, err := s.st.ListRepairAttachments(o.ID)
	s.listJSON(w, list, err, "维修附件")
}

// handleCardRepairs 一台资产的历次维修记录（挂在资产卡上）。可见范围按资产卡收窄。
func (s *Server) handleCardRepairs(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return
	}
	if !s.checkCardScope(w, r, id) {
		return
	}
	list, err := s.st.ListRepairsByCard(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询维修记录失败")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// repairActionReq 是状态流转动作的统一请求体。不同 action 只用其中部分字段，
// 前端也只发相关字段（readJSONOptional 允许空 body）。
type repairActionReq struct {
	Remark        string  `json:"remark"`
	AssigneeEmpID int64   `json:"assignee_emp_id"`
	VendorID      int64   `json:"vendor_id"`
	HandlerDesc   string  `json:"handler_desc"`
	AttachmentIDs []int64 `json:"attachment_ids"`
}

// doRepairTransition 统一走 store.Transition 单一入口，禁止在 api 层散落 UPDATE。
func (s *Server) doRepairTransition(w http.ResponseWriter, r *http.Request, o *model.RepairOrder, action string, in store.RepairTransitionInput) {
	in.Action = action
	in.Operator = operatorOf(r)
	updated, err := s.st.Transition(o.ID, in)
	if err != nil {
		writeStoreErr(w, err, "维修单不存在")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// loadRepairForAction 取单并做「必须存在」检查（对象级鉴权由各 handler 按动作判定）。
func (s *Server) loadRepairForAction(w http.ResponseWriter, r *http.Request) (*model.RepairOrder, bool) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 非法")
		return nil, false
	}
	o, err := s.st.GetRepair(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询维修单失败")
		return nil, false
	}
	if o == nil {
		writeErr(w, http.StatusNotFound, "维修单不存在")
		return nil, false
	}
	return o, true
}

func (s *Server) handleAcceptRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	var req repairActionReq
	if err := readJSONOptional(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.doRepairTransition(w, r, o, model.RepairActionAccept, store.RepairTransitionInput{Remark: req.Remark})
}

func (s *Server) handleRejectRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	var req repairActionReq
	if err := readJSONOptional(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.Remark)
	if req.Remark == "" {
		writeErr(w, http.StatusBadRequest, "驳回必须填写原因")
		return
	}
	s.doRepairTransition(w, r, o, model.RepairActionReject, store.RepairTransitionInput{Remark: req.Remark})
}

func (s *Server) handleDispatchRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	var req repairActionReq
	if err := readJSONOptional(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AssigneeEmpID <= 0 && req.VendorID <= 0 {
		writeErr(w, http.StatusBadRequest, "请指派内部维修工或外部维保供应商")
		return
	}
	assigneeName := ""
	if req.AssigneeEmpID > 0 {
		name, err := s.st.EmployeeName(req.AssigneeEmpID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "查询维修工失败")
			return
		}
		if name == "" {
			writeErr(w, http.StatusBadRequest, "指定的维修工不存在")
			return
		}
		assigneeName = name
	}
	vendorName := ""
	if req.VendorID > 0 {
		name, err := s.st.VendorName(req.VendorID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "查询供应商失败")
			return
		}
		if name == "" {
			writeErr(w, http.StatusBadRequest, "指定的供应商不存在")
			return
		}
		vendorName = name
	}
	s.doRepairTransition(w, r, o, model.RepairActionDispatch, store.RepairTransitionInput{
		Remark:        req.Remark,
		AssigneeEmpID: req.AssigneeEmpID,
		AssigneeName:  assigneeName,
		VendorID:      req.VendorID,
		VendorName:    vendorName,
	})
}

func (s *Server) handleTakeRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	me := userFrom(r)
	if !isRepairAssignee(me, o) && me.Role != model.RoleAdmin {
		writeErr(w, http.StatusForbidden, "只有被指派的维修工可以接单")
		return
	}
	s.doRepairTransition(w, r, o, model.RepairActionTake, store.RepairTransitionInput{})
}

func (s *Server) handleFinishRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	me := userFrom(r)
	if !isRepairAssignee(me, o) && me.Role != model.RoleAdmin {
		writeErr(w, http.StatusForbidden, "只有被指派的维修工可以报完工")
		return
	}
	var req repairActionReq
	if err := readJSONOptional(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.HandlerDesc)
	if req.HandlerDesc == "" {
		writeErr(w, http.StatusBadRequest, "请填写维修措施 / 更换配件说明")
		return
	}
	s.doRepairTransition(w, r, o, model.RepairActionFinish, store.RepairTransitionInput{
		HandlerDesc:   req.HandlerDesc,
		AttachmentIDs: req.AttachmentIDs,
	})
}

func (s *Server) handleConfirmRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	me := userFrom(r)
	if !isRepairReporter(me, o) && !canManageRepair(me.Role) {
		writeErr(w, http.StatusForbidden, "只有报修人（或管理员）可以确认完工")
		return
	}
	s.doRepairTransition(w, r, o, model.RepairActionConfirm, store.RepairTransitionInput{})
}

func (s *Server) handleReturnRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	me := userFrom(r)
	if !isRepairReporter(me, o) && !canManageRepair(me.Role) {
		writeErr(w, http.StatusForbidden, "只有报修人（或管理员）可以退回")
		return
	}
	var req repairActionReq
	if err := readJSONOptional(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.Remark)
	if req.Remark == "" {
		writeErr(w, http.StatusBadRequest, "退回必须填写原因")
		return
	}
	s.doRepairTransition(w, r, o, model.RepairActionReturn, store.RepairTransitionInput{Remark: req.Remark})
}

func (s *Server) handleCancelRepair(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	me := userFrom(r)
	// 待受理由报修人自己撤单；已受理 / 已派工只能由管理员取消
	if o.Status == model.RepairPending {
		if !isRepairReporter(me, o) && !canManageRepair(me.Role) {
			writeErr(w, http.StatusForbidden, "只有报修人（或管理员）可以撤单")
			return
		}
	} else if !canManageRepair(me.Role) {
		writeErr(w, http.StatusForbidden, "受理后只能由管理员取消")
		return
	}
	var req repairActionReq
	if err := readJSONOptional(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.doRepairTransition(w, r, o, model.RepairActionCancel, store.RepairTransitionInput{Remark: req.Remark})
}

// handleRepairCost 登记维修费用（P1-4）。只改费用列，不触发状态机。
func (s *Server) handleRepairCost(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadRepairForAction(w, r)
	if !ok {
		return
	}
	var req struct {
		Cost       float64 `json:"cost"`
		CostDeptID int64   `json:"cost_dept_id"`
	}
	if err := readJSONOptional(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Cost < 0 {
		writeErr(w, http.StatusBadRequest, "费用不能为负数")
		return
	}
	if err := s.st.UpdateRepairCost(o.ID, req.Cost, req.CostDeptID); err != nil {
		writeStoreErr(w, err, "维修单不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
