package store

import (
	"testing"

	"asset-mgr/model"
)

// TestColumnValueAlignment 守住「列名列表」与「值列表」一一对应。
//
// 这两组是分开维护的：加了一列却忘了加值，拼出来的 INSERT 列数与值数对不上。
// 而 INSERT 只在「同步到一个本地还没有的资产编码」时才走，
// 日常增量同步根本跑不到，能在仓库里躺很久不暴露。
func TestColumnValueAlignment(t *testing.T) {
	c := &model.AssetCard{}

	if got, want := len(ownedCardValues(c)), len(kingdeeOwnedColumns); got != want {
		t.Fatalf("kingdeeOwnedColumns 有 %d 列，ownedCardValues 却给了 %d 个值", want, got)
	}
	if got, want := len(cardInitValues(c)), len(cardInitColumns); got != want {
		t.Fatalf("cardInitColumns 有 %d 列，cardInitValues 却给了 %d 个值", want, got)
	}

	// 两组列不能重叠：INSERT 时会被拼进同一个列清单，重复列名直接报 SQL 错
	owned := make(map[string]bool, len(kingdeeOwnedColumns))
	for _, col := range kingdeeOwnedColumns {
		owned[col] = true
	}
	for _, col := range cardInitColumns {
		if owned[col] {
			t.Fatalf("列 %s 同时出现在 kingdeeOwnedColumns 与 cardInitColumns，INSERT 会重复", col)
		}
	}
}

// TestMergeOwnedFieldsSkipsFinanceWithoutFinEntry 守住「星瀚没给明细就一个财务字段都不碰」。
//
// 这是同编码重复行踩出来的坑：227 行只对应 200 个资产编码，多出来的那 27 行
// finentry 是 null。syncer 会给这种行预填**资产类别的默认**使用期限 / 残值率
// （如房屋类 240 期、5%），于是它们带着非零的假值通过「大于 0 才覆盖」，
// 把前一行刚从星瀚同步下来的真值（如 107 期、3%）又盖回去。
func TestMergeOwnedFieldsSkipsFinanceWithoutFinEntry(t *testing.T) {
	dst := &model.AssetCard{
		FinOriginalValue:    359265.08,
		FinAccumDepreciaton: 44908.14,
		FinNetValue:         314356.94,
		FinUseMonths:        107,
		FinResidualRate:     3,
	}
	// 明细为 null 的重复行：金额解析成 0，使用期限/残值率被类别默认值填成了 240 / 5
	src := &model.AssetCard{
		FinEntryPresent:  false,
		FinUseMonths:     240,
		FinResidualRate:  5,
		FinOriginalValue: 0,
	}

	mergeOwnedFields(dst, src)

	if dst.FinUseMonths != 107 {
		t.Errorf("使用期限被重复行覆盖：期望 107，得到 %d", dst.FinUseMonths)
	}
	if dst.FinResidualRate != 3 {
		t.Errorf("残值率被重复行覆盖：期望 3，得到 %v", dst.FinResidualRate)
	}
	if dst.FinOriginalValue != 359265.08 {
		t.Errorf("原值被重复行覆盖：期望 359265.08，得到 %v", dst.FinOriginalValue)
	}
	if dst.FinAccumDepreciaton != 44908.14 || dst.FinNetValue != 314356.94 {
		t.Errorf("累计折旧 / 净值被重复行覆盖：%v / %v", dst.FinAccumDepreciaton, dst.FinNetValue)
	}
}

// TestMergeOwnedFieldsTakesFinanceFromFinEntry 守住反向：明细带了就以星瀚为准。
func TestMergeOwnedFieldsTakesFinanceFromFinEntry(t *testing.T) {
	dst := &model.AssetCard{
		FinOriginalValue: 100, // 人工填的旧值
		FinUseMonths:     240, // 类别默认值
		FinResidualRate:  5,
	}
	src := &model.AssetCard{
		FinEntryPresent:     true,
		FinOriginalValue:    359265.08,
		FinAccumDepreciaton: 44908.14,
		FinNetValue:         314356.94,
		FinUseMonths:        24,
		FinResidualRate:     3,
	}

	mergeOwnedFields(dst, src)

	if dst.FinOriginalValue != 359265.08 || dst.FinAccumDepreciaton != 44908.14 || dst.FinNetValue != 314356.94 {
		t.Errorf("金额未按星瀚覆盖：%v / %v / %v",
			dst.FinOriginalValue, dst.FinAccumDepreciaton, dst.FinNetValue)
	}
	if dst.FinUseMonths != 24 {
		t.Errorf("使用期限未按星瀚覆盖：期望 24，得到 %d", dst.FinUseMonths)
	}
	if dst.FinResidualRate != 3 {
		t.Errorf("残值率未按星瀚覆盖：期望 3，得到 %v", dst.FinResidualRate)
	}
}

// TestMergeOwnedFieldsTaxOnlyWhenPresent 守住含税金额 / 税额的「部分覆盖」语义。
//
// 星瀚只对 22/200 张卡提供税额，其余 178 张是空（不是 0）。
// 那 178 张的含税金额必须保持人工维护——不能因为税额是 0 就把
// 「含税金额 = 原值」写进去，那 178 张的原值未必是含税口径。
func TestMergeOwnedFieldsTaxOnlyWhenPresent(t *testing.T) {
	t.Run("星瀚给了税额", func(t *testing.T) {
		dst := &model.AssetCard{}
		src := &model.AssetCard{
			FinEntryPresent:  true,
			FinOriginalValue: 5574.34,
			FinTax:           724.66,
			FinAmountWithTax: 6299.00, // 原值 + 税额
		}
		mergeOwnedFields(dst, src)
		if dst.FinTax != 724.66 {
			t.Errorf("税额未落库：期望 724.66，得到 %v", dst.FinTax)
		}
		if dst.FinAmountWithTax != 6299.00 {
			t.Errorf("含税金额未落库：期望 6299.00，得到 %v", dst.FinAmountWithTax)
		}
	})

	t.Run("星瀚没给税额", func(t *testing.T) {
		dst := &model.AssetCard{FinAmountWithTax: 8888.88, FinTax: 999.99} // 人工填的
		src := &model.AssetCard{
			FinEntryPresent:  true,
			FinOriginalValue: 359265.08,
			FinTax:           0,
			FinAmountWithTax: 0,
		}
		mergeOwnedFields(dst, src)
		if dst.FinTax != 999.99 {
			t.Errorf("税额被抹掉了：期望保留 999.99，得到 %v", dst.FinTax)
		}
		if dst.FinAmountWithTax != 8888.88 {
			t.Errorf("含税金额被抹掉了：期望保留 8888.88，得到 %v", dst.FinAmountWithTax)
		}
		if dst.FinOriginalValue != 359265.08 {
			t.Errorf("原值该照常覆盖：期望 359265.08，得到 %v", dst.FinOriginalValue)
		}
	})
}
