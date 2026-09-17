package store

import (
	"database/sql"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"asset-mgr/model"
)

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
	_, err = s.db.Exec(`INSERT INTO sys_user (username, password_hash, real_name, role, enabled)
		VALUES (?, ?, '管理员', ?, 1)`, username, string(hash), model.RoleAdmin)
	return err
}

// Authenticate 用户名密码校验，失败统一返回 nil，不区分用户不存在与密码错误
func (s *Store) Authenticate(username, password string) (*model.User, error) {
	var u model.User
	var hash string
	err := s.db.QueryRow(`SELECT id, username, real_name, role, employee_id, enabled, password_hash
		FROM sys_user WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &u.EmployeeID, &u.Enabled, &hash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
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
	rows, err := s.db.Query(`SELECT u.id, u.username, u.real_name, u.role, u.employee_id,
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
		if err := rows.Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &u.EmployeeID,
			&u.EmployeeName, &u.DeptID, &u.DeptName, &u.Enabled); err != nil {
			return nil, err
		}
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
	err := s.db.QueryRow(`SELECT id, username, real_name, role, employee_id, enabled
		FROM sys_user WHERE employee_id = ? LIMIT 1`, employeeID).
		Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &u.EmployeeID, &u.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateSSOUser 为云之家单点登录自动建号：password_hash 存空串，只能走 SSO，无法用密码登录。
func (s *Store) CreateSSOUser(username, realName, role string, employeeID int64) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO sys_user (username, password_hash, real_name, role, employee_id, enabled)
		VALUES (?, '', ?, ?, ?, 1)`, username, realName, role, employeeID)
	if err != nil {
		return 0, fmt.Errorf("insert sso user: %w", err)
	}
	return res.LastInsertId()
}

// GetUser 按 ID 取单个账号。
func (s *Store) GetUser(id int64) (*model.User, error) {
	var u model.User
	err := s.db.QueryRow(`SELECT id, username, real_name, role, employee_id, enabled
		FROM sys_user WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.RealName, &u.Role, &u.EmployeeID, &u.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser 新建账号，密码用 bcrypt 存哈希。
func (s *Store) CreateUser(username, password, realName, role string, employeeID, deptID int64, enabled bool) (int64, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO sys_user (username, password_hash, real_name, role, employee_id, dept_id, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, username, string(hash), realName, role, employeeID, deptID, boolToInt(enabled))
	if err != nil {
		return 0, fmt.Errorf("insert user: %w", err)
	}
	return res.LastInsertId()
}

// UpdateUser 更新账号资料；password 为空时保持原密码不变。
func (s *Store) UpdateUser(id int64, realName, role string, employeeID, deptID int64, enabled bool, password string) error {
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`UPDATE sys_user SET real_name=?, role=?, employee_id=?, dept_id=?, enabled=?, password_hash=?
			WHERE id=?`, realName, role, employeeID, deptID, boolToInt(enabled), string(hash), id)
		return err
	}
	_, err := s.db.Exec(`UPDATE sys_user SET real_name=?, role=?, employee_id=?, dept_id=?, enabled=? WHERE id=?`,
		realName, role, employeeID, deptID, boolToInt(enabled), id)
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
func (s *Store) CountEnabledAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sys_user WHERE role = ? AND enabled = 1`,
		model.RoleAdmin).Scan(&n)
	return n, err
}
