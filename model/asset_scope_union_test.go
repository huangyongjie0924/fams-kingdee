package model

import "testing"

// —— 资产/维修单可见范围的「并集」行为级回归 ——
//
// 背景（2026-09-24 多角色改造）：一个账号可同时持有多个角色（如 [部门负责人, 员工]），
// api/scope.go 会把各角色的范围**合并**成一个 scope——于是 AssetScope 的两个维度
// **同时非零**。此时必须取**并集**：命中任一维度即可见。
//
// ⚠️ 为什么单钉行为而不只钉字段：api 层的多角色测试只断言了「两个字段都被填上」，
// 但真正拿这个 scope 去过滤时，若 Allows 是短路写法，DeptIDs 会被整个吃掉——
// 字段对、行为错，正是最难发现的一类 bug。本文件断言的是 Allows 的**判定结果**。

// TestAssetScopeAllowsUnion 守住两维度同时非零时的并集判定。
// 修复前 Allows 是 `if s.SelfEmpID > 0 { return c.UserEmpID == s.SelfEmpID }` 的短路写法，
// 下面的「命中 DeptIDs 维度」一条会变红——这正是它要抓的回归。
func TestAssetScopeAllowsUnion(t *testing.T) {
	sc := AssetScope{SelfEmpID: 1, DeptIDs: []int64{2}}

	cases := []struct {
		name string
		card *AssetCard
		want bool
	}{
		{"命中 SelfEmpID 维度", &AssetCard{UserEmpID: 1, UseDeptID: 9}, true},
		{"命中 DeptIDs 维度", &AssetCard{UserEmpID: 9, UseDeptID: 2}, true},
		{"两个维度都不命中", &AssetCard{UserEmpID: 9, UseDeptID: 9}, false},
	}
	for _, c := range cases {
		if got := sc.Allows(c.card); got != c.want {
			t.Errorf("[SelfEmpID=1,DeptIDs=[2]] %s：Allows=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestAssetScopeAllowsSingleDimension 回归保护：并集改造**不得放宽单维度语义**。
// 单角色账号只会有一个维度非零，此时的判定必须与改造前完全一致。
func TestAssetScopeAllowsSingleDimension(t *testing.T) {
	selfOnly := AssetScope{SelfEmpID: 1}
	if !selfOnly.Allows(&AssetCard{UserEmpID: 1, UseDeptID: 9}) {
		t.Error("只设 SelfEmpID 时，命中 UserEmpID 的卡应可见")
	}
	if selfOnly.Allows(&AssetCard{UserEmpID: 9, UseDeptID: 1}) {
		t.Error("只设 SelfEmpID 时，仅命中 UseDeptID 的卡不应可见（单维度不得被并集放宽）")
	}

	deptOnly := AssetScope{DeptIDs: []int64{2}}
	if !deptOnly.Allows(&AssetCard{UserEmpID: 9, UseDeptID: 2}) {
		t.Error("只设 DeptIDs 时，命中 UseDeptID 的卡应可见")
	}
	if deptOnly.Allows(&AssetCard{UserEmpID: 2, UseDeptID: 9}) {
		t.Error("只设 DeptIDs 时，仅命中 UserEmpID 的卡不应可见（单维度不得被并集放宽）")
	}

	// 零值 = 不限，任何卡都放行。
	if !(AssetScope{}).Allows(&AssetCard{UserEmpID: 9, UseDeptID: 9}) {
		t.Error("零值 scope（不限）应放行全部")
	}
}

// TestRepairScopeAllowsUnion 对称地钉住 RepairScope 的**三维 OR**（model/repair.go）。
// 资产侧曾是「不对称的那一侧」（Allows 短路），维修侧从一开始就是正确的 OR 写法；
// 这里显式锁死，防止将来有人把它也改回短路形式。
func TestRepairScopeAllowsUnion(t *testing.T) {
	sc := RepairScope{ReporterEmpID: 1, AssigneeEmpID: 2, DeptIDs: []int64{3}}

	cases := []struct {
		name  string
		order *RepairOrder
		want  bool
	}{
		{"命中 ReporterEmpID", &RepairOrder{ReporterEmpID: 1, AssigneeEmpID: 9, UseDeptID: 9}, true},
		{"命中 AssigneeEmpID", &RepairOrder{ReporterEmpID: 9, AssigneeEmpID: 2, UseDeptID: 9}, true},
		{"命中 DeptIDs", &RepairOrder{ReporterEmpID: 9, AssigneeEmpID: 9, UseDeptID: 3}, true},
		{"三维都不命中", &RepairOrder{ReporterEmpID: 9, AssigneeEmpID: 9, UseDeptID: 9}, false},
	}
	for _, c := range cases {
		if got := sc.Allows(c.order); got != c.want {
			t.Errorf("[Reporter=1,Assignee=2,DeptIDs=[3]] %s：Allows=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// —— 资产可见范围第三维：有未结单维修单的资产（2026-09-24 员工口径）——
//
// employee 的可见范围 = 自己在用的资产 ∪ 有未结单维修单的资产（**不限报修人**）。
// 第三维落在 AssetScope.RepairingCardIDs，判定入口仍是 Allows。以下三条用例钉住它的行为。

// TestAssetScopeAllowsThreeWayUnion 三维同时非零时必须取并集：命中任一维度即可见。
func TestAssetScopeAllowsThreeWayUnion(t *testing.T) {
	sc := AssetScope{SelfEmpID: 1, DeptIDs: []int64{2}, RepairingCardIDs: []int64{100, 200}}

	cases := []struct {
		name string
		card *AssetCard
		want bool
	}{
		{"命中 SelfEmpID 维度", &AssetCard{ID: 999, UserEmpID: 1, UseDeptID: 9}, true},
		{"命中 DeptIDs 维度", &AssetCard{ID: 999, UserEmpID: 9, UseDeptID: 2}, true},
		{"命中 RepairingCardIDs 维度", &AssetCard{ID: 200, UserEmpID: 9, UseDeptID: 9}, true},
		{"三维都不命中", &AssetCard{ID: 999, UserEmpID: 9, UseDeptID: 9}, false},
	}
	for _, c := range cases {
		if got := sc.Allows(c.card); got != c.want {
			t.Errorf("[Self=1,Dept=[2],Repairing=[100,200]] %s：Allows=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestAssetScopeAllowsRepairingOnly 只有 RepairingCardIDs 非零时：
// Restricted() 必须为 true（否则 fail-open 到全量），且只放行集合内的 id。
func TestAssetScopeAllowsRepairingOnly(t *testing.T) {
	sc := AssetScope{RepairingCardIDs: []int64{100, 200}}
	if !sc.Restricted() {
		t.Fatal("只有 RepairingCardIDs 非零时 Restricted() 必须为 true，否则会 fail-open 到全量")
	}
	if !sc.Allows(&AssetCard{ID: 100, UserEmpID: 9, UseDeptID: 9}) {
		t.Error("命中 RepairingCardIDs 的卡应可见")
	}
	if !sc.Allows(&AssetCard{ID: 200, UserEmpID: 9, UseDeptID: 9}) {
		t.Error("命中 RepairingCardIDs 的卡应可见（第二个 id）")
	}
	if sc.Allows(&AssetCard{ID: 300, UserEmpID: 9, UseDeptID: 9}) {
		t.Error("未命中 RepairingCardIDs 的卡不应可见")
	}
}

// TestAssetScopeEmptyRepairingDoesNotWiden 第三维为空（nil 或显式空 slice）时**不得放宽**范围：
// 不能因为多了个空字段就退化成「不限」。
func TestAssetScopeEmptyRepairingDoesNotWiden(t *testing.T) {
	for _, rc := range [][]int64{nil, {}} {
		sc := AssetScope{SelfEmpID: 1, RepairingCardIDs: rc}
		if !sc.Restricted() {
			t.Fatalf("RepairingCardIDs=%v 时仍有 SelfEmpID 维度，Restricted() 应为 true", rc)
		}
		if !sc.Allows(&AssetCard{ID: 100, UserEmpID: 1, UseDeptID: 9}) {
			t.Errorf("RepairingCardIDs=%v：命中 SelfEmpID 的卡应可见", rc)
		}
		if sc.Allows(&AssetCard{ID: 100, UserEmpID: 9, UseDeptID: 9}) {
			t.Errorf("RepairingCardIDs=%v：空第三维不得放宽——仅 UserEmpID 不匹配的卡不应可见", rc)
		}
	}

	// 三个维度全为零值 → 不限（既有语义）。
	if (AssetScope{}).Restricted() {
		t.Error("零值 scope 应不限（Restricted=false）")
	}
}
