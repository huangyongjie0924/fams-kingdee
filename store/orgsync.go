package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"asset-mgr/model"
)

// 本文件是组织主数据（公司 / 部门 / 员工）从星瀚同步时的落库原语。
//
// 与资产卡同步的 syncer.resolveMaster 的关键差别：**不按名称兜底**。
//
// 原因是部门名称在金蝶侧严重重复——实测 5203 个组织节点只有 1333 个唯一名称，
// 光「财务部」一个名字就挂了 78 个节点。若按名称匹配，120102（1201 下的财务部）
// 和 120214（1202 下的财务部）会认领同一行本地数据，两个星瀚部门塌成一条。
//
// 资产卡同步按名称兜底是因为卡片只给了名称、没有别的线索；
// 组织同步拿得到权威编码，没有理由退到名称。

// orgMasterTable 把主数据类别映射到表名与编码列名。
var orgMasterTable = map[string]struct{ table, codeCol string }{
	model.MasterKindCompany:    {table: "company", codeCol: "code"},
	model.MasterKindDepartment: {table: "department", codeCol: "code"},
	model.MasterKindEmployee:   {table: "employee", codeCol: "emp_no"},
}

// LookupOrgMasterTx 只读解析一条组织主数据，找不到返回 0。
// dry-run 用它区分「会新建」和「会更新」，全程不写库。
func LookupOrgMasterTx(tx *sql.Tx, source, kind, code string) (int64, error) {
	meta, ok := orgMasterTable[kind]
	if !ok {
		return 0, fmt.Errorf("unknown org master kind %q", kind)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return 0, nil
	}
	var id int64
	err := tx.QueryRow(`SELECT local_id FROM external_master_map
		WHERE source = ? AND kind = ? AND external_id = ?`, source, kind, code).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if id > 0 {
		return id, nil
	}
	err = tx.QueryRow(fmt.Sprintf("SELECT id FROM %s WHERE %s = ? LIMIT 1", meta.table, meta.codeCol), code).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if err != nil {
		return 0, nil
	}
	return id, nil
}

// ResolveOrgMasterTx 按外部编码解析本地行，找不到就新建。
// 解析顺序：外部映射表 → 按编码 → 新建。
//
// 第二个返回值表示这一行是**新建**的还是命中了已有行。
// 调用方必须用它来区分统计口径：只按"函数被调用了几次"来累加 updated
// 会让真实落库永远报「新建 0」，而 dry-run 却按另一套逻辑报出「会新建 130」——
// 同一个同步在演练和真跑下给出两个数，是最容易让人不敢按下按钮的那种不一致。
func ResolveOrgMasterTx(tx *sql.Tx, source, kind, code, name string) (id int64, created bool, err error) {
	if _, ok := orgMasterTable[kind]; !ok {
		return 0, false, fmt.Errorf("unknown org master kind %q", kind)
	}
	code, name = strings.TrimSpace(code), strings.TrimSpace(name)
	if code == "" {
		return 0, false, fmt.Errorf("%s 缺少外部编码，无法解析（名称 %q）", kind, name)
	}
	if name == "" {
		name = code
	}

	// 1 + 2：外部映射表 → 按编码。资产卡同步自动建的行编码就是星瀚编码，这一步能命中大多数。
	existing, err := LookupOrgMasterTx(tx, source, kind, code)
	if err != nil {
		return 0, false, err
	}
	if existing > 0 {
		if err := UpsertExternalMasterMap(tx, &model.ExternalMasterMap{
			Source: source, Kind: kind, ExternalID: code, ExternalCode: code,
			ExternalName: name, LocalID: existing,
		}); err != nil {
			return 0, false, err
		}
		return existing, false, nil
	}

	// 3. 新建
	newID, err := CreateMasterTx(tx, kind, code, name)
	if err != nil {
		return 0, false, fmt.Errorf("create %s %s: %w", kind, code, err)
	}
	if err := UpsertExternalMasterMap(tx, &model.ExternalMasterMap{
		Source: source, Kind: kind, ExternalID: code, ExternalCode: code,
		ExternalName: name, LocalID: newID,
	}); err != nil {
		return 0, false, err
	}
	return newID, true, nil
}

// UpdateOrgCompanyTx 写入星瀚侧的公司属性。
func UpdateOrgCompanyTx(tx *sql.Tx, id int64, c model.Company) error {
	_, err := tx.Exec(`UPDATE company SET name=?, code=?, sort_index=?, source=? WHERE id=?`,
		c.Name, c.Code, c.SortIndex, c.Source, id)
	return err
}

