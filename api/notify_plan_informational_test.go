package api

import (
	"strings"
	"testing"

	"asset-mgr/config"
	"asset-mgr/model"
)

// 本文件是「信息性告知 = 新建一条**已读**待办」这条规则的**不依赖数据库**的单测。
//
// 为什么必须单独有它：需要真库的集成测试（notify_test.go 的 TestNotifyAcceptInformationalSwitch
// 等）在安全阀生效时（DSN 指向生产库 asset）会**全部 SKIP**——那时「测试通过」其实一条都没跑。
// 故把「informational 该不该产生 op、产生哪种 op」的判定写成不碰 store 的纯逻辑，在这里直接测，
// 这些用例在任何环境都会**真正执行**（见文件末尾的 store-free 前提说明）。
//
// ⚠️ 反向验证指引（本项目纪律）：把 shouldSendInformationalNotice 改成恒真 →
// TestShouldSendInformationalNotice 与两条 FlowCenterSkips 用例必红；
// 把 ensureReadOp 的 withRead 去掉 → TestEnsureReadOpSetsWithRead 与两条 IMtodo 用例必红。

// storeFreeService 构造一个**不接数据库**的 NotifyService 用于纯逻辑断言。
//
// 前提：ensureOp → resolveOpenID(empID)，empID<=0 时 resolveOpenID 直接返回空串、**不访问数据库**
// （见 notify_plan.go）。故本文件所有用例都传 empID=0 的探针单，绝不触发 store 调用。
// accept 分支不查 assetManagerEmpIDs，故 planFor(Accept) 亦可安全地 store-free 调用。
func storeFreeService(informational, flowCenterEnabled bool) *NotifyService {
	return &NotifyService{informational: informational, flowCenterEnabled: flowCenterEnabled}
}

// TestShouldSendInformationalNotice 钉死「流程中心通道不发送信息性告知」这条决策。
// 若把该函数改成恒真，流程中心下就会多出一条 create（污染 A 方案维护的节点留痕序列），本用例必红。
func TestShouldSendInformationalNotice(t *testing.T) {
	cases := []struct {
		name              string
		flowCenterEnabled bool
		want              bool
	}{
		{"im-todo 通道：发送", false, true},
		{"流程中心通道：跳过", true, false},
	}
	for _, c := range cases {
		if got := shouldSendInformationalNotice(c.flowCenterEnabled); got != c.want {
			t.Errorf("%s: shouldSendInformationalNotice(%v) = %v, 期望 %v",
				c.name, c.flowCenterEnabled, got, c.want)
		}
	}
}

// TestEnsureReadOpSetsWithRead 钉死「信息性告知的 op 是 opEnsure 且 withRead=true」。
// 若 ensureReadOp 忘了置 withRead（退回成普通 ensureOp），收件人会收到**未读**打扰，本用例必红。
func TestEnsureReadOpSetsWithRead(t *testing.T) {
	s := storeFreeService(false, false)
	o := &model.RepairOrder{Code: "R-1", AssetName: "空调"}
	op := s.ensureReadOp(o, 0, "accept", "报修已受理", "你的报修已被受理")
	if op.kind != opEnsure {
		t.Errorf("ensureReadOp 的 kind 应为 opEnsure，实际 %q", op.kind)
	}
	if !op.withRead {
		t.Errorf("ensureReadOp 必须置 withRead=true（新建后置已读），实际 false")
	}
}

// TestInformationalNoticeFlowCenterSkipped 钉死：流程中心通道下 informationalNotice 返回 nil
// （不产生任何 op，也就不会有任何 create / send）。
func TestInformationalNoticeFlowCenterSkipped(t *testing.T) {
	s := storeFreeService(false, true)
	o := &model.RepairOrder{Code: "R-1", AssetName: "空调"}
	ops := s.informationalNotice(o, 0, "accept", acceptedCopy)
	if len(ops) != 0 {
		t.Errorf("流程中心通道下不应产生信息性告知 op，实际 %d 条: %+v", len(ops), ops)
	}
}

