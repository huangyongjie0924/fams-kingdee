package main

import (
	"strings"
	"testing"

	"asset-mgr/model"
)

// 一致时不该报任何东西。假警会让这个工具失去信任，而它一旦不可信，
// 人就会退回"看汇总数字"——那正是这个工具要解决的问题。
func TestCompareFinFieldsAllMatch(t *testing.T) {
	want := &model.AssetCard{
		FinOriginalValue:    359265.08,
		FinAccumDepreciaton: 44908.14,
		FinNetValue:         314356.94,
		FinUseMonths:        24,
		FinResidualRate:     5,
		FinTax:              0, // 星瀚没给
		FinAmountWithTax:    0,
	}
	actual := *want

	stats := map[string]*fieldStat{}
	got := compareFinFields("12020302000003", want, &actual, stats)

	if len(got) != 0 {
		t.Fatalf("完全一致不该报明细，实际:\n%s", strings.Join(got, "\n"))
	}
	for _, label := range []string{"原值", "累计折旧", "净值", "财务使用期限", "残值率"} {
		if stats[label] == nil || stats[label].provided != 1 {
			t.Errorf("%s 的 provided 应为 1，实际 %+v", label, stats[label])
		}
	}
	// 税额为 0（星瀚没给）→ 完全不该出现在统计里
	if stats["税额"] != nil {
		t.Errorf("星瀚没给的字段不该参与统计，实际 %+v", stats["税额"])
	}
}

// 星瀚给了值、台账没抄对 —— 正是上一轮那个 bug 的形态
// （22 张卡残值率停在类别默认值 5%，星瀚给的是 3%）。
func TestCompareFinFieldsCatchesMismatch(t *testing.T) {
	want := &model.AssetCard{FinOriginalValue: 100, FinResidualRate: 3}
	actual := model.AssetCard{FinOriginalValue: 100, FinResidualRate: 5} // 停在默认值

	stats := map[string]*fieldStat{}
	got := compareFinFields("990605", want, &actual, stats)

	if len(got) != 1 {
		t.Fatalf("应报 1 条不一致，实际 %d 条:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "残值率") || !strings.Contains(got[0], "990605") {
		t.Errorf("明细应含字段名与资产编码，实际: %s", got[0])
	}
	if stats["残值率"].mismatch != 1 {
		t.Errorf("残值率 mismatch 应为 1，实际 %d", stats["残值率"].mismatch)
	}
	if stats["原值"].mismatch != 0 {
		t.Error("原值两侧一致，却报了不一致")
	}
}

// 星瀚没给值（0）时不做断言。
//
// 这是整个工具最容易写错的地方：台账对那 178 张没有税额的卡保留的是
// 人工维护值，如果断言它们等于 0，会一次性报出 178 条假警。
func TestCompareFinFieldsSkipsFieldsWithoutValue(t *testing.T) {
	want := &model.AssetCard{FinOriginalValue: 100, FinTax: 0, FinAmountWithTax: 0}
	actual := model.AssetCard{FinOriginalValue: 100, FinTax: 999.99, FinAmountWithTax: 8888.88}

	stats := map[string]*fieldStat{}
	got := compareFinFields("X", want, &actual, stats)

	if len(got) != 0 {
		t.Fatalf("星瀚没给的字段不该报不一致（台账保留的是人工值），实际:\n%s", strings.Join(got, "\n"))
	}
	if stats["税额"] != nil || stats["含税金额"] != nil {
		t.Errorf("星瀚没给的字段不该进统计，实际 税额=%+v 含税金额=%+v",
			stats["税额"], stats["含税金额"])
	}
}

// 容差：库列是 DECIMAL(14,2)，半分钱以内视为一致，超过才报。
func TestCompareFinFieldsTolerance(t *testing.T) {
	t.Run("半分钱以内不报", func(t *testing.T) {
		want := &model.AssetCard{FinOriginalValue: 100.00}
		actual := model.AssetCard{FinOriginalValue: 100.004}
		stats := map[string]*fieldStat{}
		if got := compareFinFields("X", want, &actual, stats); len(got) != 0 {
			t.Errorf("差 0.004 应视为一致，实际:\n%s", strings.Join(got, "\n"))
		}
	})
	t.Run("超过半分钱要报", func(t *testing.T) {
		want := &model.AssetCard{FinOriginalValue: 100.00}
		actual := model.AssetCard{FinOriginalValue: 100.01}
		stats := map[string]*fieldStat{}
		if got := compareFinFields("X", want, &actual, stats); len(got) != 1 {
			t.Errorf("差 0.01 应报不一致，实际 %d 条", len(got))
		}
	})
}

// 多个字段同时不一致时都要报出来，不能只报第一个。
func TestCompareFinFieldsReportsAllFields(t *testing.T) {
	want := &model.AssetCard{FinOriginalValue: 100, FinNetValue: 80, FinUseMonths: 24}
	actual := model.AssetCard{FinOriginalValue: 200, FinNetValue: 180, FinUseMonths: 36}

	stats := map[string]*fieldStat{}
	got := compareFinFields("X", want, &actual, stats)

	if len(got) != 3 {
		t.Fatalf("三个字段都不一致，应报 3 条，实际 %d 条:\n%s", len(got), strings.Join(got, "\n"))
	}
}
