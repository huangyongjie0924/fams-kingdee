package api

import (
	"log"
	"net/http"

	"asset-mgr/integration/yunzhijia"
	"asset-mgr/model"
)

// handleSSOLogin 云之家免登：用客户端下发的 ticket 解析出工号，匹配台账员工后签发本系统 JWT。
// 工号匹配到员工但还没绑定账号时，自动建一个 employee 员工账号；匹配不到员工则拒绝。
func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Ticket == "" {
		writeErr(w, http.StatusBadRequest, "缺少 ticket")
		return
	}
	if s.yzj == nil {
		writeErr(w, http.StatusServiceUnavailable, "云之家未配置")
		return
	}

	u, err := s.yzj.ResolveUser(r.Context(), req.Ticket)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "云之家登录失败")
		return
	}
	if u.JobNo == "" {
		writeErr(w, http.StatusUnauthorized, "未获取到工号")
		return
	}
	log.Printf("SSO login: jobNo=%q username=%q eid=%q", u.JobNo, u.Username, u.EID)

	empID, err := s.st.LookupEmployeeByNumber(u.JobNo)
	log.Printf("SSO lookup: jobNo=%q empID=%d err=%v", u.JobNo, empID, err)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if empID == 0 {
		writeErr(w, http.StatusUnauthorized, "工号未在台账中登记，请联系管理员")
		return
	}

	// 路径 A：免登时顺手回填云之家 openId（docs/云之家待办通知集成方案.md §6.4）。
	//
	// openId 是待办通知的**收件人标识**，缺它就只能跳过通知（§3.1）。免登是它最精确、
	// 最实时的来源；通讯录增量同步（路径 B）只负责补那些从未免登过的员工。
	// → 因此即使 eid 未配置、同步跑不起来，活跃用户的待办仍能工作。
	//
	// **写失败绝不影响登录**：openId 只是通知链路的前置数据，不能因为它写不进去
	// 就把用户挡在门外（§7 失败隔离）。日志里用 MaskOpenID 脱敏，避免 openId 泄漏。
	if u.OpenID != "" {
		if err := s.st.UpdateEmployeeOpenID(empID, u.OpenID); err != nil {
			log.Printf("SSO openId 回填失败（不影响登录）: empID=%d openId=%s err=%v",
				empID, yunzhijia.MaskOpenID(u.OpenID), err)
		}
	}

	user, err := s.st.UserByEmployee(empID)
	log.Printf("SSO user: empID=%d found=%v err=%v", empID, user != nil, err)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		name := u.Username
		if name == "" {
			name = u.JobNo
		}
		id, err := s.st.CreateSSOUser(u.JobNo, name, []string{model.RoleEmployee}, empID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "自动创建账号失败")
			return
		}
		user = &model.User{ID: id, Username: u.JobNo, RealName: name,
			Role: model.RoleEmployee, Roles: []string{model.RoleEmployee}, EmployeeID: empID, Enabled: true}
	} else if !user.Enabled {
		writeErr(w, http.StatusUnauthorized, "账号已停用")
		return
	}

	token, err := s.issueToken(user)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "签发 token 失败")
		return
	}
	log.Printf("SSO ok: user_id=%d username=%q roles=%q", user.ID, user.Username, user.EffectiveRoles())
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
}
