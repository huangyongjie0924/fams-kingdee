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
// 出错时由本函数直接写响应并返回 false，调用方只需 `if !ok { return }`。
func (s *Server) assetScope(w http.ResponseWriter, r *http.Request) (model.AssetScope, bool) {
	u := userFrom(r)

	switch u.Role {
	case model.RoleViewer:
		// eid 已经在 JWT 里，这条路径零查询
		if u.EmployeeID == 0 {
			writeErr(w, http.StatusForbidden, "当前账号未绑定员工，无法查看资产")
			return model.AssetScope{}, false
		}
		return model.AssetScope{SelfEmpID: u.EmployeeID}, true

	case model.RoleDeptHead:
		// 管辖的部门记在账号上，每次读库而不是塞进 JWT：改部门不用重新登录。
		// 只有部门负责人付这一次主键查询，其余角色零查询。
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
		return model.AssetScope{DeptIDs: ids}, true
	}

	// admin / asset_manager / counter 以及未知角色都不限制。
	// counter 是刻意的：盘点要按计划清点，范围由 count_plan.scope 自己约束，
	// 不该再被账号可见范围砍掉一半而盘不完整。
	return model.AssetScope{}, true
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
