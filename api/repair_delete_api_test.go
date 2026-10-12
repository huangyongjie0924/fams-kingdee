package api

import (
	"net/http"
	"strings"
	"testing"

	"asset-mgr/model"
	"asset-mgr/store"
)

// newPendingRepair 用 store 直接建一张待受理单（不经过 HTTP）。
//
// 走 store 而不是 POST /api/repairs：本文件考的是**删除**的权限与状态门控，
// 建单路径（分类可维修门控、报修人须绑定员工）是另一条链，不该在这里重复覆盖。
func (h *qaH) newPendingRepair(cardID int64, desc string) *model.RepairOrder {
	h.t.Helper()
	card, err := h.st.GetCard(cardID)
	if err != nil || card == nil {
		h.t.Fatalf("读探针卡失败: %v", err)
	}
	o, err := h.st.CreateRepair(card, store.RepairCreateInput{
		ReporterEmpID: 1, ReporterName: "张三", FaultDesc: desc,
		Urgency: model.RepairUrgencyNormal, CreatedBy: "qa",
	})
	if err != nil {
		h.t.Fatalf("建单失败: %v", err)
	}
	return o
}

// repairStillExists 直接查库判断单据是否还在（GetRepair 不存在时返回 nil, nil，
// 拿它断言「还在不在」容易把「查不到」与「没查」混起来）。
func (h *qaH) repairStillExists(id int64) bool {
	h.t.Helper()
	var n int
	if err := h.st.DB().QueryRow("SELECT COUNT(*) FROM repair_order WHERE id = ?", id).Scan(&n); err != nil {
		h.t.Fatalf("查维修单失败: %v", err)
	}
	return n > 0
}

// TestDeleteRepairsRequiresAdmin 守住「仅超级管理员可物理删除维修单」。
//
// 为什么这条必须钉死：这是全项目唯一的物理删除业务单据入口，不可恢复。它走的是
// requireAdmin（硬判角色）而不是 requirePerm（角色→权限位表）—— 后者只要将来有人给
// 某个角色加一个权限位，那个角色就顺带获得了删除权，而这种授予是无声的。
// 本用例对 asset_manager（持有全部 4 个 repair.* 权限位）与 employee 各测一次，
// 正是为了证明「有权限位 ≠ 能删」。
func TestDeleteRepairsRequiresAdmin(t *testing.T) {
	h := qaSetup(t)

	cardID := h.newCard("QA-REPAIR-DEL-1", 0)
	pending := h.newPendingRepair(cardID, "权限用例")

	admin := model.User{ID: 1, Username: "qa-admin", RealName: "管理员",
		Role: model.RoleAdmin, Roles: []string{model.RoleAdmin}, EmployeeID: 1}
	assetMgr := model.User{ID: 2, Username: "qa-am", RealName: "资产管理员",
		Role: model.RoleAssetManager, Roles: []string{model.RoleAssetManager}, EmployeeID: 2}
	emp := model.User{ID: 3, Username: "qa-emp", RealName: "员工",
		Role: model.RoleEmployee, Roles: []string{model.RoleEmployee}, EmployeeID: 3}

	body := map[string]any{"ids": []int64{pending.ID}}

	// 非 admin：403，且单据必须原样保留
	for _, u := range []model.User{assetMgr, emp} {
		code, resp, _ := h.do("POST", "/api/repairs/delete", h.token(u), body)
		if code != http.StatusForbidden {
			t.Errorf("%s 应被拒 403，实际 %d %v", u.Username, code, resp)
		}
		if !h.repairStillExists(pending.ID) {
			t.Fatalf("%s 被拒后单据不该消失", u.Username)
		}
	}

	// 未带 token：401（鉴权中间件先拦）
	if code, _, _ := h.do("POST", "/api/repairs/delete", "", body); code != http.StatusUnauthorized {
		t.Errorf("未登录应 401，实际 %d", code)
	}

	// admin：放行
	code, resp, _ := h.do("POST", "/api/repairs/delete", h.token(admin), body)
	if code != http.StatusOK {
		t.Fatalf("admin 删除待受理单应 200，实际 %d %v", code, resp)
	}
	if n, _ := resp["deleted"].(float64); int(n) != 1 {
		t.Errorf("应返回 deleted=1，实际 %v", resp["deleted"])
	}
	if h.repairStillExists(pending.ID) {
		t.Error("admin 删除后单据应已不存在")
	}
}

