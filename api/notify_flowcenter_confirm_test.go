package api

import (
	"testing"

	"asset-mgr/model"
)

// 本文件覆盖 confirm（结案）分支的「流程结案」op（opFinishFlow）。
//
// 需要真库：confirm 分支要查受理人（assetManagerEmpIDs → s.st.DB()），无法像 take 分支那样
// 靠 empID=0 绕过。连不上库则跳过（与 notify_test.go 同一约定）。
//
// 背景（docs/云之家流程中心集成方案.md §5 触发矩阵第 7 行）：confirm 是业务终态，
// 除把三方待办都办结外，还必须 instance/finish 结掉流程实例——否则各人待办虽消失，
// 但流程在云之家侧永远停在「进行中」，发起人视图里挂着一张永不结束的流程。

func TestPlanForConfirmFinishFlow(t *testing.T) {
	st, _ := mustTestStore(t)

	o := &model.RepairOrder{ID: 1409, Code: "WX202609210001"}

	// —— 流程中心开：confirm 必须以 opFinishFlow 结尾 ——
	on := &NotifyService{st: st, flowCenterEnabled: true}
	ops := on.planFor(o, model.RepairActionConfirm, "", "")
	if len(ops) == 0 {
		t.Fatal("confirm 应至少产出 1 个 op")
	}
	if ops[len(ops)-1].kind != opFinishFlow {
		t.Fatalf("流程中心开时 confirm 应以 opFinishFlow 结尾，实际末位 kind=%s（全部：%+v）",
			ops[len(ops)-1].kind, ops)
	}
	// 顺序保证：finish 必须排在所有 deal 之后（先把各人待办办结，再结流程）。
	for i, op := range ops[:len(ops)-1] {
		if op.kind == opFinishFlow {
			t.Errorf("第 %d 个 op 是 finish_flow，必须排在末尾（所有 deal 之后）", i)
		}
	}
	// 前面必须确实有 deal（否则「排在 deal 之后」这句无意义）。
	hasDeal := false
	for _, op := range ops[:len(ops)-1] {
		if op.kind == opDeal {
			hasDeal = true
			break
		}
	}
	if !hasDeal {
		t.Errorf("confirm 应至少有一个 deal op，实际 %+v", ops)
	}

	// —— 流程中心关：不产生 opFinishFlow（im-todo 无流程实例概念）——
	off := &NotifyService{st: st, flowCenterEnabled: false}
	for _, op := range off.planFor(o, model.RepairActionConfirm, "", "") {
		if op.kind == opFinishFlow {
			t.Errorf("流程中心关时不应产生 finish_flow op（im-todo 无流程实例）")
		}
	}
}
