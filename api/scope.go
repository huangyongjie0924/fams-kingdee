package api

import (
	"log"
	"net/http"

	"asset-mgr/model"
)

// assetScope 把登录用户翻译成资产可见范围，写权限无关——只回答「能看见哪些行」。
//
// 台账的所有读接口都是刻意不加 requirePerm 的（看得到台账就该能看详情/打标签/导出），
// 所以范围限制必须在数据行上做，不能靠路由门控，否则会连自己的资产都看不到。
//
// 多角色（一人可多角色，2026-09-24 需求）：范围是**并集**——逐角色算出各自的范围后合并。
// 任一角色「不限」则整体不限（「不限」即全集，是并集的自然结果，不给它开特例）。
//
// 三条口径（命中任一即可见，见 model.AssetScope）：
//   - SelfEmpID：自己在用的资产（employee）；
//   - DeptIDs：管辖部门（含下级）的资产（dept_head）；
//   - RepairingCardIDs：有**未结单**维修单的资产。**只由 employee 产生**，且**不限报修人**
//     （全公司口径）——任何人的报修单只要未结单，其资产就对该员工可见。理由：员工报修入口
//     本身受本函数门控，员工只能对看得见的资产报修，故「只看我报修的」等价于现状。
//
// 出错时由本函数直接写响应并返回 false，调用方只需 `if !ok { return }`。
func (s *Server) assetScope(w http.ResponseWriter, r *http.Request) (model.AssetScope, bool) {
	u := userFrom(r)

	// —— fail-closed（纵深防御第二层，第一层是 model.LegacyRoleRenames 的角色名映射）——
	//
	// 角色集合为空只可能来自脏数据：角色名拼错、新增角色忘了登记进 model.Roles、roles 列被清空。
	// 而本函数是「逐角色累加」的循环——空集合 = 循环体一次都不执行 ⇒ 直接返回**零值** scope，
	// 其语义是「不限」，即**读到全量资产**。把「解析不出角色」等同于「给你全部数据」是典型的
	// fail-open，绝不能接受。故在任何 403 分支之前先拦下：解析不出角色 ⇒ 403 + false。
	//
	// ⚠️ 合法账号不受影响：EffectiveRoles() 对任何合法角色（含单角色回落）恒非空，只有真正的
	// 脏数据才会命中这条。
	roles := u.EffectiveRoles()
	if len(roles) == 0 {
		writeErr(w, http.StatusForbidden, "账号未配置有效角色，无法确定资产可见范围")
		return model.AssetScope{}, false
	}

	var out model.AssetScope
	needDept := false

	// EffectiveRoles 已按声明顺序归一化（admin, asset_manager, counter, dept_head, maintainer, employee），
	// 故「不限」的角色（default 分支）会先于受限角色命中并直接返回，无需额外排序。
	for _, role := range roles {
		switch role {
		case model.RoleEmployee:
			// eid 已经在 JWT 里，SelfEmpID 这条路径零查询。
			// 403 只在账号**确实需要 employee 这一维**时触发，保持单角色行为不变。
			if u.EmployeeID == 0 {
				writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法查看资产")
				return model.AssetScope{}, false
			}
			out.SelfEmpID = u.EmployeeID

			// 第三维：有未结单维修单的资产（全公司口径，不限报修人）。只在这里产生——
			// 维护人员 / dept_head / counter / admin / asset_manager 都不填这一维。
			ids, err := s.st.ListOpenRepairCardIDs()
			if err != nil {
				log.Printf("asset scope: user=%d open repairs: %v", u.ID, err)
				writeErr(w, http.StatusInternalServerError, "解析可见范围失败")
				return model.AssetScope{}, false
			}
			out.RepairingCardIDs = ids

		case model.RoleDeptHead:
			// 管辖的部门记在账号上，读库而不是塞进 JWT：改部门不用重新登录。
			// 只在这里打标记，循环结束后统一查一次子树——多角色下 dept_head 只出现一次，
			// 但显式标记让「只查一次」的意图不依赖角色列表本身去重。
			needDept = true

		case model.RoleITAdmin:
			// IT 管理员：**有意不限**（2026-10-10 需求，经用户确认）。
			//
			// 为什么写在这里而不是让它落 default：本 switch 的 default 分支返回零值 scope，
			// 语义是「不限」—— 也就是说**新增角色忘登记的默认后果是看到全量数据**，
			// 这是个 fail-open 的危险默认值（与函数入口那条 fail-closed 恰好相反）。
			// 显式列出来，才能让「IT 管理员能看全量资产台账」这件事在 diff 里被看见，
			// 也才能被守卫测试钉住（见 api/role_scope_guard_test.go）。
			//
			// 取舍：他需要能检索全库资产才能挑出设备做 IT 台账关联；代价是他同时能看到
			// 全量资产的金额与财务字段（原值/净值/折旧）。这是明确接受的，不是遗漏。
			return model.AssetScope{}, true

		default:
			// admin / asset_manager / counter / maintainer 都不限制。
			// counter 是刻意的：盘点要按计划清点，范围由 count_plan.scope 自己约束，
			// 不该再被账号可见范围砍掉一半而盘不完整。
			// ⚠️ 必须先于任何 403 返回：admin 的 employee_id = 0，否则会被「未绑定员工」误伤。
			// （未知 / 空角色已在函数入口 fail-closed，不会走到这里。）
			return model.AssetScope{}, true
		}
	}

	if needDept {
		deptID, err := s.st.UserDeptID(u.ID)
		if err != nil {
			log.Printf("asset scope: user=%d dept lookup: %v", u.ID, err)
			writeErr(w, http.StatusInternalServerError, "解析可见范围失败")
			return model.AssetScope{}, false
		}
		if deptID == 0 {
			writeErr(w, http.StatusForbidden, "当前账号未设置部门，无法确定可见范围")
			return model.AssetScope{}, false
		}
		ids, err := s.deptSubtree(deptID)
		if err != nil {
			log.Printf("asset scope: dept=%d subtree: %v", deptID, err)
			writeErr(w, http.StatusInternalServerError, "解析可见范围失败")
			return model.AssetScope{}, false
		}
		out.DeptIDs = ids
	}

	return out, true
}