// TestDeleteRepairsRejectsNonDeletable 守住「不在白名单的单不可删」，且**整体回滚**。
//
// 白名单 = 待受理 + 已撤单（model.RepairDeletableStatuses）。本用例覆盖**白名单之外**的两类：
//   - accepted / dispatched：在云之家侧还挂着他人的在办待办，删掉会让待办指向 404 深链；
//   - done / rejected：同为终态，但删除会抹掉已完成业务的审计痕迹。
// 特意把 rejected 也放进来，是为了钉死「终态 ≠ 可删」—— 判定标准是「有没有未兑现的外部承诺」，
// 不是「流转结束没结束」。将来有人图省事改成「所有终态都可删」时，这条会红。
//
// 混合批量那一条是重点：只要有一张不合规，连合规的那张也不能被删。
// 「删了一半」是最坏的失败形态 —— 用户看到报错，却已经有数据没了。
func TestDeleteRepairsRejectsNonDeletable(t *testing.T) {
	h := qaSetup(t)
	admin := model.User{ID: 1, Username: "qa-admin", RealName: "管理员",
		Role: model.RoleAdmin, Roles: []string{model.RoleAdmin}, EmployeeID: 1}
	tok := h.token(admin)

	cardID := h.newCard("QA-REPAIR-DEL-2", 0)

	// 已受理的单
	accepted := h.newPendingRepair(cardID, "已被受理")
	if _, err := h.st.Transition(accepted.ID, store.RepairTransitionInput{
		Action: model.RepairActionAccept, Operator: "qa",
	}); err != nil {
		t.Fatalf("受理失败: %v", err)
	}

	// 已完工的单（终态）
	done := h.newPendingRepair(cardID, "已完工")
	for _, a := range []string{model.RepairActionAccept, model.RepairActionDispatch, model.RepairActionTake,
		model.RepairActionFinish, model.RepairActionConfirm} {
		in := store.RepairTransitionInput{Action: a, Operator: "qa", AssigneeEmpID: 7, AssigneeName: "李师傅"}
		if _, err := h.st.Transition(done.ID, in); err != nil {
			t.Fatalf("跃迁 %s 失败: %v", a, err)
		}
	}

	// 已驳回的单（同为终态，但刻意不在白名单里）
	rejected := h.newPendingRepair(cardID, "已驳回")
	if _, err := h.st.Transition(rejected.ID, store.RepairTransitionInput{
		Action: model.RepairActionReject, Operator: "qa", Remark: "不属于维修范围",
	}); err != nil {
		t.Fatalf("驳回失败: %v", err)
	}

	for _, o := range []*model.RepairOrder{accepted, done, rejected} {
		code, resp, _ := h.do("POST", "/api/repairs/delete", tok, map[string]any{"ids": []int64{o.ID}})
		if code != http.StatusBadRequest {
			t.Errorf("%s 状态的单应被拒 400，实际 %d %v", model.RepairStatusLabel(o.Status), code, resp)
		}
		// 错误信息必须把白名单两个状态都说出来，否则用户不知道到底什么能删
		if msg, _ := resp["message"].(string); msg != "" {
			for _, want := range []string{"待受理", "已撤单"} {
				if !strings.Contains(msg, want) {
					t.Errorf("%s 的拒绝理由应提到「%s」，实际: %s", o.Code, want, msg)
				}
			}
		}
		if !h.repairStillExists(o.ID) {
			t.Errorf("被拒的单 %s 不该消失", o.Code)
		}
	}

	// 混合批量：合规的（待受理） + 不合规的（已受理） ⇒ 整体拒绝，合规那张也必须还在
	kept := h.newPendingRepair(cardID, "本该被回滚保住")
	code, resp, _ := h.do("POST", "/api/repairs/delete", tok,
		map[string]any{"ids": []int64{kept.ID, accepted.ID}})
	if code != http.StatusBadRequest {
		t.Fatalf("混合批量应整体拒绝 400，实际 %d %v", code, resp)
	}
	if !h.repairStillExists(kept.ID) {
		t.Error("整体回滚后，原本合规的那张单也必须保留")
	}
	if !h.repairStillExists(accepted.ID) {
		t.Error("整体回滚后，不合规的那张单也必须保留")
	}
}

