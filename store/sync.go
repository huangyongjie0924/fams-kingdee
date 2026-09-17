package store

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"asset-mgr/model"
)

// CreateSyncRun 创建一条同步运行记录。
func (s *Store) CreateSyncRun(tx *sql.Tx, source, mode, triggeredBy, cursor string) (int64, error) {
	res, err := tx.Exec(`INSERT INTO sync_run (source, mode, triggered_by, cursor_value, status)
		VALUES (?, ?, ?, ?, ?)`, source, mode, triggeredBy, cursor, model.SyncStatusRunning)
	if err != nil {
		return 0, fmt.Errorf("create sync_run: %w", err)
	}
	return res.LastInsertId()
}

// UpdateSyncRunStats 更新运行统计。
func UpdateSyncRunStats(tx *sql.Tx, id int64, stats model.SyncRun) error {
	_, err := tx.Exec(`UPDATE sync_run SET total_count=?, created_count=?, updated_count=?,
		skipped_count=?, deleted_count=?, failed_count=? WHERE id=?`,
		stats.TotalCount, stats.CreatedCount, stats.UpdatedCount,
		stats.SkippedCount, stats.DeletedCount, stats.FailedCount, id)
	return err
}

// FinishSyncRun 结束运行记录。
func FinishSyncRun(tx *sql.Tx, id int64, status, summary string) error {
	_, err := tx.Exec(`UPDATE sync_run SET status=?, error_summary=?, finished_at=? WHERE id=?`,
		status, summary, time.Now(), id)
	return err
}

