package store

import (
	"strconv"
	"strings"
	"testing"

	"asset-mgr/model"
)

// TestNormalizeRepairIDs 是纯函数守卫（不需要库）：批量删除的入参清洗。
//
// 每一条都对应一种「静默错删」或「静默少删」：
//   - 不去重 → 上层「已删 N 张」的计数与实际不符（第二遍 DELETE 影响 0 行，但校验阶段仍读到同一张单）；
//   - 不拦非正值 → id=0 会去查一次不存在的行，报出「维修单 id=0 不存在」这种让人摸不着头脑的错误；
//   - 不拦超量 → 一个误传的巨大数组会在同一个事务里锁住大量行。
func TestNormalizeRepairIDs(t *testing.T) {
	// 去重且保序
	got, err := normalizeRepairIDs([]int64{3, 1, 3, 2, 1})
	if err != nil {
		t.Fatalf("正常入参不该报错: %v", err)
	}
	want := []int64{3, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("去重后应为 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("去重后应保序为 %v，实际 %v", want, got)
		}
	}

	// 空入参
	if _, err := normalizeRepairIDs(nil); err == nil {
		t.Error("nil ids 应被拒绝")
	}
	if _, err := normalizeRepairIDs([]int64{}); err == nil {
		t.Error("空切片应被拒绝")
	}

	// 非正值
	for _, bad := range []int64{0, -1} {
		if _, err := normalizeRepairIDs([]int64{1, bad}); err == nil {
			t.Errorf("含非法 id %d 应被拒绝", bad)
		}
	}

	// 边界：恰好等于上限应通过，多一个应被拒
	atLimit := make([]int64, RepairDeleteMaxBatch)
	for i := range atLimit {
		atLimit[i] = int64(i + 1)
	}
	if _, err := normalizeRepairIDs(atLimit); err != nil {
		t.Errorf("恰好 %d 张应通过，实际报错: %v", RepairDeleteMaxBatch, err)
	}
	overLimit := append(atLimit, int64(RepairDeleteMaxBatch+1))
	if _, err := normalizeRepairIDs(overLimit); err == nil {
		t.Errorf("超过 %d 张应被拒绝", RepairDeleteMaxBatch)
	} else if !strings.Contains(err.Error(), "最多") {
		t.Errorf("超量错误信息应说明上限，实际: %v", err)
	}
}

// TestRepairDeletableWhitelist 是纯函数守卫（不需要库）：删除白名单本身。
//
// 这条测试的价值不在「证明现在是这两个状态」，而在**让改动白名单必须显式过测试**：
// 白名单是「不可恢复操作」的唯一闸门，放宽一个状态就等于放宽一片数据。
func TestRepairDeletableWhitelist(t *testing.T) {
	// 允许删的：待受理 + 已撤单
	for _, s := range []string{model.RepairPending, model.RepairCancelled} {
		if !model.IsRepairDeletable(s) {
			t.Errorf("状态「%s」应在删除白名单里", model.RepairStatusLabel(s))
		}
	}
	// 不允许删的：在办状态 + 另外两个终态
	//   - accepted/dispatched 在云之家侧还挂着他人的在办待办，删掉会让待办指向 404
	//   - done/rejected 同为终态，但删除会抹掉已完成业务的审计痕迹
	for _, s := range []string{
		model.RepairAccepted, model.RepairDispatched, model.RepairRepairing,
		model.RepairConfirming, model.RepairDone, model.RepairRejected,
	} {
		if model.IsRepairDeletable(s) {
			t.Errorf("状态「%s」不该在删除白名单里", model.RepairStatusLabel(s))
		}
	}
	// 未知状态必须 fail-closed（库里出现怪值时不能顺手放行）
	if model.IsRepairDeletable("") || model.IsRepairDeletable("no-such-status") {
		t.Error("未知状态必须判定为不可删除（fail-closed）")
	}

	// 文案：错误提示与前端提示共用，必须把两个状态都说出来，否则用户会以为已撤单的单也能删
	label := model.RepairDeletableLabel()
	for _, want := range []string{"待受理", "已撤单"} {
		if !strings.Contains(label, want) {
			t.Errorf("白名单文案应包含「%s」，实际: %s", want, label)
		}
	}
}