// UpdateOrgDepartmentTx 写入星瀚侧的部门属性（含组织树位置）。
func UpdateOrgDepartmentTx(tx *sql.Tx, id int64, d model.Department) error {
	_, err := tx.Exec(`UPDATE department SET name=?, code=?, parent_id=?, company_id=?,
		sort_index=?, longnumber=?, level=?, enabled=?, source=? WHERE id=?`,
		d.Name, d.Code, d.ParentID, d.CompanyID, d.SortIndex,
		d.LongNumber, d.Level, d.Enabled, d.Source, id)
	return err
}

// UpdateOrgEmployeeTx 写入星瀚侧的员工属性。
func UpdateOrgEmployeeTx(tx *sql.Tx, id int64, e model.Employee) error {
	_, err := tx.Exec(`UPDATE employee SET name=?, emp_no=?, dept_id=?, company_id=?,
		phone=?, active=?, source=? WHERE id=?`,
		e.Name, e.EmpNo, e.DeptID, e.CompanyID, e.Phone, e.Active, e.Source, id)
	return err
}

// OrgConflict 描述一处「本地手工数据与星瀚数据同名，但不是同一行」的情况。
//
// 同步不按名称匹配，所以手工建的部门不会被星瀚节点认领，而是各自保留，
// 结果就是库里出现两条同名记录。这不是错误（手工行 source 为空、星瀚行 source='kingdee'，
// 可以分辨），但值得报出来让人决定要不要清理手工行。
type OrgConflict struct {
	Kind      string // company / department / employee
	Name      string
	ManualIDs []int64 // 本地手工行的 ID
	SyncedIDs []int64 // 本次同步落到（或将要落到）的行 ID
}

// FindOrgNameConflicts 找出「本地手工行」与「本次同步行」同名、但**不是同一行**的记录。
//
// syncedByName 是「名称 → 本次同步解析到的本地行 ID」，由同步器在落库时收集。
// 必须带上 ID 才能排除假冲突：绝大多数手工行（如按工号导入的员工）会被同步按编码命中，
// 那就是同一行，只是被接管而已，不是冲突。只比名称会把它们全报成冲突——
// 实测第一版就这么错了，65 名员工全被报成"冲突"，真信号被淹没。
func (s *Store) FindOrgNameConflicts(kind string, syncedByName map[string][]int64) ([]OrgConflict, error) {
	meta, ok := orgMasterTable[kind]
	if !ok {
		return nil, fmt.Errorf("unknown org master kind %q", kind)
	}
	if len(syncedByName) == 0 {
		return nil, nil
	}

	// 名称 → 同步行 ID 集合，用于判断某个手工行是不是就是同步目标
	syncedIDs := make(map[string]map[int64]bool, len(syncedByName))
	for name, ids := range syncedByName {
		set := make(map[int64]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		syncedIDs[name] = set
	}

	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT id, name, COALESCE(source, '') FROM %s WHERE name <> ''`, meta.table))
	if err != nil {
		return nil, fmt.Errorf("query %s for conflicts: %w", meta.table, err)
	}
	defer rows.Close()

	byName := make(map[string]*OrgConflict)
	for rows.Next() {
		var id int64
		var name, source string
		if err := rows.Scan(&id, &name, &source); err != nil {
			return nil, err
		}
		if !IsOrgNameConflict(name, source, id, syncedIDs) {
			continue
		}
		c, ok := byName[name]
		if !ok {
			c = &OrgConflict{Kind: kind, Name: name}
			byName[name] = c
		}
		c.ManualIDs = append(c.ManualIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]OrgConflict, 0, len(byName))
	for _, c := range byName {
		c.SyncedIDs = append(c.SyncedIDs, syncedByName[c.Name]...)
		out = append(out, *c)
	}
	// 名称排序，保证输出稳定（map 遍历顺序是随机的）
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// IsOrgNameConflict 判断一行本地数据是否构成「与星瀚同名但不是同一行」。
//
// 抽成纯函数是为了能单测：这三个条件（来源、同名、是否同一行）少判一个
// 就会得出完全相反的结论，而它们都只依赖参数。
//
//	syncedIDs 是「名称 → 本次同步落到的本地行 ID 集合」。
//	dry-run 下"将会新建"的条目用 0 占位，因此不会等于任何真实行 ID，
//	能正确地把「手工建的同名行」判成冲突。
func IsOrgNameConflict(name, source string, id int64, syncedIDs map[string]map[int64]bool) bool {
	if name == "" {
		return false
	}
	if source == "kingdee" {
		return false // 已经是同步托管的行，不算手工数据
	}
	set, hit := syncedIDs[name]
	if !hit {
		return false // 星瀚没有同名节点
	}
	return !set[id] // 这一行不是同步的目标，才是冲突
}
