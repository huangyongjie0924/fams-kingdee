package store

import (
	"strings"
	"testing"
	"time"

	"asset-mgr/model"
)

// TestBizStatusNotInOwnedColumnLists 是方案 C「同步零回归」的长期守卫。
//
// biz_status 是台账本地列，一旦混进这三个清单里的任何一个，后果分别是：
//   - kingdeeOwnedColumns：每日同步把「维修中」覆盖回空 → 维修状态一夜蒸发；
//   - cardInitColumns：新建同步卡的 INSERT 会带它（并可能与别的列重复）；
//   - cardWriteColumns：卡片编辑表单能写它 → 用户可手填「维修中」。
//
// 三条都是「改错了不会让编译失败、也不会让别的测试变红」的静默故障，所以单钉一遍。
func TestBizStatusNotInOwnedColumnLists(t *testing.T) {
	lists := map[string][]string{
		"kingdeeOwnedColumns": kingdeeOwnedColumns,
		"cardInitColumns":     cardInitColumns,
		"cardWriteColumns":    cardWriteColumns,
	}
	for name, cols := range lists {
		for _, c := range cols {
			if c == "biz_status" {
				t.Fatalf("%s 不能包含 biz_status（台账本地列，见 docs/维修流程模块架构建议.md §1.4）", name)
			}
		}
	}
}

// TestEffectiveStatus 守住有效状态的优先级：biz_status 非空取它，否则回落 status。
// 与 buildWhere / sortColumns 里的 COALESCE(NULLIF(biz_status,''), status) 是同一套口径。
func TestEffectiveStatus(t *testing.T) {
	cases := []struct{ biz, status, want string }{
		{"维修中", "在用", "维修中"}, // 本地状态优先
		{"", "在用", "在用"},     // 本地为空 → 回落托管状态
		{"", "", ""},
		{"维修中", "", "维修中"},
	}
	for _, c := range cases {
		if got := effectiveStatus(c.biz, c.status); got != c.want {
			t.Errorf("effectiveStatus(%q,%q)=%q，期望 %q", c.biz, c.status, got, c.want)
		}
	}
}

// TestFormatDocCode 守住单号格式 WX<yyyymmdd><0001>，且与 count_plan 的 PD 共用同一函数。
func TestFormatDocCode(t *testing.T) {
	today := time.Now().Format("20060102")
	got := formatDocCode("WX", 12)
	if want := "WX" + today + "0012"; got != want {
		t.Errorf("formatDocCode(\"WX\",12)=%q，期望 %q", got, want)
	}
	// 序号补零到 4 位，超过 4 位不截断
	if got := formatDocCode("PD", 12345); got != "PD"+today+"12345" {
		t.Errorf("序号超过 4 位应原样输出，得到 %q", got)
	}
}

// TestRepairTransitionRules 守住状态机：P0 允许的跃迁必须在表里，非法跃迁必须被拒。
func TestRepairTransitionRules(t *testing.T) {
	allowed := []struct {
		action, from, to string
	}{
		{model.RepairActionAccept, model.RepairPending, model.RepairAccepted},
		{model.RepairActionReject, model.RepairPending, model.RepairRejected},
		{model.RepairActionDispatch, model.RepairAccepted, model.RepairDispatched},
		{model.RepairActionTake, model.RepairDispatched, model.RepairRepairing},
		{model.RepairActionFinish, model.RepairRepairing, model.RepairConfirming},
		{model.RepairActionConfirm, model.RepairConfirming, model.RepairDone},
		{model.RepairActionReturn, model.RepairConfirming, model.RepairRepairing},
		{model.RepairActionCancel, model.RepairPending, model.RepairCancelled},
		{model.RepairActionCancel, model.RepairAccepted, model.RepairCancelled},
		{model.RepairActionCancel, model.RepairDispatched, model.RepairCancelled},
	}
	for _, c := range allowed {
		to, ok := repairTransitions[c.action][c.from]
		if !ok {
			t.Errorf("跃迁 %s: %s→%s 应被允许，却不在表里", c.action, c.from, c.to)
			continue
		}
		if to != c.to {
			t.Errorf("跃迁 %s: %s 应到 %s，实际到 %s", c.action, c.from, c.to, to)
		}
	}

	// 非法跃迁：这些组合必须不在表里
	illegal := []struct{ action, from string }{
		{model.RepairActionConfirm, model.RepairPending},   // 待受理不能直接确认
		{model.RepairActionTake, model.RepairPending},      // 未派工不能接单
		{model.RepairActionDispatch, model.RepairPending},  // 未受理不能派工
		{model.RepairActionFinish, model.RepairDispatched}, // 未接单不能报完工
		{model.RepairActionAccept, model.RepairAccepted},   // 已受理不能重复受理
		{model.RepairActionConfirm, model.RepairDone},      // 终态不可再流转
	}
	for _, c := range illegal {
		if _, ok := repairTransitions[c.action][c.from]; ok {
			t.Errorf("非法跃迁 %s: %s 不该被允许", c.action, c.from)
		}
	}
}

// TestRepairScopeAllows 守住可见范围的「命中其一即可见」语义。
func TestRepairScopeAllows(t *testing.T) {
	order := &model.RepairOrder{ReporterEmpID: 10, AssigneeEmpID: 20, UseDeptID: 30}

	if !(model.RepairScope{}).Allows(order) {
		t.Error("零值 scope（不限）应放行全部")
	}
	if !(model.RepairScope{ReporterEmpID: 10}).Allows(order) {
		t.Error("报修人应看得见自己提交的单")
	}
	if !(model.RepairScope{AssigneeEmpID: 20}).Allows(order) {
		t.Error("被指派维修工应看得见派给自己的单")
	}
	if !(model.RepairScope{DeptIDs: []int64{30, 31}}).Allows(order) {
		t.Error("部门主管应看得见本部门子树的单")
	}
	if (model.RepairScope{ReporterEmpID: 99}).Allows(order) {
		t.Error("非报修人不该看得见")
	}
	if (model.RepairScope{AssigneeEmpID: 99}).Allows(order) {
		t.Error("非指派维修工不该看得见")
	}
	if (model.RepairScope{DeptIDs: []int64{99}}).Allows(order) {
		t.Error("非本部门的单不该看得见")
	}
}

// TestRepairStatusLabelsCoverAllCodes 守住「每个状态码都有中文白话」。
func TestRepairStatusLabelsCoverAllCodes(t *testing.T) {
	for _, s := range model.RepairStatuses {
		if strings.TrimSpace(model.RepairStatusLabel(s)) == "" || model.RepairStatusLabel(s) == s {
			t.Errorf("状态码 %s 缺少中文白话标签", s)
		}
	}
}
