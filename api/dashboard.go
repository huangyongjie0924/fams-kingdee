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
// 两个不在 scope 内的数据源按 §2.5 处理：sync_run 用 CanAny(roles, sync.manage) 门控，
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
	// IT 台账可见范围直接从已算好的 assetSc 派生（itScopeOf），**不再调一次 assetScope**——
	// 那会让 employee 账号把 ListOpenRepairCardIDs 跑第二遍（见 api/scope.go 的 itScope 注释）。
	// stats 复用已算好的 overview（asset_total 不再查一次库）。
	stats, err := s.dashboardStats(u, assetSc, repairSc, itScopeOf(assetSc), overview)
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
	// seen 按 key 去重：多角色下不同角色可能产出同一个 key（如 counter 与 employee 都有
	// 「我的报修待确认」），先到先得，只加一次（顺序即下方分支的书写顺序）。
	seen := map[string]bool{}
	add := func(key, label string, count int64, link, level string) {
		if count > 0 && !seen[key] {
			seen[key] = true
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

	// 多角色（一人可多角色）：待办按「任一角色允许就包含」的并集语义组装，
	// 同一 key 只加一次（见上 add 的 seen）。
	//
	// ⚠️ 管理视角（admin / asset_manager）与个人向待办**互斥、不叠加**：
	// 个人向待办的文案（「派给我的」「我的报修待确认」）只有在账号可见范围被收窄到本人时才成立；
	// 而 admin / asset_manager 的 repairScope 不限，若叠加，会把**全局**计数贴上「我的」标签
	// （尤其 admin 通常未绑定员工、employee_id=0）。故集合里含管理角色时，只出管理向待办。
	//
	// ⚠️ it_admin（IT 管理员，2026-10-10 新增）在这里**刻意没有任何分支**：
	// 它只持有 it.manage，没有任何维修侧权限，也没有 dept_head / maintainer / counter /
	// employee 这些个人向维度，故落进 default 后一个 add 都不会命中，待办为空。
	// 这是正确结果（IT 台账的维护不产生「待办」语义——没有时限、没有球在谁手上的概念），
	// 不是漏配。若将来要给 it_admin 加待办，必须在这里显式加分支并配一条守卫测试。
	roles := u.EffectiveRoles()
	switch {
	case model.HasRole(roles, model.RoleAdmin) || model.HasRole(roles, model.RoleAssetManager):
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
	default:
		// 无管理角色：按集合里出现的角色累加各自的区块（顺序即优先级：dept_head > maintainer > counter > employee）。
		if model.HasRole(roles, model.RoleDeptHead) {
			// 部门主管：repairScope 已收窄到本部门（含下级），故 pending 即「本部门待受理」。
			if err := addRepair("pending", "本部门待受理", "/repairs?status=pending", todoLevelDanger, model.RepairPending); err != nil {
				return nil, err
			}
		}
		if model.HasRole(roles, model.RoleMaintainer) {
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
		}
		if model.HasRole(roles, model.RoleCounter) {
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
		}
		if model.HasRole(roles, model.RoleEmployee) {
			if err := addRepair("confirming", "我的报修待确认", "/repairs?status=confirming", todoLevelWarning, model.RepairConfirming); err != nil {
				return nil, err
			}
			if err := addRepair("in_progress", "我的报修处理中", "/repairs", todoLevelInfo, repairInProgressStatuses...); err != nil {
				return nil, err
			}
		}
	}

	// 同步异常：仅持有 sync.manage 的角色（当前只有 admin）——服务端判定，不靠前端 v-if。
	if model.CanAny(u.EffectiveRoles(), model.PermSyncManage) {
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
	itSc model.ITScope, ov model.DashboardOverview) ([]model.DashboardStatCard, error) {
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

	// 多角色（一人可多角色）：指标卡按**并集**组装——任一角色适用就出对应的卡。
	// 两组卡的 key 不重叠（管理向：asset_total/pending/unassigned/confirming；
	// 个人向：my_assets/my_repairs/my_tasks/my_count），故两个 if 相互独立、可同时命中。
	//
	// 与待办不同，个人向指标卡**不依赖账号的可见范围收窄**：它们用 empID 显式收窄
	// （AssetTotalByHolder/CountMyRepairsAsReporter/… 在 empID<=0 时返回 0），
	// 因此即便账号同时持有 asset_manager，也不会把全局量误当「我的」。
	roles := u.EffectiveRoles()
	if model.HasRole(roles, model.RoleAdmin) || model.HasRole(roles, model.RoleAssetManager) {
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
	}
	if model.HasRole(roles, model.RoleCounter) || model.HasRole(roles, model.RoleDeptHead) ||
		model.HasRole(roles, model.RoleMaintainer) || model.HasRole(roles, model.RoleEmployee) {
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

	// IT 设备台账指标卡（2026-10-10 需求）。
	//
	// 门控用 **权限位** 而不是角色名：这样「谁能维护 IT 台账」这件事只有一个定义处
	// （model.rolePermissions），将来把 it.manage 授予别的角色时，首页卡片自动跟上，
	// 不会出现「新角色能维护但没有首页入口」这种半截状态。admin 由 Can 兜底恒真。
	//
	// 三张卡都直接回答用户提出的三个管理问题：
	//   设备总量多少 / 其中信创多少 / 有多少台在资产卡上根本查不到。
	// 第三张是用户明确点出的盲区（「资产卡上无的设备」），故单独出卡而不是埋在筛选里。
	if model.CanAny(u.EffectiveRoles(), model.PermITManage) {
		total, xinchuang, unlinked, err := s.st.ITEquipmentCount(itSc)
		if err != nil {
			return nil, err
		}
		add("it_total", "IT 设备总数", total, "/it-equipment")
		add("it_xinchuang", "信创设备", xinchuang, "/it-equipment?is_xinchuang=1")
		add("it_unlinked", "无资产卡设备", unlinked, "/it-equipment?linked=0")
	}

	return stats, nil
}
