package syncer

import (
	"math"

	"asset-mgr/integration/kingdee"
	"asset-mgr/model"
)

// ApplyFinEntry 把星瀚资产卡的财务明细子表（finentry）落到台账卡片上。
//
// 为什么抽成导出函数：这条解析规则现在有两个调用方——同步（mapCard）
// 和核对（cmd/kdverify）。如果核对工具把规则再实现一遍，两边一有出入就会
// 报假警或漏报，而「核对工具报的错」恰恰是最不能出错的东西：它一旦不可信，
// 人就会重新回到"看汇总数字"的老路上去。
//
// 调用方需保证 e 非空。finentry 为 null 时（同编码重复行里 bizstatus=ADD 的那份）
// 不要调用——那种情况下一个财务字段都不该碰，由 mergeOwnedFields 的
// FinEntryPresent 整组开关保证。
//
// ⚠️ 调用顺序有要求：必须在「资产类别默认值预填」之后调用。
// 反过来的话，星瀚给的真值会被类别默认值（如房屋类 240 期 / 5%）盖掉。
func ApplyFinEntry(c *model.AssetCard, e *kingdee.FinEntry) {
	c.FinEntryPresent = true
	c.FinOriginalValue = parseAmount(e.OriginalVal)
	c.FinAccumDepreciaton = parseAmount(e.AccumDepre)
	c.FinNetValue = parseAmount(e.NetWorth)

	// 预计使用期数（月）就是台账的「财务使用期限」，24~600 的整数，直接取用。
	if n := parseAmount(e.PreUseAmount); n > 0 {
		c.FinUseMonths = int(n)
	}

	// 残值率是反算出来的：星瀚只给预计残值（金额），没有给比率。
	// 不能直接相除就完事——残值金额保留 2 位小数，除出来的比率是
	// 4.999729% ~ 5.00034% 这种脏值。库列是 DECIMAL(6,3)，
	// 四舍五入到 3 位后才会收敛成干净的 5.000 / 3.000。
	if rv := parseAmount(e.PreResidual); rv > 0 && c.FinOriginalValue > 0 {
		c.FinResidualRate = math.Round(rv/c.FinOriginalValue*100*1000) / 1000
	}

	// 税额直接取 originalfincard_incometax。这个键名容易误读成"所得税"，
	// 但实测它是**进项税额**：22 张有值的卡里恒等于 原值 × 13%（22/22 精确成立），
	// 而且 原值 + 税额 恰好是整数（6299.00 / 7575.00 / 68000.00 …）——正是发票含税价。
	// 所以 原值 是**不含税**金额，含税金额 = 原值 + 税额。
	//
	// 只有 22/200 张卡有这个值，其余 178 张星瀚没给（不是 0，是没填）。
	// 因此含税金额只在能确认税额时才写：否则会把 原值 当成含税金额写进去，
	// 而那 178 张卡的原值未必是含税口径（同类的办公设备/电子设备里两种都有）。
	c.FinTax = parseAmount(e.IncomeTax)
	if c.FinTax > 0 {
		c.FinAmountWithTax = round2(c.FinOriginalValue + c.FinTax)
	}
}

// round2 把金额收敛到两位小数，与库列 DECIMAL(14,2) 对齐。
// 两个两位小数相加在浮点下可能出现 6299.000000000001 这种尾差，
// 不收敛就会一路带进履历和接口响应。
func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
