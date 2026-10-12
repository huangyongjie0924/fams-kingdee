package api

import (
	"strings"

	"asset-mgr/model"
)

// 本文件实现待办文案的拼装与截断，规则以 docs/维修待办通知-产品定义.md §3 为准：
//   - title（待办列表短标题）≤ 10 中文字，动作导向；
//   - content（手机通知栏正文，只显示它）≤ 40 中文字，**必须自带全部关键信息**，
//     禁止「您有一条新待办」这类空话；
//   - itemtitle（待办项标题）＝「单号 + 资产名」，用于单据定位；
//   - 截断顺序（关键）：**先对每个变量各自截断，再拼接模板**，禁止先拼后整体截断；
//   - content 总长兜底：拼接后仍超 40 字时，按「故障描述 / 原因 → 资产名称」继续缩短，
//     保证「动作要求」句（如「请确认维修结果」）永不被截掉；
//   - urgency==high 时 content 前缀加「【紧急】」，low/normal 不加（§3.3）；
//   - content 必须非空（为空则移动端通知栏不弹通知，方案 K6）。

// 各变量的独立截断上限与 content 总长上限（§3.4），均按 Unicode 字符数（中文算 1）计。
const (
	limitNone       = 1 << 30 // 「姓名」等不截断的变量用一个大到不会触发的上限
	limitAssetName  = 16      // {资产名称}
	limitFaultDesc  = 20      // {故障描述}
	limitReason     = 20      // {驳回原因} / {退回原因}
	contentBudget   = 40      // content 总长上限（中文字数）
	urgencyHighMark = "【紧急】"
)

// contentVar 是模板里可截断的变量。
type contentVar struct {
	val   string // 变量原文
	limit int    // 该变量的独立上限（按 Unicode 字符数）
}

// contentToken 是模板片段：fixed 非空为固定文字；varIdx>=0 引用 vars[varIdx]。
type contentToken struct {
	fixed  string
	varIdx int
}

func fixedText(s string) contentToken { return contentToken{fixed: s, varIdx: -1} }
func refVar(i int) contentToken       { return contentToken{varIdx: i} }

// runeLen 返回按 Unicode 字符（中文算 1）的长度。
func runeLen(s string) int { return len([]rune(s)) }

// truncateRunes 按字符数截断：超长取前 max 字 + "…"；max<=0 返回空串。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// buildContent 按「先截变量、再拼模板、最后兜底缩短」的顺序生成 content。
//
//   - 先按每个变量各自的 limit 截断；
//   - 拼接后若仍超 budget，按 shrinkOrder（变量下标，靠前者先缩）逐个缩短，
//     每次都从**原文**重新截断（避免把上一次的「…」再截一次）；
//   - 固定片段（含动作要求句）永不被截断，只会缩短变量。
func buildContent(vars []contentVar, tokens []contentToken, shrinkOrder []int, budget int) string {
	limits := make([]int, len(vars))
	for i, v := range vars {
		limits[i] = v.limit
	}
	assemble := func() string {
		var b strings.Builder
		for _, tk := range tokens {
			if tk.varIdx >= 0 {
				b.WriteString(truncateRunes(vars[tk.varIdx].val, limits[tk.varIdx]))
			} else {
				b.WriteString(tk.fixed)
			}
		}
		return b.String()
	}
	s := assemble()
	for _, idx := range shrinkOrder {
		for runeLen(s) > budget && limits[idx] > 1 {
			limits[idx]--
			s = assemble()
		}
		if runeLen(s) <= budget {
			break
		}
	}
	return s
}

// urgencyPrefix 返回紧急前缀（仅 high 时非空，§3.3）。
func urgencyPrefix(urgency string) string {
	if urgency == model.RepairUrgencyHigh {
		return urgencyHighMark
	}
	return ""
}

// itemTitleOf 待办项标题：{单号} {资产名称}（资产名按 16 字截断，§3.2 / §3.4）。
func itemTitleOf(o *model.RepairOrder) string {
	return o.Code + " " + truncateRunes(o.AssetName, limitAssetName)
}

