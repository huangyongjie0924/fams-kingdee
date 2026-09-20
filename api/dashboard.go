package api

import (
	"net/http"

	"asset-mgr/model"
)

// 首页各角色的「待我处理」映射 + 「与我相关」指标卡（P0，见 docs/增量架构-可维修标签与首页.md §2.2）。
//
// 关键设计：哪些待办项出现、count 多少，**全部由服务端按角色算好**，前端只渲染。
// 这样既不会把管理向待办泄露给员工端（后端不返回），也避免前端出现「角色→待办」的第二套规则。
//
// 可见范围一律复用 assetScope / repairScope（api/scope.go），不新写范围逻辑。
// 两个不在 scope 内的数据源按 §2.5 处理：sync_run 用 Can(role, sync.manage) 门控，
// count_item 用 assignee_id 窄化——都不新造 scope 抽象。

// 待办的紧急程度（前端语义色）。取值与 model 前端契约一致：danger / warning / info。
// 谁紧急也是服务端判断：前端只把值映射到配色，不按 key 猜，避免出现第二套「key→颜色」规则。
const (
	todoLevelDanger  = "danger"  // 没人管 / 卡住了，需要立刻动手
	todoLevelWarning = "warning" // 轮到你了，但还在正常流程里
	todoLevelInfo    = "info"    // 只是告知进展，不催办
)

// repairInProgressStatuses 是「处理中」的状态集合（供 viewer 的「我的报修处理中」用）。
// 含 P1 的 approving / scrapping，启用后无需再改这里；P0 不产生这两个状态，故不影响当前计数。
var repairInProgressStatuses = []string{
	model.RepairAccepted, model.RepairApproving, model.RepairDispatched,
	model.RepairRepairing, model.RepairScrapping,
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)

	assetSc, ok := s.assetScope(w, r)
	if !ok {
		return
	}
	repairSc, ok := s.repairScope(w, r)
	if !ok {
		return
	}

	overview, err := s.dashboardOverview(assetSc, repairSc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询首页概览失败："+err.Error())
		return
	}
	todos, err := s.dashboardTodos(u, repairSc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询待办失败："+err.Error())
		return
	}
	// stats 复用已算好的 overview（asset_total 不再查一次库）。
	stats, err := s.dashboardStats(u, assetSc, repairSc, overview)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询我的指标失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, model.Dashboard{Todo: todos, Stats: stats, Overview: overview})
}

// dashboardOverview 组装概览区块。聚合全部下沉到 DB（GROUP BY），按 scope 在 WHERE 收窄。
func (s *Server) dashboardOverview(assetSc model.AssetScope, repairSc model.RepairScope) (model.DashboardOverview, error) {
	var ov model.DashboardOverview

	total, err := s.st.AssetTotal(assetSc)
	if err != nil {
		return ov, err
	}
	statusCounts, err := s.st.AssetStatusCounts(assetSc)
	if err != nil {
		return ov, err
	}
	byCategory, err := s.st.AssetCategoryCounts(assetSc)
	if err != nil {
		return ov, err
	}
	repairCounts, err := s.st.RepairStatusCounts(repairSc)
	if err != nil {
		return ov, err
	}

	ov.AssetTotal = total
	ov.AssetStatus = statusCounts
	ov.AssetByCategory = byCategory
	ov.RepairStatus = repairCounts
	return ov, nil
}

