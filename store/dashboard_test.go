package store

import (
	"fmt"
	"testing"
	"time"

	"asset-mgr/model"
)

// TestDashboardAggregatesAgainstRealDB 钉死需求 B 首页聚合的两条硬约束：
//   ① 统计口径用 display_status（COALESCE(NULLIF(biz_status,''),status)），不是裸 status；
//   ② 可见范围（scopeConds / buildRepairWhere）在 WHERE 收窄，不同 scope 返回不同范围。
//
// 为什么必须连真库：GROUP BY COALESCE(...) 的表达式口径、LEFT JOIN 分类归组、
// 以及 scope 条件拼进 WHERE 后是否正确收窄，都是「横跨 SQL 与 Go」的行为，单测回答不了。
//
// 自清理：探针数据用 ZZ-DASH- 前缀与专用探针 dept/emp id（9 亿段，不会与真实数据撞），
// 跑完连同 repair_order / count_item / sync_run 一并删除，生产库不留痕。
func TestDashboardAggregatesAgainstRealDB(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	ns := time.Now().UnixNano()
	code := fmt.Sprintf("ZZ-DASH-%d", ns)
	probeDept := int64(900000000 + ns%100000000)
	probeReporter := int64(910000000 + ns%100000000)
	probeAssignee := int64(920000000 + ns%100000000)

	// 取一个真实分类，验证分类分布能带出分类名。
	var catID int64
	var catName string
	if err := st.db.QueryRow("SELECT id, name FROM asset_category ORDER BY id LIMIT 1").Scan(&catID, &catName); err != nil {
		t.Skipf("库里没有分类，跳过: %v", err)
	}

	cleanup := func() {
		var cardID int64
		_ = st.db.QueryRow("SELECT id FROM asset_card WHERE asset_code = ?", code).Scan(&cardID)
		if cardID != 0 {
			_, _ = st.db.Exec("DELETE FROM repair_order WHERE card_id = ?", cardID)
			_, _ = st.db.Exec("DELETE FROM asset_history WHERE card_id = ?", cardID)
			_, _ = st.db.Exec("DELETE FROM asset_card WHERE id = ?", cardID)
		}
		_, _ = st.db.Exec("DELETE FROM count_item WHERE asset_code LIKE ?", code+"%")
		_, _ = st.db.Exec("DELETE FROM sync_run WHERE triggered_by = ?", code)
	}
	cleanup()
	defer cleanup()

	// —— 探针卡：status='在用' 但 biz_status='维修中'（模拟单据驱动的维修中）——
	// 裸 status 会把它算成「在用」；display_status 口径必须算成「维修中」。
	res, err := st.db.Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, use_dept_id, created_by)
		VALUES (?, ?, ?, ?, ?, ?, 'dash')`, code, "首页聚合探针", model.StatusInUse, model.StatusRepair, catID, probeDept)
	if err != nil {
		t.Fatalf("建探针卡失败: %v", err)
	}
	cardID, _ := res.LastInsertId()

	// —— ① 口径：维修中必须出现（裸 status 口径下会变成「在用」）——
	scoped := model.AssetScope{DeptIDs: []int64{probeDept}}
	statusCounts, err := st.AssetStatusCounts(scoped)
	if err != nil {
		t.Fatalf("资产状态分布失败: %v", err)
	}
	if len(statusCounts) != 1 || statusCounts[0].Status != model.StatusRepair || statusCounts[0].Count != 1 {
		t.Fatalf("状态分布应为 [{维修中,1}]（display_status 口径），实际 %+v", statusCounts)
	}

	// 分类分布应带出真实分类名。
	catCounts, err := st.AssetCategoryCounts(scoped)
	if err != nil {
		t.Fatalf("分类分布失败: %v", err)
	}
	if len(catCounts) != 1 || catCounts[0].Name != catName || catCounts[0].Count != 1 {
		t.Fatalf("分类分布应为 [{%s,1}]，实际 %+v", catName, catCounts)
	}

	// 总数：限定到探针部门应为 1。
	if n, err := st.AssetTotal(scoped); err != nil || n != 1 {
		t.Fatalf("探针部门资产总数应为 1，实际 %d err=%v", n, err)
	}

	// —— ② 越权收窄：用不存在的使用人 id 收窄应为 0（探针卡 user_emp_id=0，被排除）——
	if n, err := st.AssetTotal(model.AssetScope{SelfEmpID: 999999999}); err != nil || n != 0 {
		t.Fatalf("按不存在的使用人收窄，资产总数应为 0，实际 %d err=%v", n, err)
	}

	// —— 维修单聚合：可见范围收窄 ——
	if _, err := st.db.Exec(`INSERT INTO repair_order
		(card_id, asset_code, asset_name, reporter_emp_id, reporter_name, use_dept_id, use_dept_name,
		 fault_desc, urgency, status, assignee_emp_id, assignee_name, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'normal', ?, ?, ?, 'dash')`,
		cardID, code, "首页聚合探针", probeReporter, "探针报修人", probeDept, "探针部门",
		"探针故障", model.RepairPending, probeAssignee, "探针维修工"); err != nil {
		t.Fatalf("建探针维修单失败: %v", err)
	}

	repairCounts, err := st.RepairStatusCounts(model.RepairScope{DeptIDs: []int64{probeDept}})
	if err != nil {
		t.Fatalf("维修单状态分布失败: %v", err)
	}
	if len(repairCounts) != 1 || repairCounts[0].Status != model.RepairPending || repairCounts[0].Count != 1 {
		t.Fatalf("维修单状态分布应为 [{pending,待受理,1}]，实际 %+v", repairCounts)
	}
	if repairCounts[0].Label != model.RepairStatusLabel(model.RepairPending) {
		t.Errorf("维修单状态应带中文 label，实际 %q", repairCounts[0].Label)
	}

	// 命中三种 scope 口径各应为 1；不相关 scope 应为 0（越权检查）。
	cases := []struct {
		name string
		sc   model.RepairScope
		want int64
	}{
		{"部门主管（本部门子树）", model.RepairScope{DeptIDs: []int64{probeDept}}, 1},
		{"报修人（自己提交）", model.RepairScope{ReporterEmpID: probeReporter}, 1},
		{"维修工（指派给我）", model.RepairScope{AssigneeEmpID: probeAssignee}, 1},
		{"无关报修人", model.RepairScope{ReporterEmpID: probeReporter + 1}, 0},
		{"无关维修工", model.RepairScope{AssigneeEmpID: probeAssignee + 1}, 0},
		{"无关部门", model.RepairScope{DeptIDs: []int64{probeDept + 1}}, 0},
	}
	for _, c := range cases {
		n, err := st.CountRepairsByStatuses([]string{model.RepairPending}, c.sc)
		if err != nil {
			t.Fatalf("%s 计数失败: %v", c.name, err)
		}
		if n != c.want {
			t.Errorf("%s 待受理计数应为 %d，实际 %d", c.name, c.want, n)
		}
	}

	// —— count_item 专用窄化：assignee_id=me AND result='' ——
	if _, err := st.db.Exec(`INSERT INTO count_item (plan_id, card_id, asset_code, name, assignee_id, result)
		VALUES (0, ?, ?, '首页盘点探针', ?, '')`, cardID, code, probeAssignee); err != nil {
		t.Fatalf("建探针盘点明细失败: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO count_item (plan_id, card_id, asset_code, name, assignee_id, result)
		VALUES (0, ?, ?, '首页盘点探针-已盘', ?, ?)`, cardID, code+"-2", probeAssignee, model.CountResultNormal); err != nil {
		t.Fatalf("建已盘探针明细失败: %v", err)
	}
	if n, err := st.PendingCountItems(probeAssignee); err != nil || n != 1 {
		t.Fatalf("待盘点应为 1（只数 result='' 的），实际 %d err=%v", n, err)
	}
	if n, err := st.PendingCountItems(probeAssignee + 1); err != nil || n != 0 {
		t.Fatalf("无关盘点员的待盘点应为 0，实际 %d err=%v", n, err)
	}

	// —— sync_run：失败/部分失败计入同步异常 ——
	before, err := st.SyncFailureCount()
	if err != nil {
		t.Fatalf("同步异常计数失败: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO sync_run (source, resource, mode, triggered_by, status)
		VALUES ('kingdee', 'asset_card', 'incremental', ?, ?)`, code, model.SyncStatusFailed); err != nil {
		t.Fatalf("建探针同步记录失败: %v", err)
	}
	after, err := st.SyncFailureCount()
	if err != nil {
		t.Fatalf("同步异常计数失败: %v", err)
	}
	if after != before+1 {
		t.Errorf("插入一条 failed 同步记录后，同步异常计数应 +1（%d→%d），实际 %d", before, before+1, after)
	}
}
