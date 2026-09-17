package store

import (
	"database/sql"
	"fmt"
	"math"

	"asset-mgr/model"
)

// round2 把金额收敛到两位小数。库列是 DECIMAL(14,2)，浮点减法的尾差
// （如 2222.2199999999993）只会污染履历和接口响应，不该让它传下去。
func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

// FinanceUpdateResult 是一张卡在这次批量更新里「实际发生了什么」。
// 没有任何字段变化时不会被返回 —— 导出的整表原样导回，应该得到空结果，
// 而不是几百条「什么都没改」的噪音。
type FinanceUpdateResult struct {
	AssetCode string        `json:"asset_code"`
	Name      string        `json:"name"`
	Changes   []FieldChange `json:"changes"`
}

// UpdateCardsFinance 按资产编码批量更新财务信息。
//
// 四条硬约束，都是为了「导出的整表改完直接导回」这个主用法不出事：
//
//  1. 只碰财务列（原值 / 累计折旧 / 净值 / 残值率 / 财务使用期限）。名称、部门、
//     使用人、状态、数量这些列即使出现在文件里也不会被读进来 —— 一次批量导入
//     顺手把两百多张卡的部门全清空，是这套系统最不该发生的事故。
//  2. 净值不接收输入，只在原值或累计折旧变化时按 原值 − 累计折旧 重算（见下）。
//  3. 没填的列不动（见 model.FinanceUpdate 的指针语义）。
//  4. 没有任何字段变化的行不写库、不记履历、也不出现在返回值里。
//
// dryRun=true 时只算不写：批量改两百多张卡的金额不可逆，前端要先把
// 「哪张卡、哪个字段、从多少改到多少」摆出来给人确认，再真正落库。
//
// 全程单事务：任何一行失败整批回滚，不产生改了一半的中间态。
func (s *Store) UpdateCardsFinance(ups []model.FinanceUpdate, operator string, dryRun bool) ([]FinanceUpdateResult, error) {
	if len(ups) == 0 {
		return []FinanceUpdateResult{}, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 一次把要改的卡查回来，避免每行一次 cardSelect（那个 SQL 带 11 个 JOIN，
	// 乘 227 行没有意义，这里只要财务列和名字）。
	const sel = `SELECT id, asset_code, name, fin_original_value, fin_accum_depreciation,
		fin_net_value, fin_residual_rate, fin_use_months
		FROM asset_card WHERE asset_code = ? AND deleted_at IS NULL`

	results := []FinanceUpdateResult{}
	for i := range ups {
		up := &ups[i]

		var (
			id                                      int64
			code, name                              string
			origVal, accumDep, netVal, residualRate float64
			finUseMonths                            int
		)
		err := tx.QueryRow(sel, up.AssetCode).Scan(
			&id, &code, &name, &origVal, &accumDep, &netVal, &residualRate, &finUseMonths)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("资产编码 %s 不存在或已删除", up.AssetCode)
		}
		if err != nil {
			return nil, fmt.Errorf("查询资产编码 %s 失败: %w", up.AssetCode, err)
		}

		// 用两个 AssetCard 走一遍 diffCard：字段中文名、旧新值格式（金额去尾零、
		// 空值显示为空串）全部沿用改单卡那条路径，履历和预检不会出现两套说法。
		oldCard := model.AssetCard{
			ID: id, AssetCode: code, Name: name,
			FinOriginalValue: origVal, FinAccumDepreciaton: accumDep, FinNetValue: netVal,
			FinResidualRate: residualRate, FinUseMonths: finUseMonths,
		}
		newCard := oldCard

		if up.OriginalValue != nil {
			newCard.FinOriginalValue = *up.OriginalValue
		}
		if up.AccumDepreciation != nil {
			newCard.FinAccumDepreciaton = *up.AccumDepreciation
		}
		if up.ResidualRate != nil {
			newCard.FinResidualRate = *up.ResidualRate
		}
		if up.FinUseMonths != nil {
			newCard.FinUseMonths = *up.FinUseMonths
		}
		// 净值是派生值，但只在「原值或累计折旧这次真的变了」时才重算。
		//
		// 不能无条件重算：导出的整表每一行都带原值/累计折旧，一次只改 1 张卡的导入
		// 会顺带把另外两百多张卡的净值重写一遍（存量里本来就有对不上的），
		// 预检于是显示「将更新 2 张」——用户根本认不出哪些改动是自己的。
		// 而且那些写入是真实的：会刷 updated_at、会往履历里灌垃圾。
		if newCard.FinOriginalValue != oldCard.FinOriginalValue ||
			newCard.FinAccumDepreciaton != oldCard.FinAccumDepreciaton {
			// 两位小数：DECIMAL(14,2) 本来就存不下更多，不修掉的话
			// 9999.99−7777.77 会算成 2222.2199999999993 一路带进履历和接口响应。
			newCard.FinNetValue = round2(newCard.FinOriginalValue - newCard.FinAccumDepreciaton)
		}

		changes := diffCard(&oldCard, &newCard)
		if len(changes) == 0 {
			continue
		}

		if !dryRun {
			if _, err := tx.Exec(`UPDATE asset_card SET
				fin_original_value = ?, fin_accum_depreciation = ?, fin_net_value = ?,
				fin_residual_rate = ?, fin_use_months = ?
				WHERE id = ? AND deleted_at IS NULL`,
				newCard.FinOriginalValue, newCard.FinAccumDepreciaton, newCard.FinNetValue,
				newCard.FinResidualRate, newCard.FinUseMonths, id); err != nil {
				return nil, fmt.Errorf("更新 %s 失败: %w", code, err)
			}
			// 履历逐字段写：一次批量改 200 张卡，事后要能回答「这张的原值是谁什么时候改的」。
			for _, d := range changes {
				if err := addHistoryTx(tx, id, "update", d.Field, d.Old, d.New, operator); err != nil {
					return nil, err
				}
			}
		}

		results = append(results, FinanceUpdateResult{AssetCode: code, Name: name, Changes: changes})
	}

	if dryRun {
		// 只算不写：显式回滚，别让只读路径留下任何可能的写入。
		return results, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}
