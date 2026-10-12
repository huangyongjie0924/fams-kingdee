package store

import (
	"database/sql"
	"fmt"

	"asset-mgr/model"
)

// repair_notify_task 的持久化与状态机（见 docs/云之家待办通知集成方案.md §4.3 / §7.3）。
//
// 它是待办通知的「本地唯一真相」：外部依赖（云之家）失败时据此重试与幂等，
// **绝不**依赖云之家 create 报错去重。state 一律走 model.NotifyState* 常量，
// 不在本文件里散落字符串字面量。

// notifyTaskColumns 是 repair_notify_task 的显式列清单（全仓无 SELECT * 的约定）。
// 顺序必须与 scanNotifyTask 的 Scan 顺序逐一对齐——加列时两处一起改。
// channel / biz_id 追加在**末尾**：新增列一律追加，不插中间，避免既有行号语义漂移。
const notifyTaskColumns = `id, repair_id, emp_id, open_id, source_id, purpose, state, attempts, last_error, created_at, updated_at, channel, biz_id`

// notifyRow 抽象 *sql.Row 与 *sql.Rows 的公共 Scan 方法，使单行与多行共用同一段解析。
type notifyRow interface {
	Scan(dest ...any) error
}

// scanNotifyTask 按 notifyTaskColumns 的固定顺序解析一行任务。
// ⚠️ Scan 的目标顺序必须与 notifyTaskColumns **逐列对齐**——顺序漂移不会报错，
// 只会把值扫串位（如把 state 扫进 attempts），是最难查的一类静默 bug。
// 由 store/repair_notify_columns_test.go 的 TestNotifyTaskColumnsAlignWithScan 长期钉死。
func scanNotifyTask(r notifyRow) (*model.NotifyTask, error) {
	var t model.NotifyTask
	if err := r.Scan(&t.ID, &t.RepairID, &t.EmpID, &t.OpenID, &t.SourceID,
		&t.Purpose, &t.State, &t.Attempts, &t.LastError, &t.CreatedAt, &t.UpdatedAt,
		&t.Channel, &t.BizID); err != nil {
		return nil, err
	}
	return &t, nil
}

// GetNotifyTask 取「某单某人某 sourceId」的那一条任务；不存在返回 nil, nil。
// 幂等判定（create 还是流转）的第一步就是查它（§4.3）。
func (s *Store) GetNotifyTask(repairID, empID int64, sourceID string) (*model.NotifyTask, error) {
	row := s.db.QueryRow(`SELECT `+notifyTaskColumns+` FROM repair_notify_task
		WHERE repair_id = ? AND emp_id = ? AND source_id = ?`, repairID, empID, sourceID)
	t, err := scanNotifyTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get notify task (repair=%d emp=%d src=%q): %w", repairID, empID, sourceID, err)
	}
	return t, nil
}

