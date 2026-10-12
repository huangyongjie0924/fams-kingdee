package api

import (
	"strings"
	"testing"

	"asset-mgr/model"
)

// 本文件覆盖流程中心（cloudflow）通道的**纯逻辑**：planFor 的 take 分流（A 方案）、
// bizId / 节点名生成、新建通道选择、新文案。这些都是无外部依赖的纯函数，故不需要真库。
// 需要读 task.Channel 的「办结分派」在 notify_flowcenter_dispatch_test.go（待字段落地）。

// TestPlanForTakeA方案 是 A 方案的核心断言：
//   - 流程中心开：接单 = 办结维护人员「待接单」节点 + 给同一人新发「维修中」节点（两个 op）；
//   - 流程中心关：接单 = 只给维护人员置已读（一个 op，im-todo 原行为）。
//
// 反向验证：把 planFor take 分支的 `if s.flowCenterEnabled` 改成恒假（永远走 else）⇒ 本用例
// 「flowcenter 开应产出 2 个 op」变红；改成恒真 ⇒ 「flowcenter 关应产出 1 个 op」变红。
func TestPlanForTakeA方案(t *testing.T) {
	// AssigneeEmpID=0 让 resolveOpenID 直接返回空串，避免触发 DB（本用例只考 ops 形状）。
	o := &model.RepairOrder{ID: 1409, Code: "WX202609210001", AssetName: "中央空调", AssigneeEmpID: 0}

	// —— 流程中心开：先 deal「待接单」，再 ensure「维修中」——
	on := &NotifyService{flowCenterEnabled: true}
	ops := on.planFor(o, model.RepairActionTake, "", "")
	if len(ops) != 2 {
		t.Fatalf("flowcenter 开时接单应产出 2 个 op，实际 %d：%+v", len(ops), ops)
	}
	if ops[0].kind != opDeal || ops[0].purpose != "dispatch" {
		t.Errorf("第 1 个 op 应为 deal(dispatch)，实际 kind=%s purpose=%s", ops[0].kind, ops[0].purpose)
	}
	if !ops[0].withRead {
		t.Errorf("deal「待接单」应同时置已读（{read:1,deal:1}）")
	}
	if ops[1].kind != opEnsure || ops[1].purpose != "take" {
		t.Errorf("第 2 个 op 应为 ensure(take)，实际 kind=%s purpose=%s", ops[1].kind, ops[1].purpose)
	}

	// —— 流程中心关：只 read「待接单」，无新节点 ——
	off := &NotifyService{flowCenterEnabled: false}
	ops2 := off.planFor(o, model.RepairActionTake, "", "")
	if len(ops2) != 1 {
		t.Fatalf("flowcenter 关时接单应产出 1 个 op，实际 %d：%+v", len(ops2), ops2)
	}
	if ops2[0].kind != opRead || ops2[0].purpose != "dispatch" {
		t.Errorf("flowcenter 关时第 1 个 op 应为 read(dispatch)，实际 kind=%s purpose=%s", ops2[0].kind, ops2[0].purpose)
	}
}

// TestFlowBizID 钉死 bizId 的格式与唯一性维度（节点级：含 empID 与 purpose）。
func TestFlowBizID(t *testing.T) {
	got := flowBizID("repair-1409", 88, "dispatch")
	if want := "repair-1409-88-dispatch"; got != want {
		t.Errorf("flowBizID = %q, 期望 %q", got, want)
	}
	// 稳定可复现（同入参同结果）。
	if again := flowBizID("repair-1409", 88, "dispatch"); again != got {
		t.Errorf("同入参两次结果不一致: %q vs %q", got, again)
	}
	// 不同 purpose 必须不等（A 方案下同一人先后有两个节点，必须能分别办结）。
	if flowBizID("repair-1409", 88, "take") == got {
		t.Errorf("不同 purpose 竟得到同一 bizId（无法分别办结）")
	}
	// 不同 empID 必须不等。
	if flowBizID("repair-1409", 89, "dispatch") == got {
		t.Errorf("不同 empID 竟得到同一 bizId")
	}
}

// TestFlowActivityName 映射命中 + 未命中回退（不 panic、不返回空串）。
func TestFlowActivityName(t *testing.T) {
	cases := map[string]string{
		"intake":   "待受理",
		"dispatch": "待接单",
		"take":     "维修中",
		"confirm":  "待确认",
		"reject":   "已驳回",
		"return":   "已退回",
		"cancel":   "已取消",
		"done":     "已完工",
		"accept":   "已受理",
	}
	for purpose, want := range cases {
		if got := flowActivityName(purpose); got != want {
			t.Errorf("flowActivityName(%q) = %q, 期望 %q", purpose, got, want)
		}
	}
	if got := flowActivityName("some-unknown-node"); got != "some-unknown-node" {
		t.Errorf("未命中应回退原文，实际 %q", got)
	}
	if got := flowActivityName(""); got != "" {
		t.Errorf("空串应回退空串，实际 %q", got)
	}
}

