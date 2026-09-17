package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"asset-mgr/model"
)

const countItemSelect = `SELECT id, plan_id, card_id, asset_code, name, category_name,
	use_dept_name, user_name, location, use_status, book_amount,
	assignee_id, assignee_name, result, note, counted_by, counted_at
FROM count_item`

func (s *Store) ListPlans(page, pageSize int) ([]model.CountPlan, int64, error) {
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM count_plan`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count plans: %w", err)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}

	rows, err := s.db.Query(`
		SELECT p.id, p.code, p.name, p.scope_json, p.status, p.remark, p.created_by,
			p.created_at, p.started_at, p.finished_at,
			(SELECT COUNT(*) FROM count_item i WHERE i.plan_id = p.id),
			(SELECT COUNT(*) FROM count_item i WHERE i.plan_id = p.id AND i.result <> '')
		FROM count_plan p ORDER BY p.id DESC LIMIT ? OFFSET ?`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("query plans: %w", err)
	}
	defer rows.Close()

	plans := []model.CountPlan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, 0, err
		}
		plans = append(plans, *p)
	}
	return plans, total, rows.Err()
}

func scanPlan(r rowScanner) (*model.CountPlan, error) {
	var p model.CountPlan
	var scopeJSON string
	var startedAt, finishedAt sql.NullTime
	if err := r.Scan(&p.ID, &p.Code, &p.Name, &scopeJSON, &p.Status, &p.Remark, &p.CreatedBy,
		&p.CreatedAt, &startedAt, &finishedAt, &p.ItemCount, &p.CountedCount); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &p.Scope); err != nil {
		// 范围解析失败不该让整页打不开，按「全部资产」处理
		p.Scope = model.CountScope{}
	}
	if startedAt.Valid {
		p.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		p.FinishedAt = &finishedAt.Time
	}
	return &p, nil
}

func (s *Store) GetPlan(id int64) (*model.CountPlan, error) {
	row := s.db.QueryRow(`
		SELECT p.id, p.code, p.name, p.scope_json, p.status, p.remark, p.created_by,
			p.created_at, p.started_at, p.finished_at,
			(SELECT COUNT(*) FROM count_item i WHERE i.plan_id = p.id),
			(SELECT COUNT(*) FROM count_item i WHERE i.plan_id = p.id AND i.result <> '')
		FROM count_plan p WHERE p.id = ?`, id)
	p, err := scanPlan(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// CreatePlan 先插入拿自增 id，再用 id 拼编码 PD<yyyymmdd><0001>，保证编码唯一且可读。
func (s *Store) CreatePlan(name string, scope model.CountScope, remark, createdBy string) (int64, error) {
	raw, err := json.Marshal(scope)
	if err != nil {
		return 0, fmt.Errorf("marshal scope: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO count_plan (name, scope_json, remark, created_by) VALUES (?, ?, ?, ?)`,
		name, string(raw), remark, createdBy)
	if err != nil {
		return 0, fmt.Errorf("insert plan: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	code := fmt.Sprintf("PD%s%04d", time.Now().Format("20060102"), id)
	if _, err := tx.Exec(`UPDATE count_plan SET code = ? WHERE id = ?`, code, id); err != nil {
		return 0, fmt.Errorf("set plan code: %w", err)
	}
	return id, tx.Commit()
}

func (s *Store) UpdatePlan(id int64, name string, scope model.CountScope, remark string) error {
	raw, err := json.Marshal(scope)
	if err != nil {
		return fmt.Errorf("marshal scope: %w", err)
	}
	res, err := s.db.Exec(`UPDATE count_plan SET name = ?, scope_json = ?, remark = ?
		WHERE id = ? AND status = ?`, name, string(raw), remark, id, model.CountPlanDraft)
	if err != nil {
		return fmt.Errorf("update plan: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if ok, err := s.planExists(id); err != nil {
			return err
		} else if !ok {
			return sql.ErrNoRows
		}
		return errors.New("只有草稿状态的盘点计划可以修改")
	}
	return nil
}

func (s *Store) DeletePlan(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	if err := tx.QueryRow(`SELECT status FROM count_plan WHERE id = ?`, id).Scan(&status); err != nil {
		return err
	}
	if status != model.CountPlanDraft && status != model.CountPlanCancelled {
		return errors.New("只有草稿或已取消的盘点计划可以删除")
	}
	if _, err := tx.Exec(`DELETE FROM count_item WHERE plan_id = ?`, id); err != nil {
		return fmt.Errorf("delete count items: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM count_plan WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete plan: %w", err)
	}
	return tx.Commit()
}

// EmployeeName 取员工姓名用于回填盘点人快照，查不到返回空串（不报错）。
func (s *Store) EmployeeName(id int64) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM employee WHERE id = ?`, id).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}

func (s *Store) planExists(id int64) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM count_plan WHERE id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// GenerateItems 按计划范围把资产快照进 count_item。重复生成会先清空旧明细（连带清掉已录入的结果）。
func (s *Store) GenerateItems(planID int64, scope model.CountScope) (int, error) {
	// 计划范围由 count_plan.scope 自己决定，不再叠加账号可见范围
	where, args := buildWhere(model.ListQuery{
		UseDeptID:    scope.UseDeptID,
		CategoryID:   scope.CategoryID,
		AreaID:       scope.AreaID,
		UseCompanyID: scope.UseCompanyID,
	}, model.AssetScope{})
	rows, err := s.db.Query(cardSelect+where+" ORDER BY c.id", args...)
	if err != nil {
		return 0, fmt.Errorf("query assets for plan: %w", err)
	}
	var cards []model.AssetCard
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		cards = append(cards, *c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM count_item WHERE plan_id = ?`, planID); err != nil {
		return 0, fmt.Errorf("clear count items: %w", err)
	}
	for _, c := range cards {
		if _, err := tx.Exec(`INSERT INTO count_item
			(plan_id, card_id, asset_code, name, category_name, use_dept_name, user_name,
			 location, use_status, book_amount)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			planID, c.ID, c.AssetCode, c.Name, c.CategoryName, c.UseDeptName, c.UserEmpName,
			c.Location, c.UseStatus, c.Amount); err != nil {
			return 0, fmt.Errorf("insert count item: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE count_plan SET status = ?, started_at = COALESCE(started_at, NOW())
		WHERE id = ?`, model.CountPlanCounting, planID); err != nil {
		return 0, fmt.Errorf("start plan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(cards), nil
}

// AssignItems 把若干明细指派给某个员工（assignee_id 指向 employee.id）。
func (s *Store) AssignItems(planID int64, itemIDs []int64, assigneeID int64, assigneeName string) error {
	if len(itemIDs) == 0 {
		return errors.New("没有选中任何资产")
	}
	args := []any{assigneeID, assigneeName, planID}
	for _, id := range itemIDs {
		args = append(args, id)
	}
	res, err := s.db.Exec(`UPDATE count_item SET assignee_id = ?, assignee_name = ?
		WHERE plan_id = ? AND id IN (`+placeholders(len(itemIDs))+`)`, args...)
	if err != nil {
		return fmt.Errorf("assign items: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("没有可指派的盘点明细")
	}
	return nil
}

// CountResultInput 是盘点员提交的单行结果。
type CountResultInput struct {
	ItemID int64  `json:"item_id"`
	Result string `json:"result"`
	Note   string `json:"note"`
}

// SubmitResults 批量写入盘点结果。restrictEmployeeID > 0 时只能改指派给该员工的明细
// （盘点员权限），0 表示不限制（管理员代录）。
func (s *Store) SubmitResults(planID, restrictEmployeeID int64, countedBy string, results []CountResultInput) (int, error) {
	if len(results) == 0 {
		return 0, nil
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM count_plan WHERE id = ?`, planID).Scan(&status); err != nil {
		return 0, err
	}
	if status != model.CountPlanCounting {
		return 0, errors.New("盘点计划不在盘点中，无法录入")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	updated := 0
	for _, r := range results {
		sqlStr := `UPDATE count_item SET result = ?, note = ?, counted_by = ?, counted_at = NOW()
			WHERE plan_id = ? AND id = ?`
		args := []any{r.Result, r.Note, countedBy, planID, r.ItemID}
		if restrictEmployeeID > 0 {
			sqlStr += ` AND assignee_id = ?`
			args = append(args, restrictEmployeeID)
		}
		res, err := tx.Exec(sqlStr, args...)
		if err != nil {
			return 0, fmt.Errorf("submit result: %w", err)
		}
		n, _ := res.RowsAffected()
		updated += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

func (s *Store) FinishPlan(id int64) error {
	res, err := s.db.Exec(`UPDATE count_plan SET status = ?, finished_at = NOW()
		WHERE id = ? AND status = ?`, model.CountPlanDone, id, model.CountPlanCounting)
	if err != nil {
		return fmt.Errorf("finish plan: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("只有盘点中的计划可以完成")
	}
	return nil
}

func (s *Store) CancelPlan(id int64) error {
	res, err := s.db.Exec(`UPDATE count_plan SET status = ?, finished_at = NOW()
		WHERE id = ? AND status IN (?, ?)`,
		model.CountPlanCancelled, id, model.CountPlanDraft, model.CountPlanCounting)
	if err != nil {
		return fmt.Errorf("cancel plan: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("该计划无法取消")
	}
	return nil
}

// CountItemQuery 明细查询条件。AssigneeID > 0 时只返回指派给该员工的明细（盘点员视角）。
type CountItemQuery struct {
	AssigneeID int64
	Result     string
	Keyword    string
}

func (s *Store) ListItems(planID int64, q CountItemQuery) ([]model.CountItem, error) {
	conds := []string{"plan_id = ?"}
	args := []any{planID}
	if q.AssigneeID > 0 {
		conds = append(conds, "assignee_id = ?")
		args = append(args, q.AssigneeID)
	}
	if q.Result != "" {
		conds = append(conds, "result = ?")
		args = append(args, q.Result)
	}
	if q.Keyword != "" {
		conds = append(conds, "(asset_code LIKE ? OR name LIKE ?)")
		kw := "%" + q.Keyword + "%"
		args = append(args, kw, kw)
	}
	rows, err := s.db.Query(countItemSelect+" WHERE "+strings.Join(conds, " AND ")+" ORDER BY id", args...)
	if err != nil {
		return nil, fmt.Errorf("query count items: %w", err)
	}
	defer rows.Close()

	items := []model.CountItem{}
	for rows.Next() {
		it, err := scanCountItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *it)
	}
	return items, rows.Err()
}

// CardCountItem 一台资产当前落在哪些进行中的盘点计划里。
// 一张资产可能同时被多个计划圈进去，所以是列表而不是单条。
type CardCountItem struct {
	PlanID       int64  `json:"plan_id"`
	PlanCode     string `json:"plan_code"`
	PlanName     string `json:"plan_name"`
	ItemID       int64  `json:"item_id"`
	Result       string `json:"result"`
	AssigneeID   int64  `json:"assignee_id"`
	AssigneeName string `json:"assignee_name"`
}

// CountItemsByCard 查这台资产在「盘点中」计划里的明细。
// assigneeID > 0 时（盘点员视角）只返回指派给自己的，与 ListItems 的口径一致。
func (s *Store) CountItemsByCard(cardID, assigneeID int64) ([]CardCountItem, error) {
	conds := []string{"i.card_id = ?", "p.status = ?"}
	args := []any{cardID, model.CountPlanCounting}
	if assigneeID > 0 {
		conds = append(conds, "i.assignee_id = ?")
		args = append(args, assigneeID)
	}
	rows, err := s.db.Query(`SELECT p.id, p.code, p.name, i.id, i.result, i.assignee_id, i.assignee_name
		FROM count_item i JOIN count_plan p ON p.id = i.plan_id
		WHERE `+strings.Join(conds, " AND ")+` ORDER BY p.id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query count items by card: %w", err)
	}
	defer rows.Close()

	items := []CardCountItem{}
	for rows.Next() {
		var it CardCountItem
		if err := rows.Scan(&it.PlanID, &it.PlanCode, &it.PlanName, &it.ItemID,
			&it.Result, &it.AssigneeID, &it.AssigneeName); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func scanCountItem(r rowScanner) (*model.CountItem, error) {
	var it model.CountItem
	var countedAt sql.NullTime
	if err := r.Scan(&it.ID, &it.PlanID, &it.CardID, &it.AssetCode, &it.Name, &it.CategoryName,
		&it.UseDeptName, &it.UserName, &it.Location, &it.UseStatus, &it.BookAmount,
		&it.AssigneeID, &it.AssigneeName, &it.Result, &it.Note, &it.CountedBy, &countedAt); err != nil {
		return nil, err
	}
	if countedAt.Valid {
		it.CountedAt = &countedAt.Time
	}
	return &it, nil
}

func (s *Store) CountSummary(planID, assigneeID int64) (*model.CountSummary, error) {
	where := "plan_id = ?"
	args := []any{planID}
	if assigneeID > 0 {
		where += " AND assignee_id = ?"
		args = append(args, assigneeID)
	}
	var sum model.CountSummary
	params := []any{model.CountResultNormal, model.CountResultLoss, model.CountResultGain, model.CountResultDamaged}
	params = append(params, args...)
	err := s.db.QueryRow(`SELECT
		COALESCE(COUNT(*),0),
		COALESCE(SUM(result <> ''),0),
		COALESCE(SUM(result = ''),0),
		COALESCE(SUM(result = ?),0),
		COALESCE(SUM(result = ?),0),
		COALESCE(SUM(result = ?),0),
		COALESCE(SUM(result = ?),0),
		COALESCE(SUM(assignee_id > 0),0)
		FROM count_item WHERE `+where,
		params...,
	).Scan(&sum.Total, &sum.Counted, &sum.Uncounted, &sum.Normal, &sum.Loss, &sum.Gain, &sum.Damaged, &sum.Assigned)
	if err != nil {
		return nil, fmt.Errorf("count summary: %w", err)
	}
	return &sum, nil
}

// Report 汇总 + 明细，供报表页与打印使用。
func (s *Store) Report(planID int64) (*model.CountReport, error) {
	plan, err := s.GetPlan(planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, nil
	}
	items, err := s.ListItems(planID, CountItemQuery{})
	if err != nil {
		return nil, err
	}
	sum, err := s.CountSummary(planID, 0)
	if err != nil {
		return nil, err
	}
	return &model.CountReport{Plan: plan, Summary: *sum, Items: items}, nil
}
