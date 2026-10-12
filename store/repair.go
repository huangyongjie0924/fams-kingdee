package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"asset-mgr/model"
)

// repairOrderSelect 与 scanRepairOrder 的扫描顺序必须逐字对应，改一处必须同步改另一处。
const repairOrderSelect = `SELECT id, code, card_id, asset_code, asset_name,
	reporter_emp_id, reporter_name, use_dept_id, use_dept_name, location,
	fault_desc, urgency, status,
	assignee_emp_id, assignee_name, vendor_id, vendor_name, handler_desc, reject_reason,
	cost, cost_dept_id, accept_by,
	accept_at, assign_at, start_at, finish_at, confirm_at, close_at,
	created_by, created_at, updated_at
FROM repair_order`

func scanRepairOrder(r rowScanner) (*model.RepairOrder, error) {
	var o model.RepairOrder
	var acceptAt, assignAt, startAt, finishAt, confirmAt, closeAt sql.NullTime
	if err := r.Scan(
		&o.ID, &o.Code, &o.CardID, &o.AssetCode, &o.AssetName,
		&o.ReporterEmpID, &o.ReporterName, &o.UseDeptID, &o.UseDeptName, &o.Location,
		&o.FaultDesc, &o.Urgency, &o.Status,
		&o.AssigneeEmpID, &o.AssigneeName, &o.VendorID, &o.VendorName, &o.HandlerDesc, &o.RejectReason,
		&o.Cost, &o.CostDeptID, &o.AcceptBy,
		&acceptAt, &assignAt, &startAt, &finishAt, &confirmAt, &closeAt,
		&o.CreatedBy, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	o.AcceptAt = nullTimePtr(acceptAt)
	o.AssignAt = nullTimePtr(assignAt)
	o.StartAt = nullTimePtr(startAt)
	o.FinishAt = nullTimePtr(finishAt)
	o.ConfirmAt = nullTimePtr(confirmAt)
	o.CloseAt = nullTimePtr(closeAt)
	o.StatusLabel = model.RepairStatusLabel(o.Status)
	return &o, nil
}

func nullTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	tt := t.Time
	return &tt
}

// RepairCreateInput 是提交报修的输入。
type RepairCreateInput struct {
	ReporterEmpID int64
	ReporterName  string
	FaultDesc     string
	Urgency       string
	CreatedBy     string
	AttachmentIDs []int64
}