// TestDeleteRepairOrdersAgainstRealDB 验证批量删除的**全部**约束与级联清理。
//
// 为什么必须连真库：
//   - 级联删的是三张**没有外键约束**的表，漏删不会报错、只会静默留下孤儿行
//     （不变量见 TestQADataHygiene 的 orphan 断言）；
//   - 「含不可删状态时整体回滚」必须验证事务真的回滚了 —— 纯单测回答不了
//     「一张能删的单和一张不能删的单混在一起时会怎样」；
//   - 「已撤单可删」这条尤其需要真库：它的安全性前提是「撤单从不写 asset_card.biz_status」，
//     而那是个跨表事实（repair_order × asset_card），只能连库验证。
//
// 安全性：建编码固定的探针卡，跑完把探针卡及其维修单 / 流转记录 / 附件 / 待办任务全部删掉；
// 开头先清理一次，兜住上次异常退出留下的残留。
func TestDeleteRepairOrdersAgainstRealDB(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	// 关连接走 t.Cleanup（与下面 cleanup 同为 LIFO）：先注册 ⇒ 最后执行 ⇒ 删行跑完才关连接。
	t.Cleanup(func() { st.Close() })

	const code = "ZZ-REPAIR-DEL-0001"

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
				_, _ = st.db.Exec("DELETE FROM repair_notify_task WHERE repair_id = ?", id)
			}
		}
		_, _ = st.db.Exec("DELETE FROM repair_order WHERE card_id = ?", cardID)
		_, _ = st.db.Exec("DELETE FROM asset_history WHERE card_id = ?", cardID)
		_, _ = st.db.Exec("DELETE FROM asset_card WHERE id = ?", cardID)
	}
	cleanup()
	defer cleanup()

	res, err := st.db.Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, created_by)
		VALUES (?, ?, ?, '', 'qa')`, code, "批量删除探针", model.StatusInUse)
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

	newOrder := func(desc string) *model.RepairOrder {
		o, err := st.CreateRepair(card, RepairCreateInput{
			ReporterEmpID: 1, ReporterName: "张三", FaultDesc: desc,
			Urgency: model.RepairUrgencyNormal, CreatedBy: "qa",
		})
		if err != nil {
			t.Fatalf("提交报修失败: %v", err)
		}
		return o
	}

	// 各表行数辅助：只数本单的行，避免受库里其它数据影响。
	countRows := func(table, where string, args ...any) int {
		var n int
		if err := st.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+where, args...).Scan(&n); err != nil {
			t.Fatalf("统计 %s 失败: %v", table, err)
		}
		return n
	}

	// ---------- 1. 正常删除：级联清干净 ----------
	doomed := newOrder("要被删掉的单")
	// 给这张单挂上全部三类从属行，验证它们会被一起清掉。
	if _, err := st.db.Exec(`INSERT INTO repair_attachment (repair_id, origin_name, stored_path, uploaded_by)
		VALUES (?, ?, ?, 'qa')`, doomed.ID, "x.jpg", "/tmp/x.jpg"); err != nil {
		t.Fatalf("插入附件失败: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO repair_notify_task (repair_id, emp_id, open_id, source_id, purpose, state)
		VALUES (?, ?, ?, ?, 'intake', 'active')`, doomed.ID, 1, "open-1", "repair-"+strconv.FormatInt(doomed.ID, 10)); err != nil {
		t.Fatalf("插入待办任务失败: %v", err)
	}
	if n := countRows("doc_status_log", "doc_type = ? AND doc_id = ?", model.DocTypeRepair, doomed.ID); n != 1 {
		t.Fatalf("提交报修应产生 1 条流转记录，实际 %d 条", n)
	}
	if n := countRows("repair_attachment", "repair_id = ?", doomed.ID); n != 1 {
		t.Fatalf("附件应写入 1 条，实际 %d 条", n)
	}

	deleted, err := st.DeleteRepairOrders([]int64{doomed.ID})
	if err != nil {
		t.Fatalf("删除待受理单应成功，实际报错: %v", err)
	}
	if len(deleted) != 1 || deleted[0].Code != doomed.Code {
		t.Fatalf("应返回被删的 1 张单（%s），实际 %+v", doomed.Code, deleted)
	}
	for _, c := range []struct {
		table string
		where string
		arg   any
	}{
		{"repair_order", "id = ?", doomed.ID},
		{"doc_status_log", "doc_id = ?", doomed.ID},
		{"repair_attachment", "repair_id = ?", doomed.ID},
		{"repair_notify_task", "repair_id = ?", doomed.ID},
	} {
		if n := countRows(c.table, c.where, c.arg); n != 0 {
			t.Errorf("级联清理后 %s 应为 0 行，实际 %d 行（孤儿行）", c.table, n)
		}
	}
	// 单据不存在时再删一次应被拒（不是静默成功）
	if _, err := st.DeleteRepairOrders([]int64{doomed.ID}); err == nil {
		t.Error("删除已不存在的单应被拒绝，实际通过了")
	}

	// ---------- 2. 不在白名单的状态：拒绝 ----------
	// 2a) 在办状态（已受理）：云之家侧还挂着他人的在办待办，删掉会让待办指向 404 深链。
	accepted := newOrder("已被受理的单")
	if _, err := st.Transition(accepted.ID, RepairTransitionInput{
		Action: model.RepairActionAccept, Operator: "管理员",
	}); err != nil {
		t.Fatalf("受理失败: %v", err)
	}
	if _, err := st.DeleteRepairOrders([]int64{accepted.ID}); err == nil {
		t.Fatal("已受理的单不该被允许删除，实际通过了")
	} else if !strings.Contains(err.Error(), "待受理") || !strings.Contains(err.Error(), "已撤单") {
		t.Errorf("拒绝理由应把白名单两个状态都说出来，实际: %v", err)
	}
	if n := countRows("repair_order", "id = ?", accepted.ID); n != 1 {
		t.Errorf("被拒绝的单必须原样保留，实际行数 %d", n)
	}

	// 2b) 同为终态但**刻意不放行**的「已驳回」：防止将来有人图省事改成「所有终态都可删」。
	// 终态 ≠ 可删 —— 判定标准是「有没有未兑现的外部承诺」，不是「流转结束没结束」。
	rejected := newOrder("已被驳回的单")
	if _, err := st.Transition(rejected.ID, RepairTransitionInput{
		Action: model.RepairActionReject, Operator: "管理员", Remark: "不属于维修范围",
	}); err != nil {
		t.Fatalf("驳回失败: %v", err)
	}
	if _, err := st.DeleteRepairOrders([]int64{rejected.ID}); err == nil {
		t.Fatal("已驳回的单不该被允许删除（终态 ≠ 可删），实际通过了")
	}

	// ---------- 2c. 已撤单：允许删除，且级联清干净 ----------
	// 这是本轮新增的白名单状态。安全性前提：cancel 只能来自 pending/accepted/dispatched，
	// 永远不来自 repairing ⇒ 从未写过 asset_card.biz_status ⇒ 删除无需回滚资产卡状态。
	cancelled := newOrder("已撤单的单")
	if _, err := st.Transition(cancelled.ID, RepairTransitionInput{
		Action: model.RepairActionCancel, Operator: "张三",
	}); err != nil {
		t.Fatalf("撤单失败: %v", err)
	}
	// 前提校验：撤单不得写资产卡的业务状态（否则删除就得连带回滚，白名单前提就破了）
	var bizStatus string
	if err := st.db.QueryRow("SELECT biz_status FROM asset_card WHERE id = ?", cardID).Scan(&bizStatus); err != nil {
		t.Fatalf("读资产卡 biz_status 失败: %v", err)
	}
	if bizStatus != "" {
		t.Fatalf("撤单不该写 asset_card.biz_status，实际为「%s」——删除白名单的前提已破，必须重新评估", bizStatus)
	}
	// 也挂上三类从属行，验证已撤单的单同样会被级联清干净
	if _, err := st.db.Exec(`INSERT INTO repair_attachment (repair_id, origin_name, stored_path, uploaded_by)
		VALUES (?, ?, ?, 'qa')`, cancelled.ID, "y.jpg", "/tmp/y.jpg"); err != nil {
		t.Fatalf("插入附件失败: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO repair_notify_task (repair_id, emp_id, open_id, source_id, purpose, state)
		VALUES (?, ?, ?, ?, 'intake', 'deleted')`, cancelled.ID, 1, "open-1", "repair-"+strconv.FormatInt(cancelled.ID, 10)); err != nil {
		t.Fatalf("插入待办任务失败: %v", err)
	}
	// 提交 + 撤单 = 2 条流转记录
	if n := countRows("doc_status_log", "doc_type = ? AND doc_id = ?", model.DocTypeRepair, cancelled.ID); n != 2 {
		t.Fatalf("提交+撤单应产生 2 条流转记录，实际 %d 条", n)
	}

	deletedCancelled, err := st.DeleteRepairOrders([]int64{cancelled.ID})
	if err != nil {
		t.Fatalf("删除已撤单的单应成功，实际报错: %v", err)
	}
	if len(deletedCancelled) != 1 || deletedCancelled[0].Status != model.RepairCancelled {
		t.Fatalf("应返回被删的 1 张已撤单（%s），实际 %+v", cancelled.Code, deletedCancelled)
	}
	for _, c := range []struct {
		table string
		where string
		arg   any
	}{
		{"repair_order", "id = ?", cancelled.ID},
		{"doc_status_log", "doc_id = ?", cancelled.ID},
		{"repair_attachment", "repair_id = ?", cancelled.ID},
		{"repair_notify_task", "repair_id = ?", cancelled.ID},
	} {
		if n := countRows(c.table, c.where, c.arg); n != 0 {
			t.Errorf("已撤单的级联清理后 %s 应为 0 行，实际 %d 行（孤儿行）", c.table, n)
		}
	}
	// 删除后资产卡的业务状态仍应保持空（没有连带副作用）
	if err := st.db.QueryRow("SELECT biz_status FROM asset_card WHERE id = ?", cardID).Scan(&bizStatus); err != nil {
		t.Fatalf("删除后读资产卡 biz_status 失败: %v", err)
	}
	if bizStatus != "" {
		t.Errorf("删除已撤单的单不该改动 asset_card.biz_status，实际为「%s」", bizStatus)
	}

	// ---------- 3. 混合批量：整体回滚，一张都不能少 ----------
	// 这是本测试最关键的一条：只要有一张不合规，**连合规的那张也不能被删**。
	// 「删了一半」是这里最坏的失败形态 —— 用户看到报错，却已经有数据没了。
	kept := newOrder("本该被回滚保住的单")
	if _, err := st.DeleteRepairOrders([]int64{kept.ID, accepted.ID}); err == nil {
		t.Fatal("批量里含不可删状态时整体应被拒绝，实际通过了")
	}
	for _, id := range []int64{kept.ID, accepted.ID} {
		if n := countRows("repair_order", "id = ?", id); n != 1 {
			t.Errorf("整体回滚后维修单 id=%d 应仍在，实际行数 %d", id, n)
		}
	}
	if n := countRows("doc_status_log", "doc_type = ? AND doc_id = ?", model.DocTypeRepair, kept.ID); n != 1 {
		t.Errorf("整体回滚后 id=%d 的流转记录应仍在，实际 %d 条", kept.ID, n)
	}
	// 3b) 可删 + 不可删 混合：合规的那张（待受理）也必须被回滚保住
	keptPending := newOrder("待受理但混在不合规批次里")
	if _, err := st.DeleteRepairOrders([]int64{keptPending.ID, rejected.ID}); err == nil {
		t.Fatal("批量里含已驳回单时整体应被拒绝，实际通过了")
	}
	if n := countRows("repair_order", "id = ?", keptPending.ID); n != 1 {
		t.Errorf("整体回滚后待受理单 id=%d 应仍在，实际行数 %d", keptPending.ID, n)
	}

	// ---------- 4. 入参守卫（走真实入口，不只是单测那个函数） ----------
	if _, err := st.DeleteRepairOrders(nil); err == nil {
		t.Error("空 ids 应被拒绝")
	}
	if _, err := st.DeleteRepairOrders([]int64{0}); err == nil {
		t.Error("id=0 应被拒绝")
	}
	if _, err := st.DeleteRepairOrders([]int64{999999999}); err == nil {
		t.Error("不存在的 id 应被拒绝")
	}

	// ---------- 5. 通用不变量：本探针卡下不得留下任何孤儿行 ----------
	// 与 TestQADataHygiene 的 orphan 断言同一口径，只是范围收窄到本测试建的数据。
	// 已删的 doomed / cancelled 也一并纳入：孤儿行的典型来源正是「主表删了、从属没删」。
	allIDs := []any{doomed.ID, cancelled.ID, accepted.ID, rejected.ID, kept.ID, keptPending.ID}
	orphanLog, orphanAtt, orphanNotify := 0, 0, 0
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM doc_status_log l
		WHERE l.doc_type = ? AND l.doc_id IN (?, ?, ?, ?, ?, ?)
		  AND NOT EXISTS (SELECT 1 FROM repair_order r WHERE r.id = l.doc_id)`,
		append([]any{model.DocTypeRepair}, allIDs...)...).Scan(&orphanLog)
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM repair_attachment a
		WHERE a.repair_id IN (?, ?, ?, ?, ?, ?)
		  AND NOT EXISTS (SELECT 1 FROM repair_order r WHERE r.id = a.repair_id)`,
		allIDs...).Scan(&orphanAtt)
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM repair_notify_task n
		WHERE n.repair_id IN (?, ?, ?, ?, ?, ?)
		  AND NOT EXISTS (SELECT 1 FROM repair_order r WHERE r.id = n.repair_id)`,
		allIDs...).Scan(&orphanNotify)
	if orphanLog+orphanAtt+orphanNotify != 0 {
		t.Errorf("检测到孤儿行：流转记录=%d 附件=%d 待办任务=%d", orphanLog, orphanAtt, orphanNotify)
	}
}

// itoa 是本测试内的小工具（避免为一个 strconv 引入 import 到生产文件）。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
