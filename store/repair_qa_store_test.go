package store

import (
	"fmt"
	"testing"
	"time"

	"asset-mgr/model"
)

// 本文件是 QA（严过关）独立验证用的集成测试，连真库跑，自带清理。
// 与工程师的 repair_e2e_test.go 相互独立：不共用任何 fixture，探针数据用 QA- 前缀，
// 便于在库里一眼区分「QA 造的」与「工程师造的」，也便于清理残留。

const qaCardPrefix = "QA-REPAIR-"

// qaCleanupCard 删除一张 QA 探针卡及其全部从属数据（维修单 / 流转记录 / 附件 / 履历）。
func qaCleanupCard(t *testing.T, st *Store, code string) {
	t.Helper()
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

func qaNewCard(t *testing.T, st *Store, code string, bizStatus string) *model.AssetCard {
	t.Helper()
	_, err := st.db.Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, created_by)
		VALUES (?, ?, ?, ?, 'qa')`, code, "QA 探针卡", model.StatusInUse, bizStatus)
	if err != nil {
		t.Fatalf("建探针卡失败: %v", err)
	}
	c, err := st.GetCardByCode(code)
	if err != nil || c == nil {
		t.Fatalf("读回探针卡失败: %v", err)
	}
	return c
}

// TestQASyncIsolationOfBizStatus 是本次最高风险项（R1）的实测：
// 直接调用**真实同步写路径** ApplyOwnedCardTx（非 dry-run），验证
//   1) biz_status 不被同步改动、也不被清空；
//   2) status（星瀚托管列）按设计被同步覆盖。
// 用一张 QA 探针卡做，不碰任何真实业务卡。
func TestQASyncIsolationOfBizStatus(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	code := qaCardPrefix + fmt.Sprintf("SYNC-%d", time.Now().UnixNano())
	qaCleanupCard(t, st, code)
	defer qaCleanupCard(t, st, code)

	// 造一张「维修中」的卡：biz_status 非空、status 为在用。
	card := qaNewCard(t, st, code, model.StatusRepair)
	if card.BizStatus != model.StatusRepair || card.DisplayStatus != model.StatusRepair {
		t.Fatalf("前置条件不满足：biz_status=%q display_status=%q", card.BizStatus, card.DisplayStatus)
	}

	// 构造一张「来自星瀚」的卡：状态给「闲置」，并改一个托管字段（name）。
	src := &model.AssetCard{
		AssetCode: code,
		Name:      "星瀚同步改名",
		Status:    model.StatusIdle,
		Unit:      "台",
		Quantity:  1,
		Location:  "星瀚同步位置",
		Source:    model.SourceKingdeeSync,
	}

	// 走真实写路径（dryRun=false）。
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	_, action, err := ApplyOwnedCardTx(tx, src, "qa-sync", false, nil)
	if err != nil {
		tx.Rollback()
		t.Fatalf("同步落库失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	got, err := st.GetCardByCode(code)
	if err != nil || got == nil {
		t.Fatalf("同步后读回失败: %v", err)
	}
	t.Logf("同步 action=%s；同步后 status=%q biz_status=%q display_status=%q name=%q",
		action, got.Status, got.BizStatus, got.DisplayStatus, got.Name)

	if got.BizStatus != model.StatusRepair {
		t.Errorf("❌ R1 未通过：同步后 biz_status 应仍为「维修中」，实际 %q", got.BizStatus)
	}
	if got.DisplayStatus != model.StatusRepair {
		t.Errorf("❌ 同步后 display_status 应仍为「维修中」，实际 %q", got.DisplayStatus)
	}
	if got.Status != model.StatusIdle {
		t.Errorf("status 是星瀚托管列，应被同步覆盖为「闲置」，实际 %q", got.Status)
	}
	if got.Name != "星瀚同步改名" {
		t.Errorf("托管字段 name 应被同步覆盖，实际 %q", got.Name)
	}
}

// TestQADisplayStatusConsistency 验证 display_status 单点收敛：造一张 biz_status='维修中'
// 的卡，四处口径应一致——详情（GetCard）、列表（ListCards）、按状态筛选、状态排序。
func TestQADisplayStatusConsistency(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	code := qaCardPrefix + fmt.Sprintf("DISP-%d", time.Now().UnixNano())
	qaCleanupCard(t, st, code)
	defer qaCleanupCard(t, st, code)

	card := qaNewCard(t, st, code, model.StatusRepair)

	// ① 详情
	if card.DisplayStatus != model.StatusRepair {
		t.Errorf("① 详情 display_status 应为「维修中」，实际 %q", card.DisplayStatus)
	}

	// ② 列表（按关键字定位到这张卡）
	lr, err := st.ListCards(model.ListQuery{Keyword: code, Page: 1, PageSize: 10}, model.AssetScope{})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	found := false
	for _, it := range lr.Items {
		if it.ID == card.ID {
			found = true
			if it.DisplayStatus != model.StatusRepair {
				t.Errorf("② 列表 display_status 应为「维修中」，实际 %q", it.DisplayStatus)
			}
		}
	}
	if !found {
		t.Errorf("② 列表应能查到探针卡")
	}

	// ③ 按「维修中」筛选
	fr, err := st.ListCards(model.ListQuery{Status: []string{model.StatusRepair}, Page: 1, PageSize: 200}, model.AssetScope{})
	if err != nil {
		t.Fatalf("按状态筛选失败: %v", err)
	}
	hit := false
	for _, it := range fr.Items {
		if it.ID == card.ID {
			hit = true
			if it.DisplayStatus != model.StatusRepair {
				t.Errorf("③ 筛出的行 display_status 应为「维修中」，实际 %q", it.DisplayStatus)
			}
		}
	}
	if !hit {
		t.Errorf("③ 按「维修中」筛选应能筛到探针卡（buildWhere 的 COALESCE 未生效？）")
	}

	// ④ 反向：按「在用」筛选，这张卡（biz_status=维修中）不应出现
	ir, err := st.ListCards(model.ListQuery{Status: []string{model.StatusInUse}, Page: 1, PageSize: 200}, model.AssetScope{})
	if err != nil {
		t.Fatalf("按在用筛选失败: %v", err)
	}
	for _, it := range ir.Items {
		if it.ID == card.ID {
			t.Errorf("④ biz_status='维修中' 的卡不该出现在「在用」筛选里")
		}
	}
}

// TestQAStoreLevelIllegalTransitions 直接打在 store 层：非法跃迁必须被 Transition 拒绝。
// 这是「很多实现在 store 层没有跃迁校验」的高风险点。
func TestQAStoreLevelIllegalTransitions(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	code := qaCardPrefix + fmt.Sprintf("ILLEGAL-%d", time.Now().UnixNano())
	qaCleanupCard(t, st, code)
	defer qaCleanupCard(t, st, code)

	card := qaNewCard(t, st, code, "")
	order, err := st.CreateRepair(card, RepairCreateInput{
		ReporterEmpID: 9001, ReporterName: "QA员工", FaultDesc: "非法跃迁用例", CreatedBy: "qa",
	})
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}

	// 待受理（pending）下，这些动作都必须被拒
	for _, act := range []string{
		model.RepairActionTake,     // 跳过受理/派工直接接单
		model.RepairActionFinish,   // 跳过接单直接报完工
		model.RepairActionDispatch, // 跳过受理直接派工
		model.RepairActionConfirm,  // 跳过全程直接确认
		model.RepairActionReturn,   // 未到待确认不能退回
	} {
		if _, err := st.Transition(order.ID, RepairTransitionInput{Action: act, Operator: "qa"}); err == nil {
			t.Errorf("❌ 非法跃迁未被拒绝：pending 状态下 action=%s 竟然通过", act)
		} else {
			t.Logf("pending + %-9s → 正确拒绝: %v", act, err)
		}
	}

	// 正常走到已完工
	must := func(in RepairTransitionInput) {
		if _, err := st.Transition(order.ID, in); err != nil {
			t.Fatalf("合法跃迁 %s 失败: %v", in.Action, err)
		}
	}
	must(RepairTransitionInput{Action: model.RepairActionAccept, Operator: "qa"})
	must(RepairTransitionInput{Action: model.RepairActionDispatch, Operator: "qa", AssigneeEmpID: 9002, AssigneeName: "QA维修工"})
	must(RepairTransitionInput{Action: model.RepairActionTake, Operator: "qa"})
	must(RepairTransitionInput{Action: model.RepairActionFinish, Operator: "qa", HandlerDesc: "修好了"})
	must(RepairTransitionInput{Action: model.RepairActionConfirm, Operator: "qa"})

	final, _ := st.GetRepair(order.ID)
	if final.Status != model.RepairDone {
		t.Fatalf("应到达已完工，实际 %q", final.Status)
	}
	// 终态（已完工）后再流转必须被拒
	if _, err := st.Transition(order.ID, RepairTransitionInput{Action: model.RepairActionTake, Operator: "qa"}); err == nil {
		t.Errorf("❌ 已完工后再接单应被拒")
	}
	if _, err := st.Transition(order.ID, RepairTransitionInput{Action: model.RepairActionAccept, Operator: "qa"}); err == nil {
		t.Errorf("❌ 已完工后再受理应被拒")
	}
}

// TestQADataHygiene 检查维修三表里是否残留测试数据（工程师的 E2E 或历史跑测留下的）。
// 本测试只读，不修改任何数据；发现「已过宽限期」的残留即 fail（真正的泄漏）。
//
// ⚠️ 宽限期（grace window）说明：`go test ./...` 会**并行**跑各包。api 包的探针卡与本包
// 共用 `QA-REPAIR-` 前缀，若不做时间过滤，本测试会在 api 包用例「进行中」时扫到它尚未
// 清理的探针卡，产生**假红**（跨包并发假阳性，非真泄漏）。因此所有「疑似残留」计数只统计
// 创建于 90 秒前（远早于任何单次用例的生命周期）的行——真正的历史残留必落在窗口之外。
func TestQADataHygiene(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	// 宽限期：只把「创建于 90 秒前」的行计入残留，避开并行包的在途探针。
	const grace = "created_at < (NOW() - INTERVAL 90 SECOND)"

	var nRepair, nRepairE2E int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM repair_order").Scan(&nRepair)
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM repair_order
		WHERE (created_by IN ('e2e','qa') OR asset_code LIKE 'ZZ-REPAIR%' OR asset_code LIKE ?)
		  AND `+grace, qaCardPrefix+"%").Scan(&nRepairE2E)

	var nLog, nLogE2E int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM doc_status_log WHERE doc_type = ?", model.DocTypeRepair).Scan(&nLog)
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM doc_status_log
		WHERE doc_type = ? AND operator IN ('e2e','qa','qa-sync')
		  AND `+grace, model.DocTypeRepair).Scan(&nLogE2E)

	var nAtt, nAttE2E int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM repair_attachment").Scan(&nAtt)
	_ = st.db.QueryRow("SELECT COUNT(*) FROM repair_attachment WHERE uploaded_by IN ('e2e','qa','qa-sync') AND "+grace).Scan(&nAttE2E)

	var nProbeCard int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM asset_card
		WHERE (asset_code LIKE 'ZZ-REPAIR%' OR asset_code LIKE ?) AND `+grace, qaCardPrefix+"%").Scan(&nProbeCard)

	// 通用不变量（比按名字筛更可靠）：流转记录 / 附件不得指向不存在的维修单。
	// 没有 FK 约束，删除单据时漏删从属行不会报错——这正是最易漏的残留形态。
	var nOrphanLog, nOrphanAtt int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM doc_status_log l
		WHERE l.doc_type = ? AND NOT EXISTS (SELECT 1 FROM repair_order r WHERE r.id = l.doc_id)`,
		model.DocTypeRepair).Scan(&nOrphanLog)
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM repair_attachment a
		WHERE a.repair_id > 0 AND NOT EXISTS (SELECT 1 FROM repair_order r WHERE r.id = a.repair_id)`).Scan(&nOrphanAtt)

	t.Logf("repair_order: 总 %d，疑似测试残留 %d（已过 90s 宽限期）", nRepair, nRepairE2E)
	t.Logf("doc_status_log(repair): 总 %d，疑似测试残留 %d（已过宽限期）", nLog, nLogE2E)
	t.Logf("repair_attachment: 总 %d，疑似测试残留 %d（已过宽限期）", nAtt, nAttE2E)
	t.Logf("探针卡残留（asset_card 中 ZZ-REPAIR/QA-REPAIR，已过宽限期）: %d", nProbeCard)
	t.Logf("孤儿流转记录（doc_id 无对应维修单）: %d", nOrphanLog)
	t.Logf("孤儿附件（repair_id 无对应维修单）: %d", nOrphanAtt)

	if nRepairE2E > 0 || nLogE2E > 0 || nAttE2E > 0 || nProbeCard > 0 || nOrphanLog > 0 || nOrphanAtt > 0 {
		t.Errorf("⚠️ 检测到测试残留/孤儿行：repair_order=%d doc_status_log=%d repair_attachment=%d probe_card=%d orphan_log=%d orphan_att=%d",
			nRepairE2E, nLogE2E, nAttE2E, nProbeCard, nOrphanLog, nOrphanAtt)
	}
}