// CreateRepair 新建报修单。
//
// 单号对齐 count_plan：先 INSERT 拿自增 id、再 UPDATE 回填 WX<yyyymmdd><0001>。
// 同一事务内写 doc_status_log（'' → pending, action=submit）并绑定预上传的附件。
// 报修不改资产卡 biz_status——只有进入「维修中」才写（避免刚报修就把资产锁死）。
func (s *Store) CreateRepair(card *model.AssetCard, in RepairCreateInput) (*model.RepairOrder, error) {
	if card == nil {
		return nil, errors.New("资产不存在")
	}
	urgency := in.Urgency
	if urgency == "" {
		urgency = model.RepairUrgencyNormal
	}
	if !model.IsValidRepairUrgency(urgency) {
		return nil, fmt.Errorf("紧急程度取值非法：%s", urgency)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO repair_order
		(card_id, asset_code, asset_name, reporter_emp_id, reporter_name, use_dept_id, use_dept_name,
		 location, fault_desc, urgency, status, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		card.ID, card.AssetCode, card.Name, in.ReporterEmpID, in.ReporterName,
		card.UseDeptID, card.UseDeptName, card.Location, in.FaultDesc, urgency,
		model.RepairPending, in.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("insert repair: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	code := formatDocCode("WX", id)
	if _, err := tx.Exec(`UPDATE repair_order SET code = ? WHERE id = ?`, code, id); err != nil {
		return nil, fmt.Errorf("set repair code: %w", err)
	}
	if err := addDocStatusLogTx(tx, model.DocTypeRepair, id, "", model.RepairPending,
		model.RepairActionSubmit, in.CreatedBy, ""); err != nil {
		return nil, err
	}
	if err := bindRepairAttachmentsTx(tx, id, card.ID, in.AttachmentIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRepair(id)
}

// RepairTransitionInput 是状态流转的输入。不同 action 用到的字段不同，未用的留零值。
type RepairTransitionInput struct {
	Action        string
	Operator      string
	Remark        string
	AssigneeEmpID int64
	AssigneeName  string
	VendorID      int64
	VendorName    string
	HandlerDesc   string
	AttachmentIDs []int64
}

// repairTransitions 是状态机的唯一真相：action → {当前状态: 目标状态}。
// 非法跃迁（当前状态不在内层 map 里）一律拒绝并给出明确提示。
//
// approving / scrap 属 P1/P2，P0 不暴露对应路由，但规则从 P0 就定义好，
// 避免 P1 启用时改状态机表引发返工。
var repairTransitions = map[string]map[string]string{
	model.RepairActionAccept:   {model.RepairPending: model.RepairAccepted},
	model.RepairActionReject:   {model.RepairPending: model.RepairRejected},
	model.RepairActionDispatch: {model.RepairAccepted: model.RepairDispatched},
	model.RepairActionTake:     {model.RepairDispatched: model.RepairRepairing},
	model.RepairActionFinish:   {model.RepairRepairing: model.RepairConfirming},
	model.RepairActionConfirm:  {model.RepairConfirming: model.RepairDone},
	model.RepairActionReturn:   {model.RepairConfirming: model.RepairRepairing},
	// 撤单：待受理由员工撤；已受理 / 已派工由管理员取消
	model.RepairActionCancel: {
		model.RepairPending:    model.RepairCancelled,
		model.RepairAccepted:   model.RepairCancelled,
		model.RepairDispatched: model.RepairCancelled,
	},
	// P1/P2
	model.RepairActionApprove: {model.RepairAccepted: model.RepairApproving},
	model.RepairActionScrap: {
		model.RepairAccepted:  model.RepairScrapping,
		model.RepairRepairing: model.RepairScrapping,
	},
}

// Transition 是**单据状态变更的唯一入口**（禁止散落在别处 UPDATE repair_order.status）。
//
// 同一事务内完成：状态跃迁 + 时间戳 + biz_status 联动（进入/离开维修中）+ 资产履历 +
// doc_status_log 留痕。加行锁（SELECT ... FOR UPDATE）避免并发重复跃迁。
//
// biz_status 的唯一写入者就是这里：to == repairing 时置 '维修中'，离开 repairing 时清空。
func (s *Store) Transition(id int64, in RepairTransitionInput) (*model.RepairOrder, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	row := tx.QueryRow(repairOrderSelect+" WHERE id = ? FOR UPDATE", id)
	cur, err := scanRepairOrder(row)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}

	to, ok := repairTransitions[in.Action][cur.Status]
	if !ok {
		return nil, fmt.Errorf("当前状态「%s」不允许「%s」操作",
			model.RepairStatusLabel(cur.Status), model.RepairActionLabel(in.Action))
	}

	sets := []string{"status = ?", "updated_at = NOW()"}
	args := []any{to}
	switch in.Action {
	case model.RepairActionAccept:
		sets = append(sets, "accept_by = ?", "accept_at = NOW()")
		args = append(args, in.Operator)
	case model.RepairActionDispatch:
		sets = append(sets, "assignee_emp_id = ?", "assignee_name = ?", "vendor_id = ?", "vendor_name = ?", "assign_at = NOW()")
		args = append(args, in.AssigneeEmpID, in.AssigneeName, in.VendorID, in.VendorName)
	case model.RepairActionTake:
		sets = append(sets, "start_at = NOW()")
	case model.RepairActionFinish:
		sets = append(sets, "handler_desc = ?", "finish_at = NOW()")
		args = append(args, in.HandlerDesc)
	case model.RepairActionConfirm:
		sets = append(sets, "confirm_at = NOW()")
	case model.RepairActionReject:
		sets = append(sets, "reject_reason = ?", "close_at = NOW()")
		args = append(args, in.Remark)
	case model.RepairActionReturn:
		// 退回原因复用 reject_reason 列（同一「原因」语义），时间线里另有一份 doc_status_log.remark
		sets = append(sets, "reject_reason = ?")
		args = append(args, in.Remark)
	case model.RepairActionCancel:
		sets = append(sets, "close_at = NOW()")
	case model.RepairActionScrap:
		sets = append(sets, "close_at = NOW()")
	}
	if _, err := tx.Exec("UPDATE repair_order SET "+strings.Join(sets, ", ")+" WHERE id = ?",
		append(args, id)...); err != nil {
		return nil, fmt.Errorf("update repair status: %w", err)
	}

	if err := s.applyBizStatusTx(tx, cur, to, in.Operator); err != nil {
		return nil, err
	}
	if err := addDocStatusLogTx(tx, model.DocTypeRepair, id, cur.Status, to,
		in.Action, in.Operator, in.Remark); err != nil {
		return nil, err
	}
	// 报完工时绑定维修过程照片（两段式上传的绑定时机）
	if in.Action == model.RepairActionFinish && len(in.AttachmentIDs) > 0 {
		if err := bindRepairAttachmentsTx(tx, id, cur.CardID, in.AttachmentIDs); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRepair(id)
}

// applyBizStatusTx 在单据事务内同步资产卡的本地业务状态列。
//
// 规则：进入 repairing → 置 '维修中' 并写一条 asset_history（action='repair'）；
// 离开 repairing → 清空。只有这一个函数写 biz_status。
func (s *Store) applyBizStatusTx(tx *sql.Tx, cur *model.RepairOrder, to, operator string) error {
	entering := to == model.RepairRepairing && cur.Status != model.RepairRepairing
	leaving := cur.Status == model.RepairRepairing && to != model.RepairRepairing
	if !entering && !leaving {
		return nil
	}

	var cardStatus string
	err := tx.QueryRow("SELECT status FROM asset_card WHERE id = ? AND deleted_at IS NULL", cur.CardID).Scan(&cardStatus)
	if err == sql.ErrNoRows {
		return fmt.Errorf("关联资产不存在或已删除（card_id=%d）", cur.CardID)
	}
	if err != nil {
		return err
	}

	if entering {
		if _, err := tx.Exec("UPDATE asset_card SET biz_status = ? WHERE id = ? AND deleted_at IS NULL",
			model.StatusRepair, cur.CardID); err != nil {
			return fmt.Errorf("set biz_status: %w", err)
		}
		if err := addHistoryTx(tx, cur.CardID, "repair", "状态", cardStatus, model.StatusRepair, operator); err != nil {
			return err
		}
		return nil
	}
	if _, err := tx.Exec("UPDATE asset_card SET biz_status = '' WHERE id = ? AND deleted_at IS NULL",
		cur.CardID); err != nil {
		return fmt.Errorf("clear biz_status: %w", err)
	}
	return nil
}

// RepairDeleteMaxBatch 是一次批量删除维修单的上限。
//
// 定这个数的理由：删除是**不可恢复**操作，一次几十张已远超正常纠错场景；上限让「误传一个超大
// ids 数组」在触库之前就被拒绝，也顺带限制了单事务的行锁范围。
const RepairDeleteMaxBatch = 100

// DeleteRepairOrders 物理删除若干张**待受理（pending）或已撤单（cancelled）**维修单，
// 并级联清理全部从属行。
//
// 为什么只允许这两个状态（白名单的唯一真相在 model.RepairDeletableStatuses）：
//   - 判定标准是「这张单是否还挂着未兑现的外部承诺」。
//   - pending 从未被受理，云之家侧只有一条给受理人的待办；cancelled 已是终态，撤单时
//     已跑过同一套整单清理。两者删除时都由 OnRepairDeleted → deliverDeleteAll 收尾。
//   - 一旦受理 / 派工，云之家侧已经把待办推给了受理人或维护人员，删掉本地行会让那些待办
//     指向一个不存在的单据（深链 404）—— 所以 accepted / dispatched 刻意不放行。
//   - 两者都**从未写过 asset_card.biz_status**：该列只有进出 repairing 时才写，而 cancel
//     只能来自 pending / accepted / dispatched（见 repairTransitions），永远不来自 repairing。
//     故删除无需回滚资产卡状态。（若将来放开「维修中撤单」，这条前提就破了，必须一并处理。）
//
// 为什么必须「锁行校验 + 删除」放在同一个事务：ids 来自请求体，校验与删除之间若有窗口，单据
// 可能在那一瞬被受理（TOCTOU）。这里对每张单 SELECT ... FOR UPDATE，校验与删除同事务。
//
// 级联范围（这三张表**没有外键约束**，漏删就是静默的孤儿行 —— 不变量见
// store/repair_qa_store_test.go 的 TestQADataHygiene）：
//   - doc_status_log    (doc_type='repair', doc_id)
//   - repair_attachment (repair_id)
//   - repair_notify_task(repair_id)
//
// 返回被删单据（供调用方清理云之家待办 + 记审计日志）。**任一条不满足条件即整体回滚、一条都不
// 删**，并返回可读错误（含单号与当前状态）——「删了一半」是这里最坏的失败形态。
func (s *Store) DeleteRepairOrders(ids []int64) ([]*model.RepairOrder, error) {
	uniq, err := normalizeRepairIDs(ids)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1) 锁行 + 校验状态。逐条查而不是 IN(...)：要能说出「哪一张、什么状态」。
	orders := make([]*model.RepairOrder, 0, len(uniq))
	for _, id := range uniq {
		row := tx.QueryRow(repairOrderSelect+" WHERE id = ? FOR UPDATE", id)
		o, err := scanRepairOrder(row)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("维修单 id=%d 不存在（可能已被他人删除）", id)
		}
		if err != nil {
			return nil, fmt.Errorf("查询维修单 id=%d: %w", id, err)
		}
		if !model.IsRepairDeletable(o.Status) {
			return nil, fmt.Errorf("维修单 %s 当前状态是「%s」，只有「%s」的单可以删除",
				o.Code, model.RepairStatusLabel(o.Status), model.RepairDeletableLabel())
		}
		orders = append(orders, o)
	}

	// 2) 先删从属行、再删主表。反过来一旦中途失败，就会留下「主表没了、从属还在」的孤儿行。
	for _, o := range orders {
		if _, err := tx.Exec(`DELETE FROM repair_attachment WHERE repair_id = ?`, o.ID); err != nil {
			return nil, fmt.Errorf("清理维修单 %s 的附件失败: %w", o.Code, err)
		}
		if _, err := tx.Exec(`DELETE FROM doc_status_log WHERE doc_type = ? AND doc_id = ?`,
			model.DocTypeRepair, o.ID); err != nil {
			return nil, fmt.Errorf("清理维修单 %s 的流转记录失败: %w", o.Code, err)
		}
		if _, err := tx.Exec(`DELETE FROM repair_notify_task WHERE repair_id = ?`, o.ID); err != nil {
			return nil, fmt.Errorf("清理维修单 %s 的待办任务失败: %w", o.Code, err)
		}
		if _, err := tx.Exec(`DELETE FROM repair_order WHERE id = ?`, o.ID); err != nil {
			return nil, fmt.Errorf("删除维修单 %s 失败: %w", o.Code, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return orders, nil
}

// normalizeRepairIDs 去重 + 过滤非正值 + 校验批量上限，保持传入顺序。
//
// 去重是必需的：重复 id 会让上层「删了几张」的计数与实际不符（第二遍 DELETE 影响 0 行，
// 但校验阶段仍会读到同一张单）。
func normalizeRepairIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, errors.New("未选择任何维修单")
	}
	if len(ids) > RepairDeleteMaxBatch {
		return nil, fmt.Errorf("一次最多删除 %d 张维修单，当前请求 %d 张",
			RepairDeleteMaxBatch, len(ids))
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("维修单 id 非法：%d", id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// GetRepair 按 id 取单张维修单，不存在返回 nil, nil。
func (s *Store) GetRepair(id int64) (*model.RepairOrder, error) {
	row := s.db.QueryRow(repairOrderSelect+" WHERE id = ?", id)
	o, err := scanRepairOrder(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// RepairLogs 取某张维修单的状态流转记录（时间线），按发生顺序。
func (s *Store) RepairLogs(docID int64) ([]model.DocStatusLog, error) {
	rows, err := s.db.Query(`SELECT id, doc_type, doc_id, from_status, to_status, action, operator, remark, created_at
		FROM doc_status_log WHERE doc_type = ? AND doc_id = ? ORDER BY id`, model.DocTypeRepair, docID)
	if err != nil {
		return nil, fmt.Errorf("query doc status log: %w", err)
	}
	defer rows.Close()

	out := []model.DocStatusLog{}
	for rows.Next() {
		var l model.DocStatusLog
		if err := rows.Scan(&l.ID, &l.DocType, &l.DocID, &l.FromStatus, &l.ToStatus,
			&l.Action, &l.Operator, &l.Remark, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.FromLabel = model.RepairStatusLabel(l.FromStatus)
		l.ToLabel = model.RepairStatusLabel(l.ToStatus)
		l.ActionLabel = model.RepairActionLabel(l.Action)
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateRepairCost 登记维修费用（P1-4）。只改费用两列，不触发状态机。
func (s *Store) UpdateRepairCost(id int64, cost float64, costDeptID int64) error {
	res, err := s.db.Exec(`UPDATE repair_order SET cost = ?, cost_dept_id = ? WHERE id = ?`,
		cost, costDeptID, id)
	if err != nil {
		return fmt.Errorf("update repair cost: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// RowsAffected 为 0 也可能是「值没变」，这里再确认一次存在性
		if o, err := s.GetRepair(id); err != nil {
			return err
		} else if o == nil {
			return sql.ErrNoRows
		}
	}
	return nil
}

// addDocStatusLogTx 写一条状态流转记录。remark 截断到列宽（500）。
func addDocStatusLogTx(tx *sql.Tx, docType string, docID int64, from, to, action, operator, remark string) error {
	_, err := tx.Exec(`INSERT INTO doc_status_log
		(doc_type, doc_id, from_status, to_status, action, operator, remark)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		docType, docID, from, to, action, operator, trunc(remark))
	if err != nil {
		return fmt.Errorf("insert doc status log: %w", err)
	}
	return nil
}

// ReconcileBizStatus 对账 biz_status 与「真实进行中单据」，返回修正的行数。
//
// biz_status 与单据之间理论上可能漂移（例如维修单结单了但列没清掉）。写入与清除都在
// 单据状态机的同一事务内，正常不会漂移；这条对账是兜底，可在同步后或手动触发
// （体检报告思路，与项目既有 cmd/kdverify 一脉相承）。
func (s *Store) ReconcileBizStatus() (int, error) {
	// 方向一：列说「维修中」但没有 repairing 单 → 清空
	clear, err := s.db.Exec(`UPDATE asset_card c SET c.biz_status = ''
		WHERE c.deleted_at IS NULL AND c.biz_status = ?
		  AND NOT EXISTS (SELECT 1 FROM repair_order r WHERE r.card_id = c.id AND r.status = ?)`,
		model.StatusRepair, model.RepairRepairing)
	if err != nil {
		return 0, fmt.Errorf("reconcile biz_status (clear): %w", err)
	}
	nClear, _ := clear.RowsAffected()

	// 方向二：有 repairing 单但列是空的 → 补上
	set, err := s.db.Exec(`UPDATE asset_card c SET c.biz_status = ?
		WHERE c.deleted_at IS NULL AND (c.biz_status = '' OR c.biz_status IS NULL)
		  AND EXISTS (SELECT 1 FROM repair_order r WHERE r.card_id = c.id AND r.status = ?)`,
		model.StatusRepair, model.RepairRepairing)
	if err != nil {
		return int(nClear), fmt.Errorf("reconcile biz_status (set): %w", err)
	}
	nSet, _ := set.RowsAffected()
	return int(nClear + nSet), nil
}

// ListOpenRepairCardIDs 返回「当前有**未到终态**维修单」的资产 id 去重集合。
//
// 口径（2026-09-24 员工可见范围第三维）：**全公司**口径，**不限报修人**——任何人的报修单，
// 只要还没结单，其 card_id 就进入集合。只给 employee 角色用（见 api/scope.go）。
// 不限报修人的理由：员工报修入口本身受资产可见范围门控，员工只能对自己看得见的资产报修，
// 故「只看我报修的」等价于现状。
//
// 「未到终态」= status NOT IN (done, rejected, cancelled)，即 model.RepairTerminalStatuses；
// 状态值一律走占位符绑定，绝不拼进 SQL 字符串。
//
// card_id > 0 是必须的：card_id 是 NOT NULL 但 0 是「未设置」哨兵值（本项目惯例），
// 不能把 0 带进调用方的 `c.id IN (...)`（否则会误命中 id=0 的伪行，且污染结果集）。
//
// 无行时返回空 slice + nil error。未加额外索引，依赖既有 idx_repair_card；
// repair_order 只增不删（单据靠终态归档），但**未结单**的数量受运营现实约束，
// 不是全表历史——故本查询在正常运营下是轻量的。
func (s *Store) ListOpenRepairCardIDs() ([]int64, error) {
	args := make([]any, 0, len(model.RepairTerminalStatuses))
	for _, st := range model.RepairTerminalStatuses {
		args = append(args, st)
	}
	q := "SELECT DISTINCT card_id FROM repair_order WHERE card_id > 0 AND status NOT IN (" +
		placeholders(len(model.RepairTerminalStatuses)) + ")"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list open repair card ids: %w", err)
	}
	defer rows.Close()

	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// VendorName 取供应商名称用于派工快照，查不到返回空串（不报错）。
func (s *Store) VendorName(id int64) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM vendor WHERE id = ?`, id).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}
