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

// VendorName 取供应商名称用于派工快照，查不到返回空串（不报错）。
func (s *Store) VendorName(id int64) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM vendor WHERE id = ?`, id).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}
