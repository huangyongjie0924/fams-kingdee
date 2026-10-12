package api

import (
	"strings"
	"testing"

	"asset-mgr/model"
)

// order 造一个用于文案测试的维修单（字段只填文案相关的）。
func order(reporter, asset, fault, urgency, assignee string) *model.RepairOrder {
	return &model.RepairOrder{
		ID:           123,
		Code:         "WX202609200001",
		AssetName:    asset,
		ReporterName: reporter,
		AssigneeName: assignee,
		FaultDesc:    fault,
		Urgency:      urgency,
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"短", 5, "短"},
		{"12345", 5, "12345"},
		{"123456", 5, "12345…"},
		{"资产名称超长需截断", 4, "资产名称…"},
		{"", 3, ""},
		{"abc", 0, ""},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.max); got != c.want {
			t.Errorf("truncateRunes(%q,%d)=%q want %q", c.in, c.max, got, c.want)
		}
	}
}

// TestUrgencyPrefix 紧急前缀只在 high 时出现（§3.3）。
func TestUrgencyPrefix(t *testing.T) {
	high := order("张三", "3号注塑机", "异响停机", model.RepairUrgencyHigh, "")
	normal := order("张三", "3号注塑机", "异响停机", model.RepairUrgencyNormal, "")
	low := order("张三", "3号注塑机", "异响停机", model.RepairUrgencyLow, "")

	_, cHigh := intakeCopy(high)
	_, cNormal := intakeCopy(normal)
	_, cLow := intakeCopy(low)

	if !strings.HasPrefix(cHigh, "【紧急】") {
		t.Errorf("high 应加【紧急】前缀，实际 %q", cHigh)
	}
	if strings.HasPrefix(cNormal, "【紧急】") || strings.HasPrefix(cLow, "【紧急】") {
		t.Errorf("normal/low 不应加【紧急】前缀：normal=%q low=%q", cNormal, cLow)
	}
	// 前缀应作用于所有 content（不只 intake）
	_, cConfirm := confirmingCopy(high)
	if !strings.HasPrefix(cConfirm, "【紧急】") {
		t.Errorf("high 的其它 content 也应带前缀，实际 %q", cConfirm)
	}
}

// TestVariableTruncation 变量各自截断：故障描述 20、资产名 16、原因 20（§3.4）。
func TestVariableTruncation(t *testing.T) {
	longFault := strings.Repeat("故", 30)
	longAsset := strings.Repeat("机", 30)
	longReason := strings.Repeat("因", 30)

	// 单独验证：截断后的变量出现在 content 里，且原文不出现。
	_, c := dispatchedCopy(order("张三", "短资产", longFault, model.RepairUrgencyNormal, ""))
	if strings.Contains(c, longFault) {
		t.Errorf("故障描述应被截断，content=%q", c)
	}
	if !strings.Contains(c, strings.Repeat("故", 20)) {
		t.Errorf("故障描述应保留前 20 字，content=%q", c)
	}

	_, c2 := confirmingCopy(order("张三", longAsset, "x", model.RepairUrgencyNormal, ""))
	if strings.Contains(c2, longAsset) {
		t.Errorf("资产名应被截断，content=%q", c2)
	}

	_, c3 := rejectedCopy(order("张三", "短", "x", model.RepairUrgencyNormal, ""), longReason)
	if strings.Contains(c3, longReason) {
		t.Errorf("驳回原因应被截断，content=%q", c3)
	}
}