// TestDeleteRepairsAllowsCancelled 守住本轮新增的白名单状态：**已撤单的单可以删**。
//
// 为什么已撤单要放行：它是终态，撤单时已经跑过同一套整单清理（deliverDeleteAll），
// 本地已无未兑现的外部承诺；而 pending 能删而 cancelled 不能删在业务上说不通 ——
// 撤单正是「这单作废了」的表达，作废的单更该允许清掉。
//
// 同时验证级联清理：已撤单的单比待受理多一条流转记录（提交 + 撤单），
// 主表删掉后从属行一条都不能剩。
func TestDeleteRepairsAllowsCancelled(t *testing.T) {
	h := qaSetup(t)
	admin := model.User{ID: 1, Username: "qa-admin", RealName: "管理员",
		Role: model.RoleAdmin, Roles: []string{model.RoleAdmin}, EmployeeID: 1}
	tok := h.token(admin)

	cardID := h.newCard("QA-REPAIR-DEL-3", 0)
	o := h.newPendingRepair(cardID, "撤单后要删掉的")
	if _, err := h.st.Transition(o.ID, store.RepairTransitionInput{
		Action: model.RepairActionCancel, Operator: "张三",
	}); err != nil {
		t.Fatalf("撤单失败: %v", err)
	}

	code, resp, _ := h.do("POST", "/api/repairs/delete", tok, map[string]any{"ids": []int64{o.ID}})
	if code != http.StatusOK {
		t.Fatalf("删除已撤单的单应 200，实际 %d %v", code, resp)
	}
	if n, _ := resp["deleted"].(float64); int(n) != 1 {
		t.Errorf("应返回 deleted=1，实际 %v", resp["deleted"])
	}
	if h.repairStillExists(o.ID) {
		t.Error("删除后单据应已不存在")
	}
	// 级联：从属三表一条都不能剩
	for _, c := range []struct {
		table string
		where string
	}{
		{"doc_status_log", "doc_type = 'repair' AND doc_id = ?"},
		{"repair_attachment", "repair_id = ?"},
		{"repair_notify_task", "repair_id = ?"},
	} {
		var n int
		if err := h.st.DB().QueryRow("SELECT COUNT(*) FROM "+c.table+" WHERE "+c.where, o.ID).Scan(&n); err != nil {
			t.Fatalf("统计 %s 失败: %v", c.table, err)
		}
		if n != 0 {
			t.Errorf("级联清理后 %s 应为 0 行，实际 %d 行（孤儿行）", c.table, n)
		}
	}
}

// TestDeleteRepairsRejectsBadInput 守住入参守卫：空 ids / 非法 id / 不存在的 id 一律 400，
// 且不产生任何副作用。
func TestDeleteRepairsRejectsBadInput(t *testing.T) {
	h := qaSetup(t)
	admin := model.User{ID: 1, Username: "qa-admin", RealName: "管理员",
		Role: model.RoleAdmin, Roles: []string{model.RoleAdmin}, EmployeeID: 1}
	tok := h.token(admin)

	cases := []struct {
		name string
		body any
	}{
		{"空数组", map[string]any{"ids": []int64{}}},
		{"缺字段", map[string]any{}},
		{"含 0", map[string]any{"ids": []int64{0}}},
		{"含负数", map[string]any{"ids": []int64{-5}}},
		{"不存在的 id", map[string]any{"ids": []int64{999999999}}},
	}
	for _, c := range cases {
		code, resp, _ := h.do("POST", "/api/repairs/delete", tok, c.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s 应 400，实际 %d %v", c.name, code, resp)
		}
	}
}

// TestDeleteRepairsReadonlySessionBlocked 守住「云之家深链免登会话不能删」。
//
// 用的是 **admin 账号**签发的收窄 token：即使账号本身是超级管理员，只要会话带 rsc
// （免登深链），deeplinkScopeAllows 会因为它不是 GET 而直接 403。
// 这条防的是「深链免登被当成会话用」——免登 token 只应能看那一张单。
func TestDeleteRepairsReadonlySessionBlocked(t *testing.T) {
	h := qaSetup(t)

	cardID := h.newCard("QA-REPAIR-DEL-3", 0)
	pending := h.newPendingRepair(cardID, "只读会话用例")

	admin := model.User{ID: 1, Username: "qa-admin", RealName: "管理员",
		Role: model.RoleAdmin, Roles: []string{model.RoleAdmin}, EmployeeID: 1}
	scoped, err := h.srv.issueScopedToken(&admin, pending.ID)
	if err != nil {
		t.Fatalf("签发收窄 token 失败: %v", err)
	}

	code, resp, _ := h.do("POST", "/api/repairs/delete", scoped, map[string]any{"ids": []int64{pending.ID}})
	if code != http.StatusForbidden {
		t.Fatalf("免登只读会话应 403，实际 %d %v", code, resp)
	}
	if !h.repairStillExists(pending.ID) {
		t.Error("免登会话被拒后单据不该消失")
	}

	// 反向对照：同一个账号的**普通** token 必须能删 —— 证明 403 来自「会话收窄」，
	// 而不是因为别的什么原因（否则这条用例可能是被权限拦的，测不到想测的东西）。
	code, resp, _ = h.do("POST", "/api/repairs/delete", h.token(admin), map[string]any{"ids": []int64{pending.ID}})
	if code != http.StatusOK {
		t.Fatalf("同账号普通会话应能删（对照），实际 %d %v", code, resp)
	}
}
