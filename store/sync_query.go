package store

import (
	"database/sql"
	"fmt"
	"time"

	"asset-mgr/model"
)

// ListSyncRuns 查询同步运行记录，按开始时间倒序。
// ListSyncRuns 列出同步运行记录。resource 为空表示不限资源类型。
func (s *Store) ListSyncRuns(resource string, limit, offset int) ([]model.SyncRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, source, resource, mode, triggered_by, cursor_value, status,
		total_count, created_count, updated_count, skipped_count, deleted_count, failed_count,
		error_summary, started_at, finished_at
		FROM sync_run`
	args := []any{}
	if resource != "" {
		q += ` WHERE resource = ?`
		args = append(args, resource)
	}
	q += ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query sync_run: %w", err)
	}
	defer rows.Close()

	out := []model.SyncRun{}
	for rows.Next() {
		var r model.SyncRun
		var t sql.NullTime
		if err := rows.Scan(&r.ID, &r.Source, &r.Resource, &r.Mode, &r.TriggeredBy, &r.CursorValue, &r.Status,
			&r.TotalCount, &r.CreatedCount, &r.UpdatedCount, &r.SkippedCount, &r.DeletedCount, &r.FailedCount,
			&r.ErrorSummary, &r.StartedAt, &t); err != nil {
			return nil, err
		}
		if t.Valid {
			r.FinishedAt = &t.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSyncRun 按 ID 查询运行记录。
func (s *Store) GetSyncRun(id int64) (*model.SyncRun, error) {
	var r model.SyncRun
	var t sql.NullTime
	err := s.db.QueryRow(`SELECT id, source, resource, mode, triggered_by, cursor_value, status,
		total_count, created_count, updated_count, skipped_count, deleted_count, failed_count,
		error_summary, started_at, finished_at
		FROM sync_run WHERE id = ?`, id).
		Scan(&r.ID, &r.Source, &r.Resource, &r.Mode, &r.TriggeredBy, &r.CursorValue, &r.Status,
			&r.TotalCount, &r.CreatedCount, &r.UpdatedCount, &r.SkippedCount, &r.DeletedCount, &r.FailedCount,
			&r.ErrorSummary, &r.StartedAt, &t)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Valid {
		r.FinishedAt = &t.Time
	}
	return &r, nil
}

// HasSuccessfulRunSince 判断某资源在 since 之后是否有过成功的跑批。
//
// 供调度器做「启动补跑」判断：进程在定时时刻处于停机状态时，这一批就丢了，
// 只能等第二天。启动时查一次，就不会因为一次部署而整天数据不更新。
//
// 两道过滤，缺一不可：
//
//   - status='success'：失败或还在 running 的批次不能算"已经同步过"，
//     否则一次失败的跑批会让补跑判定认为今天已经跑过了。
//   - triggeredBy 非空时只统计该触发来源。调度器传 "scheduler"，
//     这样手工点「立即增量同步」（只跑资产卡、不跑组织）不会被当成
//     "今天的定时批次已经跑过"，该补跑还是补跑。
func (s *Store) HasSuccessfulRunSince(resource, triggeredBy string, since time.Time) (bool, error) {
	q, args := syncRunSinceQuery(resource, triggeredBy, since)

	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return false, fmt.Errorf("count sync_run since %s: %w", since.Format(time.RFC3339), err)
	}
	return n > 0, nil
}

// syncRunSinceQuery 抽出来是为了能单测。这个函数只有三道 WHERE 条件，
// 但每一条都对应一个真实踩过的坑（见 HasSuccessfulRunSince 的注释），
// 少一条不会编译报错、也不会让任何现有测试变红，只会让补跑判断悄悄错掉。
func syncRunSinceQuery(resource, triggeredBy string, since time.Time) (string, []any) {
	q := `SELECT COUNT(*) FROM sync_run WHERE resource = ? AND status = ? AND started_at >= ?`
	args := []any{resource, model.SyncStatusSuccess, since}
	if triggeredBy != "" {
		q += ` AND triggered_by = ?`
		args = append(args, triggeredBy)
	}
	return q, args
}

// ListSyncRunErrors 查询某次运行的错误明细。
func (s *Store) ListSyncRunErrors(runID int64, limit, offset int) ([]model.SyncRunError, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, run_id, external_id, stage, field, error_code, error_message,
		raw_value, retryable, created_at
		FROM sync_run_error WHERE run_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`, runID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query sync_run_error: %w", err)
	}
	defer rows.Close()

	out := []model.SyncRunError{}
	for rows.Next() {
		var e model.SyncRunError
		var retryable int
		if err := rows.Scan(&e.ID, &e.RunID, &e.ExternalID, &e.Stage, &e.Field, &e.ErrorCode,
			&e.ErrorMessage, &e.RawValue, &retryable, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Retryable = retryable != 0
		out = append(out, e)
	}
	return out, rows.Err()
}