// deptSubtree 返回该部门及其全部下级部门的 id。
// 部门表只有二十来行，拉全表在 Go 里 BFS 比 WITH RECURSIVE 更省心，
// 也不用假设生产库是 MySQL 8。
func (s *Server) deptSubtree(rootID int64) ([]int64, error) {
	depts, err := s.st.ListDepartments()
	if err != nil {
		return nil, err
	}
	children := map[int64][]int64{}
	for _, d := range depts {
		children[d.ParentID] = append(children[d.ParentID], d.ID)
	}
	// 坏数据可能把 parent_id 连成环，用 seen 兜住，否则这里会转不出来
	seen := map[int64]bool{rootID: true}
	out := []int64{rootID}
	for queue := []int64{rootID}; len(queue) > 0; {
		var next []int64
		for _, id := range queue {
			for _, child := range children[id] {
				if seen[child] {
					continue
				}
				seen[child] = true
				out = append(out, child)
				next = append(next, child)
			}
		}
		queue = next
	}
	return out, nil
}

// denyOutOfScope 在卡不在可见范围内时写 403 并返回 true。
// 列表走 SQL 收口，单卡读取（详情、履历、附件、扫码、盘点明细）走这里。
func denyOutOfScope(w http.ResponseWriter, c *model.AssetCard, sc model.AssetScope) bool {
	if sc.Allows(c) {
		return false
	}
	writeErr(w, http.StatusForbidden, "该资产不在你的可见范围内")
	return true
}

// repairScope 把登录用户翻译成维修单可见范围，写权限无关——只回答「能看见哪些行」。
//
// 与 assetScope 同一套约定：读接口不门控，范围限制在数据行上做。
// 多角色（一人可多角色，2026-09-24 需求）：范围是**并集**——逐角色算出各自的范围后合并。
//
//   - 维护人员：指派给自己的单 + 自己提交的单
//   - 员工 / 盘点员：只看自己提交的单
//   - 部门主管：本部门及下级部门的单（按单据的使用部门快照收窄）
//   - admin / asset_manager：不限
func (s *Server) repairScope(w http.ResponseWriter, r *http.Request) (model.RepairScope, bool) {
	u := userFrom(r)

	// —— fail-closed（与 assetScope 同一条纵深防御）——
	//
	// 空角色集合 = 脏数据，循环体一次都不执行 ⇒ 零值 scope ⇒ 语义「不限」⇒ 读到全量维修单。
	// 与 assetScope 一样，在任何 403 分支之前先拦下，绝不让「解析不出角色」变成「不限范围」。
	roles := u.EffectiveRoles()
	if len(roles) == 0 {
		writeErr(w, http.StatusForbidden, "账号未配置有效角色，无法确定维修单可见范围")
		return model.RepairScope{}, false
	}

	var out model.RepairScope
	needDept := false

	// EffectiveRoles 已按声明顺序归一化，故「不限」的角色（default 分支）会先于受限角色命中并直接返回。
	for _, role := range roles {
		switch role {
		case model.RoleMaintainer:
			if u.EmployeeID == 0 {
				writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法查看维修单")
				return model.RepairScope{}, false
			}
			// 维护人员既看「派给我的」，也看「我报修的」——自己提交的单派给别人后不该从自己眼前消失。
			// RepairScope.Allows 三个维度是 OR，故同时给两个维度即可。
			out.AssigneeEmpID = u.EmployeeID
			out.ReporterEmpID = u.EmployeeID

		case model.RoleEmployee, model.RoleCounter:
			if u.EmployeeID == 0 {
				writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法查看维修单")
				return model.RepairScope{}, false
			}
			out.ReporterEmpID = u.EmployeeID

		case model.RoleDeptHead:
			needDept = true

		case model.RoleITAdmin:
			// IT 管理员：**有意不限**，与 assetScope 里的同名分支同源同理由。
			// 与 assetScope 一样显式列出，不让它落 default——default 的「不限」是个
			// fail-open 的危险默认值，新增角色必须在这里被看见（见 api/role_scope_guard_test.go）。
			return model.RepairScope{}, true

		default:
			// admin / asset_manager 不限制。
			// ⚠️ 必须先于任何 403 返回：admin 的 employee_id = 0，否则会被「未绑定员工」误伤。
			// （未知 / 空角色已在函数入口 fail-closed，不会走到这里。）
			return model.RepairScope{}, true
		}
	}

	if needDept {
		deptID, err := s.st.UserDeptID(u.ID)
		if err != nil {
			log.Printf("repair scope: user=%d dept lookup: %v", u.ID, err)
			writeErr(w, http.StatusInternalServerError, "解析可见范围失败")
			return model.RepairScope{}, false
		}
		if deptID == 0 {
			writeErr(w, http.StatusForbidden, "当前账号未设置部门，无法确定可见范围")
			return model.RepairScope{}, false
		}
		ids, err := s.deptSubtree(deptID)
		if err != nil {
			log.Printf("repair scope: dept=%d subtree: %v", deptID, err)
			writeErr(w, http.StatusInternalServerError, "解析可见范围失败")
			return model.RepairScope{}, false
		}
		out.DeptIDs = ids
	}

	return out, true
}

