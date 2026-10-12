package store

import (
	"database/sql"
	"fmt"
	"strings"

	"asset-mgr/model"
)

// itSelect 是 IT 设备台账的**唯一**读取投影。
//
// 全仓约定：无 `SELECT *`、显式列名、固定 Scan 顺序 —— 这样将来加列不会破坏既有 Scan
// （见 model/it.go 与 store/card.go 的同款注释）。
//
// ⚠️ card_id 必须走 `COALESCE(c.card_id, 0)`：库里这一列是可空列（NULL = 未关联资产卡，
// 见 store/mysql.go 的 it_equipment DDL 注释），而 Go 侧用 int64 的 0 表示「未关联」。
// 少了 COALESCE，未关联的行会 Scan 失败（NULL → int64 报错）。
//
// ⚠️ 三张 LEFT JOIN 都是按主键关联，不会放大行数：
//   - ac（asset_card）：带出资产名称，供列表展示。**刻意不过滤 ac.deleted_at**——
//     资产卡被软删后，这条 IT 记录仍在，此时显示「卡还在时的名称」比显示空白有用得多；
//     而 asset_code 快照（本表自己的列）始终保留，是卡被删后唯一的辨认线索。
//   - ue（employee）/ ud（department）：带出使用人与部门名称。
const itSelect = `SELECT c.id, COALESCE(c.card_id, 0), c.asset_code, ac.name,
	c.device_type, c.user_emp_id, ue.name, c.dept_id, ud.name,
	c.location, c.purpose, c.status, c.is_xinchuang,
	c.brand_model, c.cpu, c.memory_gb, c.disk, c.os_version, c.remark,
	c.created_by, c.created_at, c.updated_at
FROM it_equipment c
LEFT JOIN asset_card ac ON ac.id = c.card_id
LEFT JOIN employee ue ON ue.id = c.user_emp_id
LEFT JOIN department ud ON ud.id = c.dept_id`

// itSortColumns 是排序字段白名单：请求里传的是 key，永远不会把用户输入拼进 SQL。
var itSortColumns = map[string]string{
	"asset_code":  "c.asset_code",
	"device_type": "c.device_type",
	"status":      "c.status",
	"memory_gb":   "c.memory_gb",
	"user":        "ue.name",
	"dept":        "ud.name",
	"created_at":  "c.created_at",
	"updated_at":  "c.updated_at",
}

