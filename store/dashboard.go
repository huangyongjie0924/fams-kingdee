package store

import (
	"fmt"
	"strings"

	"asset-mgr/model"
)

// 首页聚合（需求 B）。全部是只读聚合：走 InnoDB MVCC 一致性快照读（普通 SELECT），
// 不加行锁/表锁、不写任何表——与资产列表的读、与同步的写互不阻塞（架构文档 §2.3.1）。
//
// 统计口径（不可违反，见 §2.4）：资产状态分布一律用 display_status
// （= COALESCE(NULLIF(biz_status,''), status)）。禁止裸 status（它是星瀚托管列，
// 每日 00:00 被重置，用它统计「维修中」永远算不出来）；也禁止单用 biz_status
// （会漏掉在用/闲置/报废分布）。该表达式与 cardSelect / effectiveStatus / buildWhere 同源，
// 改一处必须同步改这里。
//
// 可见范围一律复用 scopeConds（资产）/ buildRepairWhere（维修单），不新写第二套范围逻辑。
//
// 性能：P0 不做缓存。227 行下每条全表聚合是亚毫秒级，缓存省不出时间，却引入第二份真相 +
// 陈旧窗口（违反设计宗旨原则 4）。评估缓存的阈值：asset_card 行数 > 5 万，或本接口
// p95 > 200ms——届时正确的解法是预聚合表 / 生成列 + 索引，而不是进程内缓存。

// dashboardCategoryTopN 限制分类分布返回的分组数，避免将来分类膨胀时响应体失控。
// 当前生产库约 10 个分类，20 足够。
const dashboardCategoryTopN = 20

// AssetTotal 返回可见范围内未软删的资产总数。
func (s *Store) AssetTotal(sc model.AssetScope) (int64, error) {
	conds, args := scopeConds(sc)
	var n int64
	q := "SELECT COUNT(*) FROM asset_card c WHERE " + strings.Join(conds, " AND ")
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count asset total: %w", err)
	}
	return n, nil
}

// AssetStatusCounts 按有效状态（display_status）分组计数，按数量倒序。
// 空串状态（status 也为空的极端行）会单独成组，不掩盖数据问题——与列表口径一致。
func (s *Store) AssetStatusCounts(sc model.AssetScope) ([]model.DashboardStatusCount, error) {
	conds, args := scopeConds(sc)
	q := `SELECT COALESCE(NULLIF(c.biz_status,''), c.status) AS display_status, COUNT(*) AS cnt
		FROM asset_card c WHERE ` + strings.Join(conds, " AND ") + `
		GROUP BY display_status ORDER BY cnt DESC, display_status`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("asset status counts: %w", err)
	}
	defer rows.Close()

	out := []model.DashboardStatusCount{}
	for rows.Next() {
		var it model.DashboardStatusCount
		if err := rows.Scan(&it.Status, &it.Count); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// AssetCategoryCounts 按分类名分组计数，按数量倒序，最多返回 dashboardCategoryTopN 组。
// 用 LEFT JOIN：悬空 category_id 或哨兵值 0 的卡归入「未设置」，不丢行。
func (s *Store) AssetCategoryCounts(sc model.AssetScope) ([]model.DashboardCategoryCount, error) {
	conds, args := scopeConds(sc)
	q := `SELECT COALESCE(cat.name, '未设置') AS name, COUNT(*) AS cnt
		FROM asset_card c
		LEFT JOIN asset_category cat ON cat.id = c.category_id
		WHERE ` + strings.Join(conds, " AND ") + `
		GROUP BY name ORDER BY cnt DESC, name LIMIT ?`
	args = append(args, dashboardCategoryTopN)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("asset category counts: %w", err)
	}
	defer rows.Close()

	out := []model.DashboardCategoryCount{}
	for rows.Next() {
		var it model.DashboardCategoryCount
		if err := rows.Scan(&it.Name, &it.Count); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// RepairStatusCounts 按维修单自身状态分组计数（口径是 repair_order.status，与卡片状态无关），
// 在可见范围内收窄，按数量倒序。Label 由 RepairStatusLabel 派生（不落库）。
func (s *Store) RepairStatusCounts(sc model.RepairScope) ([]model.DashboardStatusCount, error) {
	where, args := buildRepairWhere(model.RepairListQuery{}, sc)
	q := "SELECT status, COUNT(*) FROM repair_order" + where + " GROUP BY status ORDER BY COUNT(*) DESC, status"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("repair status counts: %w", err)
	}
	defer rows.Close()

	out := []model.DashboardStatusCount{}
	for rows.Next() {
		var it model.DashboardStatusCount
		if err := rows.Scan(&it.Status, &it.Count); err != nil {
			return nil, err
		}
		it.Label = model.RepairStatusLabel(it.Status)
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountRepairsByStatuses 统计可见范围内命中任一给定状态的维修单数（供首页待办计数）。
// 复用 buildRepairWhere 的状态筛选 + 可见范围收窄，不新写条件。
func (s *Store) CountRepairsByStatuses(statuses []string, sc model.RepairScope) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	where, args := buildRepairWhere(model.RepairListQuery{Status: statuses}, sc)
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM repair_order"+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count repairs by statuses: %w", err)
	}
	return n, nil
}

