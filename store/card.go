package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"asset-mgr/model"
)

const cardSelect = `SELECT c.id, c.asset_code, c.name, c.category_id, cat.name,
	c.spec, c.serial_no, c.unit, c.status, c.amount, c.quantity,
	c.use_company_id, uc.name, c.use_dept_id, ud.name, c.user_emp_id, ue.name, c.use_status,
	c.manager_emp_id, me.name, c.owner_company_id, oc.name, c.area_id, ar.name,
	c.location, c.purchase_date, c.card_created_at, c.use_months, c.source, c.in_stock_no, c.rfid, c.remark,
	c.fin_asset_type, c.fin_share_dept_id, sd.name, c.vendor_id, v.name,
	c.fin_amount_with_tax, c.fin_tax, c.fin_original_value, c.fin_net_value,
	c.fin_accum_depreciation, c.fin_residual_rate, c.fin_use_months, c.fin_period,
	c.fin_entry_date, c.fin_status,
	c.mt_vendor_id, mv.name, c.mt_contact, c.mt_phone, c.mt_owner_emp_id, mo.name,
	c.mt_expire_date, c.mt_remark,
	c.created_by, c.created_at, c.updated_at
FROM asset_card c
LEFT JOIN asset_category cat ON cat.id = c.category_id
LEFT JOIN company uc ON uc.id = c.use_company_id
LEFT JOIN department ud ON ud.id = c.use_dept_id
LEFT JOIN employee ue ON ue.id = c.user_emp_id
LEFT JOIN employee me ON me.id = c.manager_emp_id
LEFT JOIN company oc ON oc.id = c.owner_company_id
LEFT JOIN asset_area ar ON ar.id = c.area_id
LEFT JOIN department sd ON sd.id = c.fin_share_dept_id
LEFT JOIN vendor v ON v.id = c.vendor_id
LEFT JOIN vendor mv ON mv.id = c.mt_vendor_id
LEFT JOIN employee mo ON mo.id = c.mt_owner_emp_id`

// sortColumns 是排序字段白名单：请求里传的是 key，永远不会把用户输入拼进 SQL。
var sortColumns = map[string]string{
	"asset_code":    "c.asset_code",
	"name":          "c.name",
	"status":        "c.status",
	"amount":        "c.amount",
	"category":      "cat.name",
	"purchase_date": "c.purchase_date",
	"created_at":    "c.created_at",
	"updated_at":    "c.updated_at",
}