// TestContentBudgetAndActionTail content 总长 ≤ 40 字，且「动作要求」句永不被截掉（§3.4）。
func TestContentBudgetAndActionTail(t *testing.T) {
	// 极端：长报修人 + 长资产名 + 长故障描述 + 紧急。
	o := order(strings.Repeat("张", 10), strings.Repeat("机", 30),
		strings.Repeat("故", 30), model.RepairUrgencyHigh, strings.Repeat("工", 10))

	type tc struct {
		name string
		got  string
		tail string
	}
	_, intake := intakeCopy(o)
	_, dispatch := dispatchedCopy(o)
	_, confirming := confirmingCopy(o)
	_, returned := returnedCopy(o, strings.Repeat("因", 30))
	_, rejected := rejectedCopy(o, strings.Repeat("因", 30))
	_, repairing := repairingCopy(o)
	_, done := doneCopy(o)

	cases := []tc{
		{"intake", intake, "」，问题："},
		{"dispatch", dispatch, "」，问题："},
		{"confirming", confirming, "请确认维修结果"},
		{"returned", returned, "，请重新处理"},
		{"rejected", rejected, "」被驳回："},
		{"repairing", repairing, "接单，维修中"},
		{"done", done, "」维修已确认完工"},
	}
	for _, c := range cases {
		if got := runeLen(c.got); got > contentBudget {
			t.Errorf("%s content 超 40 字（%d）：%q", c.name, got, c.got)
		}
		if !strings.Contains(c.got, c.tail) {
			t.Errorf("%s content 丢失动作句 %q：%q", c.name, c.tail, c.got)
		}
		if c.got == "" {
			t.Errorf("%s content 不得为空（否则移动端不弹通知）", c.name)
		}
	}
}

// TestNormalContentNotShrunk 正常长度下不出现「…」（不应无故截断）。
func TestNormalContentNotShrunk(t *testing.T) {
	o := order("张三", "3号注塑机", "异响停机", model.RepairUrgencyNormal, "李四")
	for name, c := range map[string]string{
		"intake":    mustContent(intakeCopy(o)),
		"dispatch":  mustContent(dispatchedCopy(o)),
		"confirm":   mustContent(confirmingCopy(o)),
		"accept":    mustContent(acceptedCopy(o)),
		"cancel":    mustContent(cancelledCopy(o)),
		"repairing": mustContent(repairingCopy(o)),
	} {
		if strings.Contains(c, "…") {
			t.Errorf("%s 正常长度不应被截断，实际 %q", name, c)
		}
	}
}

// TestItemTitle itemtitle＝「单号 + 资产名（≤16）」。
func TestItemTitle(t *testing.T) {
	o := order("张三", strings.Repeat("机", 30), "x", model.RepairUrgencyNormal, "")
	it := itemTitleOf(o)
	if !strings.HasPrefix(it, "WX202609200001 ") {
		t.Errorf("itemtitle 应以单号开头，实际 %q", it)
	}
	if strings.Contains(it, strings.Repeat("机", 30)) {
		t.Errorf("itemtitle 的资产名应被截断，实际 %q", it)
	}
}

// TestCopyTemplatesExact 正常输入下 content 与产品定义 §3.2 模板逐字一致。
func TestCopyTemplatesExact(t *testing.T) {
	o := order("张三", "3号注塑机", "异响停机", model.RepairUrgencyNormal, "李四")
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"intake", mustContent(intakeCopy(o)), "张三报修「3号注塑机」，问题：异响停机"},
		{"accept", mustContent(acceptedCopy(o)), "你的报修「3号注塑机」已被受理，等待派工"},
		{"dispatch", mustContent(dispatchedCopy(o)), "指派你维修「3号注塑机」，问题：异响停机"},
		{"repairing", mustContent(repairingCopy(o)), "你的报修「3号注塑机」已由李四接单，维修中"},
		{"confirming", mustContent(confirmingCopy(o)), "你的「3号注塑机」已报完工，请确认维修结果"},
		{"returned", mustContent(returnedCopy(o, "仍异响")), "「3号注塑机」被报修人退回：仍异响，请重新处理"},
		{"rejected", mustContent(rejectedCopy(o, "重复报修")), "你的报修「3号注塑机」被驳回：重复报修"},
		{"cancelled", mustContent(cancelledCopy(o)), "你的报修「3号注塑机」已被管理员取消"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s content=\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
	// title 逐字
	if title, _ := intakeCopy(o); title != "新报修待受理" {
		t.Errorf("intake title=%q", title)
	}
	if title, _ := confirmingCopy(o); title != "维修待确认" {
		t.Errorf("confirming title=%q", title)
	}
}

func mustContent(_ string, content string) string { return content }