// TestInformationalNoticeIMTodoProducesReadOp 钉死：im-todo 通道下 informationalNotice 产出
// 恰好一条 opEnsure 且 withRead=true，文案来自传入的 copyFn。
func TestInformationalNoticeIMTodoProducesReadOp(t *testing.T) {
	s := storeFreeService(false, false)
	o := &model.RepairOrder{Code: "R-1", AssetName: "空调"}
	ops := s.informationalNotice(o, 0, "accept", acceptedCopy)
	if len(ops) != 1 {
		t.Fatalf("im-todo 通道下应产出恰好 1 条信息性告知 op，实际 %d 条", len(ops))
	}
	op := ops[0]
	if op.kind != opEnsure {
		t.Errorf("op.kind 应为 opEnsure，实际 %q", op.kind)
	}
	if !op.withRead {
		t.Errorf("op.withRead 应为 true（新建后置已读），实际 false")
	}
	if op.purpose != "accept" {
		t.Errorf("op.purpose 应为 accept，实际 %q", op.purpose)
	}
	// 文案应来自 acceptedCopy，证明确实调用了传入的 copyFn（而不是另写一套文案）。
	wantTitle, wantContent := acceptedCopy(o)
	if op.title != wantTitle {
		t.Errorf("op.title 应来自 acceptedCopy=%q，实际 %q", wantTitle, op.title)
	}
	if op.content != wantContent {
		t.Errorf("op.content 应来自 acceptedCopy=%q，实际 %q", wantContent, op.content)
	}
	if !strings.Contains(op.content, "已被受理") {
		t.Errorf("op.content 应含「已被受理」，实际 %q", op.content)
	}
}

// TestPlanForInformationalAcceptIMTodo 直接对 planFor 的产出断言（accept，im-todo）：
// informational=true 时应产出恰好一条 opEnsure 且 withRead=true（新建即已读）。
//
// accept 分支不查 assetManagerEmpIDs，故可 store-free 调用（见 storeFreeService 说明）。
func TestPlanForInformationalAcceptIMTodo(t *testing.T) {
	s := storeFreeService(true, false)
	o := &model.RepairOrder{Code: "R-1", AssetName: "空调", ReporterEmpID: 0}
	ops := s.planFor(o, model.RepairActionAccept, "", "")
	if len(ops) != 1 {
		t.Fatalf("informational=true 的 accept 应产出恰好 1 条 op，实际 %d 条: %+v", len(ops), ops)
	}
	if ops[0].kind != opEnsure {
		t.Errorf("accept 的信息性告知 kind 应为 opEnsure，实际 %q", ops[0].kind)
	}
	if !ops[0].withRead {
		t.Errorf("accept 的信息性告知 op.withRead 应为 true（新建即已读），实际 false")
	}
}

// TestPlanForInformationalAcceptFlowCenterSkips 直接对 planFor 的产出断言（accept，流程中心）：
// informational=true 但 flowcenter 开启时**不产出**任何 op（既不建节点、也无 send）。
func TestPlanForInformationalAcceptFlowCenterSkips(t *testing.T) {
	s := storeFreeService(true, true)
	o := &model.RepairOrder{Code: "R-1", AssetName: "空调", ReporterEmpID: 0}
	ops := s.planFor(o, model.RepairActionAccept, "", "")
	if len(ops) != 0 {
		t.Errorf("流程中心通道下 accept 不应产出信息性告知 op，实际 %d 条: %+v", len(ops), ops)
	}
}

// TestPlanForInformationalTakeIMTodo 直接对 planFor 的产出断言（take，im-todo）：
// 维护人员「只置已读」+ 报修人「新建已读待办」两条并存，后者必须 withRead=true。
func TestPlanForInformationalTakeIMTodo(t *testing.T) {
	s := storeFreeService(true, false)
	o := &model.RepairOrder{Code: "R-1", AssetName: "空调", AssigneeEmpID: 0, ReporterEmpID: 0}
	ops := s.planFor(o, model.RepairActionTake, "", "")

	// 报修人那条：opEnsure + purpose=take + withRead=true。
	var info *notifyOp
	for i := range ops {
		if ops[i].purpose == "take" && ops[i].kind == opEnsure {
			info = &ops[i]
		}
	}
	if info == nil {
		t.Fatalf("informational=true 的 take 应给报修人产出 opEnsure(take)，实际 ops=%+v", ops)
	}
	if !info.withRead {
		t.Errorf("take 的信息性告知 op.withRead 应为 true（新建即已读），实际 false")
	}
	// 维护人员那条：opRead（只置已读，本地保持 active），不得是 opDeal。
	var hasTechRead bool
	for _, op := range ops {
		if op.kind == opRead && op.purpose == "dispatch" {
			hasTechRead = true
		}
		if op.kind == opDeal && op.purpose == "dispatch" {
			t.Errorf("im-todo 的 take 不应 deal 维护人员（漏洞 2），实际产出 opDeal(dispatch)")
		}
	}
	if !hasTechRead {
		t.Errorf("im-todo 的 take 应给维护人员产出 opRead(dispatch)，实际 ops=%+v", ops)
	}
}