// denyRepairOutOfScope 在单据不在可见范围内时写 403 并返回 true。
// 列表走 SQL 收口，单张单据（详情、日志、附件）走这里。
func denyRepairOutOfScope(w http.ResponseWriter, o *model.RepairOrder, sc model.RepairScope) bool {
	if sc.Allows(o) {
		return false
	}
	writeErr(w, http.StatusForbidden, "该维修单不在你的可见范围内")
	return true
}

// checkCardScope 是「先确认这张卡看得见，再看它的从属数据」的入口：
// 履历、附件、盘点明细这类接口没有别的办法判范围。
// 不限范围的角色直接放行，不为此多查一次库。
func (s *Server) checkCardScope(w http.ResponseWriter, r *http.Request, id int64) bool {
	sc, ok := s.assetScope(w, r)
	if !ok {
		return false
	}
	if !sc.Restricted() {
		return true
	}
	c, err := s.st.GetCard(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询资产失败")
		return false
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "资产不存在")
		return false
	}
	return !denyOutOfScope(w, c, sc)
}

// —— IT 信息设备台账的可见范围（2026-10-10 需求）——

// itScopeOf 从**已算好的**资产可见范围导出 IT 台账的可见范围。
//
// 为什么是「导出」而不是另写一个 switch：用户要求「IT 台账的可见范围复用资产台账的范围」，
// 而本仓已有一次真实的教训——列表的 scope 与首页的 scope 各写一份，随时间分叉
// （见 scopeConds 上方注释）。这里同样：两个 switch 分开写，将来给 assetScope 加了新角色
// 却忘了加 itScope，就会变成「同一个人在资产台账与 IT 台账里看到不同范围」，
// 而这种不一致极难被发现（两边都「有数据」）。派生则从构造上不可能漂移。
//
// 只取两维：SelfEmpID（自己名下）与 DeptIDs（管辖部门）。
// **刻意丢弃** AssetScope.RepairingCardIDs —— 那一维的语义是「有未结单维修单的资产对员工可见」，
// 是维修模块的需要，与 IT 设备无关；照搬会让员工看到别人名下设备的 IT 配置。
// 这个丢弃是**有意为之**：若将来 AssetScope 新增了应当适用于 IT 台账的维度，必须显式加到这里。
func itScopeOf(sc model.AssetScope) model.ITScope {
	return model.ITScope{SelfEmpID: sc.SelfEmpID, DeptIDs: sc.DeptIDs}
}

// itScope 是 IT 台账读接口的入口：先按角色算资产可见范围，再导出 IT 范围。
//
// ⚠️ 不要在这里再写一遍角色 switch。要改 IT 台账的可见范围，请改 assetScope 的
// 角色分支（或 itScopeOf 的维度映射），改一处即两处生效。
//
// 唯一的副作用：employee 账号会多跑一次 ListOpenRepairCardIDs（assetScope 的第三维），
// 而结果被 itScopeOf 丢弃。代价是一次带索引的点查，换的是「两个模块的范围永不分叉」，
// 值得。若将来这成为性能问题，正确的做法是给 assetScope 拆出「只要前两维」的变体，
// 而不是在这里复制一份 switch。
func (s *Server) itScope(w http.ResponseWriter, r *http.Request) (model.ITScope, bool) {
	sc, ok := s.assetScope(w, r)
	if !ok {
		return model.ITScope{}, false
	}
	return itScopeOf(sc), true
}

// denyITOutOfScope 在记录不在可见范围内时写 403 并返回 true。
// 列表走 SQL 收口（store/it.go 的 itScopeConds），单条读取（详情、编辑回显）走这里。
func denyITOutOfScope(w http.ResponseWriter, e *model.ITEquipment, sc model.ITScope) bool {
	if sc.Allows(e) {
		return false
	}
	writeErr(w, http.StatusForbidden, "该 IT 设备记录不在你的可见范围内")
	return true
}
