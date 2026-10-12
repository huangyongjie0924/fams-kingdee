package store

import (
	"database/sql"
	"fmt"
)

// 本文件是 employee.open_id（云之家 openId）的**合法写入者**。
//
// ⚠️ 文件命名带 "yunzhijia" 是**有意的**：store/employee_openid_guard_test.go 的
// employeeWriteFiles 会用 filepath.Glob 扫 store/、syncer/、api/、cmd/，断言
// 「任何写 employee 的语句都不得把 open_id 当目标列」，并**显式排除** basename
// 含 "yunzhijia" 的文件——因为云之家回填 open_id 正是合法写入路径
// （docs/云之家待办通知集成方案.md §6.4、§6.7）。若把本文件的 SQL 挪进
// store/employee.go，守卫会（正确地）变红。
//
// 两条合法写入路径（均**只写 open_id 一列**，匹配键统一为 emp_no）：
//   - 路径 A：SSO 免登时顺手回填（api/sso.go → UpdateEmployeeOpenID）；
//   - 路径 B：通讯录增量同步（syncer/yunzhijia_org.go → SetEmployeeOpenIDByEmpNo）。
//
// 两条路径都不得触碰其它列，否则会与星瀚同步（store/orgsync.go 的
// UpdateOrgEmployeeTx 显式列清单）打架。

// EmployeeOpenID 取某员工的云之家 openId；无记录或为空返回空串。
// 这是**只读**方法，守卫不拦（守卫只断言写入目标列）。
func (s *Store) EmployeeOpenID(empID int64) (string, error) {
	var openID string
	err := s.db.QueryRow(`SELECT open_id FROM employee WHERE id = ?`, empID).Scan(&openID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("query employee.open_id (id=%d): %w", empID, err)
	}
	return openID, nil
}

// CountEmployeeOpenIDCoverage 统计在册（active=1）员工的云之家 openId 覆盖情况。
//
// 为什么需要它：openId 是待办通知的**收件人标识**，缺它就只能跳过该收件人（§3.1）。
// 若在覆盖率为 0 时就打开 todo_enabled，系统会正常启动、除了一条条
// 「skip emp=N 无云之家 openId」之外没有任何异常，表现为「待办功能上线了但一条都没发出去」——
// 这是最难定位的一类失败（看起来像「配置没生效」，实际是「没有人有 openId」）。
// 故在启动时把覆盖数打出来，为 0 时明确告警（见 api/notify.go 的 NewNotifyService）。
//
// 只读方法，守卫不拦（守卫只断言写入目标列）。
func (s *Store) CountEmployeeOpenIDCoverage() (withOpenID, total int64, err error) {
	err = s.db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN open_id <> '' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM employee
		WHERE active = 1`).Scan(&withOpenID, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("count employee.open_id coverage: %w", err)
	}
	return withOpenID, total, nil
}

// UpdateEmployeeOpenID 按员工 ID 回填云之家 openId（路径 A：SSO 免登）。
//
// 只写 open_id 一列，不触碰其它列——否则会与星瀚同步的显式列清单冲突。
func (s *Store) UpdateEmployeeOpenID(empID int64, openID string) error {
	if empID <= 0 {
		return fmt.Errorf("invalid empID %d", empID)
	}
	if _, err := s.db.Exec(`UPDATE employee SET open_id = ? WHERE id = ?`, openID, empID); err != nil {
		return fmt.Errorf("update employee.open_id (id=%d): %w", empID, err)
	}
	return nil
}

// EmpNoMatch 描述 emp_no 在 employee 表中的命中情况。
//
// 为什么要把「查无此人」与「工号不唯一」显式区分开：两者以前都被压成 `n == 0`，
// 信息在 store 层就丢了，调用方（syncer）只能把它们**一并**当成失败计数——
// 而云之家通讯录（约 4465 人，整个工作圈）远大于本系统花名册（employee 表仅百余人），
// 「本系统无此工号」是**绝大多数**、也是**完全正常**的情况。不区分就会把正常情况
// 误报成失败：退出码恒为 1、每次跑批写数千条 sync_run_error（详见 §6.5 的判读要求）。
type EmpNoMatch int

const (
	// EmpNoNotFound：本系统无此工号。
	// **正常情况**——云之家通讯录远大于本系统花名册，绝大多数通讯录人员与 FAMS 无关。
	EmpNoNotFound EmpNoMatch = iota
	// EmpNoUnique：恰好命中 1 行，可安全回填。
	EmpNoUnique
	// EmpNoAmbiguous：工号在本系统重复（≥2 行）⇒ **拒填**。
	// 真异常——宁可漏填也不能填错人（静默且危险，比「漏发」严重得多）。
	EmpNoAmbiguous
)

// SetEmployeeOpenIDByEmpNo 按工号回填云之家 openId（路径 B：通讯录增量同步）。
//
// ⚠️ 工号 emp_no **没有唯一索引**（历史数据里存在重复工号），所以绝不能直接
// `UPDATE employee SET open_id=? WHERE emp_no=?`——一旦工号撞上两行，openId 会被写到
// **错误的人**头上，而通知就会发给错误的人（静默且危险，比「漏发」严重得多）。
//
// 因此先 `SELECT COUNT(*) WHERE emp_no=?`，**仅当恰好命中 1 行才 UPDATE**；
// 0 行（查无此人）或 ≥2 行（工号不唯一）一律不写。
//
// 返回值 EmpNoMatch 把「命中情况」显式交给调用方，而不是像以前那样只给一个
// 0/1 行数让调用方自己猜——分类信息**本来就在那个 COUNT 里**，从它直接得出，
// **不新增任何查询**（同步要处理数千条记录）。
//
//	EmpNoNotFound  查无此人 → 未写（正常，调用方应计为"跳过"而非"失败"）
//	EmpNoUnique    唯一命中 → 已写 open_id
//	EmpNoAmbiguous 工号重复 → **未写**（真异常，调用方应计为"失败"并留痕）
//
// empNo 为空时直接返回 (EmpNoNotFound, nil)：无从匹配，不报错、不写库。
func (s *Store) SetEmployeeOpenIDByEmpNo(empNo, openID string) (EmpNoMatch, error) {
	if empNo == "" {
		return EmpNoNotFound, nil
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM employee WHERE emp_no = ?`, empNo).Scan(&n); err != nil {
		return EmpNoNotFound, fmt.Errorf("count employee by emp_no %q: %w", empNo, err)
	}
	// 分类信息就在这个 COUNT 里，直接据它判定，不额外查询。
	switch {
	case n == 0:
		// 查无此人：正常情况（云之家通讯录远大于本系统花名册）。
		return EmpNoNotFound, nil
	case n > 1:
		// ≥2 行：工号不唯一——宁可漏填也不能填错人，**绝不执行 UPDATE**。
		return EmpNoAmbiguous, nil
	}
	if _, err := s.db.Exec(`UPDATE employee SET open_id = ? WHERE emp_no = ?`, openID, empNo); err != nil {
		return EmpNoNotFound, fmt.Errorf("update employee.open_id by emp_no %q: %w", empNo, err)
	}
	return EmpNoUnique, nil
}