// TestPlanForInformationalTakeFlowCenterSkips 直接对 planFor 的产出断言（take，流程中心）：
// informational=true 但 flowcenter 开启时，**不得多出**信息性告知 op。
//
// ⚠️ 断言必须用**计数**，不能写成 `if op.kind == opEnsure && op.withRead`：那等于把
// 「是否是信息性告知 op」等同于 `withRead` 这个**实现细节** —— 一旦 ensureReadOp 忘了置
// withRead（另一处 bug），那条 op 就不再匹配断言条件，本用例反而**不报错**。实测：把
// shouldSendInformationalNotice 改成恒真 **并同时**去掉 withRead 时，用组合条件写的版本
// 完全不变红（两个缺陷互相掩盖）。而 flowcenter 下多出的那条 op，无论 withRead 真假都违规
// —— 它都会创建流程节点、都算打扰。
//
// 判据：flowcenter 的 take 分支**本来就有**恰好 1 条合法的 ensure op（A 方案把维护人员推进到
// 「维修中」节点，见 planFor 的 take 分支），故「ensure op 恰好 1 条」即可鉴别。
func TestPlanForInformationalTakeFlowCenterSkips(t *testing.T) {
	s := storeFreeService(true, true)
	o := &model.RepairOrder{Code: "R-1", AssetName: "空调", AssigneeEmpID: 0, ReporterEmpID: 0}
	ops := s.planFor(o, model.RepairActionTake, "", "")
	var nEnsure int
	for _, op := range ops {
		if op.kind == opEnsure {
			nEnsure++
		}
	}
	if nEnsure != 1 {
		t.Errorf("流程中心通道下 take 应恰好 1 条 ensure op（A 方案的「维修中」节点化），"+
			"多出来的即为信息性告知（违规）。实际 %d 条: %+v", nEnsure, ops)
	}
}

// TestNewNotifyServiceWiresInformationalFromConfig 钉死**接线**：NewNotifyService 必须把
// cfg.Yunzhijia.InformationalEnabled 接到 s.informational，而不是某个编译期常量。
//
// 为什么单独有它：config 层的用例只证明「yaml/env → cfg.Yunzhijia.InformationalEnabled」，
// 证不了「cfg → NotifyService.informational」这一段。若有人把 NewNotifyService 改回硬编码
// false，config 测试会全绿、功能却永远是关的——这类「接线断掉」只有本用例能拦。
//
// store-free：st=nil、yzj=nil ⇒ TodoEnabled / FlowCenterEnabled 均为 false ⇒ enabled=false
// ⇒ 不起 worker goroutine、不访问数据库（与 deeplink_integration_test.go 同款构造）。
// 故本用例在安全阀生效（api 其余集成测试全部 SKIP）时**仍会真正执行**。
func TestNewNotifyServiceWiresInformationalFromConfig(t *testing.T) {
	on := &config.Config{}
	on.Yunzhijia.InformationalEnabled = true
	if s := NewNotifyService(nil, on, nil); !s.informational {
		t.Errorf("NewNotifyService 应把 cfg.Yunzhijia.InformationalEnabled=true 接到 s.informational，" +
			"实际 false（接线断了吗？）")
	}

	off := &config.Config{}
	off.Yunzhijia.InformationalEnabled = false
	if s := NewNotifyService(nil, off, nil); s.informational {
		t.Errorf("InformationalEnabled=false 时 s.informational 应为 false，实际 true")
	}
}
