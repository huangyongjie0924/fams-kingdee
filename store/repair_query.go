package store

import (
	"database/sql"
	"fmt"
	"strings"

	"asset-mgr/model"
)

// repairOrderBy 默认排序：先按业务推进顺序（待受理最前），同组内新单在前。
// FIELD 是 MySQL 函数，本项目只跑 MySQL；未知状态落到 0（排最后），便于排障时看到怪值。
const repairOrderBy = `ORDER BY FIELD(status,
	'pending','accepted','approving','dispatched','repairing','confirming','scrapping','done','rejected','cancelled'),
	id DESC`

// buildRepairWhere 把查询条件与可见范围拼成 WHERE。
// 可见范围必须进 WHERE（而不是取回来再滤）：分页与 COUNT 都拼这条，后滤会让总数对不上。
func buildRepairWhere(q model.RepairListQuery, sc model.RepairScope) (string, []any) {
	conds := []string{"1=1"}
	args := []any{}

	// 三种收窄口径命中其一即可见（与 model.RepairScope.Allows 一致）
	if sc.Restricted() {
		parts := []string{}
		if sc.ReporterEmpID > 0 {
			parts = append(parts, "reporter_emp_id = ?")
			args = append(args, sc.ReporterEmpID)
		}
		if sc.AssigneeEmpID > 0 {
			parts = append(parts, "assignee_emp_id = ?")
			args = append(args, sc.AssigneeEmpID)
		}
		if len(sc.DeptIDs) > 0 {
			parts = append(parts, "use_dept_id IN ("+placeholders(len(sc.DeptIDs))+")")
			for _, id := range sc.DeptIDs {
				args = append(args, id)
			}
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}

	if len(q.Status) > 0 {
		conds = append(conds, "status IN ("+placeholders(len(q.Status))+")")
		for _, s := range q.Status {
			args = append(args, s)
		}
	}
	if q.UseDeptID > 0 {
		conds = append(conds, "use_dept_id = ?")
		args = append(args, q.UseDeptID)
	}
	if q.CardID > 0 {
		conds = append(conds, "card_id = ?")
		args = append(args, q.CardID)
	}
	if q.AssetCode != "" {
		conds = append(conds, "asset_code LIKE ?")
		args = append(args, "%"+q.AssetCode+"%")
	}
	if q.Keyword != "" {
		kw := "%" + q.Keyword + "%"
		conds = append(conds, "(code LIKE ? OR asset_code LIKE ? OR asset_name LIKE ? OR fault_desc LIKE ? OR reporter_name LIKE ?)")
		args = append(args, kw, kw, kw, kw, kw)
	}
	if q.From != "" {
		conds = append(conds, "created_at >= ?")
		args = append(args, q.From)
	}
	if q.To != "" {
		conds = append(conds, "created_at <= ?")
		args = append(args, q.To)
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListRepairs 维修单列表：按条件筛选 + 可见范围收窄 + 分页。
func (s *Store) ListRepairs(q model.RepairListQuery, sc model.RepairScope) (*model.RepairListResult, error) {
	where, args := buildRepairWhere(q, sc)

	var total int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM repair_order"+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count repairs: %w", err)
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > 500 {
		q.PageSize = 20
	}

	rows, err := s.db.Query(repairOrderSelect+where+" "+repairOrderBy+" LIMIT ? OFFSET ?",
		append(args, q.PageSize, (q.Page-1)*q.PageSize)...)
	if err != nil {
		return nil, fmt.Errorf("query repairs: %w", err)
	}
	defer rows.Close()

	items := []model.RepairOrder{}
	for rows.Next() {
		o, err := scanRepairOrder(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &model.RepairListResult{Items: items, Total: total, Page: q.Page, PageSize: q.PageSize}, nil
}

// ListRepairsByCard 一台资产的历次维修记录（挂在资产卡上，供报废判断有据可查）。
// 资产卡详情的维修记录走它，可见范围由调用方先做 checkCardScope 保证。
func (s *Store) ListRepairsByCard(cardID int64) ([]model.RepairOrder, error) {
	rows, err := s.db.Query(repairOrderSelect+" WHERE card_id = ? ORDER BY id DESC", cardID)
	if err != nil {
		return nil, fmt.Errorf("query repairs by card: %w", err)
	}
	defer rows.Close()

	out := []model.RepairOrder{}
	for rows.Next() {
		o, err := scanRepairOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// SaveRepairAttachment 登记维修附件元数据（物理文件由 api 层落 uploads/）。
func (s *Store) SaveRepairAttachment(repairID, cardID int64, kind, originName, storedPath string, size int64, by string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO repair_attachment
		(repair_id, card_id, kind, origin_name, stored_path, size_bytes, uploaded_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		repairID, cardID, kind, originName, storedPath, size, by)
	if err != nil {
		return 0, fmt.Errorf("insert repair attachment: %w", err)
	}
	return res.LastInsertId()
}

// ListRepairAttachments 取某张维修单的附件。
func (s *Store) ListRepairAttachments(repairID int64) ([]model.RepairAttachment, error) {
	rows, err := s.db.Query(`SELECT id, repair_id, card_id, kind, origin_name, stored_path, size_bytes, uploaded_by, created_at
		FROM repair_attachment WHERE repair_id = ? ORDER BY id`, repairID)
	if err != nil {
		return nil, fmt.Errorf("query repair attachments: %w", err)
	}
	defer rows.Close()

	out := []model.RepairAttachment{}
	for rows.Next() {
		var a model.RepairAttachment
		var stored string
		if err := rows.Scan(&a.ID, &a.RepairID, &a.CardID, &a.Kind, &a.OriginName,
			&stored, &a.SizeBytes, &a.UploadedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.URL = "/uploads/" + stored
		out = append(out, a)
	}
	return out, rows.Err()
}

// bindRepairAttachmentsTx 把「先上传待绑定」的附件（repair_id=0）回填到单据。
// 只动 repair_id=0 的行，避免把已归属其它单据的附件抢过来。
func bindRepairAttachmentsTx(tx *sql.Tx, repairID, cardID int64, ids []int64) error {
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE repair_attachment SET repair_id = ?, card_id = ?
			WHERE id = ? AND repair_id = 0`, repairID, cardID, id); err != nil {
			return fmt.Errorf("bind repair attachment %d: %w", id, err)
		}
	}
	return nil
}