func buildWhere(q model.ListQuery, sc model.AssetScope) (string, []any) {
	conds := []string{"c.deleted_at IS NULL"}
	args := []any{}

	// 可见范围必须进 WHERE 而不是取回来再滤：分页、COUNT、SUM 都拼这条 where，
	// 后滤会让总数和金额合计都对不上。零值 scope 一行都不加，admin 的 SQL 与从前逐字节一致。
	if sc.SelfEmpID > 0 {
		conds = append(conds, "c.user_emp_id = ?")
		args = append(args, sc.SelfEmpID)
	}
	if len(sc.DeptIDs) > 0 {
		conds = append(conds, "c.use_dept_id IN ("+placeholders(len(sc.DeptIDs))+")")
		for _, id := range sc.DeptIDs {
			args = append(args, id)
		}
	}

	if q.Keyword != "" {
		kw := "%" + q.Keyword + "%"
		// 使用人/使用部门/存放地点也进关键词：现场最常见的问法是「这台在谁那儿 / 哪个部门 / 放哪」，
		// 让一个框就能答，不必先展开高级搜索再逐项填。
		conds = append(conds, "(c.asset_code LIKE ? OR c.name LIKE ? OR c.spec LIKE ? OR c.serial_no LIKE ? OR c.rfid LIKE ?"+
			" OR ue.name LIKE ? OR ud.name LIKE ? OR c.location LIKE ?)")
		args = append(args, kw, kw, kw, kw, kw, kw, kw, kw)
	}
	if len(q.Status) > 0 {
		conds = append(conds, "c.status IN ("+placeholders(len(q.Status))+")")
		for _, s := range q.Status {
			args = append(args, s)
		}
	}
	for _, f := range []struct {
		col string
		val int64
	}{
		{"c.category_id", q.CategoryID},
		{"c.area_id", q.AreaID},
		{"c.use_dept_id", q.UseDeptID},
		{"c.use_company_id", q.UseCompanyID},
	} {
		if f.val > 0 {
			conds = append(conds, f.col+" = ?")
			args = append(args, f.val)
		}
	}
	for _, f := range []struct {
		col string
		val string
	}{
		{"c.source", q.Source},
		{"c.fin_status", q.FinStatus},
	} {
		if f.val != "" {
			conds = append(conds, f.col+" = ?")
			args = append(args, f.val)
		}
	}
	for _, f := range []struct {
		col string
		val string
	}{
		{"c.asset_code", q.AssetCode},
		{"c.name", q.Name},
		{"c.serial_no", q.SerialNo},
		{"me.name", q.ManagerName},
		{"ue.name", q.UserName},
		{"c.location", q.Location},
	} {
		if f.val != "" {
			conds = append(conds, f.col+" LIKE ?")
			args = append(args, "%"+f.val+"%")
		}
	}
	if q.PurchaseFrom != "" {
		conds = append(conds, "c.purchase_date >= ?")
		args = append(args, q.PurchaseFrom)
	}
	if q.PurchaseTo != "" {
		conds = append(conds, "c.purchase_date <= ?")
		args = append(args, q.PurchaseTo)
	}
	if q.AmountMin != nil {
		conds = append(conds, "c.amount >= ?")
		args = append(args, *q.AmountMin)
	}
	if q.AmountMax != nil {
		conds = append(conds, "c.amount <= ?")
		args = append(args, *q.AmountMax)
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Store) ListCards(q model.ListQuery, sc model.AssetScope) (*model.ListResult, error) {
	where, args := buildWhere(q, sc)

	var total int64
	var amountTotal sql.NullFloat64
	// 这里必须和 cardSelect 的 JOIN 对齐：buildWhere 会引用 ue.name / ud.name（关键词、使用人、使用部门），
	// 少一个 JOIN 就是整条 SQL 报 unknown column。三张表都按主键关联，不会放大 COUNT。
	countSQL := `SELECT COUNT(*), COALESCE(SUM(c.amount),0) FROM asset_card c
		LEFT JOIN employee me ON me.id = c.manager_emp_id
		LEFT JOIN employee ue ON ue.id = c.user_emp_id
		LEFT JOIN department ud ON ud.id = c.use_dept_id` + where
	if err := s.db.QueryRow(countSQL, args...).Scan(&total, &amountTotal); err != nil {
		return nil, fmt.Errorf("count cards: %w", err)
	}

	order := "c.id DESC"
	if col, ok := sortColumns[q.SortBy]; ok {
		dir := "ASC"
		if q.SortDesc {
			dir = "DESC"
		}
		order = col + " " + dir
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > 500 {
		q.PageSize = 50
	}

	rows, err := s.db.Query(cardSelect+where+" ORDER BY "+order+" LIMIT ? OFFSET ?",
		append(args, q.PageSize, (q.Page-1)*q.PageSize)...)
	if err != nil {
		return nil, fmt.Errorf("query cards: %w", err)
	}
	defer rows.Close()

	items := []model.AssetCard{}
	ids := []int64{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachTags(items, ids); err != nil {
		return nil, err
	}

	return &model.ListResult{
		Items:       items,
		Total:       total,
		AmountTotal: amountTotal.Float64,
		Page:        q.Page,
		PageSize:    q.PageSize,
	}, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCard(r rowScanner) (*model.AssetCard, error) {
	var c model.AssetCard
	var catName, ucName, udName, ueName, meName, ocName, arName sql.NullString
	var sdName, vName, mvName, moName sql.NullString
	var purchaseDate, cardCreatedAt, finEntryDate, mtExpireDate sql.NullTime

	err := r.Scan(
		&c.ID, &c.AssetCode, &c.Name, &c.CategoryID, &catName,
		&c.Spec, &c.SerialNo, &c.Unit, &c.Status, &c.Amount, &c.Quantity,
		&c.UseCompanyID, &ucName, &c.UseDeptID, &udName, &c.UserEmpID, &ueName, &c.UseStatus,
		&c.ManagerEmpID, &meName, &c.OwnerCompanyID, &ocName, &c.AreaID, &arName,
		&c.Location, &purchaseDate, &cardCreatedAt, &c.UseMonths, &c.Source, &c.InStockNo, &c.RFID, &c.Remark,
		&c.FinAssetType, &c.FinShareDeptID, &sdName, &c.VendorID, &vName,
		&c.FinAmountWithTax, &c.FinTax, &c.FinOriginalValue, &c.FinNetValue,
		&c.FinAccumDepreciaton, &c.FinResidualRate, &c.FinUseMonths, &c.FinPeriod,
		&finEntryDate, &c.FinStatus,
		&c.MtVendorID, &mvName, &c.MtContact, &c.MtPhone, &c.MtOwnerEmpID, &moName,
		&mtExpireDate, &c.MtRemark,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	c.CategoryName = catName.String
	c.UseCompanyName = ucName.String
	c.UseDeptName = udName.String
	c.UserEmpName = ueName.String
	c.ManagerEmpName = meName.String
	c.OwnerCompanyName = ocName.String
	c.AreaName = arName.String
	c.FinShareDeptName = sdName.String
	c.VendorName = vName.String
	c.MtVendorName = mvName.String
	c.MtOwnerEmpName = moName.String
	c.PurchaseDate = dateStr(purchaseDate)
	c.CardCreatedAt = dateTimeStr(cardCreatedAt)
	c.FinEntryDate = dateStr(finEntryDate)
	c.MtExpireDate = dateStr(mtExpireDate)
	return &c, nil
}

func dateStr(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02")
}

// dateTimeStr 建卡时间要精确到秒，与只展示日期的 dateStr 区分开
func dateTimeStr(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02 15:04:05")
}

func nullDate(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func (s *Store) GetCard(id int64) (*model.AssetCard, error) {
	row := s.db.QueryRow(cardSelect+" WHERE c.deleted_at IS NULL AND c.id = ?", id)
	c, err := scanCard(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tags, ids, err := s.cardTags([]int64{id})
	if err != nil {
		return nil, err
	}
	c.Tags = tags[id]
	c.TagIDs = ids[id]
	return c, nil
}

// GetCardByCode 按资产编码精确查一台资产 —— 扫码解析出来的就是编码，
// 列表页那个 asset_code 过滤是 LIKE 模糊匹配，命中多台时无法确定扫的是哪一台。
func (s *Store) GetCardByCode(code string) (*model.AssetCard, error) {
	row := s.db.QueryRow(cardSelect+" WHERE c.deleted_at IS NULL AND c.asset_code = ?", code)
	c, err := scanCard(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tags, ids, err := s.cardTags([]int64{c.ID})
	if err != nil {
		return nil, err
	}
	c.Tags = tags[c.ID]
	c.TagIDs = ids[c.ID]
	return c, nil
}

// GetCardsByIDs 批量取资产，供标签批量打印。
// 结果按入参顺序返回：IN 查询吐出来的是主键序，不重排的话打印顺序和用户勾选顺序
// 对不上，贴标时会错位。入参里不存在的 id 直接跳过，不报错。
func (s *Store) GetCardsByIDs(ids []int64) ([]model.AssetCard, error) {
	if len(ids) == 0 {
		return []model.AssetCard{}, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(
		cardSelect+" WHERE c.deleted_at IS NULL AND c.id IN ("+placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[int64]model.AssetCard, len(ids))
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		byID[c.ID] = *c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]model.AssetCard, 0, len(byID))
	seen := make(map[int64]bool, len(byID))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if c, ok := byID[id]; ok {
			out = append(out, c)
			seen[id] = true
		}
	}
	if err := s.attachTags(out, ids); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) cardTags(cardIDs []int64) (map[int64][]string, map[int64][]int64, error) {
	names := map[int64][]string{}
	ids := map[int64][]int64{}
	if len(cardIDs) == 0 {
		return names, ids, nil
	}
	args := make([]any, len(cardIDs))
	for i, id := range cardIDs {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT ct.card_id, t.id, t.name FROM asset_card_tag ct
		JOIN asset_tag t ON t.id = ct.tag_id
		WHERE ct.card_id IN (`+placeholders(len(cardIDs))+`)`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query card tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cardID, tagID int64
		var name string
		if err := rows.Scan(&cardID, &tagID, &name); err != nil {
			return nil, nil, err
		}
		names[cardID] = append(names[cardID], name)
		ids[cardID] = append(ids[cardID], tagID)
	}
	return names, ids, rows.Err()
}

func (s *Store) attachTags(items []model.AssetCard, ids []int64) error {
	if len(items) == 0 || len(ids) == 0 {
		return nil
	}
	names, tagIDs, err := s.cardTags(ids)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Tags = names[items[i].ID]
		items[i].TagIDs = tagIDs[items[i].ID]
	}
	return nil
}

// cardWriteColumns 与 cardWriteArgs 必须一一对应
var cardWriteColumns = []string{
	"asset_code", "name", "category_id", "spec", "serial_no", "unit", "status", "amount", "quantity",
	"use_company_id", "use_dept_id", "user_emp_id", "manager_emp_id",
	"owner_company_id", "area_id", "location", "purchase_date", "use_months",
	"source", "in_stock_no", "rfid", "remark",
	"fin_asset_type", "fin_share_dept_id", "vendor_id", "fin_amount_with_tax", "fin_tax",
	"fin_original_value", "fin_net_value", "fin_accum_depreciation", "fin_residual_rate",
	"fin_use_months", "fin_period", "fin_entry_date", "fin_status",
	"mt_vendor_id", "mt_contact", "mt_phone", "mt_owner_emp_id", "mt_expire_date", "mt_remark",
}

func cardWriteArgs(c *model.AssetCard) []any {
	return []any{
		c.AssetCode, c.Name, c.CategoryID, c.Spec, c.SerialNo, c.Unit, c.Status, c.Amount, c.Quantity,
		c.UseCompanyID, c.UseDeptID, c.UserEmpID, c.ManagerEmpID,
		c.OwnerCompanyID, c.AreaID, c.Location, nullDate(c.PurchaseDate), c.UseMonths,
		c.Source, c.InStockNo, c.RFID, c.Remark,
		c.FinAssetType, c.FinShareDeptID, c.VendorID, c.FinAmountWithTax, c.FinTax,
		c.FinOriginalValue, c.FinNetValue, c.FinAccumDepreciaton, c.FinResidualRate,
		c.FinUseMonths, c.FinPeriod, nullDate(c.FinEntryDate), c.FinStatus,
		c.MtVendorID, c.MtContact, c.MtPhone, c.MtOwnerEmpID, nullDate(c.MtExpireDate), c.MtRemark,
	}
}

func (s *Store) CreateCard(c *model.AssetCard, operator string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if c.AssetCode == "" {
		code, err := nextCodeTx(tx, c.CategoryID)
		if err != nil {
			return 0, err
		}
		c.AssetCode = code
	}

	cols := append([]string{}, cardWriteColumns...)
	cols = append(cols, "created_by")
	args := append(cardWriteArgs(c), operator)

	q := fmt.Sprintf("INSERT INTO asset_card (%s) VALUES (%s)",
		strings.Join(cols, ","), placeholders(len(cols)))
	res, err := tx.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("insert card: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := syncTagsTx(tx, id, c.TagIDs); err != nil {
		return 0, err
	}
	if err := addHistoryTx(tx, id, "create", "", "", c.AssetCode, operator); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) UpdateCard(id int64, c *model.AssetCard, operator string) error {
	old, err := s.GetCard(id)
	if err != nil {
		return err
	}
	if old == nil {
		return sql.ErrNoRows
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	sets := make([]string, len(cardWriteColumns))
	for i, col := range cardWriteColumns {
		sets[i] = col + " = ?"
	}
	args := append(cardWriteArgs(c), id)
	if _, err := tx.Exec("UPDATE asset_card SET "+strings.Join(sets, ",")+" WHERE id = ? AND deleted_at IS NULL", args...); err != nil {
		return fmt.Errorf("update card: %w", err)
	}
	if err := syncTagsTx(tx, id, c.TagIDs); err != nil {
		return err
	}
	for _, d := range diffCard(old, c) {
		if err := addHistoryTx(tx, id, "update", d.Field, d.Old, d.New, operator); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteCard(id int64, operator string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec("UPDATE asset_card SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL",
		time.Now(), id)
	if err != nil {
		return fmt.Errorf("delete card: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if err := addHistoryTx(tx, id, "delete", "", "", "", operator); err != nil {
		return err
	}
	return tx.Commit()
}

func syncTagsTx(tx *sql.Tx, cardID int64, tagIDs []int64) error {
	if _, err := tx.Exec("DELETE FROM asset_card_tag WHERE card_id = ?", cardID); err != nil {
		return err
	}
	for _, tid := range tagIDs {
		if tid <= 0 {
			continue
		}
		if _, err := tx.Exec("INSERT INTO asset_card_tag (card_id, tag_id) VALUES (?, ?)", cardID, tid); err != nil {
			return err
		}
	}
	return nil
}

// NextCode 预览下一个编码但不占用流水号
func (s *Store) NextCode(categoryID int64) (string, error) {
	prefix, width, seq, err := ruleFor(s.db, categoryID)
	if err != nil {
		return "", err
	}
	return formatCode(prefix, width, seq+1), nil
}

func nextCodeTx(tx *sql.Tx, categoryID int64) (string, error) {
	prefix, width, seq, err := ruleFor(tx, categoryID)
	if err != nil {
		return "", err
	}
	seq++
	if _, err := tx.Exec("UPDATE code_rule SET current_seq = ? WHERE category_id = ?", seq, ruleKey(tx, categoryID)); err != nil {
		return "", err
	}
	return formatCode(prefix, width, seq), nil
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// ruleFor 优先取分类专属规则，没有则回落到全局规则（category_id = 0）
func ruleFor(q queryer, categoryID int64) (string, int, int64, error) {
	var prefix string
	var width int
	var seq int64
	err := q.QueryRow(`SELECT prefix, seq_width, current_seq FROM code_rule
		WHERE category_id IN (?, 0) ORDER BY category_id DESC LIMIT 1`, categoryID).
		Scan(&prefix, &width, &seq)
	if err == sql.ErrNoRows {
		return "", 0, 0, fmt.Errorf("no code_rule configured")
	}
	if err != nil {
		return "", 0, 0, err
	}
	return prefix, width, seq, nil
}

func ruleKey(q queryer, categoryID int64) int64 {
	var id int64
	if err := q.QueryRow(`SELECT category_id FROM code_rule
		WHERE category_id IN (?, 0) ORDER BY category_id DESC LIMIT 1`, categoryID).Scan(&id); err != nil {
		return 0
	}
	return id
}

func formatCode(prefix string, width int, seq int64) string {
	return fmt.Sprintf("%s%0*d", prefix, width, seq)
}

// UpdateCardStatus 批量改状态：一个事务内逐条比对旧值并写履历，状态没变的跳过
func (s *Store) UpdateCardStatus(ids []int64, status, operator string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	changed := 0
	for _, id := range ids {
		var old string
		err := tx.QueryRow("SELECT status FROM asset_card WHERE id = ? AND deleted_at IS NULL", id).Scan(&old)
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("资产 id=%d 不存在", id)
		}
		if err != nil {
			return 0, err
		}
		if old == status {
			continue
		}
		if _, err := tx.Exec("UPDATE asset_card SET status = ? WHERE id = ?", status, id); err != nil {
			return 0, fmt.Errorf("更新 id=%d 失败: %w", id, err)
		}
		if err := addHistoryTx(tx, id, "update", "状态", old, status, operator); err != nil {
			return 0, err
		}
		changed++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

// ImportCards 一个事务内批量写入：任何一行失败则整批回滚，不产生部分写入
func (s *Store) ImportCards(cards []model.AssetCard, operator string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	cols := append([]string{}, cardWriteColumns...)
	cols = append(cols, "created_by")
	insertSQL := fmt.Sprintf("INSERT INTO asset_card (%s) VALUES (%s)",
		strings.Join(cols, ","), placeholders(len(cols)))
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for i := range cards {
		c := &cards[i]
		if c.AssetCode == "" {
			code, err := nextCodeTx(tx, c.CategoryID)
			if err != nil {
				return 0, fmt.Errorf("第 %d 行生成编码失败: %w", i+1, err)
			}
			c.AssetCode = code
		}
		res, err := stmt.Exec(append(cardWriteArgs(c), operator)...)
		if err != nil {
			return 0, fmt.Errorf("第 %d 行写入失败: %w", i+1, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if err := addHistoryTx(tx, id, "import", "", "", c.AssetCode, operator); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(cards), nil
}

// ExistingCodes 用于导入前查重
func (s *Store) ExistingCodes(codes []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(codes) == 0 {
		return out, nil
	}
	args := make([]any, len(codes))
	for i, c := range codes {
		args[i] = c
	}
	rows, err := s.db.Query("SELECT asset_code FROM asset_card WHERE deleted_at IS NULL AND asset_code IN ("+
		placeholders(len(codes))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}