// itScopeConds 把 IT 台账可见范围翻译成 WHERE 条件片段。
//
// ⚠️ 产出的条件带 `c.` 表别名前缀，调用方 SQL **必须**写 `FROM it_equipment c`。
// 与 scopeConds（store/card.go）同构，两维取**并集**（命中任一即可见）：
// 单维度只产出单条条件，两个及以上维度同时非零（多角色）才产出 `(A OR B)`。
//
// 与 scopeConds 的差异：本表**没有** deleted_at 列（IT 记录是纯本地台账、物理删除，
// 没有「靠终态归档」的需求），故不返回软删条件。
func itScopeConds(sc model.ITScope) (conds []string, args []any) {
	conds = []string{}
	args = []any{}

	// parts 的追加顺序必须与 args 完全一致：self → dept。
	// ⚠️ 顺序错了会把参数错位绑定（比 SQL 形态错更隐蔽）。
	parts := []string{}
	if sc.SelfEmpID > 0 {
		parts = append(parts, "c.user_emp_id = ?")
		args = append(args, sc.SelfEmpID)
	}
	if len(sc.DeptIDs) > 0 {
		parts = append(parts, "c.dept_id IN ("+placeholders(len(sc.DeptIDs))+")")
		for _, id := range sc.DeptIDs {
			args = append(args, id)
		}
	}

	switch len(parts) {
	case 0:
		// 不限：无条件（与「不做范围收窄」逐字节一致）。
	case 1:
		conds = append(conds, parts[0])
	default:
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	return conds, args
}

// itBuildWhere 组装 IT 台账列表的 WHERE。可见范围必须进 WHERE 而不是取回来再滤：
// 分页、COUNT 都拼这条 where，后滤会让总数对不上。
func itBuildWhere(q model.ITListQuery, sc model.ITScope) (string, []any) {
	conds, args := itScopeConds(sc)

	if q.Keyword != "" {
		kw := "%" + q.Keyword + "%"
		// 使用人 / 部门 / 使用地点也进关键词：现场最常见的问法是「这台在谁那儿 / 哪个部门」。
		// 品牌型号 / CPU / 系统版本也进：IT 管理员的检索习惯是「找那台 i5 的」。
		conds = append(conds, "(c.asset_code LIKE ? OR c.brand_model LIKE ? OR c.cpu LIKE ?"+
			" OR c.disk LIKE ? OR c.os_version LIKE ? OR c.location LIKE ?"+
			" OR ue.name LIKE ? OR ud.name LIKE ?)")
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
		val string
	}{
		{"c.device_type", q.DeviceType},
		{"c.purpose", q.Purpose},
		{"c.asset_code", q.AssetCode},
		{"c.brand_model", q.BrandModel},
	} {
		if f.val != "" {
			// asset_code / brand_model 用模糊匹配（用户可能只记得片段），
			// device_type / purpose 是枚举，用等值。两者分开处理，不用同一个循环。
			if f.col == "c.device_type" || f.col == "c.purpose" {
				conds = append(conds, f.col+" = ?")
				args = append(args, f.val)
			} else {
				conds = append(conds, f.col+" LIKE ?")
				args = append(args, "%"+f.val+"%")
			}
		}
	}
	for _, f := range []struct {
		col string
		val int64
	}{
		{"c.dept_id", q.DeptID},
		{"c.user_emp_id", q.UserEmpID},
		// card_id 走**原始列**而不是 COALESCE(card_id,0)：0 在这里是「不筛选」，
		// 写成 COALESCE(...) = 0 会变成「只要未关联的」，语义完全反了。
		{"c.card_id", q.CardID},
	} {
		if f.val > 0 {
			conds = append(conds, f.col+" = ?")
			args = append(args, f.val)
		}
	}
	if q.IsXinchuang != nil {
		conds = append(conds, "c.is_xinchuang = ?")
		args = append(args, *q.IsXinchuang)
	}
	// Linked 按「有没有关联资产卡」筛选。判定依据是 **card_id 是否为 NULL**，
	// 而不是 asset_code 是否为空：手工给无卡设备编了编码的情况很常见，
	// 那时 asset_code 非空但依然没有卡。
	if q.Linked != nil {
		if *q.Linked {
			conds = append(conds, "c.card_id IS NOT NULL")
		} else {
			conds = append(conds, "c.card_id IS NULL")
		}
	}

	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s *Store) ListITEquipment(q model.ITListQuery, sc model.ITScope) (*model.ITListResult, error) {
	where, args := itBuildWhere(q, sc)

	// countSQL 的 JOIN 必须与 itBuildWhere 会引用到的列对齐：
	// 关键词条件引用了 ue.name / ud.name，少一个 JOIN 就是整条 SQL 报 unknown column。
	// asset_card 这一 JOIN 目前没被 WHERE 引用，但为将来加筛选留好（主键关联不放大 COUNT）。
	countSQL := `SELECT COUNT(*),
			COALESCE(SUM(c.is_xinchuang), 0),
			COALESCE(SUM(c.card_id IS NULL), 0)
		FROM it_equipment c
		LEFT JOIN asset_card ac ON ac.id = c.card_id
		LEFT JOIN employee ue ON ue.id = c.user_emp_id
		LEFT JOIN department ud ON ud.id = c.dept_id` + where
	var total, xinchuangTotal, unlinkedTotal int64
	if err := s.db.QueryRow(countSQL, args...).Scan(&total, &xinchuangTotal, &unlinkedTotal); err != nil {
		return nil, fmt.Errorf("count it_equipment: %w", err)
	}

	order := "c.id DESC"
	if col, ok := itSortColumns[q.SortBy]; ok {
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

	rows, err := s.db.Query(itSelect+where+" ORDER BY "+order+" LIMIT ? OFFSET ?",
		append(args, q.PageSize, (q.Page-1)*q.PageSize)...)
	if err != nil {
		return nil, fmt.Errorf("query it_equipment: %w", err)
	}
	defer rows.Close()

	items := []model.ITEquipment{}
	for rows.Next() {
		e, err := scanIT(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &model.ITListResult{
		Items:          items,
		Total:          total,
		XinchuangTotal: xinchuangTotal,
		UnlinkedTotal:  unlinkedTotal,
		Page:           q.Page,
		PageSize:       q.PageSize,
	}, nil
}

func scanIT(r rowScanner) (*model.ITEquipment, error) {
	var e model.ITEquipment
	var assetName, userName, deptName sql.NullString

	// Scan 顺序必须与 itSelect 的列顺序逐字对应。
	err := r.Scan(
		&e.ID, &e.CardID, &e.AssetCode, &assetName,
		&e.DeviceType, &e.UserEmpID, &userName, &e.DeptID, &deptName,
		&e.Location, &e.Purpose, &e.Status, &e.IsXinchuang,
		&e.BrandModel, &e.CPU, &e.MemoryGB, &e.Disk, &e.OSVersion, &e.Remark,
		&e.CreatedBy, &e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	e.AssetName = assetName.String
	e.UserEmpName = userName.String
	e.DeptName = deptName.String
	return &e, nil
}

func (s *Store) GetITEquipment(id int64) (*model.ITEquipment, error) {
	row := s.db.QueryRow(itSelect+" WHERE c.id = ?", id)
	e, err := scanIT(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// GetITEquipmentByCardID 按资产卡查已有的 IT 记录。
//
// 用途是「从资产台账关联生成」这条主路径的**判重**：一张资产卡最多一条 IT 记录
// （由 uk_it_card 保证），所以点「生成 IT 设备记录」时先查一次，
// 已存在就提示「这台资产已登记过 IT 信息」并给出记录 id，而不是让 INSERT 撞唯一键报错。
// cardID <= 0 直接返回 nil（未关联不是可查条件）。
func (s *Store) GetITEquipmentByCardID(cardID int64) (*model.ITEquipment, error) {
	if cardID <= 0 {
		return nil, nil
	}
	row := s.db.QueryRow(itSelect+" WHERE c.card_id = ?", cardID)
	e, err := scanIT(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// itWriteColumns 与 itWriteArgs 必须一一对应。
//
// ⚠️ card_id 在库里是可空列，写入时必须走 NULLIF(?, 0)（见 itInsertExpr / itSetExpr），
// 否则「未关联」会被写成 0，第二条无卡设备就会撞 uk_it_card 唯一键。
//
// ⚠️ 本清单**不含** created_by（只在 INSERT 时写一次，与 cardWriteColumns 同款处理）。
// asset_name / user_emp_name / dept_name 是 LEFT JOIN 派生字段，**绝不进本清单**。
var itWriteColumns = []string{
	"card_id", "asset_code", "device_type", "user_emp_id", "dept_id", "location",
	"purpose", "status", "is_xinchuang", "brand_model", "cpu", "memory_gb",
	"disk", "os_version", "remark",
}

func itWriteArgs(e *model.ITEquipment) []any {
	return []any{
		e.CardID, e.AssetCode, e.DeviceType, e.UserEmpID, e.DeptID, e.Location,
		e.Purpose, e.Status, e.IsXinchuang, e.BrandModel, e.CPU, e.MemoryGB,
		e.Disk, e.OSVersion, e.Remark,
	}
}

// itValueExpr 返回某一列在 INSERT / UPDATE 里的取值表达式。
// 只有 card_id 需要特殊处理：0 → NULL。
//
// 这是「Go 侧用 0 表示未关联 / 库里用 NULL 表示未关联」这个双向约定的**唯一落点**，
// 读侧的对应物是 itSelect 里的 COALESCE(c.card_id, 0)。改一处必须改另一处。
func itValueExpr(col string) string {
	if col == "card_id" {
		return "NULLIF(?, 0)"
	}
	return "?"
}

func (s *Store) CreateITEquipment(e *model.ITEquipment, operator string) (int64, error) {
	cols := append([]string{}, itWriteColumns...)
	cols = append(cols, "created_by")
	args := append(itWriteArgs(e), operator)

	exprs := make([]string, len(cols))
	for i, col := range cols {
		exprs[i] = itValueExpr(col)
	}
	q := fmt.Sprintf("INSERT INTO it_equipment (%s) VALUES (%s)",
		strings.Join(cols, ","), strings.Join(exprs, ","))
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("insert it_equipment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) UpdateITEquipment(id int64, e *model.ITEquipment, operator string) error {
	sets := make([]string, len(itWriteColumns))
	for i, col := range itWriteColumns {
		sets[i] = col + " = " + itValueExpr(col)
	}
	args := append(itWriteArgs(e), id)
	res, err := s.db.Exec("UPDATE it_equipment SET "+strings.Join(sets, ",")+" WHERE id = ?", args...)
	if err != nil {
		return fmt.Errorf("update it_equipment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	// RowsAffected == 0 有两种情况：行不存在，或者提交的内容与库里完全一致（MySQL 不更新）。
	// 前者要报 404，后者是正常幂等。用一次存在性查询区分——只在这条罕见路径上多查一次库。
	if n == 0 {
		var exists int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM it_equipment WHERE id = ?", id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
	}
	return nil
}

// DeleteITEquipment 物理删除一条 IT 台账记录。
//
// 为什么可以物理删（而维修单要白名单）：IT 台账是**纯本地主数据**，不挂任何未兑现的
// 外部承诺——没有云之家待办、不改 asset_card 的任何列、不影响资产状态。
// 删掉一条登记错的记录不会让任何别的东西悬空，故不需要状态白名单这道闸门。
// （对照：维修单的删除白名单判定标准见 model.RepairDeletableStatuses 注释。）
func (s *Store) DeleteITEquipment(id int64) error {
	res, err := s.db.Exec("DELETE FROM it_equipment WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete it_equipment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ITEquipmentCount 返回（总数、信创台数、无卡台数），供首页指标卡使用。
// 一次查询出三个数，避免首页为一张卡多打两次库。
func (s *Store) ITEquipmentCount(sc model.ITScope) (total, xinchuang, unlinked int64, err error) {
	where, args := itBuildWhere(model.ITListQuery{}, sc)
	q := `SELECT COUNT(*),
			COALESCE(SUM(c.is_xinchuang), 0),
			COALESCE(SUM(c.card_id IS NULL), 0)
		FROM it_equipment c
		LEFT JOIN employee ue ON ue.id = c.user_emp_id
		LEFT JOIN department ud ON ud.id = c.dept_id` + where
	if err = s.db.QueryRow(q, args...).Scan(&total, &xinchuang, &unlinked); err != nil {
		return 0, 0, 0, fmt.Errorf("count it_equipment: %w", err)
	}
	return total, xinchuang, unlinked, nil
}

// FindITEquipmentByAssetCode 按资产编码精确查 IT 记录。
//
// 返回**切片**而不是单条，因为 asset_code 上刻意没有唯一约束（多条无卡设备的编码都是空串，
// 见 store/mysql.go 的 it_equipment DDL 注释）。调用方（导入判重）必须处理
// 「同一编码有多条」这种歧义，而不是随便取一条 —— 静默取第一条会让导入把数据写到错误的行上。
func (s *Store) FindITEquipmentByAssetCode(code string) ([]model.ITEquipment, error) {
	if strings.TrimSpace(code) == "" {
		// 空编码不能作为匹配键：库里所有无卡设备的编码都是空串，
		// 拿它去查会把「匹配到几十条」当成正常结果。
		return nil, nil
	}
	rows, err := s.db.Query(itSelect+" WHERE c.asset_code = ? ORDER BY c.id", code)
	if err != nil {
		return nil, fmt.Errorf("query it_equipment by code: %w", err)
	}
	defer rows.Close()
	out := []model.ITEquipment{}
	for rows.Next() {
		e, err := scanIT(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ImportITEquipment 在一个事务里写入整批导入数据：整批成功或整批回滚。
//
// 为什么必须整批事务：导入是「用户改完一整张表再导回」的场景，
// 部分写入会留下一个「改了一半」的台账，比完全不写更难收拾
// （与 api/importexport.go 的 handleImport 同一约定：有错就整体不写）。
//
// 逐行按 item.ExistingID 分流：>0 更新、0 新增。判定规则不在这里重复实现，
// 见 model.ITImportItem 的注释。
func (s *Store) ImportITEquipment(items []model.ITImportItem, operator string) (created, updated int, err error) {
	if len(items) == 0 {
		return 0, 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	cols := append([]string{}, itWriteColumns...)
	cols = append(cols, "created_by")
	exprs := make([]string, len(cols))
	for i, col := range cols {
		exprs[i] = itValueExpr(col)
	}
	insertSQL := fmt.Sprintf("INSERT INTO it_equipment (%s) VALUES (%s)",
		strings.Join(cols, ","), strings.Join(exprs, ","))

	sets := make([]string, len(itWriteColumns))
	for i, col := range itWriteColumns {
		sets[i] = col + " = " + itValueExpr(col)
	}
	updateSQL := "UPDATE it_equipment SET " + strings.Join(sets, ",") + " WHERE id = ?"

	for _, it := range items {
		e := it.Equipment
		if it.ExistingID > 0 {
			if _, err := tx.Exec(updateSQL, append(itWriteArgs(&e), it.ExistingID)...); err != nil {
				return 0, 0, fmt.Errorf("import update id=%d: %w", it.ExistingID, err)
			}
			updated++
			continue
		}
		if _, err := tx.Exec(insertSQL, append(itWriteArgs(&e), operator)...); err != nil {
			return 0, 0, fmt.Errorf("import insert code=%s: %w", e.AssetCode, err)
		}
		created++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return created, updated, nil
}
