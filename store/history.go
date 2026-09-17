package store

import (
	"database/sql"
	"fmt"
	"strconv"

	"asset-mgr/model"
)

type fieldDiff struct {
	Field string
	Old   string
	New   string
}

// diffCard 逐字段比对，历史里存中文字段名，直接给人看
func diffCard(old, cur *model.AssetCard) []fieldDiff {
	var out []fieldDiff
	cmp := func(label, o, n string) {
		if o != n {
			out = append(out, fieldDiff{Field: label, Old: o, New: n})
		}
	}
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	i2s := func(i int) string { return strconv.Itoa(i) }

	cmp("资产编码", old.AssetCode, cur.AssetCode)
	cmp("资产名称", old.Name, cur.Name)
	cmp("资产类别", i64s(old.CategoryID), i64s(cur.CategoryID))
	cmp("规格型号", old.Spec, cur.Spec)
	cmp("设备序列号", old.SerialNo, cur.SerialNo)
	cmp("计量单位", old.Unit, cur.Unit)
	cmp("状态", old.Status, cur.Status)
	cmp("金额", num(old.Amount), num(cur.Amount))
	cmp("数量", num(old.Quantity), num(cur.Quantity))
	cmp("使用公司", i64s(old.UseCompanyID), i64s(cur.UseCompanyID))
	cmp("使用部门", i64s(old.UseDeptID), i64s(cur.UseDeptID))
	cmp("使用人", i64s(old.UserEmpID), i64s(cur.UserEmpID))
	cmp("使用状态", old.UseStatus, cur.UseStatus)
	cmp("管理人", i64s(old.ManagerEmpID), i64s(cur.ManagerEmpID))
	cmp("所属公司", i64s(old.OwnerCompanyID), i64s(cur.OwnerCompanyID))
	cmp("区域", i64s(old.AreaID), i64s(cur.AreaID))
	cmp("存放地点", old.Location, cur.Location)
	cmp("购入日期", old.PurchaseDate, cur.PurchaseDate)
	cmp("建卡时间", old.CardCreatedAt, cur.CardCreatedAt)
	cmp("使用期限", i2s(old.UseMonths), i2s(cur.UseMonths))
	cmp("来源", old.Source, cur.Source)
	cmp("入库/收货单号", old.InStockNo, cur.InStockNo)
	cmp("RFID", old.RFID, cur.RFID)
	cmp("备注", old.Remark, cur.Remark)
	cmp("资产类型", old.FinAssetType, cur.FinAssetType)
	cmp("分摊部门", i64s(old.FinShareDeptID), i64s(cur.FinShareDeptID))
	cmp("供应商", i64s(old.VendorID), i64s(cur.VendorID))
	cmp("含税金额", num(old.FinAmountWithTax), num(cur.FinAmountWithTax))
	cmp("税额", num(old.FinTax), num(cur.FinTax))
	cmp("原值", num(old.FinOriginalValue), num(cur.FinOriginalValue))
	cmp("净值", num(old.FinNetValue), num(cur.FinNetValue))
	cmp("累计折旧", num(old.FinAccumDepreciaton), num(cur.FinAccumDepreciaton))
	cmp("残值率", num(old.FinResidualRate), num(cur.FinResidualRate))
	cmp("财务使用期限", i2s(old.FinUseMonths), i2s(cur.FinUseMonths))
	cmp("入账期间", old.FinPeriod, cur.FinPeriod)
	cmp("入账时间", old.FinEntryDate, cur.FinEntryDate)
	cmp("财务信息状态", old.FinStatus, cur.FinStatus)
	cmp("维保供应商", i64s(old.MtVendorID), i64s(cur.MtVendorID))
	cmp("供应商联系人", old.MtContact, cur.MtContact)
	cmp("联系方式", old.MtPhone, cur.MtPhone)
	cmp("维保负责人", i64s(old.MtOwnerEmpID), i64s(cur.MtOwnerEmpID))
	cmp("维保到期时间", old.MtExpireDate, cur.MtExpireDate)
	cmp("维保说明", old.MtRemark, cur.MtRemark)
	return out
}

func i64s(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

func addHistoryTx(tx *sql.Tx, cardID int64, action, field, oldVal, newVal, operator string) error {
	_, err := tx.Exec(`INSERT INTO asset_history (card_id, action, field, old_value, new_value, operator)
		VALUES (?, ?, ?, ?, ?, ?)`, cardID, action, field, trunc(oldVal), trunc(newVal), operator)
	if err != nil {
		return fmt.Errorf("insert history: %w", err)
	}
	return nil
}

func trunc(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

func (s *Store) ListHistory(cardID int64) ([]model.HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT id, card_id, action, field, old_value, new_value, operator, created_at
		FROM asset_history WHERE card_id = ? ORDER BY id DESC`, cardID)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	out := []model.HistoryEntry{}
	for rows.Next() {
		var h model.HistoryEntry
		if err := rows.Scan(&h.ID, &h.CardID, &h.Action, &h.Field, &h.OldValue, &h.NewValue,
			&h.Operator, &h.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