// intakeCopy：提交报修 → 全体资产管理员。content＝`{报修人}报修「{资产名称}」，问题：{故障描述}`。
func intakeCopy(o *model.RepairOrder) (title, content string) {
	title = "新报修待受理"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{
			{val: o.ReporterName, limit: limitNone}, // 姓名不截断
			{val: o.AssetName, limit: limitAssetName},
			{val: o.FaultDesc, limit: limitFaultDesc},
		},
		[]contentToken{refVar(0), fixedText("报修「"), refVar(1), fixedText("」，问题："), refVar(2)},
		[]int{2, 1}, // 先缩故障描述，再缩资产名
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// acceptedCopy：受理 → 报修人。content＝`你的报修「{资产名称}」已被受理，等待派工`。
func acceptedCopy(o *model.RepairOrder) (title, content string) {
	title = "报修已受理"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{{val: o.AssetName, limit: limitAssetName}},
		[]contentToken{fixedText("你的报修「"), refVar(0), fixedText("」已被受理，等待派工")},
		[]int{0},
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// dispatchedCopy：派工 → 被指派维修工。content＝`指派你维修「{资产名称}」，问题：{故障描述}`。
func dispatchedCopy(o *model.RepairOrder) (title, content string) {
	title = "维修派工待接单"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{
			{val: o.AssetName, limit: limitAssetName},
			{val: o.FaultDesc, limit: limitFaultDesc},
		},
		[]contentToken{fixedText("指派你维修「"), refVar(0), fixedText("」，问题："), refVar(1)},
		[]int{1, 0},
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// takeAckCopy：接单（A 方案「维修中」节点）→ **维修工本人**。
// content＝`{单号} 已接单「{资产名称}」，请完成维修后及时报完工`。
//
// ⚠️ 与 repairingCopy 不同：那条是给**报修人**的第三人称「维修进行中」，这条是给**维修工自己**的
// 动作导向待办（流程中心 A 方案），故带单号定位、带动作要求句。
func takeAckCopy(o *model.RepairOrder) (title, content string) {
	title = "维修进行中"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{
			{val: o.Code, limit: limitNone}, // 单号固定长度、定位用，不截断
			{val: o.AssetName, limit: limitAssetName},
		},
		[]contentToken{refVar(0), fixedText(" 已接单「"), refVar(1), fixedText("」，请完成维修后及时报完工")},
		[]int{1}, // 只缩资产名；单号与动作要求句永不截
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// repairingCopy：接单 → 报修人（第三人称信息性通知，P0 默认关闭）。
// content＝`你的报修「{资产名称}」已由{维修工}接单，维修中`。
func repairingCopy(o *model.RepairOrder) (title, content string) {
	title = "维修进行中"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{
			{val: o.AssetName, limit: limitAssetName},
			{val: o.AssigneeName, limit: limitNone},
		},
		[]contentToken{fixedText("你的报修「"), refVar(0), fixedText("」已由"), refVar(1), fixedText("接单，维修中")},
		[]int{0},
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// confirmingCopy：报完工 → 报修人。content＝`你的「{资产名称}」已报完工，请确认维修结果`。
func confirmingCopy(o *model.RepairOrder) (title, content string) {
	title = "维修待确认"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{{val: o.AssetName, limit: limitAssetName}},
		[]contentToken{fixedText("你的「"), refVar(0), fixedText("」已报完工，请确认维修结果")},
		[]int{0},
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// returnedCopy：退回 → 被指派维修工。content＝`「{资产名称}」被报修人退回：{退回原因}，请重新处理`。
func returnedCopy(o *model.RepairOrder, reason string) (title, content string) {
	title = "维修被退回待返工"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{
			{val: o.AssetName, limit: limitAssetName},
			{val: reason, limit: limitReason},
		},
		[]contentToken{fixedText("「"), refVar(0), fixedText("」被报修人退回："), refVar(1), fixedText("，请重新处理")},
		[]int{1, 0}, // 先缩退回原因，再缩资产名；「，请重新处理」永不截
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// rejectedCopy：驳回 → 报修人。content＝`你的报修「{资产名称}」被驳回：{驳回原因}`。
func rejectedCopy(o *model.RepairOrder, reason string) (title, content string) {
	title = "报修被驳回"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{
			{val: o.AssetName, limit: limitAssetName},
			{val: reason, limit: limitReason},
		},
		[]contentToken{fixedText("你的报修「"), refVar(0), fixedText("」被驳回："), refVar(1)},
		[]int{1, 0},
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// cancelledCopy：管理员取消 → 报修人。content＝`你的报修「{资产名称}」已被管理员取消`。
func cancelledCopy(o *model.RepairOrder) (title, content string) {
	title = "报修已取消"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{{val: o.AssetName, limit: limitAssetName}},
		[]contentToken{fixedText("你的报修「"), refVar(0), fixedText("」已被管理员取消")},
		[]int{0},
		contentBudget-runeLen(prefix),
	)
	return title, content
}

// doneCopy：确认完工 → 被指派维修工（第三人称信息性通知，P0 默认关闭）。
// content＝`「{资产名称}」维修已确认完工`（产品定义未给该模板，按同风格补全）。
func doneCopy(o *model.RepairOrder) (title, content string) {
	title = "维修已完工"
	prefix := urgencyPrefix(o.Urgency)
	content = prefix + buildContent(
		[]contentVar{{val: o.AssetName, limit: limitAssetName}},
		[]contentToken{fixedText("「"), refVar(0), fixedText("」维修已确认完工")},
		[]int{0},
		contentBudget-runeLen(prefix),
	)
	return title, content
}