// UpsertNotifyTask 以唯一键 (repair_id, emp_id, source_id) 落库，已存在则刷新可变字段。
//
// 用 ON DUPLICATE KEY UPDATE 而非「先查后写」：入队路径可能并发（worker pool），
// 先查后写在两个 goroutine 同时看不到记录时会各自插入，唯一键报错会让一条合法通知丢失。
// 交给数据库做原子 upsert 最稳。
//
// ⚠️ state / attempts / last_error **不在** UPDATE 列表里——它们的推进走
// UpdateNotifyTaskState / IncrNotifyTaskAttempts。否则「入队时再 upsert 一次」
// 会把已经推进到 dealt/active 的状态静默重置回 pending。
//
// ⚠️ channel / biz_id 用「非空才覆盖」的 UPDATE（IF 包裹：传入为空则保留原值，非空才覆盖），
// 因为本表有**两个写入者**：
//   - 发送路径（deliverEnsure）恒传当前通道 → 覆盖，反映「这条待办此刻走哪个通道」；
//   - 错误路径（recordFailure / markFailed）只传业务字段、channel 为空 → **保留**原值，
//     绝不能把已记下的通道抹成空串（抹了会让 flowcenter 待办被误判成 im-todo，办结时走错接口）。
func (s *Store) UpsertNotifyTask(t *model.NotifyTask) error {
	if t == nil {
		return fmt.Errorf("nil notify task")
	}
	state := t.State
	if state == "" {
		state = model.NotifyStatePending
	}
	if !model.IsValidNotifyState(state) {
		return fmt.Errorf("invalid notify state %q", state)
	}
	// 空通道按**存量语义**（im-todo）落库，与列默认值一致：错误路径来不及知道真实通道时，
	// 落到最保守的那个（切换前发出的都是 im-todo），而不是空串。
	channel := t.Channel
	if channel == "" {
		channel = model.NotifyChannelIMTodo
	}
	_, err := s.db.Exec(`INSERT INTO repair_notify_task
		(repair_id, emp_id, open_id, source_id, purpose, state, attempts, last_error, channel, biz_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE open_id = VALUES(open_id), purpose = VALUES(purpose),
			channel = IF(VALUES(channel) = '', channel, VALUES(channel)),
			biz_id  = IF(VALUES(biz_id)  = '', biz_id,  VALUES(biz_id))`,
		t.RepairID, t.EmpID, t.OpenID, t.SourceID, t.Purpose, state, t.Attempts, t.LastError,
		channel, t.BizID)
	if err != nil {
		return fmt.Errorf("upsert notify task (repair=%d emp=%d src=%q): %w", t.RepairID, t.EmpID, t.SourceID, err)
	}
	return nil
}

// UpdateNotifyTaskState 推进一条任务的状态，并记录 last_error（成功时传空串清掉）。
//
// state 必须来自 model.NotifyState* —— 非法值直接拒绝，避免把未知字符串写进库后
// 让「按 state 过滤」的重试器再也捞不到这条任务。任务不存在返回 sql.ErrNoRows。
func (s *Store) UpdateNotifyTaskState(repairID, empID int64, sourceID, state, lastError string) error {
	if !model.IsValidNotifyState(state) {
		return fmt.Errorf("invalid notify state %q", state)
	}
	res, err := s.db.Exec(`UPDATE repair_notify_task SET state = ?, last_error = ?
		WHERE repair_id = ? AND emp_id = ? AND source_id = ?`, state, lastError, repairID, empID, sourceID)
	if err != nil {
		return fmt.Errorf("update notify state (repair=%d emp=%d src=%q): %w", repairID, empID, sourceID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// IncrNotifyTaskAttempts 尝试次数 +1 并记录本次失败原因（重试器用）。
// 任务不存在返回 sql.ErrNoRows。
func (s *Store) IncrNotifyTaskAttempts(repairID, empID int64, sourceID, lastError string) error {
	res, err := s.db.Exec(`UPDATE repair_notify_task
		SET attempts = attempts + 1, last_error = ?
		WHERE repair_id = ? AND emp_id = ? AND source_id = ?`, lastError, repairID, empID, sourceID)
	if err != nil {
		return fmt.Errorf("incr notify attempts (repair=%d emp=%d src=%q): %w", repairID, empID, sourceID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListFailedNotifyTasks 列出投递失败、等待重试的任务（重试器入口）。
//
// maxAttempts<=0 表示不限制尝试次数；limit<=0 时用默认 100，避免一次拉爆内存。
// 排序用 updated_at, id：先重试「更早失败」的，且同一毫秒内按 id 稳定排序。
func (s *Store) ListFailedNotifyTasks(maxAttempts, limit int) ([]*model.NotifyTask, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + notifyTaskColumns + ` FROM repair_notify_task WHERE state = ?`
	args := []any{model.NotifyStateFailed}
	if maxAttempts > 0 {
		q += ` AND attempts < ?`
		args = append(args, maxAttempts)
	}
	q += ` ORDER BY updated_at, id LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list failed notify tasks: %w", err)
	}
	defer rows.Close()

	out := []*model.NotifyTask{}
	for rows.Next() {
		t, err := scanNotifyTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
