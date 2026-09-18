package store

import (
	"database/sql"
	"fmt"

	"asset-mgr/model"
)

func (s *Store) ListCategories() ([]*model.TreeNode, error) {
	rows, err := s.db.Query(`SELECT id, parent_id, name, code, use_months, residual_rate, sort_index
		FROM asset_category ORDER BY sort_index, id`)
	if err != nil {
		return nil, fmt.Errorf("query categories: %w", err)
	}
	defer rows.Close()

	nodes := []*model.TreeNode{}
	for rows.Next() {
		n := &model.TreeNode{}
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Name, &n.Code, &n.UseMonths, &n.ResidualRate, &n.SortIndex); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buildTree(nodes), nil
}

func (s *Store) ListAreas() ([]*model.TreeNode, error) {
	rows, err := s.db.Query(`SELECT id, parent_id, name, short_name, code, sort_index
		FROM asset_area ORDER BY sort_index, id`)
	if err != nil {
		return nil, fmt.Errorf("query areas: %w", err)
	}
	defer rows.Close()

	nodes := []*model.TreeNode{}
	for rows.Next() {
		n := &model.TreeNode{}
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Name, &n.ShortName, &n.Code, &n.SortIndex); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buildTree(nodes), nil
}

func buildTree(nodes []*model.TreeNode) []*model.TreeNode {
	byID := make(map[int64]*model.TreeNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	roots := []*model.TreeNode{}
	for _, n := range nodes {
		if p, ok := byID[n.ParentID]; ok && n.ParentID != 0 {
			p.Children = append(p.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	return roots
}

func (s *Store) SaveCategory(n *model.TreeNode) (int64, error) {
	if n.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO asset_category (name, code, parent_id, use_months, residual_rate, sort_index)
			VALUES (?, ?, ?, ?, ?, ?)`, n.Name, n.Code, n.ParentID, n.UseMonths, n.ResidualRate, n.SortIndex)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE asset_category SET name=?, code=?, parent_id=?, use_months=?, residual_rate=?, sort_index=?
		WHERE id=?`, n.Name, n.Code, n.ParentID, n.UseMonths, n.ResidualRate, n.SortIndex, n.ID)
	return n.ID, err
}

func (s *Store) SaveArea(n *model.TreeNode) (int64, error) {
	if n.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO asset_area (name, short_name, code, parent_id, sort_index)
			VALUES (?, ?, ?, ?, ?)`, n.Name, n.ShortName, n.Code, n.ParentID, n.SortIndex)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE asset_area SET name=?, short_name=?, code=?, parent_id=?, sort_index=? WHERE id=?`,
		n.Name, n.ShortName, n.Code, n.ParentID, n.SortIndex, n.ID)
	return n.ID, err
}

// deleteRef 删主数据前先确认没有资产在引用，避免台账出现悬空外键
func (s *Store) deleteRef(table, refColumn string, id int64) error {
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM asset_card WHERE "+refColumn+" = ? AND deleted_at IS NULL", id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("已被 %d 条资产引用，不能删除", n)
	}
	res, err := s.db.Exec("DELETE FROM "+table+" WHERE id = ?", id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteCategory(id int64) error {
	return s.deleteRef("asset_category", "category_id", id)
}
func (s *Store) DeleteArea(id int64) error    { return s.deleteRef("asset_area", "area_id", id) }
func (s *Store) DeleteVendor(id int64) error  { return s.deleteRef("vendor", "vendor_id", id) }
func (s *Store) DeleteCompany(id int64) error { return s.deleteRef("company", "owner_company_id", id) }
func (s *Store) DeleteDepartment(id int64) error {
	return s.deleteRef("department", "use_dept_id", id)
}
func (s *Store) DeleteEmployee(id int64) error { return s.deleteRef("employee", "manager_emp_id", id) }

func (s *Store) ListCompanies() ([]model.Company, error) {
	rows, err := s.db.Query(`SELECT id, name, code, tax_no, sort_index, COALESCE(source,'')
		FROM company ORDER BY sort_index, id`)
	if err != nil {
		return nil, fmt.Errorf("query companies: %w", err)
	}
	defer rows.Close()
	out := []model.Company{}
	for rows.Next() {
		var c model.Company
		if err := rows.Scan(&c.ID, &c.Name, &c.Code, &c.TaxNo, &c.SortIndex, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SaveCompany(c *model.Company) (int64, error) {
	if c.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO company (name, code, tax_no, sort_index) VALUES (?, ?, ?, ?)`,
			c.Name, c.Code, c.TaxNo, c.SortIndex)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE company SET name=?, code=?, tax_no=?, sort_index=? WHERE id=?`,
		c.Name, c.Code, c.TaxNo, c.SortIndex, c.ID)
	return c.ID, err
}

func (s *Store) ListDepartments() ([]model.Department, error) {
	rows, err := s.db.Query(`SELECT d.id, d.name, d.code, d.parent_id, d.company_id, COALESCE(co.name,''),
		d.sort_index, COALESCE(d.longnumber,''), d.level, d.enabled, COALESCE(d.source,'')
		FROM department d LEFT JOIN company co ON co.id = d.company_id ORDER BY d.sort_index, d.id`)
	if err != nil {
		return nil, fmt.Errorf("query departments: %w", err)
	}
	defer rows.Close()
	out := []model.Department{}
	for rows.Next() {
		var d model.Department
		if err := rows.Scan(&d.ID, &d.Name, &d.Code, &d.ParentID, &d.CompanyID, &d.CompanyName,
			&d.SortIndex, &d.LongNumber, &d.Level, &d.Enabled, &d.Source); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) SaveDepartment(d *model.Department) (int64, error) {
	if d.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO department (name, code, parent_id, company_id, sort_index)
			VALUES (?, ?, ?, ?, ?)`, d.Name, d.Code, d.ParentID, d.CompanyID, d.SortIndex)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE department SET name=?, code=?, parent_id=?, company_id=?, sort_index=? WHERE id=?`,
		d.Name, d.Code, d.ParentID, d.CompanyID, d.SortIndex, d.ID)
	return d.ID, err
}

func (s *Store) ListEmployees(keyword string) ([]model.Employee, error) {
	q := `SELECT e.id, e.emp_no, e.name, e.dept_id, COALESCE(d.name,''), e.company_id, COALESCE(co.name,''),
		e.phone, e.active, COALESCE(e.source,'')
		FROM employee e
		LEFT JOIN department d ON d.id = e.dept_id
		LEFT JOIN company co ON co.id = e.company_id`
	args := []any{}
	if keyword != "" {
		q += " WHERE e.name LIKE ? OR e.emp_no LIKE ?"
		args = append(args, "%"+keyword+"%", "%"+keyword+"%")
	}
	q += " ORDER BY e.id"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query employees: %w", err)
	}
	defer rows.Close()
	out := []model.Employee{}
	for rows.Next() {
		var e model.Employee
		if err := rows.Scan(&e.ID, &e.EmpNo, &e.Name, &e.DeptID, &e.DeptName, &e.CompanyID,
			&e.CompanyName, &e.Phone, &e.Active, &e.Source); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) SaveEmployee(e *model.Employee) (int64, error) {
	if e.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO employee (emp_no, name, dept_id, company_id, phone, active)
			VALUES (?, ?, ?, ?, ?, ?)`, e.EmpNo, e.Name, e.DeptID, e.CompanyID, e.Phone, e.Active)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE employee SET emp_no=?, name=?, dept_id=?, company_id=?, phone=?, active=? WHERE id=?`,
		e.EmpNo, e.Name, e.DeptID, e.CompanyID, e.Phone, e.Active, e.ID)
	return e.ID, err
}

func (s *Store) ListVendors() ([]model.Vendor, error) {
	rows, err := s.db.Query(`SELECT id, name, code, contact, phone, remark FROM vendor ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query vendors: %w", err)
	}
	defer rows.Close()
	out := []model.Vendor{}
	for rows.Next() {
		var v model.Vendor
		if err := rows.Scan(&v.ID, &v.Name, &v.Code, &v.Contact, &v.Phone, &v.Remark); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SaveVendor(v *model.Vendor) (int64, error) {
	if v.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO vendor (name, code, contact, phone, remark) VALUES (?, ?, ?, ?, ?)`,
			v.Name, v.Code, v.Contact, v.Phone, v.Remark)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE vendor SET name=?, code=?, contact=?, phone=?, remark=? WHERE id=?`,
		v.Name, v.Code, v.Contact, v.Phone, v.Remark, v.ID)
	return v.ID, err
}

func (s *Store) ListTags() ([]model.Tag, error) {
	rows, err := s.db.Query(`SELECT id, name, color FROM asset_tag ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	defer rows.Close()
	out := []model.Tag{}
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SaveTag(t *model.Tag) (int64, error) {
	if t.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO asset_tag (name, color) VALUES (?, ?)`, t.Name, t.Color)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE asset_tag SET name=?, color=? WHERE id=?`, t.Name, t.Color, t.ID)
	return t.ID, err
}

func (s *Store) DeleteTag(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM asset_card_tag WHERE tag_id = ?", id); err != nil {
		return err
	}
	res, err := tx.Exec("DELETE FROM asset_tag WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// LookupCategoryByName 供 Excel 导入把名称解析成 id
func (s *Store) LookupCategoryByName(name string) (int64, int, float64, error) {
	var id int64
	var months int
	var rate float64
	err := s.db.QueryRow(`SELECT id, use_months, residual_rate FROM asset_category WHERE name = ? LIMIT 1`, name).
		Scan(&id, &months, &rate)
	if err == sql.ErrNoRows {
		return 0, 0, 0, nil
	}
	return id, months, rate, err
}

// LookupCategoryMonthsAndRate 按分类 id 取折旧期限与残值率
func (s *Store) LookupCategoryMonthsAndRate(id int64) (int, float64, error) {
	var months int
	var rate float64
	err := s.db.QueryRow(`SELECT use_months, residual_rate FROM asset_category WHERE id = ?`, id).
		Scan(&months, &rate)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return months, rate, err
}

func (s *Store) lookupID(table, name string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM "+table+" WHERE name = ? LIMIT 1", name).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (s *Store) LookupCompanyByName(n string) (int64, error)    { return s.lookupID("company", n) }
func (s *Store) LookupDepartmentByName(n string) (int64, error) { return s.lookupID("department", n) }
func (s *Store) LookupEmployeeByName(n string) (int64, error)   { return s.lookupID("employee", n) }
func (s *Store) LookupAreaByName(n string) (int64, error)       { return s.lookupID("asset_area", n) }
func (s *Store) LookupVendorByName(n string) (int64, error)     { return s.lookupID("vendor", n) }

// LookupUniqueByName 只在名称唯一命中时返回 id，重名一律返回 0。
// 金蝶主数据编码唯一但名称可重复（同一名称对应多个编码，如部门 120102/120214 都叫财务部），
// 名称只能作为唯一命中时的兜底，随便取第一行会把两个不同的部门绑成一个。
func (s *Store) LookupUniqueByName(table, name string) (int64, error) {
	var id, n int64
	if err := s.db.QueryRow("SELECT COALESCE(MIN(id),0), COUNT(*) FROM "+table+" WHERE name = ?", name).
		Scan(&id, &n); err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, nil
	}
	return id, nil
}

func (s *Store) lookupIDByCode(table, code string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM "+table+" WHERE code = ? LIMIT 1", code).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (s *Store) LookupCategoryByCode(code string) (int64, error) {
	return s.lookupIDByCode("asset_category", code)
}

func (s *Store) LookupCompanyByCode(code string) (int64, error) {
	return s.lookupIDByCode("company", code)
}

func (s *Store) LookupDepartmentByCode(code string) (int64, error) {
	return s.lookupIDByCode("department", code)
}

func (s *Store) LookupVendorByCode(code string) (int64, error) {
	return s.lookupIDByCode("vendor", code)
}

func (s *Store) LookupAreaByCode(code string) (int64, error) {
	return s.lookupIDByCode("asset_area", code)
}

func (s *Store) LookupEmployeeByNumber(no string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM employee WHERE emp_no = ? LIMIT 1", no).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}