// AssetTotalByHolder 统计「这个人名下」的资产数：user_emp_id = empID，且在可见范围内。
//
// empID <= 0 一律返回 0，绝不退化成全量：账号没绑员工时「与我相关的资产」正确答案是 0，
// 返回全量会让员工首页变成管理员首页（正是本次改动要修的毛病）。
// 可见范围复用 scopeConds，与列表/总数同一口径。
func (s *Store) AssetTotalByHolder(sc model.AssetScope, empID int64) (int64, error) {
	if empID <= 0 {
		return 0, nil
	}
	conds, args := scopeConds(sc)
	conds = append(conds, "c.user_emp_id = ?")
	args = append(args, empID)

	var n int64
	q := "SELECT COUNT(*) FROM asset_card c WHERE " + strings.Join(conds, " AND ")
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count assets by holder: %w", err)
	}
	return n, nil
}

// repairOwnerColumns 是 countMyRepairsByOwner 允许的归属列白名单。
// 列名无法作为 SQL 参数绑定，只能拼进语句，故用白名单把取值钉死在这两列上。
var repairOwnerColumns = map[string]bool{
	"reporter_emp_id": true,
	"assignee_emp_id": true,
}

// countMyRepairsByOwner 数「归属人是 empID、且还在途」的维修单：
// 归属列由 ownerCol 指定，在途 = status NOT IN（三个终态），可见范围复用 repairScopeConds。
//
// 两个对外方法（报修人视角 / 维修工视角）只差归属列，故共用一个实现，不复制两份 SQL。
func (s *Store) countMyRepairsByOwner(ownerCol string, empID int64, sc model.RepairScope) (int64, error) {
	if empID <= 0 {
		return 0, nil
	}
	if !repairOwnerColumns[ownerCol] {
		return 0, fmt.Errorf("unsupported repair owner column: %q", ownerCol)
	}

	conds := []string{"1=1"}
	args := []any{}
	scConds, scArgs := repairScopeConds(sc)
	conds = append(conds, scConds...)
	args = append(args, scArgs...)
	conds = append(conds, ownerCol+" = ?")
	args = append(args, empID)
	conds = append(conds, "status NOT IN ("+placeholders(len(model.RepairTerminalStatuses))+")")
	for _, st := range model.RepairTerminalStatuses {
		args = append(args, st)
	}

	var n int64
	q := "SELECT COUNT(*) FROM repair_order WHERE " + strings.Join(conds, " AND ")
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count my repairs by %s: %w", ownerCol, err)
	}
	return n, nil
}

// CountMyRepairsAsReporter 我报修的、尚未结束的维修单数（员工视角「我的报修」）。
// empID <= 0 返回 0，不退化为全量。
func (s *Store) CountMyRepairsAsReporter(empID int64, sc model.RepairScope) (int64, error) {
	return s.countMyRepairsByOwner("reporter_emp_id", empID, sc)
}

// CountMyRepairsAsAssignee 派给我、尚未结束的维修单数（维修工视角「我的维修」）。
// empID <= 0 返回 0，不退化为全量。
func (s *Store) CountMyRepairsAsAssignee(empID int64, sc model.RepairScope) (int64, error) {
	return s.countMyRepairsByOwner("assignee_emp_id", empID, sc)
}

// PendingCountItems 盘点员「我的待盘点」：指派给我、且尚未录入结果的盘点明细。
//
// 这是**专用窄化**，刻意不复用 asset/repair scope——count_item 不在这两套范围里
// （架构文档 §2.5）。条件 `assignee_id = ? AND result = ''` 走 idx_count_item_assignee。
func (s *Store) PendingCountItems(assigneeID int64) (int64, error) {
	if assigneeID <= 0 {
		return 0, nil
	}
	var n int64
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM count_item WHERE assignee_id = ? AND result = ''", assigneeID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending count items: %w", err)
	}
	return n, nil
}

// SyncFailureCount 同步异常：失败 / 部分失败的跑批数。仅持有 sync.manage 的角色调用
// （当前只有 admin，门控在 api 层按 Can(role, sync.manage) 判定）。
// partial 表示该批有部分行失败（failed_count > 0），同样需要管理员关注，故一并计入。
func (s *Store) SyncFailureCount() (int64, error) {
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sync_run WHERE status IN (?, ?)",
		model.SyncStatusFailed, model.SyncStatusPartial).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sync failures: %w", err)
	}
	return n, nil
}
