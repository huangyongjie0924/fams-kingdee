package store

import (
	"testing"

	"asset-mgr/model"
)

// TestRepairWorkflowAgainstRealDB 端到端跑一遍 P0 闭环，验证状态机与 biz_status 的原子联动。
//
// 为什么必须连真库：单测能验状态机表与优先级函数，但「单据跃迁 + asset_card.biz_status
// 在同一事务内落库」「cardSelect 增列后 scanCard 列数对得上」「buildWhere 按有效状态筛得出」
// 这几条横跨 store 与 SQL，只有真库能回答。
//
// 安全性：建一张编码固定的探针卡，跑完后把探针卡及其维修单 / 流转记录 / 履历全部删掉；
// 开头先清理一次，兜住上一次异常退出留下的残留。整个测试自清理，库里不留痕迹。
func TestRepairWorkflowAgainstRealDB(t *testing.T) {
	dsn := testDSN(t)
	st, err := New(dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	const code = "ZZ-REPAIR-E2E-0001"

	cleanup := func() {
		var cardID int64
		_ = st.db.QueryRow("SELECT id FROM asset_card WHERE asset_code = ?", code).Scan(&cardID)
		if cardID == 0 {
			return
		}
		rows, err := st.db.Query("SELECT id FROM repair_order WHERE card_id = ?", cardID)
		if err == nil {
			var ids []int64
			for rows.Next() {
				var id int64
				_ = rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
			for _, id := range ids {
				_, _ = st.db.Exec("DELETE FROM doc_status_log WHERE doc_type = ? AND doc_id = ?", model.DocTypeRepair, id)
				_, _ = st.db.Exec("DELETE FROM repair_attachment WHERE repair_id = ?", id)
			}
		}
		_, _ = st.db.Exec("DELETE FROM repair_order WHERE card_id = ?", cardID)
		_, _ = st.db.Exec("DELETE FROM asset_history WHERE card_id = ?", cardID)
		_, _ = st.db.Exec("DELETE FROM asset_card WHERE id = ?", cardID)
	}
	cleanup()
	defer cleanup()

	res, err := st.db.Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, created_by)
		VALUES (?, ?, ?, '', 'e2e')`, code, "维修流程探针", model.StatusInUse)
	if err != nil {
		t.Fatalf("建探针卡失败: %v", err)
	}
	cardID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("取探针卡 id 失败: %v", err)
	}
	card, err := st.GetCard(cardID)
	if err != nil || card == nil {
		t.Fatalf("读回探针卡失败: %v", err)
	}
	if card.DisplayStatus != model.StatusInUse {
		t.Fatalf("初始有效状态应为「在用」，实际 %q", card.DisplayStatus)
	}

	// —— 提交报修 ——
	order, err := st.CreateRepair(card, RepairCreateInput{
		ReporterEmpID: 1, ReporterName: "张三", FaultDesc: "开机不制冷", Urgency: model.RepairUrgencyHigh, CreatedBy: "e2e",
	})
	if err != nil {
		t.Fatalf("提交报修失败: %v", err)
	}
	if order.Status != model.RepairPending {
		t.Fatalf("初始状态应为 pending，实际 %q", order.Status)
	}
	if len(order.Code) < 3 || order.Code[:2] != "WX" {
		t.Fatalf("单号应以 WX 开头，实际 %q", order.Code)
	}
	// 报修不该改资产卡状态（避免刚报修就把资产锁死）
	if c, _ := st.GetCard(cardID); c.BizStatus != "" {
		t.Fatalf("待受理阶段不该写 biz_status，实际 %q", c.BizStatus)
	}

	// —— 受理 → 派工 ——
	if _, err := st.Transition(order.ID, RepairTransitionInput{Action: model.RepairActionAccept, Operator: "管理员"}); err != nil {
		t.Fatalf("受理失败: %v", err)
	}
	if _, err := st.Transition(order.ID, RepairTransitionInput{
		Action: model.RepairActionDispatch, Operator: "管理员", AssigneeEmpID: 7, AssigneeName: "李师傅",
	}); err != nil {
		t.Fatalf("派工失败: %v", err)
	}

	// —— 接单 → 进入维修中，biz_status 应置「维修中」——
	if _, err := st.Transition(order.ID, RepairTransitionInput{Action: model.RepairActionTake, Operator: "李师傅"}); err != nil {
		t.Fatalf("接单失败: %v", err)
	}
	c, _ := st.GetCard(cardID)
	if c.BizStatus != model.StatusRepair || c.DisplayStatus != model.StatusRepair {
		t.Fatalf("进入维修中后 biz_status/display_status 应为「维修中」，实际 %q / %q", c.BizStatus, c.DisplayStatus)
	}

	// 按有效状态筛选「维修中」应能筛到这张卡（buildWhere 的 COALESCE 生效）
	lr, err := st.ListCards(model.ListQuery{Status: []string{model.StatusRepair}, Page: 1, PageSize: 50}, model.AssetScope{})
	if err != nil {
		t.Fatalf("按状态筛选失败: %v", err)
	}
	found := false
	for _, it := range lr.Items {
		if it.ID == cardID {
			found = true
		}
	}
	if !found {
		t.Fatalf("按「维修中」筛选应能筛到探针卡（有效状态筛选）")
	}

	// —— 报完工 → 离开维修中，biz_status 应清空 ——
	if _, err := st.Transition(order.ID, RepairTransitionInput{
		Action: model.RepairActionFinish, Operator: "李师傅", HandlerDesc: "更换压缩机",
	}); err != nil {
		t.Fatalf("报完工失败: %v", err)
	}
	c, _ = st.GetCard(cardID)
	if c.BizStatus != "" {
		t.Fatalf("离开维修中后 biz_status 应清空，实际 %q", c.BizStatus)
	}
	if c.DisplayStatus != model.StatusInUse {
		t.Fatalf("清空后有效状态应回落到「在用」，实际 %q", c.DisplayStatus)
	}

	// —— 确认完工 → 终态 ——
	final, err := st.Transition(order.ID, RepairTransitionInput{Action: model.RepairActionConfirm, Operator: "张三"})
	if err != nil {
		t.Fatalf("确认完工失败: %v", err)
	}
	if final.Status != model.RepairDone {
		t.Fatalf("确认后应为 done，实际 %q", final.Status)
	}

	// —— 非法跃迁防护：终态不可再流转 ——
	if _, err := st.Transition(order.ID, RepairTransitionInput{Action: model.RepairActionTake, Operator: "李师傅"}); err == nil {
		t.Fatalf("终态后再接单应被拒绝，实际通过了")
	}

	// —— 留痕：submit/accept/dispatch/take/finish/confirm 共 6 条 ——
	logs, err := st.RepairLogs(order.ID)
	if err != nil {
		t.Fatalf("读流转记录失败: %v", err)
	}
	if len(logs) != 6 {
		t.Fatalf("流转记录应为 6 条，实际 %d 条", len(logs))
	}

	// —— 资产履历：进入维修中写一条 action=repair ——
	hist, err := st.ListHistory(cardID)
	if err != nil {
		t.Fatalf("读履历失败: %v", err)
	}
	repairHist := 0
	for _, h := range hist {
		if h.Action == "repair" {
			repairHist++
		}
	}
	if repairHist != 1 {
		t.Fatalf("资产履历里 action=repair 应恰好 1 条，实际 %d 条", repairHist)
	}

	// —— 资产卡维修记录 ——
	byCard, err := st.ListRepairsByCard(cardID)
	if err != nil {
		t.Fatalf("读资产卡维修记录失败: %v", err)
	}
	if len(byCard) != 1 {
		t.Fatalf("资产卡应关联 1 条维修记录，实际 %d 条", len(byCard))
	}
}
