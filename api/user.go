package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"asset-mgr/model"
)

// validRole 校验单个角色名。保留给单值路径 / 其它调用点使用。
func validRole(role string) bool {
	for _, r := range model.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// validRoles 校验角色集合：归一化后为空即非法（去重 / 剔非法后一个不剩，说明传进来的全是垃圾）。
func validRoles(roles []string) bool {
	return len(model.NormalizeRoles(roles)) > 0
}

// parseUserRoles 解析请求里的角色：优先用 roles（多角色），否则回落到单值 role（旧前端）。
// 与 model.NormalizeRoles 一样会 trim / 去重 / 剔非法 / 按声明顺序排序。
func parseUserRoles(req *userReq) []string {
	roles := req.Roles
	if len(roles) == 0 && req.Role != "" {
		roles = []string{req.Role}
	}
	return model.NormalizeRoles(roles)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.st.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

type userReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	RealName string `json:"real_name"`
	// Role 是主角色，保留以兼容旧前端（只传 role、不传 roles 时按单角色处理）。
	Role string `json:"role"`
	// Roles 是账号的全部角色（一人可多角色）。传了它就以它为准，忽略 Role。
	Roles      []string `json:"roles"`
	EmployeeID int64    `json:"employee_id"`
	DeptID     int64    `json:"dept_id"`
	Enabled    bool     `json:"enabled"`
}

// roleBindingErr 校验角色与绑定项是否配套。宁可在建号时挡住，
// 也好过让人登进去才发现「什么都没有」——那时只剩一句 403。
//
// 多角色语义：**任一**角色需要绑定就要求绑定。检查顺序：
//   - 含 dept_head 且 deptID == 0 → 部门负责人必须选管辖部门；
//   - 含 employee / counter / maintainer 且 employeeID == 0 → 按角色给具体文案。
func roleBindingErr(roles []string, employeeID, deptID int64) string {
	if model.HasRole(roles, model.RoleDeptHead) && deptID == 0 {
		return "部门负责人必须选择管辖的部门"
	}
	if employeeID == 0 {
		switch {
		case model.HasRole(roles, model.RoleEmployee):
			return "员工账号必须绑定员工，否则看不到任何资产"
		case model.HasRole(roles, model.RoleCounter):
			return "盘点员必须绑定员工，否则无法被指派资产"
		case model.HasRole(roles, model.RoleMaintainer):
			return "维护人员必须绑定员工，否则无法被指派维修单"
		}
	}
	return ""
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req userReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.Username, &req.RealName, &req.Role)
	roles := parseUserRoles(&req)
	if req.Username == "" {
		writeErr(w, http.StatusBadRequest, "用户名不能为空")
		return
	}
	if len(req.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "密码至少 6 位")
		return
	}
	if !validRoles(roles) {
		writeErr(w, http.StatusBadRequest, "角色无效")
		return
	}
	if msg := roleBindingErr(roles, req.EmployeeID, req.DeptID); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	id, err := s.st.CreateUser(req.Username, req.Password, req.RealName, roles, req.EmployeeID, req.DeptID, req.Enabled)
	if err != nil {
		if strings.Contains(err.Error(), "Duplicate") {
			writeErr(w, http.StatusConflict, "用户名已存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	var req userReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.RealName, &req.Role)
	roles := parseUserRoles(&req)
	if !validRoles(roles) {
		writeErr(w, http.StatusBadRequest, "角色无效")
		return
	}
	if req.Password != "" && len(req.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "密码至少 6 位")
		return
	}
	if msg := roleBindingErr(roles, req.EmployeeID, req.DeptID); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}

	target, err := s.st.GetUser(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if target == nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	// 把自己降级/停用后马上会失去权限，容易被锁在门外，直接挡住。
	// 多角色语义：只要新角色集合里还留着 admin（且不停用）就放行，反之挡住。
	me := userFrom(r)
	if me.ID == id && (!model.HasRole(roles, model.RoleAdmin) || !req.Enabled) {
		writeErr(w, http.StatusBadRequest, "不能修改自己的角色或停用自己")
		return
	}
	// 目标账号原本含 admin，而新集合不含 admin 或要停用它 → 至少保留一个其它启用管理员。
	if model.HasRole(target.EffectiveRoles(), model.RoleAdmin) &&
		(!model.HasRole(roles, model.RoleAdmin) || !req.Enabled) {
		if err := s.requireOtherAdmin(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := s.st.UpdateUser(id, req.RealName, roles, req.EmployeeID, req.DeptID, req.Enabled, req.Password); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 ID")
		return
	}
	if userFrom(r).ID == id {
		writeErr(w, http.StatusBadRequest, "不能删除自己的账号")
		return
	}
	target, err := s.st.GetUser(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if target == nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	// 多角色：主角色不是 admin、但角色集合含 admin 的账号同样是管理员，也要保护。
	if model.HasRole(target.EffectiveRoles(), model.RoleAdmin) && target.Enabled {
		if err := s.requireOtherAdmin(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := s.st.DeleteUser(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// requireOtherAdmin 要求除目标账号外还存在其它启用中的管理员，避免系统失去管理员。
func (s *Server) requireOtherAdmin() error {
	n, err := s.st.CountEnabledAdmins()
	if err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("至少保留一个启用的管理员账号")
	}
	return nil
}