// dashboardTodos 按角色算「待我处理」。
//
// 只保留 count > 0 的项：待办即「有待处理的事」，为 0 的项不是待办，不返回；
// 前端在 todo 为空时展示空态即可。这一取舍由后端决定，前端不做二次过滤。
func (s *Server) dashboardTodos(u model.User, repairSc model.RepairScope) ([]model.DashboardTodo, error) {
	todos := []model.DashboardTodo{}
	add := func(key, label string, count int64, link, level string) {
		if count > 0 {
			todos = append(todos, model.DashboardTodo{Key: key, Label: label, Count: count, Link: link, Level: level})
		}
	}
	// addRepairSc 按**指定**的可见范围统计命中给定状态的维修单数，命中则入列。
	// 待办文案写「派给我的」时就不能拿账号的完整 scope 数：完整 scope 还含「我报修的」，
	// 会把派给别人的单算进来，与文案不符。这种情况传一个只含指派维度的 scope 进来。
	addRepairSc := func(key, label, link, level string, sc model.RepairScope, statuses ...string) error {
		n, err := s.st.CountRepairsByStatuses(statuses, sc)
		if err != nil {
			return err
		}
		add(key, label, n, link, level)
		return nil
	}
	// addRepair 用账号的完整范围，适用「本就按可见范围定义」的待办（管理视角、部门主管、员工自己的单）。
	addRepair := func(key, label, link, level string, statuses ...string) error {
		return addRepairSc(key, label, link, level, repairSc, statuses...)
	}

	switch u.Role {
	case model.RoleAdmin, model.RoleAssetManager:
		// 受理 / 派工 / 确认：admin 与 asset_manager 的可见范围都不限，count 为全局。
		// pending 没人接手 → danger；派工/确认在正常流程里 → warning。
		if err := addRepair("pending", "待受理", "/repairs?status=pending", todoLevelDanger, model.RepairPending); err != nil {
			return nil, err
		}
		if err := addRepair("unassigned", "待派工", "/repairs?status=accepted", todoLevelWarning, model.RepairAccepted); err != nil {
			return nil, err
		}
		if err := addRepair("confirming", "待确认", "/repairs?status=confirming", todoLevelWarning, model.RepairConfirming); err != nil {
			return nil, err
		}
	case model.RoleRepairTech:
		// 这两项待办说的是「派给我的」，只能按指派维度数：账号 repairScope 现已含
		// 「我报修的」，用它数会把派给别人的单（我报修、别人接手）也算成我的待办。
		// 可见范围（列表 / 详情）不受影响，仍走完整 scope。
		// 待接单是「球在我这、还没动」→ danger；维修中只是进展 → info。
		assigneeSc := model.RepairScope{AssigneeEmpID: u.EmployeeID}
		if err := addRepairSc("to_take", "派给我的待接单", "/repairs?status=dispatched", todoLevelDanger, assigneeSc, model.RepairDispatched); err != nil {
			return nil, err
		}
		if err := addRepairSc("repairing", "我名下维修中", "/repairs?status=repairing", todoLevelInfo, assigneeSc, model.RepairRepairing); err != nil {
			return nil, err
		}
	case model.RoleDeptHead:
		// 部门主管：repairScope 已收窄到本部门（含下级），故 pending 即「本部门待受理」。
		if err := addRepair("pending", "本部门待受理", "/repairs?status=pending", todoLevelDanger, model.RepairPending); err != nil {
			return nil, err
		}
	case model.RoleCounter:
		// 我的待盘点：count_item 专用窄化（不在 asset/repair scope 内）。
		n, err := s.st.PendingCountItems(u.EmployeeID)
		if err != nil {
			return nil, err
		}
		add("count_pending", "我的待盘点", n, "/count/mine", todoLevelWarning)
		// 我的报修待确认：repairScope 对 counter 收窄到 reporter_emp_id=me。
		if err := addRepair("confirming", "我的报修待确认", "/repairs?status=confirming", todoLevelWarning, model.RepairConfirming); err != nil {
			return nil, err
		}
	case model.RoleViewer:
		if err := addRepair("confirming", "我的报修待确认", "/repairs?status=confirming", todoLevelWarning, model.RepairConfirming); err != nil {
			return nil, err
		}
		if err := addRepair("in_progress", "我的报修处理中", "/repairs", todoLevelInfo, repairInProgressStatuses...); err != nil {
			return nil, err
		}
	}

	// 同步异常：仅持有 sync.manage 的角色（当前只有 admin）——服务端判定，不靠前端 v-if。
	if model.Can(u.Role, model.PermSyncManage) {
		n, err := s.st.SyncFailureCount()
		if err != nil {
			return nil, err
		}
		add("sync_failed", "同步异常", n, "/sync", todoLevelDanger)
	}

	return todos, nil
}

// dashboardStats 按角色算「与我相关」的指标卡。
//
// 与 dashboardTodos 的分工：todo 是「要你去做的事」（count=0 不出），
// stat 是「与你相关的量」——只要该角色适用就该出现，0 也出（「我的资产 0 台」是有意义的信息，
// 「待受理 0 单」则不是待办）。这就是设计宗旨里「第一屏 = 与我相关的事」：
// 缺了这组卡，员工首页只能拿 overview 的全量概览冒充（本次改动要修的正是这个）。
//
// 管理视角（admin / asset_manager）给全局量；员工视角一律按归属人收窄，绝不退化成全量。
// asset_total 复用已查好的 ov.AssetTotal，不重复查库。
func (s *Server) dashboardStats(u model.User, assetSc model.AssetScope, repairSc model.RepairScope,
	ov model.DashboardOverview) ([]model.DashboardStatCard, error) {
	stats := []model.DashboardStatCard{}
	add := func(key, label string, count int64, link string) {
		stats = append(stats, model.DashboardStatCard{Key: key, Label: label, Count: count, Link: link})
	}
	// addRepair 复用待办那套「按状态计数」的写法，只是 0 也出卡。
	addRepair := func(key, label, link string, statuses ...string) error {
		n, err := s.st.CountRepairsByStatuses(statuses, repairSc)
		if err != nil {
			return err
		}
		add(key, label, n, link)
		return nil
	}

	switch u.Role {
	case model.RoleAdmin, model.RoleAssetManager:
		// 管理视角：资产总数直接复用 overview 已查好的值（不重复查库）。
		add("asset_total", "资产总数", ov.AssetTotal, "/assets")
		if err := addRepair("pending", "待受理", "/repairs?status=pending", model.RepairPending); err != nil {
			return nil, err
		}
		if err := addRepair("unassigned", "待派工", "/repairs?status=accepted", model.RepairAccepted); err != nil {
			return nil, err
		}
		if err := addRepair("confirming", "待确认", "/repairs?status=confirming", model.RepairConfirming); err != nil {
			return nil, err
		}

	case model.RoleCounter, model.RoleDeptHead, model.RoleRepairTech, model.RoleViewer:
		// 员工视角：四个计数全部按归属人收窄（store 层 empID<=0 返回 0，不退化为全量）。
		n, err := s.st.AssetTotalByHolder(assetSc, u.EmployeeID)
		if err != nil {
			return nil, err
		}
		add("my_assets", "我的资产", n, "/assets")

		n, err = s.st.CountMyRepairsAsReporter(u.EmployeeID, repairSc)
		if err != nil {
			return nil, err
		}
		add("my_repairs", "我的报修", n, "/repairs")

		n, err = s.st.CountMyRepairsAsAssignee(u.EmployeeID, repairSc)
		if err != nil {
			return nil, err
		}
		add("my_tasks", "我的维修", n, "/repairs")

		n, err = s.st.PendingCountItems(u.EmployeeID)
		if err != nil {
			return nil, err
		}
		add("my_count", "我的待盘点", n, "/count/mine")
	}

	return stats, nil
}
