package store

import (
	"database/sql"
	"fmt"

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