// TestNewTaskChannel 新建通道只看开关：开→流程中心，关→im-todo。
func TestNewTaskChannel(t *testing.T) {
	if got := (&NotifyService{flowCenterEnabled: true}).newTaskChannel(); got != notifyChannelFlowCenter {
		t.Errorf("开关开时新建通道应为 %q，实际 %q", notifyChannelFlowCenter, got)
	}
	if got := (&NotifyService{flowCenterEnabled: false}).newTaskChannel(); got != notifyChannelIMTodo {
		t.Errorf("开关关时新建通道应为 %q，实际 %q", notifyChannelIMTodo, got)
	}
}

// TestTakeAckCopy 新文案：给维护人员本人的「维修中」节点，含单号 + 动作要求句 + ≤40 字。
func TestTakeAckCopy(t *testing.T) {
	o := &model.RepairOrder{Code: "WX202609210001", AssetName: "中央空调外机", Urgency: model.RepairUrgencyNormal}
	title, content := takeAckCopy(o)
	if title == "" {
		t.Error("title 不应为空")
	}
	if !strings.Contains(content, o.Code) {
		t.Errorf("content 应含单号 %q，实际 %q", o.Code, content)
	}
	if !strings.Contains(content, "已接单") {
		t.Errorf("content 应含「已接单」，实际 %q", content)
	}
	if !strings.Contains(content, "请完成维修后及时报完工") {
		t.Errorf("content 应含动作要求句，实际 %q", content)
	}
	if runeLen(content) > contentBudget {
		t.Errorf("content 超 %d 字：%q（%d 字）", contentBudget, content, runeLen(content))
	}

	// urgency=high 加前缀，且仍不超预算。
	o.Urgency = model.RepairUrgencyHigh
	_, c2 := takeAckCopy(o)
	if !strings.HasPrefix(c2, urgencyHighMark) {
		t.Errorf("urgency=high 应加前缀，实际 %q", c2)
	}
	if runeLen(c2) > contentBudget {
		t.Errorf("紧急 content 超 %d 字：%q（%d 字）", contentBudget, c2, runeLen(c2))
	}
}

// TestPlanForReturnFlowCenter 钉死 #8 退回在流程中心下的「先办结旧节点再发新节点」：
// flowcenter 开 → deal(报修人,confirm) → deal(维护人员,take) → ensure(维护人员,return)，共 3 个 op；
// flowcenter 关 → 只有 deal(报修人) + ensure(维护人员)，共 2 个 op（im-todo 原行为）。
//
// 中间那条 deal(维护人员) 是安全网：正常路径下它是 no-op（维护人员节点已被 finish 办结），
// 但若 finish 的 done 失败，它会把旧节点补办结，避免旧节点成为孤儿（见 notify_plan.go 注释）。
// 反向验证：去掉 `if s.flowCenterEnabled { dealOp(assignee) }` ⇒ flowcenter 开时只剩 2 个 op ⇒ 变红。
func TestPlanForReturnFlowCenter(t *testing.T) {
	// empID 全 0：resolveOpenID 直接返回空串，避免触发 DB（本用例只考 ops 形状）。
	o := &model.RepairOrder{ID: 1409, Code: "WX202609210001"}

	on := &NotifyService{flowCenterEnabled: true}
	ops := on.planFor(o, model.RepairActionReturn, "缺配件", "")
	if len(ops) != 3 {
		t.Fatalf("flowcenter 开时退回应产出 3 个 op，实际 %d：%+v", len(ops), ops)
	}
	if ops[0].kind != opDeal || ops[0].purpose != "confirm" {
		t.Errorf("第 1 个 op 应为 deal(confirm)，实际 kind=%s purpose=%s", ops[0].kind, ops[0].purpose)
	}
	if ops[1].kind != opDeal || ops[1].purpose != "take" {
		t.Errorf("第 2 个 op 应为 deal(take)，实际 kind=%s purpose=%s", ops[1].kind, ops[1].purpose)
	}
	if ops[2].kind != opEnsure || ops[2].purpose != "return" {
		t.Errorf("第 3 个 op 应为 ensure(return)，实际 kind=%s purpose=%s", ops[2].kind, ops[2].purpose)
	}

	off := &NotifyService{flowCenterEnabled: false}
	ops2 := off.planFor(o, model.RepairActionReturn, "缺配件", "")
	if len(ops2) != 2 {
		t.Fatalf("flowcenter 关时退回应产出 2 个 op，实际 %d：%+v", len(ops2), ops2)
	}
	if ops2[0].kind != opDeal || ops2[1].kind != opEnsure {
		t.Errorf("flowcenter 关时应为 [deal, ensure]，实际 %+v", ops2)
	}
}