// AddSyncRunError 记录单条错误。
func AddSyncRunError(tx *sql.Tx, runID int64, e model.SyncRunError) error {
	_, err := tx.Exec(`INSERT INTO sync_run_error
		(run_id, external_id, stage, field, error_code, error_message, raw_value, retryable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, e.ExternalID, e.Stage, e.Field, e.ErrorCode, trunc(e.ErrorMessage), trunc(e.RawValue), boolToInt(e.Retryable))
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSyncState 获取指定资源的同步游标。
func (s *Store) GetSyncState(source, resource string) (*model.SyncState, error) {
	var st model.SyncState
	var t sql.NullTime
	err := s.db.QueryRow(`SELECT id, source, resource, cursor_value, last_run_id, last_success_at, updated_at
		FROM sync_state WHERE source = ? AND resource = ?`, source, resource).
		Scan(&st.ID, &st.Source, &st.Resource, &st.CursorValue, &st.LastRunID, &t, &st.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Valid {
		st.LastSuccessAt = &t.Time
	}
	return &st, nil
}

// UpdateSyncState 更新游标；成功时才调用。
func UpdateSyncState(tx *sql.Tx, source, resource, cursor string, runID int64) error {
	_, err := tx.Exec(`INSERT INTO sync_state (source, resource, cursor_value, last_run_id, last_success_at)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE cursor_value=VALUES(cursor_value), last_run_id=VALUES(last_run_id), last_success_at=VALUES(last_success_at)`,
		source, resource, cursor, runID, time.Now())
	return err
}

// GetExternalAssetMap 按外部 ID 查询映射。
func (s *Store) GetExternalAssetMap(source, externalID string) (*model.ExternalAssetMap, error) {
	return s.scanExternalAssetMap(s.db.QueryRow(`SELECT id, source, external_id, external_code, card_id,
		external_version, payload_hash, last_sync_run_id, status, created_at, updated_at, deleted_at
		FROM external_asset_map WHERE source = ? AND external_id = ?`, source, externalID))
}

// GetExternalAssetMapByCardID 按本地 card_id 查询映射。
// 一张台账卡可能对应多张外部卡（金蝶合并卡共用资产编号），返回任意一条即可用于反查来源。
func (s *Store) GetExternalAssetMapByCardID(source string, cardID int64) (*model.ExternalAssetMap, error) {
	return s.scanExternalAssetMap(s.db.QueryRow(`SELECT id, source, external_id, external_code, card_id,
		external_version, payload_hash, last_sync_run_id, status, created_at, updated_at, deleted_at
		FROM external_asset_map WHERE source = ? AND card_id = ? AND deleted_at IS NULL LIMIT 1`, source, cardID))
}

// CreateMasterTx 在同步事务内按外部主数据新建本地主数据行。
// 仅当名称为非空时才调用：名称为空的引用无法建出可读的主数据。
func CreateMasterTx(tx *sql.Tx, kind, code, name string) (int64, error) {
	var q string
	var args []any
	switch kind {
	case model.MasterKindCategory:
		q = "INSERT INTO asset_category (name, code) VALUES (?, ?)"
		args = []any{name, code}
	case model.MasterKindDepartment:
		q = "INSERT INTO department (name, code) VALUES (?, ?)"
		args = []any{name, code}
	case model.MasterKindArea:
		q = "INSERT INTO asset_area (name, code) VALUES (?, ?)"
		args = []any{name, code}
	case model.MasterKindEmployee:
		q = "INSERT INTO employee (name, emp_no) VALUES (?, ?)"
		args = []any{name, code}
	case model.MasterKindVendor:
		q = "INSERT INTO vendor (name, code) VALUES (?, ?)"
		args = []any{name, code}
	case model.MasterKindCompany:
		q = "INSERT INTO company (name, code) VALUES (?, ?)"
		args = []any{name, code}
	default:
		return 0, fmt.Errorf("unknown master kind %q", kind)
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("create %s %q: %w", kind, name, err)
	}
	return res.LastInsertId()
}
func (s *Store) scanExternalAssetMap(r *sql.Row) (*model.ExternalAssetMap, error) {
	var m model.ExternalAssetMap
	var t sql.NullTime
	err := r.Scan(&m.ID, &m.Source, &m.ExternalID, &m.ExternalCode, &m.CardID,
		&m.ExternalVersion, &m.PayloadHash, &m.LastSyncRunID, &m.Status,
		&m.CreatedAt, &m.UpdatedAt, &t)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Valid {
		m.DeletedAt = &t.Time
	}
	return &m, nil
}

// UpsertExternalAssetMap 在事务内插入或更新外部资产映射。
func UpsertExternalAssetMap(tx *sql.Tx, m *model.ExternalAssetMap) error {
	if m.Status == "" {
		m.Status = model.ExternalMapStatusActive
	}
	_, err := tx.Exec(`INSERT INTO external_asset_map
		(source, external_id, external_code, card_id, external_version, payload_hash, last_sync_run_id, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			external_code=VALUES(external_code),
			card_id=VALUES(card_id),
			external_version=VALUES(external_version),
			payload_hash=VALUES(payload_hash),
			last_sync_run_id=VALUES(last_sync_run_id),
			status=VALUES(status),
			deleted_at=NULL`,
		m.Source, m.ExternalID, m.ExternalCode, m.CardID, m.ExternalVersion, m.PayloadHash, m.LastSyncRunID, m.Status)
	return err
}

// UpsertExternalMasterMap 在事务内插入或更新外部主数据映射。
func UpsertExternalMasterMap(tx *sql.Tx, m *model.ExternalMasterMap) error {
	_, err := tx.Exec(`INSERT INTO external_master_map
		(source, kind, external_id, external_code, external_name, local_id)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			external_code=VALUES(external_code),
			external_name=VALUES(external_name),
			local_id=VALUES(local_id)`,
		m.Source, m.Kind, m.ExternalID, m.ExternalCode, m.ExternalName, m.LocalID)
	return err
}

// LookupExternalMasterMap 查询外部主数据映射的本地 ID。
func (s *Store) LookupExternalMasterMap(source, kind, externalID string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT local_id FROM external_master_map
		WHERE source = ? AND kind = ? AND external_id = ?`, source, kind, externalID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// 同步写入动作
const (
	CardActionCreated   = "created"
	CardActionUpdated   = "updated"
	CardActionUnchanged = "unchanged"
)

// kingdeeOwnedColumns 外部同步托管（owned）的资产字段，与 ownedCardValues 一一对应。
// 其余列一律保留本地维护值——序列号、RFID、入库单号、台账金额、税额、分摊部门、
// 管理人、标签、附件等金蝶接口不提供或不做主，不能被同步清零。
var kingdeeOwnedColumns = []string{
	"name", "category_id", "spec", "unit", "quantity",
	"use_dept_id", "user_emp_id", "use_status", "area_id", "location",
	"purchase_date", "card_created_at", "source", "remark", "vendor_id",
	"status",
	"fin_asset_type", "owner_company_id", "fin_amount_with_tax",
	"fin_original_value", "fin_net_value",
}

// 新建同步卡时按类别默认值初始化，之后视为本地字段，不再被同步覆盖
var cardInitColumns = []string{"use_months", "fin_use_months", "fin_residual_rate"}

func ownedCardValues(c *model.AssetCard) []any {
	return []any{
		c.Name, c.CategoryID, c.Spec, c.Unit, c.Quantity,
		c.UseDeptID, c.UserEmpID, c.UseStatus, c.AreaID, c.Location,
		nullDate(c.PurchaseDate), nullDate(c.CardCreatedAt), c.Source, c.Remark, c.VendorID,
		statusOrDefault(c.Status),
		c.FinAssetType, c.OwnerCompanyID, c.FinAmountWithTax,
		c.FinOriginalValue, c.FinNetValue,
	}
}

// statusOrDefault 金蝶的使用状态没映射出台账枚举时（syncer 留空）落到「闲置」，
// 避免新建卡写进空状态。
func statusOrDefault(s string) string {
	if strings.TrimSpace(s) == "" {
		return model.StatusIdle
	}
	return s
}

// mergeOwnedFields 把外部卡的托管字段叠加到本地已有卡上。
// 金蝶必然提供值的字段每次覆盖；常为空的可选字段只在金蝶有值时才覆盖，
// 避免把台账里人工维护的规格、备注、购入日期、供应商清掉。
// 状态同理：syncer 只在金蝶 usestatus 命中映射表时才给出值，未命中时保留本地状态。
func mergeOwnedFields(dst, src *model.AssetCard) {
	dst.Name = src.Name
	dst.CategoryID = src.CategoryID
	dst.Unit = src.Unit
	// 数量：星瀚同步来的卡以星瀚为准（assetamount 全库 227 张都有值，实测都 >0），
	// 「>0 才覆盖」既能保住对齐，又不会抹掉手工卡自己填的数量
	// （手工新建的卡不在金蝶里，同步压根走不到这儿，这层是双保险）。
	if src.Quantity > 0 {
		dst.Quantity = src.Quantity
	}
	dst.UseDeptID = src.UseDeptID
	dst.UserEmpID = src.UserEmpID
	dst.UseStatus = src.UseStatus
	dst.AreaID = src.AreaID
	dst.Location = src.Location
	dst.Source = src.Source

	if src.Spec != "" {
		dst.Spec = src.Spec
	}
	if src.Remark != "" {
		dst.Remark = src.Remark
	}
	if src.PurchaseDate != "" {
		dst.PurchaseDate = src.PurchaseDate
	}
	if src.CardCreatedAt != "" {
		dst.CardCreatedAt = src.CardCreatedAt
	}
	if src.VendorID != 0 {
		dst.VendorID = src.VendorID
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.FinAssetType != "" {
		dst.FinAssetType = src.FinAssetType
	}
	if src.OwnerCompanyID != 0 {
		dst.OwnerCompanyID = src.OwnerCompanyID
	}
	// 金额类字段星瀚一律给 0，实测已复核：price 恒为 0.000000；财务明细子表 finentry
	// 有值的 200 行里 fin_originalval / fin_networth 全是 0，另有 27 行 finentry 为 null。
	// 也就是说这个接口根本取不到资产原值 / 累计折旧 / 净值。
	// 所以这里只在大于 0 时覆盖，保住台账人工维护（卡片编辑 / Excel 导入）的金额；
	// 将来星瀚侧把这三个数补进接口返回，这段逻辑不用改就会自动生效。
	if src.FinAmountWithTax > 0 {
		dst.FinAmountWithTax = src.FinAmountWithTax
	}
	if src.FinOriginalValue > 0 {
		dst.FinOriginalValue = src.FinOriginalValue
	}
	if src.FinNetValue > 0 {
		dst.FinNetValue = src.FinNetValue
	}
}

// ApplyOwnedCardTx 按资产编码落库一张同步卡：只覆盖金蝶托管字段，
// 返回 (card_id, created|updated|unchanged)。dryRun 为真时只比对不写库。
func ApplyOwnedCardTx(tx *sql.Tx, c *model.AssetCard, operator string, dryRun bool) (int64, string, error) {
	var existingID int64
	err := tx.QueryRow("SELECT id FROM asset_card WHERE asset_code = ?", c.AssetCode).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return 0, "", err
	}

	if existingID > 0 {
		old, err := getCardTx(tx, existingID)
		restored := false
		if err == sql.ErrNoRows {
			// 同编码卡片被软删除过：外部资产重新出现时恢复原卡，不新建
			old, err = getCardTxAny(tx, existingID)
			restored = true
		}
		if err != nil {
			return 0, "", err
		}

		merged := *old
		mergeOwnedFields(&merged, c)
		diffs := diffCard(old, &merged)
		action := CardActionUnchanged
		if restored {
			action = CardActionUpdated
		} else if len(diffs) > 0 {
			action = CardActionUpdated
		}
		if dryRun {
			return existingID, action, nil
		}

		if restored {
			if err := RestoreCardTx(tx, existingID, operator); err != nil {
				return 0, "", err
			}
		}
		// 无变化时不落任何写：避免 updated_at 被同步无谓刷新
		if action != CardActionUnchanged {
			sets := make([]string, len(kingdeeOwnedColumns))
			for i, col := range kingdeeOwnedColumns {
				sets[i] = col + " = ?"
			}
			sets = append(sets, "ext_json = ?")
			args := append(ownedCardValues(&merged), nullJSON(c.ExtJSON))
			args = append(args, existingID)
			if _, err := tx.Exec("UPDATE asset_card SET "+strings.Join(sets, ",")+" WHERE id = ?", args...); err != nil {
				return 0, "", fmt.Errorf("update card: %w", err)
			}
		}
		for _, d := range diffs {
			if err := addHistoryTx(tx, existingID, "update", d.Field, d.Old, d.New, operator); err != nil {
				return 0, "", err
			}
		}
		return existingID, action, nil
	}

	if dryRun {
		return 0, CardActionCreated, nil
	}

	cols := append([]string{}, kingdeeOwnedColumns...)
	cols = append(cols, cardInitColumns...)
	cols = append(cols, "created_by", "ext_json")
	args := append(ownedCardValues(c), c.UseMonths, c.FinUseMonths, c.FinResidualRate, operator, nullJSON(c.ExtJSON))
	q := fmt.Sprintf("INSERT INTO asset_card (%s) VALUES (%s)", strings.Join(cols, ","), placeholders(len(cols)))
	res, err := tx.Exec(q, args...)
	if err != nil {
		return 0, "", fmt.Errorf("insert card: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", err
	}
	if err := addHistoryTx(tx, id, "create", "", "", c.AssetCode, operator); err != nil {
		return 0, "", err
	}
	return id, CardActionCreated, nil
}

func nullJSON(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func getCardTx(tx *sql.Tx, id int64) (*model.AssetCard, error) {
	row := tx.QueryRow(cardSelect+" WHERE c.deleted_at IS NULL AND c.id = ?", id)
	return scanCard(row)
}

// getCardTxAny 包含已软删除的卡片，用于判断是否需要恢复。
func getCardTxAny(tx *sql.Tx, id int64) (*model.AssetCard, error) {
	row := tx.QueryRow(cardSelect+" WHERE c.id = ?", id)
	return scanCard(row)
}

// SoftDeleteCardTx 软删除资产卡并记录履历。
func SoftDeleteCardTx(tx *sql.Tx, id int64, operator string) error {
	res, err := tx.Exec("UPDATE asset_card SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL", time.Now(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return addHistoryTx(tx, id, "delete", "", "", "", operator)
}

// RestoreCardTx 恢复软删除的资产卡。
func RestoreCardTx(tx *sql.Tx, id int64, operator string) error {
	res, err := tx.Exec("UPDATE asset_card SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return addHistoryTx(tx, id, "restore", "", "", "", operator)
}

// HashPayload 对 JSON payload 取 MD5 hash，用于判断外部数据是否变化。
func HashPayload(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:]), nil
}
