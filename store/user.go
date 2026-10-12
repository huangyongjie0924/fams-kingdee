package store

import (
	"database/sql"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"asset-mgr/model"
)

// primaryRole 返回归一化后的第一个角色（主角色），写入 sys_user.role 列。
//
// 它必须与 model.FormatRoles 写在**同一条 SQL** 里：role 列恒等于 roles 列的第一个元素，
// 两者一旦分开写就会漂移（旧 token / 旧前端读 role，FIND_IN_SET 查 roles，漂移即不一致）。
// 输入为空（理论上不该发生，api 层会先挡）时返回空串，避免 panic。
func primaryRole(roles []string) string {
	n := model.NormalizeRoles(roles)
	if len(n) == 0 {
		return ""
	}
	return n[0]
}

// EnsureAdmin 首次启动且 sys_user 为空时创建初始管理员
func (s *Store) EnsureAdmin(username, password string) error {
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sys_user").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if username == "" || password == "" {
		return fmt.Errorf("init admin user/password required on first run")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	// role / roles 同一条 SQL 一起写：roles = [admin]，role = admin（主角色）。
	_, err = s.db.Exec(`INSERT INTO sys_user (username, password_hash, real_name, role, roles, enabled)
		VALUES (?, ?, '管理员', ?, ?, 1)`,
		username, string(hash), model.RoleAdmin, model.FormatRoles([]string{model.RoleAdmin}))
	return err
}

// Authenticate 用户名密码校验，失败统一返回 nil，不区分用户不存在与密码错误
func (s *Store) Authenticate(username, password string) (*model.User, error) {
	var u model.User
	var hash, rolesStr string
	err := s.db.QueryRow(`SELECT id, username, real_name, role, roles, employee_id, enabled, password_hash
		FROM sys_user WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &rolesStr, &u.EmployeeID, &u.Enabled, &hash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// roles 列是**全部角色**；为空（存量行 / 未迁移）时 EffectiveRoles() 会自动回落到 role。
	u.Roles = model.ParseRoles(rolesStr)
	if !u.Enabled {
		return nil, nil
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, nil
	}
	return &u, nil
}

// ListUsers 账号列表，带上绑定的员工姓名供前端展示。
func (s *Store) ListUsers() ([]model.User, error) {
	rows, err := s.db.Query(`SELECT u.id, u.username, u.real_name, u.role, u.roles, u.employee_id,
		COALESCE(e.name, ''), u.dept_id, COALESCE(d.name, ''), u.enabled
		FROM sys_user u
		LEFT JOIN employee e ON e.id = u.employee_id
		LEFT JOIN department d ON d.id = u.dept_id
		ORDER BY u.id`)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	out := []model.User{}
	for rows.Next() {
		var u model.User
		var rolesStr string
		if err := rows.Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &rolesStr, &u.EmployeeID,
			&u.EmployeeName, &u.DeptID, &u.DeptName, &u.Enabled); err != nil {
			return nil, err
		}
		u.Roles = model.ParseRoles(rolesStr)
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserDeptID 取账号管辖的部门，供「部门负责人」解析可见范围用。
// 账号不存在或没设部门都返回 0。
func (s *Store) UserDeptID(userID int64) (int64, error) {
	var deptID int64
	err := s.db.QueryRow(`SELECT dept_id FROM sys_user WHERE id = ?`, userID).Scan(&deptID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query user dept: %w", err)
	}
	return deptID, nil
}

// UserByEmployee 按绑定的员工 id 取登录账号，未绑定返回 nil,nil。
func (s *Store) UserByEmployee(employeeID int64) (*model.User, error) {
	var u model.User
	var rolesStr string
	err := s.db.QueryRow(`SELECT id, username, real_name, role, roles, employee_id, enabled
		FROM sys_user WHERE employee_id = ? LIMIT 1`, employeeID).
		Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &rolesStr, &u.EmployeeID, &u.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.Roles = model.ParseRoles(rolesStr)
	return &u, nil
}

// CreateSSOUser 为云之家单点登录自动建号：password_hash 存空串，只能走 SSO，无法用密码登录。
//
// roles 为账号的全部角色；role 列写主角色（= roles 的第一个），两列同一条 SQL 一起写。
func (s *Store) CreateSSOUser(username, realName string, roles []string, employeeID int64) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO sys_user (username, password_hash, real_name, role, roles, employee_id, enabled)
		VALUES (?, '', ?, ?, ?, ?, 1)`,
		username, realName, primaryRole(roles), model.FormatRoles(roles), employeeID)
	if err != nil {
		return 0, fmt.Errorf("insert sso user: %w", err)
	}
	return res.LastInsertId()
}

// GetUser 按 ID 取单个账号。
func (s *Store) GetUser(id int64) (*model.User, error) {
	var u model.User
	var rolesStr string
	err := s.db.QueryRow(`SELECT id, username, real_name, role, roles, employee_id, enabled
		FROM sys_user WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &rolesStr, &u.EmployeeID, &u.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.Roles = model.ParseRoles(rolesStr)
	return &u, nil
}

// CreateUser 新建账号，密码用 bcrypt 存哈希。
//
// roles 为账号的全部角色（一人可多角色）。roles 与 role 两列在**同一条 SQL** 里一起写：
// roles = FormatRoles(roles)（逗号分隔、无空格），role = 归一化后的第一个角色（主角色）。
// 分开写会让两列漂移，故这里刻意只发一条 INSERT。
func (s *Store) CreateUser(username, password, realName string, roles []string, employeeID, deptID int64, enabled bool) (int64, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO sys_user (username, password_hash, real_name, role, roles, employee_id, dept_id, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		username, string(hash), realName, primaryRole(roles), model.FormatRoles(roles),
		employeeID, deptID, boolToInt(enabled))
	if err != nil {
		return 0, fmt.Errorf("insert user: %w", err)
	}
	return res.LastInsertId()
}

// UpdateUser 更新账号资料；password 为空时保持原密码不变。
//
// 与 CreateUser 同理：roles 与 role 两列在**同一条 SQL** 里一起写，保证 role == roles[0]。
func (s *Store) UpdateUser(id int64, realName string, roles []string, employeeID, deptID int64, enabled bool, password string) error {
	rolesStr := model.FormatRoles(roles)
	role := primaryRole(roles)
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`UPDATE sys_user SET real_name=?, role=?, roles=?, employee_id=?, dept_id=?, enabled=?, password_hash=?
			WHERE id=?`, realName, role, rolesStr, employeeID, deptID, boolToInt(enabled), string(hash), id)
		return err
	}
	_, err := s.db.Exec(`UPDATE sys_user SET real_name=?, role=?, roles=?, employee_id=?, dept_id=?, enabled=? WHERE id=?`,
		realName, role, rolesStr, employeeID, deptID, boolToInt(enabled), id)
	return err
}

// DeleteUser 删除账号（sys_user 无软删列，直接删）。
func (s *Store) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM sys_user WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CountEnabledAdmins 统计启用中的管理员数量，用于「不能删/停最后一个管理员」的保护。
//
// 多角色下必须按 roles 列统计：主角色不是 admin、但角色集合里含 admin 的账号也算管理员，
// 只看 role 列会漏掉它们（把最后一个真管理员误判成 0，从而放行「停用最后一个管理员」）。
// FIND_IN_SET 依赖 roles 列为逗号分隔且**无空格**（FormatRoles 保证）。
func (s *Store) CountEnabledAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sys_user WHERE FIND_IN_SET(?, roles) > 0 AND enabled = 1`,
		model.RoleAdmin).Scan(&n)
	return n, err
}
