package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"asset-mgr/model"
)

func validRole(role string) bool {
	for _, r := range model.Roles {
		if r == role {
			return true
		}
	}
	return false
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
	Username   string `json:"username"`
	Password   string `json:"password"`
	RealName   string `json:"real_name"`
	Role       string `json:"role"`
	EmployeeID int64  `json:"employee_id"`
	DeptID     int64  `json:"dept_id"`
	Enabled    bool   `json:"enabled"`
}

// roleBindingErr 校验角色与绑定项是否配套。宁可在建号时挡住，
// 也好过让人登进去才发现「什么都没有」——那时只剩一句 403。
func roleBindingErr(role string, employeeID, deptID int64) string {
	switch role {
	case model.RoleDeptHead:
		if deptID == 0 {
			return "部门负责人必须选择管辖的部门"
		}
	case model.RoleViewer:
		if employeeID == 0 {
			return "只读账号必须绑定员工，否则看不到任何资产"
		}
	case model.RoleCounter:
		if employeeID == 0 {
			return "盘点员必须绑定员工，否则无法被指派资产"
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
	if req.Username == "" {
		writeErr(w, http.StatusBadRequest, "用户名不能为空")
		return
	}
	if len(req.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "密码至少 6 位")
		return
	}
	if !validRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "角色无效")
		return
	}
	if msg := roleBindingErr(req.Role, req.EmployeeID, req.DeptID); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	id, err := s.st.CreateUser(req.Username, req.Password, req.RealName, req.Role, req.EmployeeID, req.DeptID, req.Enabled)
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
	if !validRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "角色无效")
		return
	}
	if req.Password != "" && len(req.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "密码至少 6 位")
		return
	}
	if msg := roleBindingErr(req.Role, req.EmployeeID, req.DeptID); msg != "" {
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
	// 把自己降级/停用后马上会失去权限，容易被锁在门外，直接挡住
	me := userFrom(r)
	if me.ID == id && (req.Role != model.RoleAdmin || !req.Enabled) {
		writeErr(w, http.StatusBadRequest, "不能修改自己的角色或停用自己")
		return
	}
	if target.Role == model.RoleAdmin && (req.Role != model.RoleAdmin || !req.Enabled) {
		if err := s.requireOtherAdmin(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := s.st.UpdateUser(id, req.RealName, req.Role, req.EmployeeID, req.DeptID, req.Enabled, req.Password); err != nil {
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
	if target.Role == model.RoleAdmin && target.Enabled {
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
